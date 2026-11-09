package edotgo

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	logpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodeConfigurationPrecedenceAndRedaction(t *testing.T) {
	cleanEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "invalid")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "also-invalid")
	c := DefaultConfig()
	c.OTLP = ExporterConfig{Protocol: "http/json", Endpoint: "https://common.example/base", Headers: map[string]string{"Authorization": "common-secret"}, Timeout: 2 * time.Second, Compression: "gzip"}
	c.LogExporter = ExporterConfig{Endpoint: "https://logs.example/custom", Headers: map[string]string{}, Timeout: time.Second, Compression: "none"}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	logs := c.exporterFor("LOGS")
	traces := c.exporterFor("TRACES")
	if logs.Protocol != "http/json" || logs.endpoint("LOGS") != "https://logs.example/custom" || logs.Timeout != time.Second || logs.Compression != "none" || logs.Headers == nil || len(logs.Headers) != 0 {
		t.Fatal("signal settings did not override common settings")
	}
	if traces.endpoint("TRACES") != "https://common.example/base/v1/traces" || traces.Headers["Authorization"] != "common-secret" {
		t.Fatal("common code settings missing")
	}
	c.OTLP.Headers["Authorization"] = "changed"
	if traces.Headers["Authorization"] != "common-secret" {
		t.Fatal("resolved headers alias caller map")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		text := fmt.Sprintf(format, c)
		for _, secret := range []string{"common-secret", "changed", "common.example", "logs.example"} {
			if strings.Contains(text, secret) {
				t.Errorf("format %s leaked private setting", format)
			}
		}
	}
}

func TestInvalidCodeExporterSettings(t *testing.T) {
	for _, bad := range []ExporterConfig{
		{Protocol: "binary"}, {Endpoint: "not-a-url"}, {Endpoint: "https://user:password@example.com"},
		{Endpoint: "https://:4318"}, {Endpoint: "https://example.com:65536"},
		{Endpoint: "https://example.com:0"}, {Endpoint: "https://example.com:"}, {Endpoint: "https://example.com/?"},
		{Endpoint: "https://example.com/?token=secret"}, {Endpoint: "https://example.com/#secret"},
		{Protocol: "grpc", Endpoint: "https://example.com/v1/traces"}, {Timeout: -time.Second},
		{Compression: "brotli"}, {Headers: map[string]string{"Authorization": "bad\r\nheader"}},
		{Endpoint: "http://localhost:4318", TLSConfig: &tls.Config{}},
	} {
		cleanEnv(t)
		c := DefaultConfig()
		c.OTLP = bad
		_, err := New(context.Background(), c)
		if err == nil {
			t.Errorf("accepted invalid settings")
		}
		if err != nil && strings.Contains(err.Error(), "password") {
			t.Error("validation leaked credential")
		}
	}
}

