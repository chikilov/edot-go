// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func newTraceExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	switch protocolFor("TRACES") {
	case "grpc":
		return otlptracegrpc.New(ctx)
	case "http/json":
		return otlptracehttp.New(ctx, otlptracehttp.WithEncoding(otlptracehttp.EncodingJSON))
	default:
		return otlptracehttp.New(ctx, otlptracehttp.WithEncoding(otlptracehttp.EncodingProtobuf))
	}
}
func newMetricExporter(ctx context.Context) (sdkmetric.Exporter, error) {
	switch protocolFor("METRICS") {
	case "grpc":
		return otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithTemporalitySelector(temporalitySelector()))
	case "http/json":
		client, err := jsonHTTPClient("METRICS")
		if err != nil {
			return nil, err
		}
		exp, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithTemporalitySelector(temporalitySelector()), otlpmetrichttp.WithHTTPClient(client))
		if err != nil {
			client.CloseIdleConnections()
			return nil, err
		}
		return &jsonMetricExporter{Exporter: exp, client: client}, nil
	default:
		return otlpmetrichttp.New(ctx, otlpmetrichttp.WithTemporalitySelector(temporalitySelector()))
	}
}
func newLogExporter(ctx context.Context) (sdklog.Exporter, error) {
	switch protocolFor("LOGS") {
	case "grpc":
		return otlploggrpc.New(ctx)
	case "http/json":
		client, err := jsonHTTPClient("LOGS")
		if err != nil {
			return nil, err
		}
		exp, err := otlploghttp.New(ctx, otlploghttp.WithHTTPClient(client))
		if err != nil {
			client.CloseIdleConnections()
			return nil, err
		}
		return &jsonLogExporter{Exporter: exp, client: client}, nil
	default:
		return otlploghttp.New(ctx)
	}
}

type jsonMetricExporter struct {
	sdkmetric.Exporter
	client *http.Client
}

func (e *jsonMetricExporter) Shutdown(ctx context.Context) error {
	defer e.client.CloseIdleConnections()
	return e.Exporter.Shutdown(ctx)
}

type jsonLogExporter struct {
	sdklog.Exporter
	client *http.Client
}

func (e *jsonLogExporter) Shutdown(ctx context.Context) error {
	defer e.client.CloseIdleConnections()
	return e.Exporter.Shutdown(ctx)
}
