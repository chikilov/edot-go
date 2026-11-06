package edotgo

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	logpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"
)

func cleanEnv(t *testing.T) {
	t.Helper()
	for _, item := range os.Environ() {
		k, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(k, "OTEL_") || strings.HasPrefix(k, "EDOT_GO_") || strings.HasPrefix(k, "ELASTIC_OTEL_JAVA_") {
			t.Setenv(k, "")
		}
	}
}
func stringAttr(attrs []*commonpb.KeyValue, key string) string {
	for _, a := range attrs {
		if a.Key == key {
			return a.Value.GetStringValue()
		}
	}
	return ""
}

func TestOTLPWireExport(t *testing.T) {
	cleanEnv(t)
	var mu sync.Mutex
	var spans tracepb.ExportTraceServiceRequest
	var metrics metricpb.ExportMetricsServiceRequest
	var logs logpb.ExportLogsServiceRequest
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "ApiKey test-only" {
			t.Errorf("authentication header not preserved")
		}
		if r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Errorf("unexpected content type")
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		seen[r.URL.Path]++
		var msg proto.Message
		switch r.URL.Path {
		case "/ingest/v1/traces":
			msg = &spans
		case "/ingest/v1/metrics":
			msg = &metrics
		case "/ingest/v1/logs":
			msg = &logs
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if err := proto.Unmarshal(body, msg); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
	}))
	defer server.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL+"/ingest")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=ApiKey%20test-only")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment=production,service.name=resource-name")
	t.Setenv("OTEL_SERVICE_NAME", "orders")
	t.Setenv("EDOT_GO_RUNTIME_METRICS_ENABLED", "false")
	t.Setenv("EDOT_GO_SPAN_ATTRIBUTES_COPY_FROM_BAGGAGE_INCLUDE", "tenant.*")
	t.Setenv("EDOT_GO_LOG_ATTRIBUTES_COPY_FROM_BAGGAGE_INCLUDE", "tenant.*")
	s, err := NewFromEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	m, _ := baggage.NewMember("tenant.id", "team-a")
	secret, _ := baggage.NewMember("private.token", "do-not-copy")
	bag, _ := baggage.New(m, secret)
	ctx := baggage.ContextWithBaggage(context.Background(), bag)
	ctx, slow := s.TracerProvider.Tracer("test").Start(ctx, "slow", trace.WithTimestamp(time.Now().Add(-20*time.Millisecond)))
	s.Logger("test").InfoContext(ctx, "order placed")
	_, fast := s.TracerProvider.Tracer("test").Start(ctx, "fast")
	fast.End()
	slow.End()
	counter, err := s.MeterProvider.Meter("test").Int64Counter("orders.total")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(ctx, 7)
	flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.ForceFlush(flush); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range []string{"traces", "metrics", "logs"} {
		if seen["/ingest/v1/"+p] == 0 {
			t.Errorf("missing %s export", p)
		}
	}
	if len(spans.ResourceSpans) != 1 {
		t.Fatalf("resources: %d", len(spans.ResourceSpans))
	}
	rs := spans.ResourceSpans[0]
	if stringAttr(rs.Resource.Attributes, "service.name") != "orders" {
		t.Error("service name precedence")
	}
	if stringAttr(rs.Resource.Attributes, "deployment.environment.name") != "production" {
		t.Error("legacy environment migration")
	}
	if stringAttr(rs.Resource.Attributes, "telemetry.distro.name") != "edot-go-unofficial" {
		t.Error("distribution identity")
	}
	spanCount := 0
	for _, scope := range rs.ScopeSpans {
		for _, span := range scope.Spans {
			spanCount++
			stack := stringAttr(span.Attributes, "code.stacktrace")
			if span.Name == "slow" && !strings.Contains(stack, "TestOTLPWireExport") {
				t.Error("missing start callsite")
			}
			if span.Name == "fast" && stack != "" {
				t.Error("fast span contains stack")
			}
			if stringAttr(span.Attributes, "tenant.id") != "team-a" {
				t.Error("missing allowed baggage")
			}
			if stringAttr(span.Attributes, "private.token") != "" {
				t.Error("copied unapproved baggage")
			}
		}
	}
	if spanCount != 2 {
		t.Errorf("spans: %d", spanCount)
	}
	if len(logs.ResourceLogs) != 1 {
		t.Fatal("missing logs")
	}
	record := logs.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if record.Body.GetStringValue() != "order placed" || len(record.TraceId) != 16 || len(record.SpanId) != 8 {
		t.Error("log trace correlation missing")
	}
	if stringAttr(record.Attributes, "tenant.id") != "team-a" || stringAttr(record.Attributes, "private.token") != "" {
		t.Error("log baggage filtering")
	}
	found := false
	for _, rm := range metrics.ResourceMetrics {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "orders.total" {
					found = true
					if m.GetSum().AggregationTemporality != 1 || m.GetSum().DataPoints[0].GetAsInt() != 7 {
						t.Error("expected delta counter value 7")
					}
				}
			}
		}
	}
	if !found {
		t.Error("missing counter")
	}
}

