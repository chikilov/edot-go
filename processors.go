// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"context"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

const maxInflightStacks = 4096
const maxStackDepth = 48

// capturedStack retains only program counters until a span exceeds the duration
// threshold. Entries are pooled and bounded, including pending captures.
type capturedStack struct {
	pcs [maxStackDepth]uintptr
	n   int
}

// stackProcessor captures the start callsite, then symbolizes only slow spans.
// Stack walking and formatting never execute while holding the map mutex.
type stackProcessor struct {
	sdktrace.SpanProcessor
	min      time.Duration
	patterns []string
	mu       sync.Mutex
	stacks   map[spanKey]*capturedStack
	slots    int // Stored entries plus captures reserved outside the mutex.
	stopped  bool
	pool     sync.Pool
	skipped  atomic.Uint64
}

func newStackProcessor(next sdktrace.SpanProcessor, min time.Duration, patterns []string) *stackProcessor {
	p := &stackProcessor{SpanProcessor: next, min: min, patterns: append([]string(nil), patterns...), stacks: make(map[spanKey]*capturedStack)}
	p.pool.New = func() any { return new(capturedStack) }
	return p
}

func (p *stackProcessor) OnStart(ctx context.Context, s sdktrace.ReadWriteSpan) {
	for _, m := range baggage.FromContext(ctx).Members() {
		if included(m.Key(), p.patterns) {
			s.SetAttributes(attribute.String(m.Key(), m.Value()))
		}
	}
	if p.min >= 0 && s.SpanContext().IsSampled() {
		p.mu.Lock()
		reserved := !p.stopped && p.slots < maxInflightStacks
		if reserved {
			p.slots++
		} else if !p.stopped {
			p.skipped.Add(1)
		}
		p.mu.Unlock()
		if reserved {
			stack := p.pool.Get().(*capturedStack)
			stack.n = runtime.Callers(2, stack.pcs[:])
			p.mu.Lock()
			stopped := p.stopped
			if stopped {
				p.slots--
			} else {
				p.stacks[key(s)] = stack
			}
			p.mu.Unlock()
			if stopped {
				p.pool.Put(stack)
			}
		}
	}
	p.SpanProcessor.OnStart(ctx, s)
}

func callsite(stack *capturedStack) string {
	frames := runtime.CallersFrames(stack.pcs[:stack.n])
	var b strings.Builder
	b.Grow(stack.n * 80)
	var line [20]byte
	for {
		f, more := frames.Next()
		b.WriteString(f.Function)
		b.WriteString("\n\t")
		b.WriteString(f.File)
		b.WriteByte(':')
		b.Write(strconv.AppendInt(line[:0], int64(f.Line), 10))
		b.WriteByte('\n')
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
	if p.min >= 0 && s.SpanContext().IsSampled() {
		p.mu.Lock()
		stack := p.stacks[key(s)]
		if stack != nil {
			delete(p.stacks, key(s))
			p.slots--
		}
		p.mu.Unlock()
		if stack != nil {
			if s.EndTime().Sub(s.StartTime()) >= p.min {
				has := false
				for _, a := range s.Attributes() {
					if a.Key == "code.stacktrace" {
						has = true
						break
					}
				}
				if !has {
					attrs := make([]attribute.KeyValue, len(s.Attributes())+1)
					copy(attrs, s.Attributes())
					attrs[len(attrs)-1] = attribute.String("code.stacktrace", callsite(stack))
					s = spanWithAttributes{s, attrs}
				}
			}
			p.pool.Put(stack)
		}
	}
	p.SpanProcessor.OnEnd(s)
}
func (p *stackProcessor) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	p.stopped = true
	for _, stack := range p.stacks {
		p.pool.Put(stack)
	}
	p.slots -= len(p.stacks)
	clear(p.stacks)
	p.mu.Unlock()
	return p.SpanProcessor.Shutdown(ctx)
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
