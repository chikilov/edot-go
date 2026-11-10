package edotgo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestConfigJSONAndStructuredLogRedaction(t *testing.T) {
	c := ExporterConfig{Endpoint: "https://private-collector.example", Headers: map[string]string{"Authorization": "ApiKey dummy-private-secret"}}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	slog.New(slog.NewJSONHandler(&log, nil)).Info("configuration", "exporter", c)
	for _, output := range []string{string(data), log.String()} {
		if strings.Contains(output, "dummy-private-secret") || strings.Contains(output, "private-collector.example") {
			t.Error("JSON/structured logging exposed private exporter configuration")
		}
	}
}

func TestOTLPHTTPDoesNotFollowRedirects(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "http/json"} {
		for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
			t.Run(protocol+"/"+signal, func(t *testing.T) {
				cleanEnv(t)
				var received atomic.Int32
				destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					received.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "application/x-protobuf")
				}))
				defer destination.Close()
				source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Location", destination.URL+r.URL.Path)
					w.WriteHeader(http.StatusTemporaryRedirect)
				}))
				defer source.Close()
				c := DefaultConfig()
				c.OTLP = ExporterConfig{Endpoint: source.URL, Protocol: protocol, Headers: map[string]string{"Authorization": "ApiKey dummy-secret-only", "X-Private-Key": "dummy-custom-secret"}}
				send := configuredFaultSender(t, signal, c.exporterFor(signal))
				err := send(context.Background())
				if received.Load() != 0 {
					t.Error("redirect destination received telemetry/headers")
				}
				if err == nil {
					t.Error("redirect was treated as delivery")
				}
			})
		}
	}
}

func TestJSONMalformedResponseDoesNotEchoServerData(t *testing.T) {
	for signal, field := range map[string]string{"TRACES": "rejectedSpans", "METRICS": "rejectedDataPoints", "LOGS": "rejectedLogRecords"} {
		t.Run(signal, func(t *testing.T) {
			cleanEnv(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"partialSuccess":{"`+field+`":"dummy-reflected-server-secret"}}`)
			}))
			defer server.Close()
			c := DefaultConfig()
			c.OTLP = ExporterConfig{Protocol: "http/json", Endpoint: server.URL}
			err := configuredFaultSender(t, signal, c.exporterFor(signal))(context.Background())
			if err == nil {
				t.Fatal("malformed response accepted")
			}
			if strings.Contains(err.Error(), "dummy-reflected-server-secret") {
				t.Fatal("response parser echoed server content")
			}
		})
	}
}

func TestHTTPResponseSizeBound(t *testing.T) {
	cleanEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = io.Copy(w, io.LimitReader(zeroReader{}, maxJSONBody*2))
	}))
	defer server.Close()
	c := DefaultConfig()
	c.OTLP = ExporterConfig{Protocol: "http/protobuf", Endpoint: server.URL}
	err := configuredFaultSender(t, "TRACES", c.exporterFor("TRACES"))(context.Background())
	if err == nil || (!strings.Contains(err.Error(), "exceeds 16 MiB") && !strings.Contains(err.Error(), "body too large")) {
		t.Fatalf("response not bounded: %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestUntrustedTLSCertificateRejected(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "http/json"} {
		for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
			t.Run(protocol+"/"+signal, func(t *testing.T) {
				cleanEnv(t)
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS connection reached handler") }))
				defer server.Close()
				c := DefaultConfig()
				c.OTLP = ExporterConfig{Protocol: protocol, Endpoint: server.URL}
				if err := configuredFaultSender(t, signal, c.exporterFor(signal))(context.Background()); err == nil {
					t.Fatal("untrusted server certificate accepted")
				}
			})
		}
	}
}
