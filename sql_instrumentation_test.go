package edotgo

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type testSQLConnector struct{}

func (testSQLConnector) Connect(context.Context) (driver.Conn, error) { return testSQLConn{}, nil }
func (testSQLConnector) Driver() driver.Driver                        { return nil }

type testSQLConn struct{}

func (testSQLConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (testSQLConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (testSQLConn) Close() error                        { return nil }

var testSQLFailure = errors.New("private driver error")

func (testSQLConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, testSQLFailure
}

func TestSQLInstrumentationOmitsQueryAndDriverError(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	mp := sdkmetric.NewMeterProvider()
	ctx := context.Background()
	t.Cleanup(func() {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
	})
	sdk := &SDK{TracerProvider: tp, MeterProvider: mp}
	db := sdk.OpenSQLDB(testSQLConnector{})
	t.Cleanup(func() { _ = db.Close() })

	ctx, root := tp.Tracer("test").Start(ctx, "root")
	defer root.End()
	const privateSQL = "UPDATE orders SET secret = 'private value'"
	if _, err := db.ExecContext(ctx, privateSQL); !errors.Is(err, testSQLFailure) {
		t.Fatalf("expected the original driver error, got %v", err)
	}
	found := false
	for _, span := range recorder.Ended() {
		found = found || span.Parent().SpanID() == root.SpanContext().SpanID()
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.Emit(), privateSQL) {
				t.Fatal("span exposed SQL text")
			}
		}
		for _, event := range span.Events() {
			for _, attr := range event.Attributes {
				if strings.Contains(attr.Value.Emit(), testSQLFailure.Error()) {
					t.Fatal("span exposed driver error text")
				}
			}
		}
	}
	if !found {
		t.Fatal("expected a SQL span under the request span")
	}
}
