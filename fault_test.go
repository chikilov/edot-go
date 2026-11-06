package edotgo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	logpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Exercise the production exporter factories directly so async SDK error
// reporting cannot hide a failed export from the assertions.
func faultSender(t *testing.T, signal string) func(context.Context) error {
	return makeFaultSender(t, signal)
}
func makeFaultSender(t *testing.T, signal string, supplied ...exporterSettings) func(context.Context) error {
	t.Helper()
	ctx := context.Background()
	switch signal {
	case "TRACES":
		e, err := newTraceExporter(ctx, supplied...)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = e.Shutdown(context.Background()) })
		s := tracetest.SpanStub{Name: "fault-test", StartTime: time.Now(), EndTime: time.Now(), Resource: resource.Empty(), SpanContext: trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled})}.Snapshot()
		return func(ctx context.Context) error { return e.ExportSpans(ctx, []sdktrace.ReadOnlySpan{s}) }
	case "METRICS":
		e, err := newMetricExporter(ctx, supplied...)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = e.Shutdown(context.Background()) })
		data := &sdkmetric.ResourceMetrics{Resource: resource.Empty(), ScopeMetrics: []sdkmetric.ScopeMetrics{{Metrics: []sdkmetric.Metrics{{Name: "fault.test", Data: sdkmetric.Sum[int64]{Temporality: sdkmetric.DeltaTemporality, IsMonotonic: true, DataPoints: []sdkmetric.DataPoint[int64]{{Value: 1, Time: time.Now()}}}}}}}}
		return func(ctx context.Context) error { return e.Export(ctx, data) }
	case "LOGS":
		e, err := newLogExporter(ctx, supplied...)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = e.Shutdown(context.Background()) })
		var r sdklog.Record
		r.SetBody(attribute.StringValue("fault-test"))
		r.SetTimestamp(time.Now())
		return func(ctx context.Context) error { return e.Export(ctx, []sdklog.Record{r}) }
	default:
		t.Fatalf("unknown signal %s", signal)
		return nil
	}
}

func TestHTTPRetryAndPermanentFailure(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "http/json"} {
		for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
			for _, code := range []int{429, 503, 400} {
				t.Run(protocol+"/"+signal+"/"+strconv.Itoa(code), func(t *testing.T) {
					cleanEnv(t)
					var attempts atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						_, _ = io.Copy(io.Discard, r.Body)
						if attempts.Add(1) == 1 || code == 400 {
							w.Header().Set("Retry-After", "1")
							w.WriteHeader(code)
							_, _ = io.WriteString(w, "injected failure")
							return
						}
						if protocol == "http/json" {
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, `{}`)
						} else {
							w.Header().Set("Content-Type", "application/x-protobuf")
						}
					}))
					t.Cleanup(server.Close)
					t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", protocol)
					t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
					send := faultSender(t, signal)
					ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
					defer cancel()
					err := send(ctx)
					if code == 400 {
						if err == nil || attempts.Load() != 1 {
							t.Fatalf("permanent failure: attempts=%d err=%v", attempts.Load(), err)
						}
					} else if err != nil || attempts.Load() != 2 {
						t.Fatalf("retry: attempts=%d err=%v", attempts.Load(), err)
					}
				})
			}
		}
	}
}

func TestUnreachableEndpointDeadline(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "http/json", "grpc"} {
		for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
			t.Run(protocol+"/"+signal, func(t *testing.T) {
				cleanEnv(t)
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				address := l.Addr().String()
				_ = l.Close()
				t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", protocol)
				t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+address)
				send := faultSender(t, signal)
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				start := time.Now()
				if err := send(ctx); err == nil {
					t.Fatal("unreachable endpoint succeeded")
				}
				if time.Since(start) > 2*time.Second {
					t.Fatal("export exceeded deadline bound")
				}
			})
		}
	}
}

func TestSlowHTTPServerTimeout(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "http/json"} {
		for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
			t.Run(protocol+"/"+signal, func(t *testing.T) {
				cleanEnv(t)
				release := make(chan struct{})
				var attempts atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					attempts.Add(1)
					select {
					case <-release:
					case <-r.Context().Done():
					}
				}))
				t.Cleanup(server.Close)
				t.Cleanup(func() { close(release) })
				t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", protocol)
				t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
				t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "75")
				send := faultSender(t, signal)
				ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
				defer cancel()
				start := time.Now()
				if err := send(ctx); err == nil {
					t.Fatal("slow server unexpectedly succeeded")
				}
				if time.Since(start) > 2*time.Second {
					t.Fatal("slow server not bounded")
				}
				if attempts.Load() == 0 {
					t.Fatal("server was not reached")
				}
			})
		}
	}
}

