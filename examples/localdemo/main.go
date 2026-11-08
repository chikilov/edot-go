// localdemo sends synthetic frontend->backend requests to a local OTLP collector.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	edotgo "github.com/chikilov/edot-go"
	"go.opentelemetry.io/otel/trace"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() (err error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return fmt.Errorf("set the local collector endpoint before running localdemo")
	}
	cfg, err := edotgo.ConfigFromEnv()
	if err != nil {
		return err
	}
	if cfg.Disabled || !cfg.Traces || !cfg.Metrics || !cfg.Logs {
		return fmt.Errorf("localdemo requires all signal exporters")
	}
	cfg.RuntimeMetrics = false
	cfg.Sampler = "always_on"
	cfg.ServiceName = "edot-go-local-frontend"
	front, err := edotgo.New(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = errors.Join(err, front.Shutdown(ctx))
	}()
	cfg.ServiceName = "edot-go-local-backend"
	back, err := edotgo.New(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = errors.Join(err, back.Shutdown(ctx))
	}()
	backCounter, err := back.MeterProvider.Meter("localdemo").Int64Counter("localdemo.backend.requests")
	if err != nil {
		return err
	}
	frontCounter, err := front.MeterProvider.Meter("localdemo").Int64Counter("localdemo.frontend.requests")
	if err != nil {
		return err
	}
	backend := httptest.NewServer(back.HTTPHandler("GET /work", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backCounter.Add(r.Context(), 1)
		if r.URL.Query().Get("fail") == "1" {
			back.Logger("localdemo").ErrorContext(r.Context(), "synthetic backend failure")
			http.Error(w, "synthetic failure", 500)
			return
		}
		back.Logger("localdemo").InfoContext(r.Context(), "synthetic backend success")
		_, _ = io.WriteString(w, "ok")
	})))
	defer backend.Close()
	downstream := &http.Client{Transport: front.HTTPTransport(http.DefaultTransport), Timeout: 3 * time.Second}
	defer downstream.CloseIdleConnections()
	frontend := httptest.NewServer(front.HTTPHandler("GET /demo", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		frontCounter.Add(r.Context(), 1)
		req, err := http.NewRequestWithContext(r.Context(), "GET", backend.URL+"/work?"+r.URL.RawQuery, nil)
		if err != nil {
			http.Error(w, "request construction failed", 500)
			return
		}
		response, err := downstream.Do(req)
		if err != nil {
			front.Logger("localdemo").ErrorContext(r.Context(), "downstream connection failed")
			http.Error(w, "downstream failed", 502)
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode >= 500 {
			front.Logger("localdemo").ErrorContext(r.Context(), "synthetic frontend failure")
		} else {
			front.Logger("localdemo").InfoContext(r.Context(), "synthetic frontend success")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		_ = json.NewEncoder(w).Encode(map[string]string{"trace_id": trace.SpanContextFromContext(r.Context()).TraceID().String()})
	})))
	defer frontend.Close()
	caller := &http.Client{Timeout: 5 * time.Second}
	defer caller.CloseIdleConnections()
	ids := []string{}
	statuses := []int{}
	for i := 0; i < 6; i++ {
		suffix := ""
		if i%3 == 0 {
			suffix = "?fail=1"
		}
		response, err := caller.Get(frontend.URL + "/demo" + suffix)
		if err != nil {
			return err
		}
		var body map[string]string
		decodeErr := json.NewDecoder(response.Body).Decode(&body)
		_ = response.Body.Close()
		if decodeErr != nil {
			return decodeErr
		}
		ids = append(ids, body["trace_id"])
		statuses = append(statuses, response.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := errors.Join(front.ForceFlush(ctx), back.ForceFlush(ctx)); err != nil {
		return err
	}
	fd, bd := front.Diagnostics(), back.Diagnostics()
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		TraceIDs     []string           `json:"trace_ids"`
		HTTPStatuses []int              `json:"http_statuses"`
		Frontend     edotgo.Diagnostics `json:"frontend"`
		Backend      edotgo.Diagnostics `json:"backend"`
	}{ids, statuses, fd, bd}); err != nil {
		return err
	}
	for _, d := range []edotgo.Diagnostics{fd, bd} {
		if d.Traces.FailedBatches+d.Metrics.FailedBatches+d.Logs.FailedBatches > 0 {
			return fmt.Errorf("synthetic telemetry export failed")
		}
	}
	return nil
}
