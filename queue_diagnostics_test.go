package edotgo

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	apilog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type blockedQueueLogExporter struct {
	started, release chan struct{}
	once             sync.Once
}

func (e *blockedQueueLogExporter) Export(ctx context.Context, _ []sdklog.Record) error {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (e *blockedQueueLogExporter) ForceFlush(context.Context) error { return nil }
func (e *blockedQueueLogExporter) Shutdown(context.Context) error   { return nil }

func TestQueueDiagnosticsRealOverflow(t *testing.T) {
	t.Setenv("OTEL_GO_X_OBSERVABILITY", "true")
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	old := otel.GetMeterProvider()
	otel.SetMeterProvider(meter)
	defer otel.SetMeterProvider(old)
	defer meter.Shutdown(context.Background())
	exp := &blockedSpanExporter{started: make(chan struct{}), release: make(chan struct{})}
	logexp := &blockedQueueLogExporter{started: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(exp.release); close(logexp.release) }) }
	defer unblock()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(exp, sdktrace.WithMaxQueueSize(2), sdktrace.WithMaxExportBatchSize(1), sdktrace.WithBatchTimeout(time.Hour))))
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(logexp, sdklog.WithMaxQueueSize(2), sdklog.WithExportMaxBatchSize(1), sdklog.WithExportInterval(time.Hour))))
	defer func() { unblock(); _ = tp.Shutdown(context.Background()); _ = lp.Shutdown(context.Background()) }()
	emit := func() {
		_, span := tp.Tracer("queue-test").Start(context.Background(), "synthetic")
		span.End()
		var r apilog.Record
		r.SetBody(attribute.StringValue("synthetic"))
		lp.Logger("queue-test").Emit(context.Background(), r)
	}
	emit()
	for _, started := range []chan struct{}{exp.started, logexp.started} {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("export did not start")
		}
	}
	for i := 0; i < 5; i++ {
		emit()
	}
	snapshot := func() QueueDiagnostics {
		t.Helper()
		var data metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &data); err != nil {
			t.Fatal(err)
		}
		return QueueDiagnosticsFromMetrics(&data)
	}
	before := snapshot()
	if len(before.Processors) != 2 {
		t.Fatalf("missing observed queues: %+v", before)
	}
	for _, p := range before.Processors {
		if !p.DepthAvailable || !p.CapacityAvailable || p.Depth != 2 || p.Capacity != 2 {
			t.Fatalf("wrong saturated queue: %+v", p)
		}
		if p.Signal == "traces" && (!p.OverflowCountAvailable || p.QueueFullDropped != 3 || p.OverflowTemporality != "cumulative") {
			t.Fatalf("wrong trace drops: %+v", p)
		}
		if p.Signal == "logs" && (!p.OverflowReportingDelayed || p.OverflowCountAvailable) {
			t.Fatalf("logs should report drops only after dequeue: %+v", p)
		}
	}
	unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := tp.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lp.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	after := snapshot()
	for _, p := range after.Processors {
		if !p.DepthAvailable || p.Depth != 0 || !p.OverflowCountAvailable || p.QueueFullDropped != 3 {
			t.Fatalf("wrong drained queue/drops: %+v", p)
		}
	}
}

func TestQueueDiagnosticsMissingUnknownAndRedaction(t *testing.T) {
	if len(QueueDiagnosticsFromMetrics(nil).Processors) != 0 {
		t.Fatal("missing data invented observations")
	}
	attrs := attribute.NewSet(attribute.String("otel.component.name", "batching_span_processor/7"), attribute.String("Authorization", "private-secret"))
	data := metricdata.ResourceMetrics{ScopeMetrics: []metricdata.ScopeMetrics{{Scope: instrumentation.Scope{Name: "untrusted"}, Metrics: []metricdata.Metrics{{Name: "otel.sdk.processor.span.queue.size", Data: metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{{Value: 9, Attributes: attrs}}}}}}}}
	if len(QueueDiagnosticsFromMetrics(&data).Processors) != 0 {
		t.Fatal("accepted unrelated scope")
	}
	data.ScopeMetrics[0].Scope.Name = "go.opentelemetry.io/otel/sdk/trace/internal/observ"
	result := QueueDiagnosticsFromMetrics(&data)
	if len(result.Processors) != 1 || !result.Processors[0].DepthAvailable || result.Processors[0].OverflowCountAvailable || result.Processors[0].CapacityAvailable {
		t.Fatalf("missing values were not preserved: %+v", result)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "private-secret") || strings.Contains(string(encoded), "Authorization") {
		t.Fatal("unrelated attributes leaked")
	}
	data.ScopeMetrics[0].Metrics[0].Data = metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{{Value: 1, Attributes: attribute.NewSet(attribute.String("otel.component.name", "private-endpoint"))}}}
	if len(QueueDiagnosticsFromMetrics(&data).Processors) != 0 {
		t.Fatal("accepted private arbitrary component identifier")
	}
}

func TestQueueDiagnosticsDeltaIsNotLifetimeTotal(t *testing.T) {
	attrs := attribute.NewSet(attribute.String("otel.component.name", "batching_span_processor/2"), attribute.String("error.type", "queue_full"))
	data := metricdata.ResourceMetrics{ScopeMetrics: []metricdata.ScopeMetrics{{Scope: instrumentation.Scope{Name: "go.opentelemetry.io/otel/sdk/trace/internal/observ"}, Metrics: []metricdata.Metrics{{Name: "otel.sdk.processor.span.processed", Data: metricdata.Sum[int64]{Temporality: metricdata.DeltaTemporality, IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{Value: 3, Attributes: attrs}}}}}}}}
	got := QueueDiagnosticsFromMetrics(&data).Processors
	if len(got) != 1 || got[0].OverflowTemporality != "delta" || got[0].QueueFullDropped != 3 || !got[0].OverflowCountAvailable {
		t.Fatalf("lost temporality: %+v", got)
	}
}

func TestQueueDiagnosticsDeltaQueueIsNotCurrentDepth(t *testing.T) {
	attrs := attribute.NewSet(attribute.String("otel.component.name", "batching_span_processor/3"))
	data := metricdata.ResourceMetrics{ScopeMetrics: []metricdata.ScopeMetrics{{Scope: instrumentation.Scope{Name: "go.opentelemetry.io/otel/sdk/trace/internal/observ"}, Metrics: []metricdata.Metrics{{Name: "otel.sdk.processor.span.queue.size", Data: metricdata.Sum[int64]{Temporality: metricdata.DeltaTemporality, IsMonotonic: false, DataPoints: []metricdata.DataPoint[int64]{{Value: 2, Attributes: attrs}}}}}}}}
	if len(QueueDiagnosticsFromMetrics(&data).Processors) != 0 {
		t.Fatal("delta queue change presented as current depth")
	}
}