func TestDynamicSamplerAndParents(t *testing.T) {
	s := newSampler("parentbased_traceidratio", 0)
	id := trace.TraceID{1, 2, 3}
	root := sdktrace.SamplingParameters{TraceID: id, ParentContext: context.Background()}
	if s.ShouldSample(root).Decision != sdktrace.Drop {
		t.Fatal("ratio zero sampled root")
	}
	parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: id, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled, Remote: true})
	p := root
	p.ParentContext = trace.ContextWithRemoteSpanContext(context.Background(), parent)
	if s.ShouldSample(p).Decision != sdktrace.RecordAndSample {
		t.Error("sampled remote parent lost")
	}
	if err := s.setRatio(1); err != nil {
		t.Fatal(err)
	}
	if s.ShouldSample(root).Decision != sdktrace.RecordAndSample {
		t.Error("ratio update ineffective")
	}
	parent = parent.WithTraceFlags(0)
	p.ParentContext = trace.ContextWithRemoteSpanContext(context.Background(), parent)
	if s.ShouldSample(p).Decision != sdktrace.Drop {
		t.Error("unsampled parent overridden")
	}
	for _, r := range []float64{-1, 2, math.NaN(), math.Inf(1)} {
		if s.setRatio(r) == nil {
			t.Error("accepted invalid ratio")
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_ = s.setRatio(float64(j % 2))
				s.ShouldSample(root)
				_ = s.Description()
			}
		}()
	}
	wg.Wait()
}

func TestConfigValidation(t *testing.T) {
	for _, tt := range []struct{ k, v string }{
		{"OTEL_TRACES_SAMPLER_ARG", "NaN"}, {"OTEL_TRACES_SAMPLER_ARG", "1.1"}, {"OTEL_TRACES_SAMPLER", "experimental_composite_parentbased_traceidratio"},
		{"OTEL_EXPORTER_OTLP_PROTOCOL", "invalid"}, {"OTEL_LOGS_EXPORTER", "console"}, {"EDOT_GO_SPAN_STACKTRACE_MIN_DURATION", "garbage"},
		{"EDOT_GO_SPAN_ATTRIBUTES_COPY_FROM_BAGGAGE_INCLUDE", "["}, {"OTEL_SDK_DISABLED", "invalid"},
	} {
		t.Run(tt.k+tt.v, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv(tt.k, tt.v)
			if _, err := ConfigFromEnv(); err == nil {
				t.Error("accepted invalid configuration")
			}
		})
	}
	t.Run("defaults", func(t *testing.T) {
		cleanEnv(t)
		c, err := ConfigFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if c.SampleRatio != 1 || c.StacktraceMinDuration != 5*time.Millisecond {
			t.Error("unexpected defaults")
		}
	})
	t.Run("alias", func(t *testing.T) {
		cleanEnv(t)
		t.Setenv("OTEL_JAVA_EXPERIMENTAL_SPAN_STACKTRACE_MIN_DURATION", "-1")
		c, err := ConfigFromEnv()
		if err != nil || c.StacktraceMinDuration >= 0 {
			t.Error("legacy disable alias")
		}
	})
	t.Run("precedence", func(t *testing.T) {
		cleanEnv(t)
		t.Setenv("OTEL_JAVA_EXPERIMENTAL_SPAN_STACKTRACE_MIN_DURATION", "1s")
		t.Setenv("EDOT_GO_SPAN_STACKTRACE_MIN_DURATION", "0")
		c, err := ConfigFromEnv()
		if err != nil || c.StacktraceMinDuration != 0 {
			t.Error("Go setting must win")
		}
	})
}

