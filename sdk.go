// SPDX-License-Identifier: Apache-2.0
// Package edotgo is an unofficial, partial Go port of EDOT Java's portable
// SDK customization layer. It is not an Elastic-supported distribution.
package edotgo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const Version = "0.2.1"

// SDK owns all providers. Create one per process and call Shutdown at exit.
// Providers are explicit by default; InstallGlobals is an opt-in convenience.
type SDK struct {
	TracerProvider        *sdktrace.TracerProvider
	MeterProvider         *sdkmetric.MeterProvider
	LoggerProvider        *sdklog.LoggerProvider
	Propagator            propagation.TextMapPropagator
	sampler               *dynamicSampler
	traces, metrics, logs atomic.Bool
	once                  sync.Once
	shutdownErr           error
}

func NewFromEnv(ctx context.Context) (*SDK, error) {
	c, err := ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	return New(ctx, c)
}

// New reads the supplied config and OTLP exporter environment settings. No
// credentials or endpoint URLs are logged. It never changes environment values.
func New(ctx context.Context, c Config) (result *SDK, err error) {
	if err = c.validate(); err != nil {
		return nil, err
	}
	s := &SDK{sampler: newSampler(c.Sampler, c.SampleRatio), Propagator: propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})}
	s.traces.Store(true)
	s.metrics.Store(true)
	s.logs.Store(true)
	// Clean up earlier exporters if initialization fails partway through.
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 5_000_000_000)
			defer cancel()
			err = errors.Join(err, s.Shutdown(cleanup))
		}
	}()
	if c.Disabled {
		s.TracerProvider = sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
		s.MeterProvider = sdkmetric.NewMeterProvider()
		s.LoggerProvider = sdklog.NewLoggerProvider()
		return s, nil
	}
	r, err := resource.New(ctx, resource.WithTelemetrySDK(), resource.WithHost(), resource.WithProcessPID(), resource.WithProcessRuntimeName(), resource.WithProcessRuntimeVersion(), resource.WithFromEnv())
	if err != nil {
		return nil, fmt.Errorf("detect resources: %w", err)
	}
	attrs := r.Set()
	extra := []attribute.KeyValue{attribute.String("telemetry.distro.name", "edot-go-unofficial"), attribute.String("telemetry.distro.version", Version)}
	if legacy, ok := attrs.Value("deployment.environment"); ok {
		if _, exists := attrs.Value("deployment.environment.name"); !exists {
			extra = append(extra, attribute.String("deployment.environment.name", legacy.AsString()))
		}
	}
	if c.ServiceName != "" {
		extra = append(extra, attribute.String("service.name", c.ServiceName))
	} else if _, ok := attrs.Value("service.name"); !ok {
		extra = append(extra, attribute.String("service.name", "unknown_service:go"))
	}
	r, err = resource.Merge(r, resource.NewSchemaless(extra...))
	if err != nil {
		return nil, err
	}
	traceOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(r), sdktrace.WithSampler(s.sampler)}
	if c.Traces {
		exp, e := newTraceExporter(ctx)
		if e != nil {
			return nil, fmt.Errorf("create trace exporter: %w", e)
		}
		batch := sdktrace.NewBatchSpanProcessor(spanGate{exp, &s.traces})
		traceOpts = append(traceOpts, sdktrace.WithSpanProcessor(newStackProcessor(batch, c.StacktraceMinDuration, c.SpanBaggageInclude)))
	}
	s.TracerProvider = sdktrace.NewTracerProvider(traceOpts...)
	metricOpts := []sdkmetric.Option{sdkmetric.WithResource(r)}
	if c.Metrics {
		exp, e := newMetricExporter(ctx)
		if e != nil {
			return nil, fmt.Errorf("create metric exporter: %w", e)
		}
		metricOpts = append(metricOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricGate{exp, &s.metrics})))
	}
	s.MeterProvider = sdkmetric.NewMeterProvider(metricOpts...)
	logOpts := []sdklog.LoggerProviderOption{sdklog.WithResource(r)}
	if c.Logs {
		exp, e := newLogExporter(ctx)
		if e != nil {
			return nil, fmt.Errorf("create log exporter: %w", e)
		}
		batch := sdklog.NewBatchProcessor(logGate{exp, &s.logs})
		logOpts = append(logOpts, sdklog.WithProcessor(&baggageLogProcessor{Processor: batch, patterns: append([]string(nil), c.LogBaggageInclude...)}))
	}
	s.LoggerProvider = sdklog.NewLoggerProvider(logOpts...)
	if c.Metrics && c.RuntimeMetrics {
		if e := otelruntime.Start(otelruntime.WithMeterProvider(s.MeterProvider)); e != nil {
			return nil, fmt.Errorf("start runtime metrics: %w", e)
		}
	}
	return s, nil
}

