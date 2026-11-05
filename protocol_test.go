package edotgo

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	logpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricdata "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type wireCapture struct {
	mu      sync.Mutex
	traces  *tracepb.ExportTraceServiceRequest
	metrics *metricpb.ExportMetricsServiceRequest
	logs    *logpb.ExportLogsServiceRequest
}

func (w *wireCapture) store(msg proto.Message) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch m := msg.(type) {
	case *tracepb.ExportTraceServiceRequest:
		w.traces = m
	case *metricpb.ExportMetricsServiceRequest:
		w.metrics = m
	case *logpb.ExportLogsServiceRequest:
		w.logs = m
	}
}
func emitProtocolData(t *testing.T, w *wireCapture) {
	t.Helper()
	s, err := NewFromEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	ctx, span := s.TracerProvider.Tracer("protocol-test").Start(context.Background(), "request")
	id := span.SpanContext()
	span.SetAttributes(attribute.String("test.transport", "verified"))
	s.Logger("protocol-test").InfoContext(ctx, "transport test")
	c, err := s.MeterProvider.Meter("protocol-test").Int64Counter("test.count")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(ctx, 9)
	span.End()
	flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.ForceFlush(flush); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.traces == nil || w.logs == nil || w.metrics == nil {
		t.Fatal("missing signal exports")
	}
	traceID := id.TraceID()
	spanID := id.SpanID()
	sp := w.traces.ResourceSpans[0].ScopeSpans[0].Spans[0]
	lr := w.logs.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if !bytes.Equal(sp.TraceId, traceID[:]) || !bytes.Equal(lr.TraceId, traceID[:]) || !bytes.Equal(lr.SpanId, spanID[:]) {
		t.Error("trace IDs corrupted in transport")
	}
	if sp.Name != "request" || lr.Body.GetStringValue() != "transport test" {
		t.Error("signal payload corrupted")
	}
	found := false
	for _, rm := range w.metrics.ResourceMetrics {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "test.count" {
					found = true
					if m.GetSum().AggregationTemporality != metricdata.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA || m.GetSum().DataPoints[0].GetAsInt() != 9 {
						t.Error("delta metric payload")
					}
				}
			}
		}
	}
	if !found {
		t.Error("missing metric")
	}
}

// Decode standard protobuf JSON after checking and converting OTLP hex IDs.
func unmarshalWireJSON(data []byte, msg proto.Message) error {
	var value any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return err
	}
	var visit func(any) error
	visit = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for k, item := range x {
				if k == "traceId" || k == "spanId" || k == "parentSpanId" {
					if s, ok := item.(string); ok {
						if len(s) != 0 && ((k == "traceId" && len(s) != 32) || (k != "traceId" && len(s) != 16)) {
							return fmt.Errorf("non-OTLP ID length for %s", k)
						}
						raw, err := hex.DecodeString(s)
						if err != nil {
							return err
						}
						x[k] = base64.StdEncoding.EncodeToString(raw)
						continue
					}
				}
				if err := visit(item); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range x {
				if err := visit(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(value); err != nil {
		return err
	}
	converted, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return protojson.Unmarshal(converted, msg)
}

func TestHTTPJSONWire(t *testing.T) {
	for _, mode := range []string{"plain", "gzip", "tls"} {
		t.Run(mode, func(t *testing.T) {
			cleanEnv(t)
			capture := &wireCapture{}
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("expected JSON content type")
				}
				if r.Header.Get("Authorization") != "ApiKey wire-test" {
					t.Error("JSON authentication missing")
				}
				if mode == "gzip" && r.Header.Get("Content-Encoding") != "gzip" {
					t.Error("expected gzip request")
				}
				data, err := readBody(r.Body, r.Header.Get("Content-Encoding"))
				if err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				var msg proto.Message
				switch r.URL.Path {
				case "/v1/traces":
					msg = &tracepb.ExportTraceServiceRequest{}
				case "/v1/metrics":
					msg = &metricpb.ExportMetricsServiceRequest{}
				case "/v1/logs":
					msg = &logpb.ExportLogsServiceRequest{}
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				if err := unmarshalWireJSON(data, msg); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				capture.store(msg)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				if mode == "gzip" {
					w.Header().Set("Content-Encoding", "gzip")
					gz := gzip.NewWriter(w)
					_, _ = gz.Write([]byte(`{}`))
					_ = gz.Close()
				} else {
					_, _ = io.WriteString(w, `{}`)
				}
			})
			var server *httptest.Server
			if mode == "tls" {
				server = httptest.NewTLSServer(h)
				file := filepath.Join(t.TempDir(), "ca.pem")
				if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", file)
			} else {
				server = httptest.NewServer(h)
			}
			defer server.Close()
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/json")
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
			t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=ApiKey%20wire-test")
			t.Setenv("EDOT_GO_RUNTIME_METRICS_ENABLED", "false")
			if mode == "gzip" {
				t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "gzip")
			}
			emitProtocolData(t, capture)
		})
	}
}

