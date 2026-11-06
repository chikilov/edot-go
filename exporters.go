// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"context"
	"sync/atomic"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Disabled sending drops batches at export time; it is not a persistent queue
// and does not interrupt a request that is already in flight.
type spanGate struct {
	sdktrace.SpanExporter
	enabled     *atomic.Bool
	diagnostics *diagnosticState
}

func (e spanGate) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	items := uint64(len(spans))
	if !e.enabled.Load() {
		e.diagnostics.dropped("traces", items)
		return nil
	}
	e.diagnostics.begin("traces")
	err := e.SpanExporter.ExportSpans(ctx, spans)
	e.diagnostics.finish("traces", items, err)
	return err
}

type metricGate struct {
	sdkmetric.Exporter
	enabled     *atomic.Bool
	diagnostics *diagnosticState
}

func (e metricGate) Export(ctx context.Context, data *metricdata.ResourceMetrics) error {
	items := metricItems(data)
	if !e.enabled.Load() {
		e.diagnostics.dropped("metrics", items)
		return nil
	}
	e.diagnostics.begin("metrics")
	err := e.Exporter.Export(ctx, data)
	e.diagnostics.finish("metrics", items, err)
	return err
}

type logGate struct {
	sdklog.Exporter
	enabled     *atomic.Bool
	diagnostics *diagnosticState
}

func (e logGate) Export(ctx context.Context, records []sdklog.Record) error {
	items := uint64(len(records))
	if !e.enabled.Load() {
		e.diagnostics.dropped("logs", items)
		return nil
	}
	e.diagnostics.begin("logs")
	err := e.Exporter.Export(ctx, records)
	e.diagnostics.finish("logs", items, err)
	return err
}