func temporalitySelector() sdkmetric.TemporalitySelector {
	preference := strings.ToUpper(os.Getenv("OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE"))
	return func(k sdkmetric.InstrumentKind) metricdata.Temporality {
		if preference == "CUMULATIVE" {
			return metricdata.CumulativeTemporality
		}
		switch k {
		case sdkmetric.InstrumentKindCounter, sdkmetric.InstrumentKindHistogram:
			return metricdata.DeltaTemporality
		case sdkmetric.InstrumentKindObservableCounter:
			if preference != "LOWMEMORY" {
				return metricdata.DeltaTemporality
			}
		}
		return metricdata.CumulativeTemporality
	}
}

// InstallGlobals replaces global providers; call once during startup, before
// starting application goroutines. Shutdown does not restore old providers.
func (s *SDK) InstallGlobals() {
	otel.SetTracerProvider(s.TracerProvider)
	otel.SetMeterProvider(s.MeterProvider)
	global.SetLoggerProvider(s.LoggerProvider)
	otel.SetTextMapPropagator(s.Propagator)
}

func (s *SDK) SetSampleRatio(ratio float64) error {
	if s.sampler.name != "traceidratio" && s.sampler.name != "parentbased_traceidratio" {
		return fmt.Errorf("sample ratio is only adjustable for ratio samplers")
	}
	return s.sampler.setRatio(ratio)
}
func (s *SDK) SetSendingTraces(enabled bool)  { s.traces.Store(enabled) }
func (s *SDK) SetSendingMetrics(enabled bool) { s.metrics.Store(enabled) }
func (s *SDK) SetSendingLogs(enabled bool)    { s.logs.Store(enabled) }

// Logger creates a slog bridge. Use InfoContext/ErrorContext to correlate logs
// with the active span and copy allowed baggage from the request context.
func (s *SDK) Logger(name string) *slog.Logger {
	return otelslog.NewLogger(name, otelslog.WithLoggerProvider(s.LoggerProvider))
}
func (s *SDK) HTTPHandler(operation string, next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, operation, otelhttp.WithTracerProvider(s.TracerProvider), otelhttp.WithMeterProvider(s.MeterProvider), otelhttp.WithPropagators(s.Propagator))
}
func (s *SDK) HTTPTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base, otelhttp.WithTracerProvider(s.TracerProvider), otelhttp.WithMeterProvider(s.MeterProvider), otelhttp.WithPropagators(s.Propagator))
}

func (s *SDK) ForceFlush(ctx context.Context) error {
	var errs []error
	if s.TracerProvider != nil {
		errs = append(errs, s.TracerProvider.ForceFlush(ctx))
	}
	if s.MeterProvider != nil {
		errs = append(errs, s.MeterProvider.ForceFlush(ctx))
	}
	if s.LoggerProvider != nil {
		errs = append(errs, s.LoggerProvider.ForceFlush(ctx))
	}
	return errors.Join(errs...)
}

// Shutdown attempts all providers and reports their joined errors. The first
// caller owns shutdown; subsequent calls return the same result.
func (s *SDK) Shutdown(ctx context.Context) error {
	s.once.Do(func() {
		var errs []error
		if s.TracerProvider != nil {
			errs = append(errs, s.TracerProvider.Shutdown(ctx))
		}
		if s.MeterProvider != nil {
			errs = append(errs, s.MeterProvider.Shutdown(ctx))
		}
		if s.LoggerProvider != nil {
			errs = append(errs, s.LoggerProvider.Shutdown(ctx))
		}
		s.shutdownErr = errors.Join(errs...)
	})
	return s.shutdownErr
}
