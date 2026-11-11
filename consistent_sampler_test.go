package edotgo

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func consistentTraceID(t *testing.T, value string) trace.TraceID {
	t.Helper()
	id, err := trace.TraceIDFromHex(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestConsistent56RootThreshold(t *testing.T) {
	cases := []struct {
		ratio    float64
		id       string
		decision sdktrace.SamplingDecision
		state    string
	}{
		{0, "000000000000000000ffffffffffffff", sdktrace.Drop, ""},
		{1, "00000000000000000000000000000001", sdktrace.RecordAndSample, "th:0"},
		{0.5, "0000000000000000007fffffffffffff", sdktrace.Drop, ""},
		{0.5, "00000000000000000080000000000000", sdktrace.RecordAndSample, "th:8"},
		{0.25, "000000000000000000bfffffffffffff", sdktrace.Drop, ""},
		{0.25, "000000000000000000c0000000000000", sdktrace.RecordAndSample, "th:c"},
	}
	for _, tc := range cases {
		s := newConsistentParentSampler(tc.ratio)
		got := s.ShouldSample(sdktrace.SamplingParameters{ParentContext: context.Background(), TraceID: consistentTraceID(t, tc.id)})
		if got.Decision != tc.decision || got.Tracestate.Get("ot") != tc.state {
			t.Fatalf("ratio %g id %s got %v %s", tc.ratio, tc.id, got.Decision, got.Tracestate)
		}
	}
}

func TestConsistent56ParentState(t *testing.T) {
	id := consistentTraceID(t, "00000000000000000000000000000001")
	cases := []struct {
		ot      string
		sampled bool
		want    sdktrace.SamplingDecision
		state   string
	}{
		{"th:8;rv:ffffffffffffff;x:keep", false, sdktrace.RecordAndSample, "th:8;rv:ffffffffffffff;x:keep"},
		{"th:8;rv:00000000000000", true, sdktrace.Drop, "rv:00000000000000"},
		{"", true, sdktrace.RecordAndSample, ""},
		{"", false, sdktrace.Drop, ""},
		{"th:INVALID;x:keep", true, sdktrace.RecordAndSample, "x:keep"},
		{"th:8;", true, sdktrace.RecordAndSample, "th:8;"},
	}
	for _, tc := range cases {
		t.Run(tc.ot, func(t *testing.T) {
			ts, err := trace.ParseTraceState("vendor=keep")
			if err != nil {
				t.Fatal(err)
			}
			if tc.ot != "" {
				ts, err = ts.Insert("ot", tc.ot)
				if err != nil {
					t.Fatal(err)
				}
			}
			flags := trace.TraceFlags(0)
			if tc.sampled {
				flags = trace.FlagsSampled
			}
			parent := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: id, SpanID: trace.SpanID{1}, TraceFlags: flags, TraceState: ts, Remote: true,
			})
			got := newConsistentParentSampler(0).ShouldSample(sdktrace.SamplingParameters{
				ParentContext: trace.ContextWithSpanContext(context.Background(), parent), TraceID: id,
			})
			if got.Decision != tc.want || got.Tracestate.Get("ot") != tc.state || got.Tracestate.Get("vendor") != "keep" {
				t.Fatalf("got %v %s", got.Decision, got.Tracestate)
			}
		})
	}
}

func TestConsistent56DynamicRatioAndBounds(t *testing.T) {
	sdk := &SDK{sampler: newSampler("experimental_composite_parentbased_traceidratio", 0)}
	p := sdktrace.SamplingParameters{ParentContext: context.Background(), TraceID: consistentTraceID(t, "00000000000000000080000000000000")}
	if sdk.sampler.ShouldSample(p).Decision != sdktrace.Drop {
		t.Fatal("ratio zero sampled")
	}
	if err := sdk.SetSampleRatio(0.5); err != nil || sdk.sampler.ShouldSample(p).Decision != sdktrace.RecordAndSample {
		t.Fatal("dynamic ratio did not update")
	}
	if err := sdk.SetSampleRatio(-1); err == nil || sdk.sampler.ShouldSample(p).Decision != sdktrace.RecordAndSample {
		t.Fatal("invalid update changed the sampler")
	}
	if state := parseConsistentState(strings.Repeat("x", 257)); state.threshold <= consistentMax {
		t.Fatal("oversized tracestate was accepted")
	}
	for _, input := range []string{"rv:0", "th:123456789abcdef", "th:A"} {
		state := parseConsistentState(input)
		if input != "rv:0" && state.threshold <= consistentMax {
			t.Fatal("invalid hex threshold was accepted")
		}
	}
}

func TestConsistent56JavaReferenceVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/consistent56-java.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Ratio         float64 `json:"ratio"`
		TraceID       string  `json:"trace_id"`
		ParentSampled *bool   `json:"parent_sampled"`
		ParentOT      string  `json:"parent_ot"`
		Decision      string  `json:"decision"`
		ExpectedOT    string  `json:"expected_ot"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for i, tc := range cases {
		ctx := context.Background()
		id := consistentTraceID(t, tc.TraceID)
		if tc.ParentSampled != nil {
			ts, _ := trace.ParseTraceState("vendor=keep")
			if tc.ParentOT != "" {
				ts, err = ts.Insert("ot", tc.ParentOT)
				if err != nil {
					t.Fatal(err)
				}
			}
			flags := trace.TraceFlags(0)
			if *tc.ParentSampled {
				flags = trace.FlagsSampled
			}
			ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: id, SpanID: trace.SpanID{1}, TraceFlags: flags, TraceState: ts, Remote: true,
			}))
		}
		got := newConsistentParentSampler(tc.Ratio).ShouldSample(sdktrace.SamplingParameters{ParentContext: ctx, TraceID: id})
		decision := "DROP"
		if got.Decision == sdktrace.RecordAndSample {
			decision = "RECORD_AND_SAMPLE"
		}
		if decision != tc.Decision || got.Tracestate.Get("ot") != tc.ExpectedOT {
			t.Errorf("Java case %d: got %s %q, expected %s %q", i, decision, got.Tracestate.Get("ot"), tc.Decision, tc.ExpectedOT)
		}
	}
}

func TestConsistent56EnvironmentConfiguration(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "experimental_composite_parentbased_traceidratio")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.5")
	c, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.Sampler != "experimental_composite_parentbased_traceidratio" || c.SampleRatio != 0.5 {
		t.Fatal("environment configuration did not select the consistent56 sampler")
	}
}
