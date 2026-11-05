package edotgo

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestStackProcessorThresholdAndPreservedAttribute(t *testing.T) {
	for _, tc := range []struct {
		name     string
		min      time.Duration
		slow     bool
		existing string
		want     bool
	}{
		{"short", time.Hour, false, "", false}, {"slow", time.Millisecond, true, "", true},
		{"disabled", -1, true, "", false}, {"existing", 0, false, "user stack", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exp := tracetest.NewInMemoryExporter()
			p := newStackProcessor(sdktrace.NewSimpleSpanProcessor(exp), tc.min, nil)
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(p))
			defer tp.Shutdown(context.Background())
			opts := []trace.SpanStartOption{}
			if tc.slow {
				opts = append(opts, trace.WithTimestamp(time.Now().Add(-time.Second)))
			}
			if tc.existing != "" {
				opts = append(opts, trace.WithAttributes(attribute.String("code.stacktrace", tc.existing)))
			}
			_, s := tp.Tracer("test").Start(context.Background(), "operation", opts...)
			s.End()
			spans := exp.GetSpans()
			if len(spans) != 1 {
				t.Fatal("missing span")
			}
			stack := ""
			for _, a := range spans[0].Attributes {
				if a.Key == "code.stacktrace" {
					stack = a.Value.AsString()
				}
			}
			if (stack != "") != tc.want {
				t.Errorf("stack presence %q", stack)
			}
			if tc.existing != "" && stack != tc.existing {
				t.Error("overwrote user stack")
			}
			if tc.want && tc.existing == "" && !strings.Contains(stack, "TestStackProcessorThreshold") {
				t.Error("lost start callsite")
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.slots != 0 || len(p.stacks) != 0 {
				t.Error("ended span retained stack")
			}
		})
	}
}

func TestStackProcessorCapacityReused(t *testing.T) {
	p := newStackProcessor(discardProcessor{}, time.Hour, nil)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(p))
	defer tp.Shutdown(context.Background())
	tr := tp.Tracer("capacity")
	spans := make([]trace.Span, maxInflightStacks+1)
	for i := range spans {
		_, spans[i] = tr.Start(context.Background(), "open")
	}
	p.mu.Lock()
	slots, stored := p.slots, len(p.stacks)
	p.mu.Unlock()
	if slots != maxInflightStacks || stored != maxInflightStacks {
		t.Fatalf("capacity: %d slots %d entries", slots, stored)
	}
	spans[0].End()
	_, replacement := tr.Start(context.Background(), "replacement")
	p.mu.Lock()
	_, ok := p.stacks[spanKey{replacement.SpanContext().TraceID(), replacement.SpanContext().SpanID()}]
	p.mu.Unlock()
	if !ok {
		t.Error("released slot was not reusable")
	}
	for _, span := range spans[1:] {
		span.End()
	}
	replacement.End()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.slots != 0 || len(p.stacks) != 0 {
		t.Fatal("capacity leaked after end")
	}
}

func TestStackProcessorConcurrentShutdown(t *testing.T) {
	p := newStackProcessor(discardProcessor{}, 0, nil)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(p))
	tr := tp.Tracer("concurrent")
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				_, s := tr.Start(context.Background(), "work")
				s.End()
			}
		}()
	}
	close(start)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err := tp.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.slots != 0 || len(p.stacks) != 0 {
		t.Fatalf("shutdown retained %d slots and %d stacks", p.slots, len(p.stacks))
	}
}
