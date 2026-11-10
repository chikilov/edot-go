// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

func signalEnv(signal, key string) string {
	if v := os.Getenv("OTEL_EXPORTER_OTLP_" + signal + "_" + key); v != "" {
		return v
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_" + key)
}

func otlpHTTPClient(signal string, supplied ...exporterSettings) (*http.Client, error) {
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
	guarded := &guardedHTTPTransport{base: base}
	var transport http.RoundTripper = guarded
	if settings.Protocol == "http/json" {
		transport = &jsonTransport{base: guarded, signal: signal}
	}
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// Bound successful and error response bodies before upstream reads them. The
// underlying transport performs any automatic gzip decoding before this guard.
type guardedHTTPTransport struct{ base *http.Transport }

func (t *guardedHTTPTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }
func (t *guardedHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	response.Body = &boundedOTLPBody{ReadCloser: response.Body, remaining: maxJSONBody}
	return response, nil
}

type boundedOTLPBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedOTLPBody) Read(p []byte) (int, error) {
	if b.remaining < 0 {
		return 0, fmt.Errorf("OTLP response exceeds 16 MiB")
	}
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	if b.remaining < 0 {
		return n, fmt.Errorf("OTLP response exceeds 16 MiB")
	}
	return n, err
}
