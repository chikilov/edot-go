// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"context"
	"fmt"
	"path"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func included(key string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, key); ok {
			return true
		}
	}
	return false
}

type spanKey struct {
	trace trace.TraceID
	span  trace.SpanID
}

func key(s sdktrace.ReadOnlySpan) spanKey {
	return spanKey{s.SpanContext().TraceID(), s.SpanContext().SpanID()}
}

// stackProcessor captures the start callsite, then attaches it only to slow
// spans at export. At most 4096 in-flight callsites are retained per SDK.
type stackProcessor struct {
	sdktrace.SpanProcessor
	min      time.Duration
	patterns []string
	mu       sync.Mutex
	stacks   map[spanKey]string
}

func (p *stackProcessor) OnStart(ctx context.Context, s sdktrace.ReadWriteSpan) {
	for _, m := range baggage.FromContext(ctx).Members() {
		if included(m.Key(), p.patterns) {
			s.SetAttributes(attribute.String(m.Key(), m.Value()))
		}
	}
	if p.min >= 0 && s.SpanContext().IsSampled() {
		p.mu.Lock()
		if len(p.stacks) < 4096 {
			p.stacks[key(s)] = callsite()
		}
		p.mu.Unlock()
	}
	p.SpanProcessor.OnStart(ctx, s)
}
func callsite() string {
	pcs := make([]uintptr, 48)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	var b strings.Builder
	for {
		f, more := frames.Next()
		fmt.Fprintf(&b, "%s\n\t%s:%d\n", f.Function, f.File, f.Line)
		if !more {
			break
		}
	}
	return b.String()
}

type spanWithAttributes struct {
	sdktrace.ReadOnlySpan
	attrs []attribute.KeyValue
}

func (s spanWithAttributes) Attributes() []attribute.KeyValue { return s.attrs }
func (p *stackProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	p.mu.Lock()
	stack := p.stacks[key(s)]
	delete(p.stacks, key(s))
	p.mu.Unlock()
	if stack != "" && s.EndTime().Sub(s.StartTime()) >= p.min {
		has := false
		for _, a := range s.Attributes() {
			if a.Key == "code.stacktrace" {
				has = true
				break
			}
		}
		if !has {
			attrs := append([]attribute.KeyValue(nil), s.Attributes()...)
			attrs = append(attrs, attribute.String("code.stacktrace", stack))
			s = spanWithAttributes{s, attrs}
		}
	}
	p.SpanProcessor.OnEnd(s)
}
func (p *stackProcessor) Shutdown(ctx context.Context) error {
	err := p.SpanProcessor.Shutdown(ctx)
	p.mu.Lock()
	clear(p.stacks)
	p.mu.Unlock()
	return err
}

type baggageLogProcessor struct {
	sdklog.Processor
	patterns []string
}

func (p *baggageLogProcessor) OnEmit(ctx context.Context, r *sdklog.Record) error {
	for _, m := range baggage.FromContext(ctx).Members() {
		if included(m.Key(), p.patterns) {
			r.AddAttributes(attribute.String(m.Key(), m.Value()))
		}
	}
	return p.Processor.OnEmit(ctx, r)
}
