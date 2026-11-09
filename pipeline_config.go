// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"fmt"
	"time"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// BatchConfig configures a signal's in-memory batch processor. Zero fields
// inherit upstream environment/defaults; positive fields override them.
// Queue and batch limits count spans or log records, not bytes. Full queues
// retain upstream non-blocking drop behavior; there is no durable disk queue.
type BatchConfig struct {
	MaxQueueSize       int
	MaxExportBatchSize int
	ExportInterval     time.Duration
	ExportTimeout      time.Duration
}

// MetricReaderConfig controls periodic metric collection/export, independently
// of the exporter's network timeout. Zero fields inherit upstream behavior.
// MaxExportBatchSize counts data points; zero leaves the upstream default.
type MetricReaderConfig struct {
	Interval           time.Duration
	Timeout            time.Duration
	MaxExportBatchSize int
}

func (c BatchConfig) validate() error {
	if c.MaxQueueSize < 0 || c.MaxExportBatchSize < 0 || c.ExportInterval < 0 || c.ExportTimeout < 0 {
		return fmt.Errorf("batch sizes and durations must be positive or zero to inherit")
	}
	if c.MaxQueueSize > 0 && c.MaxExportBatchSize > c.MaxQueueSize {
		return fmt.Errorf("explicit export batch size must not exceed explicit queue size")
	}
	return nil
}
func (c MetricReaderConfig) validate() error {
	if c.Interval < 0 || c.Timeout < 0 || c.MaxExportBatchSize < 0 {
		return fmt.Errorf("metric reader sizes and durations must be positive or zero to inherit")
	}
	return nil
}
func (c BatchConfig) traceOptions() []sdktrace.BatchSpanProcessorOption {
	var opts []sdktrace.BatchSpanProcessorOption
	if c.MaxQueueSize > 0 {
		opts = append(opts, sdktrace.WithMaxQueueSize(c.MaxQueueSize))
	}
	if c.MaxExportBatchSize > 0 {
		opts = append(opts, sdktrace.WithMaxExportBatchSize(c.MaxExportBatchSize))
	}
	if c.ExportInterval > 0 {
		opts = append(opts, sdktrace.WithBatchTimeout(c.ExportInterval))
	}
	if c.ExportTimeout > 0 {
		opts = append(opts, sdktrace.WithExportTimeout(c.ExportTimeout))
	}
	return opts
}
func (c BatchConfig) logOptions() []sdklog.BatchProcessorOption {
	var opts []sdklog.BatchProcessorOption
	if c.MaxQueueSize > 0 {
		opts = append(opts, sdklog.WithMaxQueueSize(c.MaxQueueSize))
	}
	if c.MaxExportBatchSize > 0 {
		opts = append(opts, sdklog.WithExportMaxBatchSize(c.MaxExportBatchSize))
	}
	if c.ExportInterval > 0 {
		opts = append(opts, sdklog.WithExportInterval(c.ExportInterval))
	}
	if c.ExportTimeout > 0 {
		opts = append(opts, sdklog.WithExportTimeout(c.ExportTimeout))
	}
	return opts
}
func (c MetricReaderConfig) options() []sdkmetric.PeriodicReaderOption {
	var opts []sdkmetric.PeriodicReaderOption
	if c.Interval > 0 {
		opts = append(opts, sdkmetric.WithInterval(c.Interval))
	}
	if c.Timeout > 0 {
		opts = append(opts, sdkmetric.WithTimeout(c.Timeout))
	}
	if c.MaxExportBatchSize > 0 {
		opts = append(opts, sdkmetric.WithMaxExportBatchSize(c.MaxExportBatchSize))
	}
	return opts
}
