package edotgo

import (
	"context"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"testing"
	"time"
)

type discardProcessor struct{}

func (discardProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (discardProcessor) OnEnd(sdktrace.ReadOnlySpan)                     {}
func (discardProcessor) ForceFlush(context.Context) error                { return nil }
func (discardProcessor) Shutdown(context.Context) error                  { return nil }

// Real SDK spans include sampling, IDs and processor calls. No network exporter
// is used so stack processing, rather than network latency, dominates.
func BenchmarkStackProcessor(b *testing.B) {
	for _, tc := range []struct {
		name string
		min  time.Duration
	}{{"disabled", -1}, {"short", time.Hour}, {"all", 0}} {
		for _, parallel := range []bool{false, true} {
			name := tc.name + "/serial"
			if parallel {
				name = tc.name + "/parallel"
			}
			b.Run(name, func(b *testing.B) {
				p := newStackProcessor(discardProcessor{}, tc.min, nil)
				tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(p))
				defer tp.Shutdown(context.Background())
				tr := tp.Tracer("benchmark")
				ctx := context.Background()
				b.ReportAllocs()
				b.ResetTimer()
				if parallel {
					b.RunParallel(func(pb *testing.PB) {
						for pb.Next() {
							_, s := tr.Start(ctx, "operation")
							s.End()
						}
					})
				} else {
					for b.Loop() {
						_, s := tr.Start(ctx, "operation")
						s.End()
					}
				}
			})
		}
	}
}