func TestTemporality(t *testing.T) {
	for _, tt := range []struct {
		pref string
		k    sdkmetric.InstrumentKind
		want metricdata.Temporality
	}{
		{"", sdkmetric.InstrumentKindCounter, metricdata.DeltaTemporality},
		{"", sdkmetric.InstrumentKindUpDownCounter, metricdata.CumulativeTemporality},
		{"LOWMEMORY", sdkmetric.InstrumentKindObservableCounter, metricdata.CumulativeTemporality},
		{"DELTA", sdkmetric.InstrumentKindObservableCounter, metricdata.DeltaTemporality},
		{"CUMULATIVE", sdkmetric.InstrumentKindHistogram, metricdata.CumulativeTemporality},
	} {
		t.Setenv("OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE", tt.pref)
		if temporalitySelector()(tt.k) != tt.want {
			t.Errorf("preference %s kind %v", tt.pref, tt.k)
		}
	}
}

func TestDisabledSDKAndHTTPPropagation(t *testing.T) {
	cleanEnv(t)
	c := DefaultConfig()
	c.Disabled = true
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())
	_, span := s.TracerProvider.Tracer("test").Start(context.Background(), "disabled")
	if span.IsRecording() {
		t.Error("disabled SDK records spans")
	}
	span.End()
	if err := s.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	const incoming = "00-11111111111111111111111111111111-2222222222222222-01"
	var propagated string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { propagated = r.Header.Get("traceparent") }))
	defer target.Close()
	client := &http.Client{Transport: s.HTTPTransport(nil)}
	h := s.HTTPHandler("test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), "GET", target.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		resp.Body.Close()
	}))
	req := httptest.NewRequest("GET", "http://app/", nil)
	req.Header.Set("traceparent", incoming)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !strings.Contains(propagated, "11111111111111111111111111111111") {
		t.Errorf("lost distributed trace context: %s", propagated)
	}
}

// Ensure exporters drop data while disabled, then resume without recreation.
type countingSpanExporter struct {
	mu sync.Mutex
	n  int
}

func (e *countingSpanExporter) ExportSpans(_ context.Context, s []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.n += len(s)
	return nil
}
func (e *countingSpanExporter) Shutdown(context.Context) error { return nil }
func TestSendingGate(t *testing.T) {
	s := &SDK{}
	exp := &countingSpanExporter{}
	p := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spanGate{exp, &s.traces, nil}))
	defer p.Shutdown(context.Background())
	_, span := p.Tracer("test").Start(context.Background(), "dropped")
	span.End()
	s.SetSendingTraces(true)
	_, span = p.Tracer("test").Start(context.Background(), "sent", trace.WithAttributes(attribute.String("test", "value")))
	span.End()
	if exp.n != 1 {
		t.Errorf("exported %d spans", exp.n)
	}
}

// The embedded interfaces supply unused methods; these spies only exercise Export.
type metricSpy struct {
	sdkmetric.Exporter
	n int
}

func (e *metricSpy) Export(context.Context, *metricdata.ResourceMetrics) error { e.n++; return nil }

type logSpy struct {
	sdklog.Exporter
	n int
}

func (e *logSpy) Export(_ context.Context, r []sdklog.Record) error { e.n += len(r); return nil }
func TestMetricAndLogSendingGates(t *testing.T) {
	s := &SDK{}
	m := &metricSpy{}
	l := &logSpy{}
	mg := metricGate{m, &s.metrics, nil}
	lg := logGate{l, &s.logs, nil}
	if err := mg.Export(context.Background(), &metricdata.ResourceMetrics{}); err != nil {
		t.Fatal(err)
	}
	if err := lg.Export(context.Background(), []sdklog.Record{{}}); err != nil {
		t.Fatal(err)
	}
	if m.n != 0 || l.n != 0 {
		t.Fatal("disabled gates exported data")
	}
	s.SetSendingMetrics(true)
	s.SetSendingLogs(true)
	if err := mg.Export(context.Background(), &metricdata.ResourceMetrics{}); err != nil {
		t.Fatal(err)
	}
	if err := lg.Export(context.Background(), []sdklog.Record{{}}); err != nil {
		t.Fatal(err)
	}
	if m.n != 1 || l.n != 1 {
		t.Fatal("gates did not resume sending")
	}
}
