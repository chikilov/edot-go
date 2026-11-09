package edotgo

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	logpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestInvalidPipelineSettings(t *testing.T) {
	for _, batch := range []BatchConfig{{MaxQueueSize: -1}, {MaxExportBatchSize: -1}, {ExportInterval: -1}, {ExportTimeout: -1}, {MaxQueueSize: 2, MaxExportBatchSize: 3}} {
		for _, signal := range []string{"traces", "logs"} {
			c := DefaultConfig()
			if signal == "traces" {
				c.TraceBatch = batch
			} else {
				c.LogBatch = batch
			}
			if _, err := New(context.Background(), c); err == nil {
				t.Errorf("accepted invalid %s batch config", signal)
			}
		}
	}
	for _, reader := range []MetricReaderConfig{{Interval: -1}, {Timeout: -1}, {MaxExportBatchSize: -1}} {
		c := DefaultConfig()
		c.MetricReader = reader
		if _, err := New(context.Background(), c); err == nil {
			t.Error("accepted invalid metric reader config")
		}
	}
}

func TestPipelineCodeOverridesAndZeroInheritsEnvironment(t *testing.T) {
	for _, explicit := range []bool{true, false} {
		name := "zero-inherits-env"
		if explicit {
			name = "code-overrides-env"
		}
		t.Run(name, func(t *testing.T) {
			cleanEnv(t)
			for _, prefix := range []string{"OTEL_BSP_", "OTEL_BLRP_"} {
				t.Setenv(prefix+"MAX_QUEUE_SIZE", "1")
				t.Setenv(prefix+"MAX_EXPORT_BATCH_SIZE", "1")
				if explicit {
					t.Setenv(prefix+"SCHEDULE_DELAY", "60000")
				} else {
					t.Setenv(prefix+"SCHEDULE_DELAY", "25")
				}
			}
			if explicit {
				t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "60000")
			} else {
				t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "25")
			}
			var traces, logs, points, maxTrace, maxLog atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				var n int64
				switch r.URL.Path {
				case "/v1/traces":
					m := new(tracepb.ExportTraceServiceRequest)
					if err := proto.Unmarshal(b, m); err != nil {
						t.Error(err)
						return
					}
					for _, rs := range m.ResourceSpans {
						for _, ss := range rs.ScopeSpans {
							n += int64(len(ss.Spans))
						}
					}
					if n > 4 {
						t.Error("trace batch exceeded code limit")
					}
					traces.Add(n)
					if n > maxTrace.Load() {
						maxTrace.Store(n)
					}
				case "/v1/logs":
					m := new(logpb.ExportLogsServiceRequest)
					if err := proto.Unmarshal(b, m); err != nil {
						t.Error(err)
						return
					}
					for _, rs := range m.ResourceLogs {
						for _, ss := range rs.ScopeLogs {
							n += int64(len(ss.LogRecords))
						}
					}
					if n > 4 {
						t.Error("log batch exceeded code limit")
					}
					logs.Add(n)
					if n > maxLog.Load() {
						maxLog.Store(n)
					}
				case "/v1/metrics":
					m := new(metricpb.ExportMetricsServiceRequest)
					if err := proto.Unmarshal(b, m); err != nil {
						t.Error(err)
						return
					}
					for _, rs := range m.ResourceMetrics {
						for _, ss := range rs.ScopeMetrics {
							for _, m := range ss.Metrics {
								n += int64(len(m.GetSum().GetDataPoints()))
							}
						}
					}
					if explicit && n > 2 {
						t.Error("metric batch exceeded code limit")
					}
					points.Add(n)
				}
				w.Header().Set("Content-Type", "application/x-protobuf")
			}))
			defer server.Close()
			c := DefaultConfig()
			c.RuntimeMetrics = false
			c.StacktraceMinDuration = -1
			c.OTLP = ExporterConfig{Endpoint: server.URL, Protocol: "http/protobuf"}
			count := 1
			if explicit {
				c.TraceBatch = BatchConfig{MaxQueueSize: 32, MaxExportBatchSize: 4, ExportInterval: 25 * time.Millisecond, ExportTimeout: time.Second}
				c.LogBatch = c.TraceBatch
				c.MetricReader = MetricReaderConfig{Interval: 25 * time.Millisecond, Timeout: time.Second, MaxExportBatchSize: 2}
				count = 10
			}
			s, err := New(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if err := s.Shutdown(ctx); err != nil {
					t.Error(err)
				}
			}()
			tr := s.TracerProvider.Tracer("batch-options")
			logger := s.Logger("batch-options")
			for i := 0; i < count; i++ {
				ctx, span := tr.Start(context.Background(), "request")
				logger.InfoContext(ctx, "record")
				span.End()
			}
			for _, name := range []string{"batch.a", "batch.b", "batch.c", "batch.d", "batch.e"} {
				counter, err := s.MeterProvider.Meter("batch-options").Int64Counter(name)
				if err != nil {
					t.Fatal(err)
				}
				counter.Add(context.Background(), 1)
			}
			until := time.Now().Add(3 * time.Second)
			for traces.Load() < int64(count) || logs.Load() < int64(count) || points.Load() < 5 {
				if time.Now().After(until) {
					t.Fatalf("periodic exports missing: traces=%d logs=%d points=%d", traces.Load(), logs.Load(), points.Load())
				}
				time.Sleep(5 * time.Millisecond)
			}
			if explicit && (maxTrace.Load() < 2 || maxLog.Load() < 2) {
				t.Fatal("environment batch size overrode code")
			}
			if os.Getenv("OTEL_BSP_MAX_QUEUE_SIZE") != "1" {
				t.Fatal("SDK changed process environment")
			}
		})
	}
}

func TestPipelineExportTimeouts(t *testing.T) {
	cleanEnv(t)
	for _, key := range []string{"OTEL_BSP_EXPORT_TIMEOUT", "OTEL_BLRP_EXPORT_TIMEOUT", "OTEL_METRIC_EXPORT_TIMEOUT"} {
		t.Setenv(key, "60000")
	}
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	c := DefaultConfig()
	c.RuntimeMetrics = false
	c.OTLP = ExporterConfig{Endpoint: server.URL, Protocol: "http/protobuf", Timeout: 5 * time.Second}
	c.TraceBatch = BatchConfig{MaxExportBatchSize: 1, ExportTimeout: 50 * time.Millisecond}
	c.LogBatch = c.TraceBatch
	c.MetricReader = MetricReaderConfig{Interval: 20 * time.Millisecond, Timeout: 50 * time.Millisecond}
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	}()
	ctx, span := s.TracerProvider.Tracer("timeouts").Start(context.Background(), "slow")
	s.Logger("timeouts").InfoContext(ctx, "slow")
	span.End()
	counter, err := s.MeterProvider.Meter("timeouts").Int64Counter("timeout.test")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(ctx, 1)
	until := time.Now().Add(2 * time.Second)
	for {
		d := s.Diagnostics()
		if d.Traces.LastFailureKind == "deadline" && d.Metrics.LastFailureKind == "deadline" && d.Logs.LastFailureKind == "deadline" {
			break
		}
		if time.Now().After(until) {
			t.Fatalf("code timeouts did not bound all signals: %+v", d)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