func TestProgrammaticHTTPExport(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "http/json"} {
		for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
			t.Run(protocol+"/"+signal, func(t *testing.T) {
				cleanEnv(t)
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.URL.Path != "/prefix/v1/"+strings.ToLower(signal) {
						t.Errorf("wrong API endpoint %s", r.URL.Path)
					}
					if r.Header.Get("Authorization") != "ApiKey from-code" || r.Header.Get("X-Env") != "" {
						t.Error("headers not replaced")
					}
					if r.Header.Get("Content-Encoding") != "" {
						t.Error("code compression none did not override environment gzip")
					}
					_, _ = io.Copy(io.Discard, r.Body)
					if protocol == "http/json" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{}`)
					} else {
						w.Header().Set("Content-Type", "application/x-protobuf")
					}
				}))
				defer server.Close()
				t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
				t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=wrong,X-Env=present")
				t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "gzip")
				c := DefaultConfig()
				c.OTLP = ExporterConfig{Protocol: protocol, Endpoint: server.URL + "/prefix", Headers: map[string]string{"Authorization": "ApiKey from-code"}, Compression: "none", Timeout: time.Second}
				c.Traces = signal == "TRACES"
				c.Metrics = signal == "METRICS"
				c.Logs = signal == "LOGS"
				c.RuntimeMetrics = false
				s, err := New(context.Background(), c)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Shutdown(context.Background())
				ctx, span := s.TracerProvider.Tracer("api-test").Start(context.Background(), "request")
				s.Logger("api-test").InfoContext(ctx, "hello")
				counter, err := s.MeterProvider.Meter("api-test").Int64Counter("test.count")
				if err != nil {
					t.Fatal(err)
				}
				counter.Add(ctx, 1)
				span.End()
				if err := s.ForceFlush(context.Background()); err != nil {
					t.Fatal(err)
				}
				if requests.Load() == 0 {
					t.Fatal("explicit code endpoint was not reached")
				}
				stats := s.Diagnostics()
				var got SignalDiagnostics
				switch signal {
				case "TRACES":
					got = stats.Traces
				case "METRICS":
					got = stats.Metrics
				case "LOGS":
					got = stats.Logs
				}
				if got.SuccessfulItems != 1 || got.SuccessfulBatches != 1 || got.FailedBatches != 0 || got.LastSuccessAt.IsZero() {
					t.Errorf("incorrect export diagnostics %+v", got)
				}
			})
		}
	}
}

func TestProgrammaticTLSConfig(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "http/json"} {
		for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
			t.Run(protocol+"/"+signal, func(t *testing.T) {
				cleanEnv(t)
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					if protocol == "http/json" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{}`)
					} else {
						w.Header().Set("Content-Type", "application/x-protobuf")
					}
				}))
				defer server.Close()
				roots := x509.NewCertPool()
				roots.AddCert(server.Certificate())
				c := DefaultConfig()
				c.OTLP = ExporterConfig{Protocol: protocol, Endpoint: server.URL, TLSConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
				// Resolve to a clone; mutating the original root pool must not change it.
				settings := c.exporterFor(signal)
				if settings.TLSConfig == c.OTLP.TLSConfig || settings.TLSConfig.RootCAs == roots {
					t.Fatal("TLS config was not snapshotted")
				}
				send := configuredFaultSender(t, signal, settings)
				if err := send(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// Reuse the fault suite's real signal payloads with explicit factory settings.
func configuredFaultSender(t *testing.T, signal string, settings exporterSettings) func(context.Context) error {
	t.Helper()
	return makeFaultSender(t, signal, settings)
}

func TestProgrammaticGRPCExport(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("TLS=%t", secure), func(t *testing.T) {
			cleanEnv(t)
			capture := &wireCapture{}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var opts []grpc.ServerOption
			var apiTLS *tls.Config
			scheme := "http"
			if secure {
				certServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				defer certServer.Close()
				roots := x509.NewCertPool()
				roots.AddCert(certServer.Certificate())
				apiTLS = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
				scheme = "https"
				opts = append(opts, grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: certServer.TLS.Certificates, MinVersion: tls.VersionTLS12})))
			}
			server := grpc.NewServer(opts...)
			defer server.Stop()
			tracepb.RegisterTraceServiceServer(server, &traceReceiver{capture: capture, t: t})
			metricpb.RegisterMetricsServiceServer(server, &metricReceiver{capture: capture, t: t})
			logpb.RegisterLogsServiceServer(server, &logReceiver{capture: capture, t: t})
			go func() { _ = server.Serve(listener) }()
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/json")
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
			t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=wrong")
			c := DefaultConfig()
			c.OTLP = ExporterConfig{Protocol: "grpc", Endpoint: scheme + "://" + listener.Addr().String(), Headers: map[string]string{"Authorization": "ApiKey grpc-test"}, TLSConfig: apiTLS, Compression: "none", Timeout: time.Second}
			for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
				send := configuredFaultSender(t, signal, c.exporterFor(signal))
				if err := send(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			capture.mu.Lock()
			defer capture.mu.Unlock()
			if capture.traces == nil || capture.metrics == nil || capture.logs == nil {
				t.Fatal("missing gRPC signals")
			}
		})
	}
}

func TestProgrammaticTimeoutAndEmptyHeaders(t *testing.T) {
	cleanEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("empty code header map failed to clear environment authentication")
		}
		_, _ = io.Copy(io.Discard, r.Body)
		timer := time.NewTimer(80 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=from-env")
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "10")
	c := DefaultConfig()
	c.OTLP = ExporterConfig{Protocol: "http/json", Endpoint: server.URL, Headers: map[string]string{}, Timeout: time.Second}
	send := configuredFaultSender(t, "LOGS", c.exporterFor("LOGS"))
	if err := send(context.Background()); err != nil {
		t.Fatalf("code timeout did not override environment: %v", err)
	}
}

func TestEndpointAuthorityValidation(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "http/json", "grpc"} {
		for _, endpoint := range []string{"https://:4318", "https://example.com:65536", "https://example.com:0", "https://example.com:", "https://example.com/?"} {
			t.Run(protocol+"/"+endpoint, func(t *testing.T) {
				s := exporterSettings{ExporterConfig: ExporterConfig{Protocol: protocol, Endpoint: endpoint}}
				if err := s.validate(); err == nil {
					t.Fatal("accepted invalid endpoint")
				}
			})
		}
		for _, endpoint := range []string{"https://localhost", "http://localhost:4318", "https://[::1]:4318", "https://example.com:65535"} {
			s := exporterSettings{ExporterConfig: ExporterConfig{Protocol: protocol, Endpoint: endpoint}}
			if err := s.validate(); err != nil {
				t.Fatalf("valid endpoint rejected: %v", err)
			}
		}
	}
}
