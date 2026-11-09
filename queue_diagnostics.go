// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"sort"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// ProcessorQueueDiagnostics contains upstream SDK observations for one processor.
// Component identifies the upstream processor, not an SDK instance or service.
// Depth excludes the batch currently being exported. QueueFullDropped is the
// observed counter value, with the temporality indicated by OverflowTemporality.
// For log processors, overflow reporting can lag until the next dequeue/flush.
type ProcessorQueueDiagnostics struct {
	Signal                   string
	Component                string
	Depth                    int64
	Capacity                 int64
	DepthAvailable           bool
	CapacityAvailable        bool
	QueueFullDropped         int64
	OverflowCountAvailable   bool
	OverflowTemporality      string
	OverflowReportingDelayed bool
}

// QueueDiagnostics is a sanitized, process-scoped view of upstream SDK metrics.
// It must not be attributed to a specific SDK instance by creation order.
type QueueDiagnostics struct{ Processors []ProcessorQueueDiagnostics }

// QueueDiagnosticsFromMetrics reads pinned upstream SDK observability metrics.
// Enable OTEL_GO_X_OBSERVABILITY=true before constructing processors and route
// the global OTel MeterProvider to a provider with a reader/exporter. This
// function never changes environment/global providers, collects metrics, or
// sends data. Missing metrics remain unavailable, not zero-valued measurements.
// It accepts only the known instrumentation scopes and queue metric names;
// resource attributes, payloads and unrelated metric attributes are excluded.
func QueueDiagnosticsFromMetrics(data *metricdata.ResourceMetrics) QueueDiagnostics {
	out := QueueDiagnostics{Processors: []ProcessorQueueDiagnostics{}}
	if data == nil {
		return out
	}
	type key struct{ signal, component string }
	processors := map[key]*ProcessorQueueDiagnostics{}
	for _, scope := range data.ScopeMetrics {
		signal, prefix := "", ""
		switch scope.Scope.Name {
		case "go.opentelemetry.io/otel/sdk/trace/internal/observ":
			signal, prefix = "traces", "otel.sdk.processor.span."
		case "go.opentelemetry.io/otel/sdk/log/internal/observ":
			signal, prefix = "logs", "otel.sdk.processor.log."
		default:
			continue
		}
		get := func(attrs attribute.Set) *ProcessorQueueDiagnostics {
			component, ok := attrs.Value("otel.component.name")
			if !ok || component.Type() != attribute.STRING {
				return nil
			}
			name := component.AsString()
			// Upstream generates batching_span_processor/<id> and batching_log_processor/<id>.
			expected := "batching_span_processor/"
			if signal == "logs" {
				expected = "batching_log_processor/"
			}
			if !validProcessorName(name, expected) {
				return nil
			}
			k := key{signal, name}
			p := processors[k]
			if p == nil {
				p = &ProcessorQueueDiagnostics{Signal: signal, Component: name, OverflowReportingDelayed: signal == "logs"}
				processors[k] = p
			}
			return p
		}
		for _, m := range scope.Metrics {
			switch m.Name {
			case prefix + "queue.size", prefix + "queue.capacity":
				var points []metricdata.DataPoint[int64]
				switch value := m.Data.(type) {
				case metricdata.Gauge[int64]:
					points = value.DataPoints
				case metricdata.Sum[int64]:
					// Upstream uses observable up/down counters. Delta values
					// are changes since a previous collection, not queue depth.
					if value.IsMonotonic || value.Temporality != metricdata.CumulativeTemporality {
						continue
					}
					points = value.DataPoints
				default:
					continue
				}
				for _, point := range points {
					if point.Value < 0 {
						continue
					}
					p := get(point.Attributes)
					if p == nil {
						continue
					}
					if m.Name == prefix+"queue.size" {
						p.Depth = point.Value
						p.DepthAvailable = true
					} else {
						p.Capacity = point.Value
						p.CapacityAvailable = true
					}
				}
			case prefix + "processed":
				sum, ok := m.Data.(metricdata.Sum[int64])
				if !ok || !sum.IsMonotonic {
					continue
				}
				temporality := ""
				switch sum.Temporality {
				case metricdata.CumulativeTemporality:
					temporality = "cumulative"
				case metricdata.DeltaTemporality:
					temporality = "delta"
				default:
					continue
				}
				for _, point := range sum.DataPoints {
					if point.Value < 0 {
						continue
					}
					p := get(point.Attributes)
					if p == nil {
						continue
					}
					// A processed counter without queue_full is not a queue-drop measurement.
					reason, ok := point.Attributes.Value("error.type")
					if !ok || reason.AsString() != "queue_full" {
						continue
					}
					p.QueueFullDropped += point.Value
					p.OverflowCountAvailable = true
					p.OverflowTemporality = temporality
				}
			}
		}
	}
	for _, p := range processors {
		out.Processors = append(out.Processors, *p)
	}
	sort.Slice(out.Processors, func(i, j int) bool {
		a, b := out.Processors[i], out.Processors[j]
		if a.Signal != b.Signal {
			return a.Signal < b.Signal
		}
		return a.Component < b.Component
	})
	return out
}

func validProcessorName(name, prefix string) bool {
	if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
		return false
	}
	for _, c := range name[len(prefix):] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
