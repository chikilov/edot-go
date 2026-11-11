package edotgo

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"google.golang.org/protobuf/proto"
)

func opampRemote(hash, body string) *protobufs.AgentRemoteConfig {
	return &protobufs.AgentRemoteConfig{
		ConfigHash: []byte(hash),
		Config: &protobufs.AgentConfigMap{ConfigMap: map[string]*protobufs.AgentConfigObject{
			"elastic": {Body: []byte(body), ContentType: "application/json"},
		}},
	}
}

func TestOpAMPHTTPSApplyRejectAndReset(t *testing.T) {
	sdk := &SDK{sampler: newSampler("experimental_composite_parentbased_traceidratio", 1)}
	sdk.SetSendingLogs(true)
	sdk.SetSendingMetrics(true)
	sdk.SetSendingTraces(true)
	var stage atomic.Int32
	done := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-only" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			t.Error(err)
			return
		}
		var msg protobufs.AgentToServer
		if err := proto.Unmarshal(body, &msg); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if status := msg.RemoteConfigStatus; status != nil {
			switch string(status.LastRemoteConfigHash) {
			case "apply":
				if status.Status == protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED {
					if sdk.logs.Load() {
						t.Error("APPLIED was reported before disabling logs")
					}
					stage.CompareAndSwap(0, 1)
				}
			case "invalid":
				if status.Status == protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED {
					if sdk.logs.Load() {
						t.Error("invalid configuration changed the sending state")
					}
					stage.CompareAndSwap(1, 2)
				}
			case "reset":
				if status.Status == protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED {
					if !sdk.logs.Load() {
						t.Error("removed setting did not restore the baseline")
					}
					select {
					case done <- struct{}{}:
					default:
					}
				}
			}
		}
		response := &protobufs.ServerToAgent{
			InstanceUid: msg.InstanceUid,
			Capabilities: uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus |
				protobufs.ServerCapabilities_ServerCapabilities_OffersRemoteConfig),
		}
		switch stage.Load() {
		case 0:
			response.RemoteConfig = opampRemote("apply", `{"sampling_rate":"0","send_logs":"false"}`)
		case 1:
			response.RemoteConfig = opampRemote("invalid", `{"sampling_rate":"2","send_logs":"true"}`)
		default:
			response.RemoteConfig = opampRemote("reset", `{}`)
		}
		data, err := proto.Marshal(response)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		_, _ = w.Write(data)
	}))
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- sdk.RunOpAMP(ctx, OpAMPConfig{
			Endpoint: server.URL, Headers: http.Header{"Authorization": {"Bearer fixture-only"}},
			TLSConfig: &tls.Config{RootCAs: pool}, Interval: time.Second, ServiceName: "fixture",
		})
	}()
	select {
	case <-done:
	case err := <-result:
		t.Fatalf("OpAMP stopped before completing the configuration sequence: %v", err)
	case <-ctx.Done():
		t.Fatal("OpAMP exchange timed out")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected shutdown result: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("OpAMP did not stop")
	}
	if sdk.sampler.delegate.Load().ratio != 1 {
		t.Fatal("removed sampling setting did not restore baseline")
	}
}

func TestOpAMPSecurityAndConfigRedaction(t *testing.T) {
	sdk := &SDK{sampler: newSampler("traceidratio", 1)}
	for _, config := range []OpAMPConfig{
		{Endpoint: "http://localhost/opamp"},
		{Endpoint: "https://user:secret@example.invalid/opamp"},
		{Endpoint: "https://example.invalid", TLSConfig: &tls.Config{InsecureSkipVerify: true}},
	} {
		if err := sdk.RunOpAMP(context.Background(), config); err == nil {
			t.Fatal("accepted unsafe OpAMP settings")
		}
	}
	data, err := json.Marshal(OpAMPConfig{Headers: http.Header{"Authorization": {"private-secret"}}})
	if err != nil || strings.Contains(string(data), "private-secret") {
		t.Fatal("OpAMP configuration leaked credentials")
	}
	for _, body := range []string{`null`, `{"send_logs":"yes"}`, `{"sampling_rate":"NaN"}`, `{"unknown":"true"}`, `{"send_logs":"false","send_logs":"true"}`, `{"send_logs":null}`} {
		if _, _, err := elasticRuntimeConfig(opampRemote("test", body), RuntimeConfig{}, false); err == nil {
			t.Fatalf("accepted invalid configuration %s", body)
		}
	}
}

func TestOpAMPDoesNotFollowRedirects(t *testing.T) {
	var forwarded atomic.Int32
	destination := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		forwarded.Add(1)
	}))
	defer destination.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	pool := x509.NewCertPool()
	pool.AddCert(source.Certificate())
	pool.AddCert(destination.Certificate())
	sdk := &SDK{sampler: newSampler("traceidratio", 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := sdk.RunOpAMP(ctx, OpAMPConfig{
		Endpoint: source.URL, TLSConfig: &tls.Config{RootCAs: pool},
		Headers: http.Header{"Authorization": {"private-fixture"}},
	})
	if !errors.Is(err, context.DeadlineExceeded) || forwarded.Load() != 0 {
		t.Fatalf("unexpected redirect behavior: %v, forwarded %d", err, forwarded.Load())
	}
}
