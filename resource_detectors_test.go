package edotgo

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type fixtureDetector struct {
	err error
}

func (d fixtureDetector) Detect(ctx context.Context) (*resource.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return resource.NewSchemaless(
		attribute.String("cloud.provider", "fixture"),
		attribute.String("service.name", "detected-service"),
	), d.err
}

func TestResourceDetectorsPreserveExplicitOverrides(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "cloud.provider=environment")
	t.Setenv("OTEL_SERVICE_NAME", "")
	c := DefaultConfig()
	c.Traces, c.Metrics, c.Logs, c.RuntimeMetrics = false, false, false, false
	c.ServiceName = "configured-service"
	c.ResourceDetectors = []resource.Detector{fixtureDetector{}}
	sdk, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer sdk.Shutdown(context.Background())
	recorder := tracetest.NewSpanRecorder()
	sdk.TracerProvider.RegisterSpanProcessor(recorder)
	_, span := sdk.TracerProvider.Tracer("test").Start(context.Background(), "request")
	span.End()
	set := recorder.Ended()[0].Resource().Set()
	if value, _ := set.Value("cloud.provider"); value.AsString() != "environment" {
		t.Fatal("environment did not override detector attributes")
	}
	if value, _ := set.Value("service.name"); value.AsString() != "configured-service" {
		t.Fatal("explicit service name did not override detector")
	}

	c.ResourceDetectors = []resource.Detector{fixtureDetector{err: errors.New("detector failure")}}
	if sdk, err := New(context.Background(), c); err == nil {
		sdk.Shutdown(context.Background())
		t.Fatal("detector failure did not fail initialization")
	}
}
