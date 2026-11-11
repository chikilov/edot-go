// SPDX-License-Identifier: Apache-2.0
// Consistent56 behavior follows OpenTelemetry Java contrib's
// ConsistentSampler, ConsistentParentBasedSampler and OtelTraceState.
package edotgo

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const consistentMax = uint64(1) << 56

type consistentParentSampler struct {
	threshold uint64
	ratio     float64
}

func (s consistentParentSampler) Description() string {
	return fmt.Sprintf("ConsistentParentProbabilityBased{%g}", s.ratio)
}

func (s consistentParentSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	parent := trace.SpanContextFromContext(p.ParentContext)
	state := parseConsistentState(parent.TraceState().Get("ot"))
	threshold, reliable := s.threshold, true
	if parent.IsValid() {
		threshold = state.threshold
		if threshold > consistentMax {
			reliable = false
			threshold = consistentMax
			if parent.IsSampled() {
				threshold = 0
			}
		}
	}
	randomness := state.randomness
	if randomness >= consistentMax {
		randomness = 0
		for _, b := range p.TraceID[9:] {
			randomness = randomness<<8 | uint64(b)
		}
	}
	decision := sdktrace.Drop
	state.threshold = consistentMax + 1
	if threshold <= randomness {
		decision = sdktrace.RecordAndSample
		if reliable {
			state.threshold = threshold
		}
	}
	// The Java API ignores empty tracestate updates and keeps the parent member.
	// Preserve that behavior for compatibility with its 1.61.0-alpha sampler.
	ts := parent.TraceState()
	if value := state.serialize(); value != "" {
		// serialize produces a bounded valid W3C vendor value.
		ts, _ = ts.Insert("ot", value)
	}
	return sdktrace.SamplingResult{Decision: decision, Tracestate: ts}
}

type consistentState struct {
	threshold  uint64
	randomness uint64
	other      []string
}

var consistentPair = regexp.MustCompile(`^[a-z][a-z0-9]*:[A-Za-z0-9._-]*$`)

func emptyConsistentState() consistentState {
	return consistentState{threshold: consistentMax + 1, randomness: consistentMax}
}

func parseConsistentState(value string) consistentState {
	state := emptyConsistentState()
	if value == "" || len(value) > 256 {
		return state
	}
	for _, pair := range strings.Split(value, ";") {
		if !consistentPair.MatchString(pair) {
			return emptyConsistentState()
		}
		key, val, _ := strings.Cut(pair, ":")
		switch key {
		case "th":
			state.threshold = parseConsistentHex(val, false)
		case "rv":
			state.randomness = parseConsistentHex(val, true)
		default:
			state.other = append(state.other, pair)
		}
	}
	return state
}

func parseConsistentHex(value string, random bool) uint64 {
	if len(value) > 14 || random && len(value) != 14 {
		return consistentMax + 1
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return consistentMax + 1
		}
	}
	if value == "" {
		return 0
	}
	n, _ := strconv.ParseUint(value, 16, 56)
	return n << (4 * (14 - len(value)))
}

func (s consistentState) serialize() string {
	parts := make([]string, 0, 2+len(s.other))
	length := 0
	if s.threshold < consistentMax {
		value := strings.TrimRight(fmt.Sprintf("%014x", s.threshold), "0")
		if value == "" {
			value = "0"
		}
		parts = append(parts, "th:"+value)
		length = len(parts[0])
	}
	if s.randomness < consistentMax {
		parts = append(parts, fmt.Sprintf("rv:%014x", s.randomness))
		length = len(strings.Join(parts, ";"))
	}
	for _, pair := range s.other {
		next := length + len(pair)
		if length > 0 {
			next++
		}
		if next > 256 {
			break
		}
		parts = append(parts, pair)
		length = next
	}
	return strings.Join(parts, ";")
}

func newConsistentParentSampler(ratio float64) sdktrace.Sampler {
	return consistentParentSampler{
		threshold: consistentMax - uint64(math.Round(ratio*float64(consistentMax))),
		ratio:     ratio,
	}
}
