package edotgo

import (
	"context"
	"errors"
	logpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProtocolSpecificHeaderValidation(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, key, value string
		valid                      bool
	}{
		{"http-control-name", "http/protobuf", "X-\x00Token", "safe", false},
		{"http-unicode-name", "http/json", "X-한글", "safe", false},
		{"http-control-value", "http/protobuf", "Authorization", "secret\x00token", false},
		{"http-del-value", "http/json", "Authorization", "secret\x7ftoken", false},
		{"http-tab-value", "http/protobuf", "Authorization", "ApiKey\ttoken", true},
		{"http-token-punctuation", "http/json", "X!Token", "safe", true},
		{"grpc-punctuation", "grpc", "X!Token", "safe", false},
		{"grpc-unicode", "grpc", "Authorization", "ApiKey 한글", false},
		{"grpc-unicode-name", "grpc", "X-K", "safe", false},
		{"grpc-upper-name", "grpc", "Authorization", "ApiKey token", true},
		{"grpc-binary", "grpc", "payload-bin", "\x00\xff\r\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := exporterSettings{ExporterConfig: ExporterConfig{Protocol: tc.protocol, Headers: map[string]string{tc.key: tc.value}}}
			err := s.validate()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, got error %v", tc.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), tc.value) {
				t.Error("validation exposed header value")
			}
		})
	}
}

func TestJSONSuccessResponseValidation(t *testing.T) {
	for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
		for _, tc := range []struct {
			name, contentType, body string
			fail                    bool
		}{
			{"html-login", "text/html", "<html>login required</html>", true},
			{"missing-content-type", "", `{"partialSuccess":{"rejectedLogRecords":"1"}}`, true},
			{"empty-success", "text/plain", "", false},
			{"json-unknown-field", "application/json; charset=utf-8", `{"futureField":42}`, false},
		} {
			t.Run(signal+"/"+tc.name, func(t *testing.T) {
				cleanEnv(t)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					if tc.contentType != "" {
						w.Header().Set("Content-Type", tc.contentType)
					}
					w.WriteHeader(200)
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				c := DefaultConfig()
				c.OTLP = ExporterConfig{Protocol: "http/json", Endpoint: server.URL}
				send := configuredFaultSender(t, signal, c.exporterFor(signal))
				err := send(context.Background())
				if (err != nil) != tc.fail {
					t.Fatalf("expected failure=%v, got %v", tc.fail, err)
				}
			})
		}
	}
}

func TestForceFlushDoesNotStarveOtherSignals(t *testing.T) {
	cleanEnv(t)
	release := make(chan struct{})
	var once sync.Once
	var metrics, logs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/v1/traces":
			select {
			case <-release:
			case <-r.Context().Done():
			}
		case "/v1/metrics":
			metrics.Add(1)
		case "/v1/logs":
			logs.Add(1)
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	c := DefaultConfig()
	c.RuntimeMetrics = false
	c.OTLP = ExporterConfig{Protocol: "http/protobuf", Endpoint: server.URL, Timeout: time.Second}
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		once.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	}()
	ctx, span := s.TracerProvider.Tracer("flush").Start(context.Background(), "slow trace")
	s.Logger("flush").InfoContext(ctx, "log before shutdown")
	counter, err := s.MeterProvider.Meter("flush").Int64Counter("flush.test")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(ctx, 1)
	span.End()
	flush, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = s.ForceFlush(flush)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected shared deadline, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("flush exceeded shared deadline")
	}
	if metrics.Load() == 0 || logs.Load() == 0 {
		t.Errorf("blocked trace starved other signals: metrics=%d logs=%d", metrics.Load(), logs.Load())
	}
}

func TestDuplicateHeaderNamesRejected(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "http/json", "grpc"} {
		s := exporterSettings{ExporterConfig: ExporterConfig{Protocol: protocol, Headers: map[string]string{"Authorization": "secret-one", "authorization": "secret-two"}}}
		if err := s.validate(); err == nil {
			t.Fatalf("%s accepted ambiguous authentication headers", protocol)
		}
	}
}

