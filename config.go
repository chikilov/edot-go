// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"fmt"
	"math"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// Config describes the portable subset of EDOT Java configuration.
// Explicit exporter settings override exporter environment variables without
// mutating process environment or replacing global OpenTelemetry handlers.
type Config struct {
	OTLP           ExporterConfig
	TraceExporter  ExporterConfig
	MetricExporter ExporterConfig
	LogExporter    ExporterConfig
	// OnExportError runs synchronously on an exporter goroutine. Keep it fast;
	// do not call ForceFlush/Shutdown or export telemetry from this callback.
	// Different signals may invoke it concurrently. Panics are counted and recovered.
	OnExportError         func(ExportError)
	ServiceName           string
	Sampler               string
	SampleRatio           float64
	StacktraceMinDuration time.Duration // Negative disables capture.
	SpanBaggageInclude    []string
	LogBaggageInclude     []string
	RuntimeMetrics        bool
	Disabled              bool
	Traces                bool
	Metrics               bool
	Logs                  bool
}

// DefaultConfig returns Java-inspired defaults, using a standard Go sampler.
func DefaultConfig() Config {
	return Config{Sampler: "parentbased_traceidratio", SampleRatio: 1,
		StacktraceMinDuration: 5 * time.Millisecond, RuntimeMetrics: true,
		Traces: true, Metrics: true, Logs: true}
}

// ConfigFromEnv reads supported settings. Unsupported exporters and protocols
// fail explicitly instead of silently sending data to an unexpected destination.
func ConfigFromEnv() (Config, error) {
	c := DefaultConfig()
	c.ServiceName = os.Getenv("OTEL_SERVICE_NAME")
	if v := os.Getenv("OTEL_TRACES_SAMPLER"); v != "" {
		c.Sampler = v
	}
	if v := os.Getenv("OTEL_TRACES_SAMPLER_ARG"); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return c, fmt.Errorf("OTEL_TRACES_SAMPLER_ARG: expected number")
		}
		c.SampleRatio = n
	}
	for _, item := range []struct {
		key    string
		target *bool
	}{
		{"OTEL_SDK_DISABLED", &c.Disabled}, {"EDOT_GO_RUNTIME_METRICS_ENABLED", &c.RuntimeMetrics},
	} {
		if v := os.Getenv(item.key); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return c, fmt.Errorf("%s: expected boolean", item.key)
			}
			*item.target = b
		}
	}
	for _, item := range []struct {
		key    string
		target *bool
	}{
		{"OTEL_TRACES_EXPORTER", &c.Traces}, {"OTEL_METRICS_EXPORTER", &c.Metrics}, {"OTEL_LOGS_EXPORTER", &c.Logs},
	} {
		switch os.Getenv(item.key) {
		case "", "otlp":
			*item.target = true
		case "none":
			*item.target = false
		default:
			return c, fmt.Errorf("%s: supported exporters are otlp and none", item.key)
		}
	}
	// Accept Java-compatible environment names after the Go-specific setting.
	for _, key := range []string{"EDOT_GO_SPAN_STACKTRACE_MIN_DURATION", "OTEL_JAVA_EXPERIMENTAL_SPAN_STACKTRACE_MIN_DURATION", "ELASTIC_OTEL_JAVA_SPAN_STACKTRACE_MIN_DURATION"} {
		if v := os.Getenv(key); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				if n, e := strconv.ParseInt(v, 10, 64); e == nil && (n == 0 || n == -1) {
					d = time.Duration(n)
				} else {
					return c, fmt.Errorf("%s: expected duration, e.g. 5ms or -1", key)
				}
			}
			c.StacktraceMinDuration = d
			break
		}
	}
	c.SpanBaggageInclude = patterns("EDOT_GO_SPAN_ATTRIBUTES_COPY_FROM_BAGGAGE_INCLUDE", "OTEL_JAVA_EXPERIMENTAL_SPAN_ATTRIBUTES_COPY_FROM_BAGGAGE_INCLUDE")
	c.LogBaggageInclude = patterns("EDOT_GO_LOG_ATTRIBUTES_COPY_FROM_BAGGAGE_INCLUDE", "OTEL_JAVA_EXPERIMENTAL_LOG_ATTRIBUTES_COPY_FROM_BAGGAGE_INCLUDE")
	return c, c.validate()
}

func patterns(keys ...string) []string {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			var out []string
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
			return out
		}
	}
	return nil
}

func (c Config) validate() error {
	if math.IsNaN(c.SampleRatio) || math.IsInf(c.SampleRatio, 0) || c.SampleRatio < 0 || c.SampleRatio > 1 {
		return fmt.Errorf("sample ratio must be between 0 and 1")
	}
	switch c.Sampler {
	case "always_on", "always_off", "traceidratio", "parentbased_always_on", "parentbased_always_off", "parentbased_traceidratio":
	default:
		return fmt.Errorf("unsupported sampler %q", c.Sampler)
	}
	for _, ps := range [][]string{c.SpanBaggageInclude, c.LogBaggageInclude} {
		for _, p := range ps {
			if _, err := path.Match(p, ""); err != nil {
				return fmt.Errorf("invalid baggage glob %q", p)
			}
		}
	}
	if c.Disabled {
		return nil
	}
	for signal, enabled := range map[string]bool{"TRACES": c.Traces, "METRICS": c.Metrics, "LOGS": c.Logs} {
		if !enabled {
			continue
		}
		if err := c.exporterFor(signal).validate(); err != nil {
			return fmt.Errorf("%s: %w", signal, err)
		}
	}
	if c.Metrics {
		switch strings.ToUpper(os.Getenv("OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE")) {
		case "", "DELTA", "CUMULATIVE", "LOWMEMORY":
		default:
			return fmt.Errorf("unsupported metric temporality preference")
		}
	}
	return nil
}

// Signal-specific protocol overrides the common setting. The existing default
// remains HTTP/protobuf for backwards compatibility.
func protocolFor(signal string) string {
	if v := os.Getenv("OTEL_EXPORTER_OTLP_" + signal + "_PROTOCOL"); v != "" {
		return v
	}
	if v := os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"); v != "" {
		return v
	}
	return "http/protobuf"
}
