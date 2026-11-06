package edotgo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type diagnosticTraceExporter struct {
	sdktrace.SpanExporter
	err error
}

func (e diagnosticTraceExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return e.err
}

type diagnosticMetricExporter struct {
	sdkmetric.Exporter
	err error
}

func (e diagnosticMetricExporter) Export(context.Context, *metricdata.ResourceMetrics) error {
	return e.err
}

type diagnosticLogExporter struct {
	sdklog.Exporter
	err error
}

func (e diagnosticLogExporter) Export(context.Context, []sdklog.Record) error { return e.err }

func TestDiagnosticsGatesAndFailures(t *testing.T) {
	for _, signal := range []string{"traces", "metrics", "logs"} {
		t.Run(signal, func(t *testing.T) {
			var enabled atomic.Bool
			var events []ExportError
			d := &diagnosticState{callback: func(e ExportError) { events = append(events, e) }}
			run := func(err error) error {
				switch signal {
				case "traces":
					return (spanGate{diagnosticTraceExporter{err: err}, &enabled, d}).ExportSpans(context.Background(), make([]sdktrace.ReadOnlySpan, 2))
				case "metrics":
					return (metricGate{diagnosticMetricExporter{err: err}, &enabled, d}).Export(context.Background(), &metricdata.ResourceMetrics{ScopeMetrics: []metricdata.ScopeMetrics{{Metrics: []metricdata.Metrics{{Data: metricdata.Sum[int64]{DataPoints: make([]metricdata.DataPoint[int64], 2)}}}}}})
				default:
					return (logGate{diagnosticLogExporter{err: err}, &enabled, d}).Export(context.Background(), make([]sdklog.Record, 2))
				}
			}
			if err := run(nil); err != nil {
				t.Fatal(err)
			}
			enabled.Store(true)
			if err := run(nil); err != nil {
				t.Fatal(err)
			}
			raw := errors.New("secret-api-key at https://private-endpoint with payload")
			if err := run(raw); err != raw {
				t.Fatal("changed upstream error semantics")
			}
			stats := d.counters(signal).snapshot()
			if stats.ExportCalls != 2 || stats.SuccessfulBatches != 1 || stats.FailedBatches != 1 || stats.SuccessfulItems != 2 || stats.ItemsInFailedBatches != 2 || stats.DisabledDroppedItems != 2 || stats.InFlightBatches != 0 {
				t.Fatalf("bad counters %+v", stats)
			}
			if len(events) != 1 || events[0].Signal != signal || events[0].Kind != "export" || events[0].Items != 2 {
				t.Error("bad callback")
			}
			data, _ := json.Marshal(struct {
				Stats  SignalDiagnostics
				Events []ExportError
			}{stats, events})
			for _, secret := range []string{"secret-api-key", "private-endpoint", "payload"} {
				if strings.Contains(string(data), secret) {
					t.Fatal("diagnostics leaked exporter error")
				}
			}
			if stats.LastSuccessAt.IsZero() || stats.LastFailureAt.IsZero() {
				t.Error("missing timestamps")
			}
		})
	}
}

func TestDiagnosticConcurrentSnapshotsAndCallbackPanic(t *testing.T) {
	d := &diagnosticState{callback: func(ExportError) { panic("user callback") }}
	s := &SDK{diagnostics: d}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				d.begin("traces")
				d.finish("traces", 1, context.DeadlineExceeded)
				_ = s.Diagnostics()
			}
		}()
	}
	wg.Wait()
	got := s.Diagnostics()
	if got.Traces.ExportCalls != 800 || got.Traces.FailedBatches != 800 || got.CallbackPanics != 800 || got.Traces.LastFailureKind != "deadline" {
		t.Errorf("bad concurrent state %+v", got)
	}
	if got.QueueDepthAvailable || got.QueueOverflowCountAvailable {
		t.Error("claimed unavailable upstream queue statistics")
	}
	d.begin("logs")
	if s.Diagnostics().Logs.InFlightBatches != 1 {
		t.Fatal("missing in-flight operation")
	}
	d.finish("logs", 1, context.Canceled)
	if s.Diagnostics().Logs.LastFailureKind != "canceled" {
		t.Error("canceled classification")
	}
}

func TestMetricItemCounting(t *testing.T) {
	m := &metricdata.ResourceMetrics{ScopeMetrics: []metricdata.ScopeMetrics{{Metrics: []metricdata.Metrics{
		{Data: metricdata.Sum[int64]{DataPoints: make([]metricdata.DataPoint[int64], 1)}},
		{Data: metricdata.Sum[float64]{DataPoints: make([]metricdata.DataPoint[float64], 2)}},
		{Data: metricdata.Gauge[int64]{DataPoints: make([]metricdata.DataPoint[int64], 3)}},
		{Data: metricdata.Gauge[float64]{DataPoints: make([]metricdata.DataPoint[float64], 4)}},
		{Data: metricdata.Histogram[int64]{DataPoints: make([]metricdata.HistogramDataPoint[int64], 5)}},
		{Data: metricdata.Histogram[float64]{DataPoints: make([]metricdata.HistogramDataPoint[float64], 6)}},
		{Data: metricdata.ExponentialHistogram[int64]{DataPoints: make([]metricdata.ExponentialHistogramDataPoint[int64], 7)}},
		{Data: metricdata.ExponentialHistogram[float64]{DataPoints: make([]metricdata.ExponentialHistogramDataPoint[float64], 8)}},
		{Data: metricdata.Summary{DataPoints: make([]metricdata.SummaryDataPoint, 9)}},
	}}}}
	if metricItems(m) != 45 || metricItems(nil) != 0 {
		t.Error("incorrect metric point count")
	}
}

func TestDiagnosticGRPCClassification(t *testing.T) {
	d := &diagnosticState{}
	d.begin("traces")
	d.finish("traces", 1, status.Error(codes.DeadlineExceeded, "sensitive detail"))
	if got := d.traces.snapshot(); got.LastFailureKind != "deadline" {
		t.Fatalf("wrong gRPC classification %+v", got)
	}
	d.begin("traces")
	d.finish("traces", 1, status.Error(codes.Canceled, "sensitive detail"))
	if got := d.traces.snapshot(); got.LastFailureKind != "canceled" {
		t.Fatalf("wrong gRPC classification %+v", got)
	}
}
