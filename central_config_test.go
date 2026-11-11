package edotgo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCentralConfigAppliesOnlyValidUpdates(t *testing.T) {
	var invalid atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if invalid.Load() {
			fmt.Fprint(w, `{"sample_ratio":2,"sending_logs":true}`)
			return
		}
		fmt.Fprint(w, `{"sample_ratio":0,"sending_logs":false}`)
	}))
	defer server.Close()
	sdk := &SDK{sampler: newSampler("traceidratio", 1)}
	sdk.SetSendingLogs(true)
	sdk.SetSendingMetrics(true)
	headers := http.Header{"Authorization": {"Bearer fixture"}}
	fetch := func() error {
		return sdk.fetchRuntimeConfig(context.Background(), server.Client(), server.URL, headers)
	}
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	description := sdk.sampler.Description()
	if sdk.logs.Load() || !sdk.metrics.Load() || description == newSampler("traceidratio", 1).Description() {
		t.Fatal("expected the update to change only logs and the sample ratio")
	}
	invalid.Store(true)
	if err := fetch(); err == nil || sdk.logs.Load() || sdk.sampler.Description() != description {
		t.Fatal("invalid update changed settings or was accepted")
	}
}
