package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	edotgo "example.com/edotgo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() (err error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sdk, err := edotgo.NewFromEnv(ctx)
	if err != nil {
		return err
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = errors.Join(err, sdk.Shutdown(shutdown))
	}()
	logger := sdk.Logger("example.orders")
	tracer := sdk.TracerProvider.Tracer("example.orders")
	counter, err := sdk.MeterProvider.Meter("example.orders").Int64Counter("orders.created")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /orders/{id}", sdk.HTTPHandler("GET /orders/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), "load-order")
		defer span.End()
		span.SetAttributes(attribute.String("order.id", r.PathValue("id")))
		if r.PathValue("id") == "error" {
			err := fmt.Errorf("example order lookup failed")
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			logger.ErrorContext(ctx, "order lookup failed", "error", err)
			http.Error(w, "lookup failed", http.StatusInternalServerError)
			return
		}
		counter.Add(ctx, 1)
		logger.InfoContext(ctx, "order loaded", "order.id", r.PathValue("id"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ok"}`)
	})))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	server := &http.Server{Addr: "127.0.0.1:8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Print("example listening on http://127.0.0.1:8080")
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		return err
	}
	return nil
}