type traceReceiver struct {
	tracepb.UnimplementedTraceServiceServer
	capture *wireCapture
	t       *testing.T
}

func (s *traceReceiver) Export(ctx context.Context, r *tracepb.ExportTraceServiceRequest) (*tracepb.ExportTraceServiceResponse, error) {
	checkGRPCAuth(s.t, ctx)
	s.capture.store(r)
	return &tracepb.ExportTraceServiceResponse{}, nil
}

type metricReceiver struct {
	metricpb.UnimplementedMetricsServiceServer
	capture *wireCapture
	t       *testing.T
}

func (s *metricReceiver) Export(ctx context.Context, r *metricpb.ExportMetricsServiceRequest) (*metricpb.ExportMetricsServiceResponse, error) {
	checkGRPCAuth(s.t, ctx)
	s.capture.store(r)
	return &metricpb.ExportMetricsServiceResponse{}, nil
}

type logReceiver struct {
	logpb.UnimplementedLogsServiceServer
	capture *wireCapture
	t       *testing.T
}

func (s *logReceiver) Export(ctx context.Context, r *logpb.ExportLogsServiceRequest) (*logpb.ExportLogsServiceResponse, error) {
	checkGRPCAuth(s.t, ctx)
	s.capture.store(r)
	return &logpb.ExportLogsServiceResponse{}, nil
}
func checkGRPCAuth(t *testing.T, ctx context.Context) {
	t.Helper()
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("authorization"); len(v) != 1 || v[0] != "ApiKey grpc-test" {
		t.Error("gRPC metadata auth missing")
	}
}
func TestGRPCWire(t *testing.T) {
	cleanEnv(t)
	capture := &wireCapture{}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	defer server.Stop()
	tracepb.RegisterTraceServiceServer(server, &traceReceiver{capture: capture, t: t})
	metricpb.RegisterMetricsServiceServer(server, &metricReceiver{capture: capture, t: t})
	logpb.RegisterLogsServiceServer(server, &logReceiver{capture: capture, t: t})
	go func() { _ = server.Serve(l) }()
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+l.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=ApiKey%20grpc-test")
	t.Setenv("EDOT_GO_RUNTIME_METRICS_ENABLED", "false")
	emitProtocolData(t, capture)
}

func TestProtocolPrecedence(t *testing.T) {
	cleanEnv(t)
	for _, common := range []string{"http/protobuf", "http/json", "grpc"} {
		t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", common)
		if _, err := ConfigFromEnv(); err != nil {
			t.Fatal(err)
		}
		for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
			if got := protocolFor(signal); got != common {
				t.Errorf("%s common protocol %s", signal, got)
			}
		}
	}
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/json")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "http/protobuf")
	if protocolFor("TRACES") != "http/json" || protocolFor("METRICS") != "http/protobuf" || protocolFor("LOGS") != "grpc" {
		t.Fatal("signal overrides failed")
	}
	// Invalid common configuration is harmless if every enabled signal overrides it.
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "invalid")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "grpc")
	if _, err := ConfigFromEnv(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "invalid")
	if _, err := ConfigFromEnv(); err == nil {
		t.Error("accepted invalid signal protocol")
	}
}

func TestJSONPartialSuccessAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"partial", 200, `{"partialSuccess":{"rejectedLogRecords":"1","errorMessage":"denied"}}`, "denied"},
		{"bad-json", 200, `{broken`, "OTLP JSON response"},
		{"failure", 400, `{"message":"bad request"}`, "bad request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleanEnv(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/json")
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
			exp, err := newLogExporter(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer exp.Shutdown(context.Background())
			err = exp.Export(context.Background(), []sdklog.Record{{}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestJSONIntegerAndExemplarEncoding(t *testing.T) {
	const large = int64(9007199254740993)
	msg := &metricpb.ExportMetricsServiceRequest{ResourceMetrics: []*metricdata.ResourceMetrics{{ScopeMetrics: []*metricdata.ScopeMetrics{{Metrics: []*metricdata.Metric{{Name: "precision", Data: &metricdata.Metric_Sum{Sum: &metricdata.Sum{
		AggregationTemporality: metricdata.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
		DataPoints: []*metricdata.NumberDataPoint{{TimeUnixNano: 18446744073709551615, Value: &metricdata.NumberDataPoint_AsInt{AsInt: large},
			Attributes: []*commonpb.KeyValue{{Key: "raw", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BytesValue{BytesValue: []byte{1, 2, 3}}}}},
			Exemplars:  []*metricdata.Exemplar{{TraceId: bytes.Repeat([]byte{0xab}, 16), SpanId: bytes.Repeat([]byte{0xcd}, 8), Value: &metricdata.Exemplar_AsInt{AsInt: large}}},
		}},
	}}}}}}}}}
	data, err := marshalOTLPJSON(msg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"asInt":"9007199254740993"`, `"timeUnixNano":"18446744073709551615"`, `"aggregationTemporality":1`, `"traceId":"abababababababababababababababab"`, `"spanId":"cdcdcdcdcdcdcdcd"`, `"bytesValue":"AQID"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON missing %s", want)
		}
	}
	var decoded metricpb.ExportMetricsServiceRequest
	if err := unmarshalWireJSON(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(msg, &decoded) {
		t.Error("JSON roundtrip lost numeric precision or IDs")
	}
}

func TestMixedProtocols(t *testing.T) {
	cleanEnv(t)
	capture := &wireCapture{}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	defer server.Stop()
	metricpb.RegisterMetricsServiceServer(server, &metricReceiver{capture: capture, t: t})
	go func() { _ = server.Serve(l) }()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "ApiKey grpc-test" {
			t.Error("mixed HTTP auth")
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/custom/traces":
			if r.Header.Get("Content-Type") != "application/x-protobuf" {
				t.Error("traces should be protobuf")
			}
			msg := &tracepb.ExportTraceServiceRequest{}
			if err := proto.Unmarshal(data, msg); err != nil {
				t.Error(err)
			}
			capture.store(msg)
			w.Header().Set("Content-Type", "application/x-protobuf")
		case "/custom/logs":
			if r.Header.Get("Content-Type") != "application/json" {
				t.Error("logs should be JSON")
			}
			msg := &logpb.ExportLogsServiceRequest{}
			if err := unmarshalWireJSON(data, msg); err != nil {
				t.Error(err)
			}
			capture.store(msg)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected mixed endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer httpServer.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+l.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", httpServer.URL+"/custom/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_PROTOCOL", "http/json")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", httpServer.URL+"/custom/logs")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=ApiKey%20grpc-test")
	t.Setenv("EDOT_GO_RUNTIME_METRICS_ENABLED", "false")
	emitProtocolData(t, capture)
}