func TestGRPCTransientAndPermanentFailure(t *testing.T) {
	for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
		for _, permanent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/permanent=%t", signal, permanent), func(t *testing.T) {
				cleanEnv(t)
				var attempts atomic.Int32
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
					n := attempts.Add(1)
					if permanent {
						return nil, status.Error(codes.InvalidArgument, "injected permanent failure")
					}
					if n == 1 {
						st, e := status.New(codes.Unavailable, "injected outage").WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(5 * time.Millisecond)})
						if e != nil {
							return nil, e
						}
						return nil, st.Err()
					}
					return handler(ctx, req)
				}))
				t.Cleanup(server.Stop)
				capture := &wireCapture{}
				tracepb.RegisterTraceServiceServer(server, &traceReceiver{capture: capture, t: t})
				metricpb.RegisterMetricsServiceServer(server, &metricReceiver{capture: capture, t: t})
				logpb.RegisterLogsServiceServer(server, &logReceiver{capture: capture, t: t})
				go func() { _ = server.Serve(l) }()
				t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
				t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+l.Addr().String())
				t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=ApiKey%20grpc-test")
				send := faultSender(t, signal)
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				err = send(ctx)
				if permanent {
					if err == nil || attempts.Load() != 1 {
						t.Fatalf("permanent gRPC failure attempts=%d err=%v", attempts.Load(), err)
					}
				} else if err != nil || attempts.Load() != 2 {
					t.Fatalf("gRPC retry attempts=%d err=%v", attempts.Load(), err)
				}
			})
		}
	}
}

type blockedSpanExporter struct {
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
	exported atomic.Int32
}

func (e *blockedSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
		e.exported.Add(int32(len(spans)))
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (e *blockedSpanExporter) Shutdown(context.Context) error { return nil }

func TestQueueFullDropsWithoutBlockingApplication(t *testing.T) {
	e := &blockedSpanExporter{started: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(e.release) })
	batch := sdktrace.NewBatchSpanProcessor(e, sdktrace.WithMaxQueueSize(2), sdktrace.WithMaxExportBatchSize(1), sdktrace.WithBatchTimeout(time.Hour), sdktrace.WithExportTimeout(5*time.Second))
	p := newStackProcessor(batch, 0, nil)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(p))
	defer tp.Shutdown(context.Background())
	tr := tp.Tracer("queue")
	_, s := tr.Start(context.Background(), "in-flight")
	s.End()
	select {
	case <-e.started:
	case <-time.After(2 * time.Second):
		t.Fatal("export did not start")
	}
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		for i := 0; i < 3; i++ {
			_, s := tr.Start(context.Background(), "queued")
			s.End()
		}
	}()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("full queue blocked application")
	}
	p.mu.Lock()
	remaining := p.slots
	p.mu.Unlock()
	if remaining != 0 {
		t.Error("queue overflow retained stack captures")
	}
	release.Do(func() { close(e.release) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := tp.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if e.exported.Load() != 3 {
		t.Fatalf("expected in-flight + 2 queued spans, got %d", e.exported.Load())
	}
}

func TestShutdownDuringBlockedHTTPExport(t *testing.T) {
	cleanEnv(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
	t.Setenv("OTEL_BSP_MAX_EXPORT_BATCH_SIZE", "1")
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "1000")
	c := DefaultConfig()
	c.Metrics = false
	c.Logs = false
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	_, span := s.TracerProvider.Tracer("shutdown").Start(context.Background(), "blocked")
	span.End()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("export did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = s.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
	if next := s.Shutdown(context.Background()); !errors.Is(next, context.DeadlineExceeded) {
		t.Fatalf("shutdown result not stable: %v", next)
	}
	if !strings.Contains(err.Error(), "deadline") {
		t.Error("missing timeout diagnostic")
	}
}

func TestPersistentOutageRespectsDeadline(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "http/json"} {
		for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
			t.Run(protocol+"/"+signal, func(t *testing.T) {
				cleanEnv(t)
				var attempts atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					attempts.Add(1)
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(503)
				}))
				t.Cleanup(server.Close)
				t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", protocol)
				t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)
				send := faultSender(t, signal)
				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
				defer cancel()
				start := time.Now()
				err := send(ctx)
				if err == nil {
					t.Fatal("persistent outage unexpectedly succeeded")
				}
				if time.Since(start) > 2*time.Second {
					t.Fatal("outage exceeded caller deadline")
				}
				if attempts.Load() != 1 {
					t.Fatalf("unexpected retry during throttling: %d", attempts.Load())
				}
			})
		}
	}
}
