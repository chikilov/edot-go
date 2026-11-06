// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"context"
	"google.golang.org/grpc/credentials"
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

func newTraceExporter(ctx context.Context, supplied ...exporterSettings) (sdktrace.SpanExporter, error) {
	s := exporterSettingsFor("TRACES", supplied)
	if err := s.validate(); err != nil {
		return nil, err
	}
	if s.Protocol == "grpc" {
		opts := []otlptracegrpc.Option{}
		if s.Endpoint != "" {
			opts = append(opts, otlptracegrpc.WithEndpointURL(s.endpoint("TRACES")))
		}
		if s.Headers != nil {
			opts = append(opts, otlptracegrpc.WithHeaders(s.Headers))
		}
		if s.Timeout > 0 {
			opts = append(opts, otlptracegrpc.WithTimeout(s.Timeout))
		}
		if s.TLSConfig != nil {
			opts = append(opts, otlptracegrpc.WithTLSCredentials(credentials.NewTLS(s.TLSConfig)))
		}
		if s.Compression != "" {
			compressor := s.Compression
			if compressor == "none" {
				compressor = ""
			}
			opts = append(opts, otlptracegrpc.WithCompressor(compressor))
		}
		return otlptracegrpc.New(ctx, opts...)
	}
	opts := []otlptracehttp.Option{}
	encoding := otlptracehttp.EncodingProtobuf
	if s.Protocol == "http/json" {
		encoding = otlptracehttp.EncodingJSON
	}
	opts = append(opts, otlptracehttp.WithEncoding(encoding))
	if s.Endpoint != "" {
		opts = append(opts, otlptracehttp.WithEndpointURL(s.endpoint("TRACES")))
	}
	if s.Headers != nil {
		opts = append(opts, otlptracehttp.WithHeaders(s.Headers))
	}
	if s.Timeout > 0 {
		opts = append(opts, otlptracehttp.WithTimeout(s.Timeout))
	}
	if s.TLSConfig != nil {
		opts = append(opts, otlptracehttp.WithTLSClientConfig(s.TLSConfig))
	}
	if s.Compression != "" {
		compression := otlptracehttp.NoCompression
		if s.Compression == "gzip" {
			compression = otlptracehttp.GzipCompression
		}
		opts = append(opts, otlptracehttp.WithCompression(compression))
	}
	return otlptracehttp.New(ctx, opts...)
}

func newMetricExporter(ctx context.Context, supplied ...exporterSettings) (sdkmetric.Exporter, error) {
	s := exporterSettingsFor("METRICS", supplied)
	if err := s.validate(); err != nil {
		return nil, err
	}
	if s.Protocol == "grpc" {
		opts := []otlpmetricgrpc.Option{}
		opts = append(opts, otlpmetricgrpc.WithTemporalitySelector(temporalitySelector()))
		if s.Endpoint != "" {
			opts = append(opts, otlpmetricgrpc.WithEndpointURL(s.endpoint("METRICS")))
		}
		if s.Headers != nil {
			opts = append(opts, otlpmetricgrpc.WithHeaders(s.Headers))
		}
		if s.Timeout > 0 {
			opts = append(opts, otlpmetricgrpc.WithTimeout(s.Timeout))
		}
		if s.TLSConfig != nil {
			opts = append(opts, otlpmetricgrpc.WithTLSCredentials(credentials.NewTLS(s.TLSConfig)))
		}
		if s.Compression != "" {
			compressor := s.Compression
			if compressor == "none" {
				compressor = ""
			}
			opts = append(opts, otlpmetricgrpc.WithCompressor(compressor))
		}
		return otlpmetricgrpc.New(ctx, opts...)
	}
	opts := []otlpmetrichttp.Option{}
	opts = append(opts, otlpmetrichttp.WithTemporalitySelector(temporalitySelector()))
	if s.Endpoint != "" {
		opts = append(opts, otlpmetrichttp.WithEndpointURL(s.endpoint("METRICS")))
	}
	if s.Headers != nil {
		opts = append(opts, otlpmetrichttp.WithHeaders(s.Headers))
	}
	if s.Timeout > 0 {
		opts = append(opts, otlpmetrichttp.WithTimeout(s.Timeout))
	}
	if s.TLSConfig != nil {
		opts = append(opts, otlpmetrichttp.WithTLSClientConfig(s.TLSConfig))
	}
	if s.Compression != "" {
		compression := otlpmetrichttp.NoCompression
		if s.Compression == "gzip" {
			compression = otlpmetrichttp.GzipCompression
		}
		opts = append(opts, otlpmetrichttp.WithCompression(compression))
	}
	if s.Protocol == "http/json" {
		client, err := jsonHTTPClient("METRICS", s)
		if err != nil {
			return nil, err
		}
		opts = append(opts, otlpmetrichttp.WithHTTPClient(client))
		exp, err := otlpmetrichttp.New(ctx, opts...)
		if err != nil {
			client.CloseIdleConnections()
			return nil, err
		}
		return &jsonMetricExporter{Exporter: exp, client: client}, nil
	}
	return otlpmetrichttp.New(ctx, opts...)
}

func newLogExporter(ctx context.Context, supplied ...exporterSettings) (sdklog.Exporter, error) {
	s := exporterSettingsFor("LOGS", supplied)
	if err := s.validate(); err != nil {
		return nil, err
	}
	if s.Protocol == "grpc" {
		opts := []otlploggrpc.Option{}
		if s.Endpoint != "" {
			opts = append(opts, otlploggrpc.WithEndpointURL(s.endpoint("LOGS")))
		}
		if s.Headers != nil {
			opts = append(opts, otlploggrpc.WithHeaders(s.Headers))
		}
		if s.Timeout > 0 {
			opts = append(opts, otlploggrpc.WithTimeout(s.Timeout))
		}
		if s.TLSConfig != nil {
			opts = append(opts, otlploggrpc.WithTLSCredentials(credentials.NewTLS(s.TLSConfig)))
		}
		if s.Compression != "" {
			compressor := s.Compression
			if compressor == "none" {
				compressor = ""
			}
			opts = append(opts, otlploggrpc.WithCompressor(compressor))
		}
		return otlploggrpc.New(ctx, opts...)
	}
	opts := []otlploghttp.Option{}
	if s.Endpoint != "" {
		opts = append(opts, otlploghttp.WithEndpointURL(s.endpoint("LOGS")))
	}
	if s.Headers != nil {
		opts = append(opts, otlploghttp.WithHeaders(s.Headers))
	}
	if s.Timeout > 0 {
		opts = append(opts, otlploghttp.WithTimeout(s.Timeout))
	}
	if s.TLSConfig != nil {
		opts = append(opts, otlploghttp.WithTLSClientConfig(s.TLSConfig))
	}
	if s.Compression != "" {
		compression := otlploghttp.NoCompression
		if s.Compression == "gzip" {
			compression = otlploghttp.GzipCompression
		}
		opts = append(opts, otlploghttp.WithCompression(compression))
	}
	if s.Protocol == "http/json" {
		client, err := jsonHTTPClient("LOGS", s)
		if err != nil {
			return nil, err
		}
		opts = append(opts, otlploghttp.WithHTTPClient(client))
		exp, err := otlploghttp.New(ctx, opts...)
		if err != nil {
			client.CloseIdleConnections()
			return nil, err
		}
		return &jsonLogExporter{Exporter: exp, client: client}, nil
	}
	return otlploghttp.New(ctx, opts...)
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
