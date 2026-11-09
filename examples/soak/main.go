// soak produces bounded-cardinality synthetic traffic and JSONL resource samples.
// The local harness controls the phase file and injects external failures.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	edotgo "github.com/chikilov/edot-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type sample struct {
	Kind          string             `json:"kind"`
	Seconds       float64            `json:"seconds"`
	Emitted       uint64             `json:"emitted"`
	MaxEmitMicros int64              `json:"max_emit_micros"`
	Heap          uint64             `json:"heap_bytes"`
	Goroutines    int                `json:"goroutines"`
	Phase         string             `json:"phase"`
	TraceIDs      map[string]string  `json:"trace_ids,omitempty"`
	Diagnostics   edotgo.Diagnostics `json:"diagnostics"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	duration := flag.Duration("duration", 5*time.Minute, "traffic duration")
	rate := flag.Int("rate", 25, "synthetic requests per second")
	phaseFile := flag.String("phase-file", "", "local harness phase file")
	flag.Parse()
	if *duration < 10*time.Second || *rate < 1 || *rate > 10000 {
		return fmt.Errorf("duration >= 10s and rate 1..10000 required")
	}
	c, err := edotgo.ConfigFromEnv()
	if err != nil {
		return err
	}
	c.RuntimeMetrics = false
	c.Sampler = "always_on"
	c.StacktraceMinDuration = 0
	if c.Disabled || !c.Traces || !c.Metrics || !c.Logs {
		return fmt.Errorf("soak requires all signals enabled")
	}
	s, err := edotgo.New(context.Background(), c)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	}()
	tr := s.TracerProvider.Tracer("soak")
	logger := s.Logger("soak")
	counter, err := s.MeterProvider.Meter("soak").Int64Counter("soak.requests")
	if err != nil {
		return err
	}
	done := make(chan struct{})
	flushDone := make(chan struct{})
	go func() {
		defer close(flushDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
				_ = s.ForceFlush(ctx)
				cancel()
			}
		}
	}()
	start := time.Now()
	tick := time.NewTicker(time.Second / time.Duration(*rate))
	defer tick.Stop()
	samples := time.NewTicker(10 * time.Second)
	defer samples.Stop()
	end := time.NewTimer(*duration)
	defer end.Stop()
	phase := "warmup"
	var emitted uint64
	var maxMicros int64
	ids := map[string]string{}
	enc := json.NewEncoder(os.Stdout)
	snapshot := func(kind string) error {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return enc.Encode(sample{Kind: kind, Seconds: time.Since(start).Seconds(), Emitted: emitted, MaxEmitMicros: maxMicros, Heap: m.HeapAlloc, Goroutines: runtime.NumGoroutine(), Phase: phase, TraceIDs: ids, Diagnostics: s.Diagnostics()})
	}
	if err = snapshot("sample"); err != nil {
		close(done)
		<-flushDone
		return err
	}
	running := true
	for running {
		select {
		case <-end.C:
			running = false
		case <-samples.C:
			if *phaseFile != "" {
				if b, e := os.ReadFile(*phaseFile); e == nil {
					phase = strings.TrimSpace(string(b))
				}
			}
			if err = snapshot("sample"); err != nil {
				close(done)
				<-flushDone
				return err
			}
		case <-tick.C:
			begin := time.Now()
			ctx, span := tr.Start(context.Background(), "soak.request", trace.WithAttributes(attribute.String("soak.phase", phase)))
			ids[phase] = span.SpanContext().TraceID().String()
			counter.Add(ctx, 1, metric.WithAttributes(attribute.String("soak.phase", phase)))
			logger.InfoContext(ctx, "synthetic soak request")
			span.End()
			emitted++
			if n := time.Since(begin).Microseconds(); n > maxMicros {
				maxMicros = n
			}
		}
	}
	close(done)
	<-flushDone
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = s.ForceFlush(ctx)
	cancel()
	if err != nil {
		return fmt.Errorf("final flush failed (sanitized)")
	}
	if err = snapshot("final"); err != nil {
		return err
	}
	return nil
}
