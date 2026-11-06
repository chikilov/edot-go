// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ExportError is a sanitized export failure notification. It intentionally
// excludes raw errors, endpoints, headers and payloads, which can contain secrets.
// Kind is export, canceled, or deadline. Internal exporter retries are included
// in one notification after the final result, not one notification per request.
type ExportError struct {
	Signal string
	Items  uint64
	Kind   string
}

type SignalDiagnostics struct {
	ExportCalls          uint64
	SuccessfulBatches    uint64
	FailedBatches        uint64
	SuccessfulItems      uint64
	ItemsInFailedBatches uint64
	DisabledDroppedItems uint64
	InFlightBatches      int64
	LastSuccessAt        time.Time
	LastFailureAt        time.Time
	LastFailureKind      string
}

type StackDiagnostics struct {
	InFlight          int
	Capacity          int
	SkippedAtCapacity uint64
}

// Diagnostics is an instance-local snapshot, with no secrets or telemetry data.
// Counters are individually atomic; concurrent updates may span a snapshot.
// Queue depth/overflow are not exposed by the upstream batching API. They must
// not be inferred from InFlightBatches (which counts exporter calls, not a queue).
type Diagnostics struct {
	Traces                      SignalDiagnostics
	Metrics                     SignalDiagnostics
	Logs                        SignalDiagnostics
	Stack                       StackDiagnostics
	QueueDepthAvailable         bool
	QueueOverflowCountAvailable bool
	CallbackPanics              uint64
}

type signalCounters struct {
	calls, success, failure, successfulItems, failedItems, disabled atomic.Uint64
	inFlight                                                        atomic.Int64
	lastSuccess, lastFailure                                        atomic.Int64
	lastKind                                                        atomic.Uint32
}

type diagnosticState struct {
	traces, metrics, logs signalCounters
	callback              func(ExportError)
	callbackPanics        atomic.Uint64
}

func (d *diagnosticState) counters(signal string) *signalCounters {
	switch signal {
	case "traces":
		return &d.traces
	case "metrics":
		return &d.metrics
	default:
		return &d.logs
	}
}
func (d *diagnosticState) dropped(signal string, items uint64) {
	if d != nil {
		d.counters(signal).disabled.Add(items)
	}
}
func (d *diagnosticState) begin(signal string) {
	if d != nil {
		c := d.counters(signal)
		c.calls.Add(1)
		c.inFlight.Add(1)
	}
}
func (d *diagnosticState) finish(signal string, items uint64, err error) {
	if d == nil {
		return
	}
	c := d.counters(signal)
	c.inFlight.Add(-1)
	if err == nil {
		c.success.Add(1)
		c.successfulItems.Add(items)
		c.lastSuccess.Store(time.Now().UnixNano())
		return
	}
	kind, code := "export", uint32(1)
	if errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled {
		kind, code = "canceled", 2
	} else if errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded {
		kind, code = "deadline", 3
	}
	c.failure.Add(1)
	c.failedItems.Add(items)
	c.lastKind.Store(code)
	c.lastFailure.Store(time.Now().UnixNano())
	if d.callback != nil {
		func() {
			defer func() {
				if recover() != nil {
					d.callbackPanics.Add(1)
				}
			}()
			d.callback(ExportError{Signal: signal, Items: items, Kind: kind})
		}()
	}
}
func (c *signalCounters) snapshot() SignalDiagnostics {
	s := SignalDiagnostics{ExportCalls: c.calls.Load(), SuccessfulBatches: c.success.Load(), FailedBatches: c.failure.Load(), SuccessfulItems: c.successfulItems.Load(), ItemsInFailedBatches: c.failedItems.Load(), DisabledDroppedItems: c.disabled.Load(), InFlightBatches: c.inFlight.Load()}
	if n := c.lastSuccess.Load(); n != 0 {
		s.LastSuccessAt = time.Unix(0, n).UTC()
	}
	if n := c.lastFailure.Load(); n != 0 {
		s.LastFailureAt = time.Unix(0, n).UTC()
	}
	switch c.lastKind.Load() {
	case 1:
		s.LastFailureKind = "export"
	case 2:
		s.LastFailureKind = "canceled"
	case 3:
		s.LastFailureKind = "deadline"
	}
	return s
}

// Diagnostics returns a point-in-time view of this SDK instance. It does not
// replace OpenTelemetry's global error handler or send diagnostic telemetry.
func (s *SDK) Diagnostics() Diagnostics {
	var out Diagnostics
	if s.diagnostics != nil {
		d := s.diagnostics
		out.Traces = d.traces.snapshot()
		out.Metrics = d.metrics.snapshot()
		out.Logs = d.logs.snapshot()
		out.CallbackPanics = d.callbackPanics.Load()
	}
	if s.stack != nil {
		p := s.stack
		p.mu.Lock()
		out.Stack = StackDiagnostics{InFlight: p.slots, Capacity: maxInflightStacks, SkippedAtCapacity: p.skipped.Load()}
		p.mu.Unlock()
	}
	return out
}

// Metric items are data points, not metric instruments or time series.
func metricItems(data *metricdata.ResourceMetrics) uint64 {
	var n uint64
	if data == nil {
		return 0
	}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				n += uint64(len(d.DataPoints))
			case metricdata.Sum[float64]:
				n += uint64(len(d.DataPoints))
			case metricdata.Gauge[int64]:
				n += uint64(len(d.DataPoints))
			case metricdata.Gauge[float64]:
				n += uint64(len(d.DataPoints))
			case metricdata.Histogram[int64]:
				n += uint64(len(d.DataPoints))
			case metricdata.Histogram[float64]:
				n += uint64(len(d.DataPoints))
			case metricdata.ExponentialHistogram[int64]:
				n += uint64(len(d.DataPoints))
			case metricdata.ExponentialHistogram[float64]:
				n += uint64(len(d.DataPoints))
			case metricdata.Summary:
				n += uint64(len(d.DataPoints))
			}
		}
	}
	return n
}
