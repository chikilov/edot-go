// cloudcheck emits a synthetic trace, metric, and correlated log, then reports
// sanitized exporter diagnostics. It does not query Elasticsearch or Kibana.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	edotgo "github.com/chikilov/edot-go"
	"go.opentelemetry.io/otel/trace"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() (err error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return fmt.Errorf("set OTEL_EXPORTER_OTLP_ENDPOINT and authentication locally before running cloudcheck")
	}
	c, err := edotgo.ConfigFromEnv()
	if err != nil {
		return err
	}
	if c.Disabled || !c.Traces || !c.Metrics || !c.Logs {
		return fmt.Errorf("cloudcheck requires all three signal exporters enabled")
	}
	c.ServiceName = "edot-go-cloudcheck"
	c.RuntimeMetrics = false
	c.Sampler = "always_on"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sdk, err := edotgo.New(ctx, c)
	if err != nil {
		return fmt.Errorf("initialize telemetry: %w", err)
	}
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		err = errors.Join(err, sdk.Shutdown(shutdown))
	}()
	ctx, span := sdk.TracerProvider.Tracer("cloudcheck").Start(ctx, "cloudcheck.request", trace.WithSpanKind(trace.SpanKindServer))
	id := span.SpanContext().TraceID()
	sdk.Logger("cloudcheck").InfoContext(ctx, "edot-go Cloud connectivity check")
	counter, err := sdk.MeterProvider.Meter("cloudcheck").Int64Counter("cloudcheck.requests")
	if err != nil {
		span.End()
		return err
	}
	counter.Add(ctx, 1)
	span.End()
	if err := sdk.ForceFlush(ctx); err != nil {
		return fmt.Errorf("flush telemetry: %w", err)
	}
	d := sdk.Diagnostics()
	output := struct {
		Service     string             `json:"service"`
		TraceID     string             `json:"trace_id"`
		Diagnostics edotgo.Diagnostics `json:"diagnostics"`
	}{c.ServiceName, id.String(), d}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		return err
	}
	if d.Traces.FailedBatches+d.Metrics.FailedBatches+d.Logs.FailedBatches > 0 || d.Traces.SuccessfulItems == 0 || d.Metrics.SuccessfulItems == 0 || d.Logs.SuccessfulItems == 0 {
		return fmt.Errorf("one or more signal exports failed; inspect sanitized diagnostics and local OpenTelemetry error reporting")
	}
	return nil
}
