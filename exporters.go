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
	enabled *atomic.Bool
}

func (e spanGate) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if !e.enabled.Load() {
		return nil
	}
	return e.SpanExporter.ExportSpans(ctx, spans)
}

type metricGate struct {
	sdkmetric.Exporter
	enabled *atomic.Bool
}

func (e metricGate) Export(ctx context.Context, data *metricdata.ResourceMetrics) error {
	if !e.enabled.Load() {
		return nil
	}
	return e.Exporter.Export(ctx, data)
}

type logGate struct {
	sdklog.Exporter
	enabled *atomic.Bool
}

func (e logGate) Export(ctx context.Context, records []sdklog.Record) error {
	if !e.enabled.Load() {
		return nil
	}
	return e.Exporter.Export(ctx, records)
}
