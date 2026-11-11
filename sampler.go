// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"fmt"
	"math"
	"sync/atomic"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// dynamicSampler uses an immutable delegate, swapped atomically at runtime.
// Delegates include standard Go samplers and the opt-in consistent56 sampler.
type dynamicSampler struct {
	name     string
	delegate atomic.Pointer[samplerHolder]
}
type samplerHolder struct {
	sampler sdktrace.Sampler
	ratio   float64
}

func newSampler(name string, ratio float64) *dynamicSampler {
	s := &dynamicSampler{name: name}
	_ = s.setRatio(ratio)
	return s
}
func (s *dynamicSampler) setRatio(ratio float64) error {
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 1 {
		return fmt.Errorf("sample ratio must be between 0 and 1")
	}
	var d sdktrace.Sampler
	switch s.name {
	case "always_on":
		d = sdktrace.AlwaysSample()
	case "always_off":
		d = sdktrace.NeverSample()
	case "experimental_composite_parentbased_traceidratio":
		d = newConsistentParentSampler(ratio)
	case "traceidratio":
		d = sdktrace.TraceIDRatioBased(ratio)
	case "parentbased_always_on":
		d = sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "parentbased_always_off":
		d = sdktrace.ParentBased(sdktrace.NeverSample())
	default:
		d = sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	}
	s.delegate.Store(&samplerHolder{sampler: d, ratio: ratio})
	return nil
}
func (s *dynamicSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	return s.delegate.Load().sampler.ShouldSample(p)
}
func (s *dynamicSampler) Description() string { return s.delegate.Load().sampler.Description() }