func TestGRPCBinaryMetadataRoundTrip(t *testing.T) {
	cleanEnv(t)
	binary := "\x00\xff\r\n"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		got := md.Get("payload-bin")
		if len(got) != 1 || got[0] != binary {
			t.Error("binary metadata corrupted on wire")
		}
		return next(ctx, req)
	}))
	defer server.Stop()
	capture := &wireCapture{}
	tracepb.RegisterTraceServiceServer(server, &traceReceiver{capture: capture, t: t})
	metricpb.RegisterMetricsServiceServer(server, &metricReceiver{capture: capture, t: t})
	logpb.RegisterLogsServiceServer(server, &logReceiver{capture: capture, t: t})
	go func() { _ = server.Serve(listener) }()
	c := DefaultConfig()
	c.OTLP = ExporterConfig{Protocol: "grpc", Endpoint: "http://" + listener.Addr().String(), Headers: map[string]string{"Authorization": "ApiKey grpc-test", "Payload-Bin": binary}}
	for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
		send := configuredFaultSender(t, signal, c.exporterFor(signal))
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := send(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestJSONExporterProtobufResponseCompatibility(t *testing.T) {
	for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
		var response proto.Message
		switch signal {
		case "TRACES":
			response = &tracepb.ExportTraceServiceResponse{PartialSuccess: &tracepb.ExportTracePartialSuccess{RejectedSpans: 1, ErrorMessage: "rejected"}}
		case "METRICS":
			response = &metricpb.ExportMetricsServiceResponse{PartialSuccess: &metricpb.ExportMetricsPartialSuccess{RejectedDataPoints: 1, ErrorMessage: "rejected"}}
		case "LOGS":
			response = &logpb.ExportLogsServiceResponse{PartialSuccess: &logpb.ExportLogsPartialSuccess{RejectedLogRecords: 1, ErrorMessage: "rejected"}}
		}
		partial, err := proto.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			body []byte
			fail bool
		}{{"empty", nil, false}, {"partial", partial, true}, {"malformed", []byte{0xff}, true}} {
			t.Run(signal+"/"+tc.name, func(t *testing.T) {
				cleanEnv(t)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "application/x-protobuf; charset=binary")
					w.WriteHeader(200)
					_, _ = w.Write(tc.body)
				}))
				defer server.Close()
				c := DefaultConfig()
				c.OTLP = ExporterConfig{Protocol: "http/json", Endpoint: server.URL}
				send := configuredFaultSender(t, signal, c.exporterFor(signal))
				err := send(context.Background())
				if (err != nil) != tc.fail {
					t.Fatalf("expected failure=%v, got %v", tc.fail, err)
				}
			})
		}
	}
}

func TestShutdownDoesNotStarveOtherSignals(t *testing.T) {
	cleanEnv(t)
	release := make(chan struct{})
	var once sync.Once
	var metrics, logs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/v1/traces":
			select {
			case <-release:
			case <-r.Context().Done():
			}
		case "/v1/metrics":
			metrics.Add(1)
		case "/v1/logs":
			logs.Add(1)
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	c := DefaultConfig()
	c.RuntimeMetrics = false
	c.OTLP = ExporterConfig{Protocol: "http/protobuf", Endpoint: server.URL, Timeout: time.Second}
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		once.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	}()
	ctx, span := s.TracerProvider.Tracer("flush").Start(context.Background(), "slow trace")
	s.Logger("flush").InfoContext(ctx, "log before shutdown")
	counter, err := s.MeterProvider.Meter("flush").Int64Counter("flush.test")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(ctx, 1)
	span.End()
	flush, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = s.Shutdown(flush)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected shared deadline, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("flush exceeded shared deadline")
	}
	if metrics.Load() == 0 || logs.Load() == 0 {
		t.Errorf("blocked trace starved other signals: metrics=%d logs=%d", metrics.Load(), logs.Load())
	}
	if next := s.Shutdown(context.Background()); !errors.Is(next, context.DeadlineExceeded) {
		t.Fatalf("shutdown result changed: %v", next)
	}
}
