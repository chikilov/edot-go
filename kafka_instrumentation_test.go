package edotgo

import (
	"context"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestKafkaTracePropagation(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, parent := tp.Tracer("test").Start(context.Background(), "parent")
	defer parent.End()
	tracer := (&SDK{TracerProvider: tp}).kafkaTracer()
	outgoing := &kgo.Record{Topic: "orders", Key: []byte("private-key"), Context: ctx}
	tracer.OnProduceRecordBuffered(outgoing)
	tracer.OnProduceRecordUnbuffered(outgoing, nil)
	incoming := &kgo.Record{Topic: outgoing.Topic, Key: outgoing.Key, Headers: outgoing.Headers}
	tracer.OnFetchRecordBuffered(incoming)
	tracer.OnFetchRecordUnbuffered(incoming, true)
	spans := recorder.Ended()
	if len(spans) != 2 || spans[0].Parent().SpanID() != parent.SpanContext().SpanID() ||
		spans[1].Parent().SpanID() != spans[0].SpanContext().SpanID() {
		t.Fatal("Kafka hooks did not preserve the producer-to-consumer parent chain")
	}
	for _, span := range spans {
		for _, attr := range span.Attributes() {
			if attr.Value.Emit() == "private-key" {
				t.Fatal("record key appeared in a span")
			}
		}
	}
}
