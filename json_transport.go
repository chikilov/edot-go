// SPDX-License-Identifier: Apache-2.0
package edotgo

// Metrics/logs HTTP exporters in the pinned Go SDK only support protobuf.
// This transport adapts their requests and successful responses at the HTTP
// boundary, retaining upstream resource conversion, retry and partial-success
// handling. Traces retain native request encoding and use response validation.
import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	logpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const maxJSONBody = 16 << 20 // Limit decoded request/response bodies to 16 MiB.

func signalEnv(signal, key string) string {
	if v := os.Getenv("OTEL_EXPORTER_OTLP_" + signal + "_" + key); v != "" {
		return v
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_" + key)
}

func jsonHTTPClient(signal string, supplied ...exporterSettings) (*http.Client, error) {
	settings := exporterSettingsFor(signal, supplied)

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if file := signalEnv(signal, "CERTIFICATE"); file != "" && settings.TLSConfig == nil {
		pem, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("%s CA certificate: %w", signal, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s CA certificate: no valid certificates", signal)
		}
		tlsConfig.RootCAs = pool
	}
	cert, key := signalEnv(signal, "CLIENT_CERTIFICATE"), signalEnv(signal, "CLIENT_KEY")
	if (cert != "" || key != "") && settings.TLSConfig == nil {
		if cert == "" || key == "" {
			return nil, fmt.Errorf("%s: client certificate and key must both be set", signal)
		}
		pair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return nil, fmt.Errorf("%s client TLS: %w", signal, err)
		}
		tlsConfig.Certificates = []tls.Certificate{pair}
	}
	if settings.TLSConfig != nil {
		tlsConfig = settings.TLSConfig.Clone()
	}
	timeout := 10 * time.Second
	if v := signalEnv(signal, "TIMEOUT"); v != "" && settings.Timeout == 0 {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 || n > int64((1<<63-1)/time.Millisecond) {
			return nil, fmt.Errorf("%s timeout: expected positive milliseconds", signal)
		}
		timeout = time.Duration(n) * time.Millisecond
	}
	if settings.Timeout > 0 {
		timeout = settings.Timeout
	}
	base := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second, TLSClientConfig: tlsConfig}
	return &http.Client{Timeout: timeout, Transport: &jsonTransport{base: base, signal: signal}}, nil
}

type jsonTransport struct {
	base   *http.Transport
	signal string
}

func (t *jsonTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }

func readLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxJSONBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxJSONBody {
		return nil, fmt.Errorf("OTLP JSON body exceeds 16 MiB")
	}
	return b, nil
}
func readBody(body io.Reader, encoding string) ([]byte, error) {
	if strings.EqualFold(encoding, "gzip") {
		r, err := gzip.NewReader(body)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return readLimited(r)
	}
	if encoding != "" {
		return nil, fmt.Errorf("unsupported OTLP content encoding %q", encoding)
	}
	return readLimited(body)
}

func (t *jsonTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	defer req.Body.Close()
	data, err := readBody(req.Body, req.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, err
	}
	var msg proto.Message
	// Traces already use the upstream native JSON encoder. Only metrics/logs
	// need request conversion; all signals share response validation.
	if t.signal != "TRACES" {
		switch t.signal {
		case "METRICS":
			msg = &metricpb.ExportMetricsServiceRequest{}
		case "LOGS":
			msg = &logpb.ExportLogsServiceRequest{}
		default:
			return nil, fmt.Errorf("unsupported JSON signal")
		}
		if err = proto.Unmarshal(data, msg); err != nil {
			return nil, err
		}
		data, err = marshalOTLPJSON(msg)
		if err != nil {
			return nil, err
		}
	}
	if len(data) > maxJSONBody {
		return nil, fmt.Errorf("OTLP JSON request exceeds 16 MiB")
	}
	if strings.EqualFold(req.Header.Get("Content-Encoding"), "gzip") {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		if _, err = w.Write(data); err != nil {
			return nil, err
		}
		if err = w.Close(); err != nil {
			return nil, err
		}
		data = buf.Bytes()
	}
	out := req.Clone(req.Context())
	out.Header = req.Header.Clone()
	out.Header.Set("Content-Type", "application/json")
	out.Header.Set("Accept", "application/json")
	out.Header.Set("User-Agent", "edot-go-unofficial/"+Version+" OTLP/HTTP JSON")
	out.Body = io.NopCloser(bytes.NewReader(data))
	out.ContentLength = int64(len(data))
	out.Header.Del("Content-Length")
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	resp, err := t.base.RoundTrip(out)
	if err != nil {
		return nil, err
	}
	// Let upstream retry/status handling inspect failure responses unchanged.
	contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	data, err = readBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, err
	}
	if len(data) > 0 {
		switch t.signal {
		case "TRACES":
			msg = &tracepb.ExportTraceServiceResponse{}
		case "METRICS":
			msg = &metricpb.ExportMetricsServiceResponse{}
		case "LOGS":
			msg = &logpb.ExportLogsServiceResponse{}
		}
		switch contentType {
		case "application/json":
			if err = (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, msg); err != nil {
				return nil, fmt.Errorf("OTLP JSON response: %w", err)
			}
			data, err = proto.Marshal(msg)
			if err != nil {
				return nil, err
			}
		case "application/x-protobuf":
			// Retain interoperability with receivers returning a binary response.
			if err = proto.Unmarshal(data, msg); err != nil {
				return nil, fmt.Errorf("OTLP protobuf response: %w", err)
			}
		default:
			return nil, fmt.Errorf("unexpected OTLP response content type")
		}
	}

	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))
	resp.Header = resp.Header.Clone()
	resp.Header.Set("Content-Type", "application/x-protobuf")
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	resp.Uncompressed = true
	return resp, nil
}

// OTLP JSON differs from ordinary protobuf JSON: IDs are hex, enums are numeric.
// UseNumber avoids rounding double values or accidentally coercing integers.
func marshalOTLPJSON(msg proto.Message) ([]byte, error) {
	data, err := (protojson.MarshalOptions{UseEnumNumbers: true}).Marshal(msg)
	if err != nil {
		return nil, err
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err = dec.Decode(&value); err != nil {
		return nil, err
	}
	if err = hexIDs(value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func hexIDs(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if key == "traceId" || key == "spanId" || key == "parentSpanId" {
				if text, ok := item.(string); ok {
					b, err := base64.StdEncoding.DecodeString(text)
					if err != nil {
						return err
					}
					v[key] = hex.EncodeToString(b)
					continue
				}
			}
			if err := hexIDs(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if err := hexIDs(item); err != nil {
				return err
			}
		}
	}
	return nil
}
