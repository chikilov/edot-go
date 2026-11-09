// queuediagnostics demonstrates upstream queue observations with synthetic local
// traffic only. It explicitly installs a diagnostics-only global MeterProvider.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	edotgo "github.com/chikilov/edot-go"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if !strings.EqualFold(os.Getenv("OTEL_GO_X_OBSERVABILITY"), "true") {
		return fmt.Errorf("run with OTEL_GO_X_OBSERVABILITY=true")
	}
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer provider.Shutdown(context.Background())
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	defer otel.SetMeterProvider(previous)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	started := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	defer unblock()
	c := edotgo.DefaultConfig()
	c.Metrics = false
	c.RuntimeMetrics = false
	c.StacktraceMinDuration = -1
	c.Sampler = "always_on"
	c.OTLP = edotgo.ExporterConfig{Protocol: "http/protobuf", Endpoint: server.URL, Headers: map[string]string{}, Timeout: 3 * time.Second, Compression: "none"}
	c.TraceBatch = edotgo.BatchConfig{MaxQueueSize: 2, MaxExportBatchSize: 1, ExportInterval: time.Hour, ExportTimeout: 3 * time.Second}
	c.LogBatch = c.TraceBatch
	s, err := edotgo.New(context.Background(), c)
	if err != nil {
		return err
	}
	defer func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	}()
	emit := func() {
		ctx, span := s.TracerProvider.Tracer("queue-example").Start(context.Background(), "synthetic")
		s.Logger("queue-example").InfoContext(ctx, "synthetic queue example")
		span.End()
	}
	emit()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			return fmt.Errorf("local export did not start")
		}
	}
	for i := 0; i < 5; i++ {
		emit()
	}
	snapshot := func(phase string) error {
		var data metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &data); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Phase  string
			Queues edotgo.QueueDiagnostics
		}{phase, edotgo.QueueDiagnosticsFromMetrics(&data)})
	}
	if err := snapshot("saturated"); err != nil {
		return err
	}
	unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.ForceFlush(ctx); err != nil {
		return err
	}
	return snapshot("drained")
}
