package edotgo

import (
	"context"
	"net"
	"testing"
	"time"

	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
)

func TestGRPCInstrumentationPropagatesTrace(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		_ = mp.Shutdown(context.Background())
	})
	sdk := &SDK{TracerProvider: tp, MeterProvider: mp, Propagator: propagation.TraceContext{}}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(sdk.GRPCServerOption())
	healthpb.RegisterHealthServer(server, health.NewServer())
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	client, err := grpc.NewClient("passthrough:///test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}), sdk.GRPCClientOption())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx, root := tp.Tracer("test").Start(ctx, "root")
	defer root.End()
	if _, err := healthpb.NewHealthClient(client).Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}

	var outgoing, incoming sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		switch span.SpanKind() {
		case trace.SpanKindClient:
			outgoing = span
		case trace.SpanKindServer:
			incoming = span
		}
	}
	if outgoing == nil || incoming == nil {
		t.Fatal("expected client and server spans")
	}
	if outgoing.Parent().SpanID() != root.SpanContext().SpanID() ||
		incoming.Parent().SpanID() != outgoing.SpanContext().SpanID() ||
		incoming.SpanContext().TraceID() != root.SpanContext().TraceID() {
		t.Fatal("RPC spans did not preserve the parent chain")
	}
}
