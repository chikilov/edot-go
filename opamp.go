// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"golang.org/x/net/http/httpguts"
)

// OpAMPConfig configures OpAMP over HTTPS/protobuf. Credentials are separate
// from OTLP. InstanceUID may be persisted by the application across restarts.
type OpAMPConfig struct {
	Endpoint    string
	Headers     http.Header
	TLSConfig   *tls.Config
	InstanceUID [16]byte
	ServiceName string
	Environment string
	Interval    time.Duration
	OnError     func(error)
}

func (OpAMPConfig) String() string               { return "OpAMPConfig{redacted}" }
func (OpAMPConfig) GoString() string             { return "OpAMPConfig{redacted}" }
func (OpAMPConfig) MarshalJSON() ([]byte, error) { return []byte(`{"redacted":true}`), nil }

type quietOpAMPLogger struct{}

func (quietOpAMPLogger) Debugf(context.Context, string, ...interface{}) {}
func (quietOpAMPLogger) Errorf(context.Context, string, ...interface{}) {}

// RunOpAMP connects to an OpAMP HTTP server and applies the supported keys of
// its "elastic" JSON config map. It blocks until ctx is canceled, then stops
// the client. Run one configuration controller per SDK and cancel it before
// shutting down the SDK. onError callbacks must return promptly.
func (s *SDK) RunOpAMP(ctx context.Context, c OpAMPConfig) error {
	if s.sampler == nil {
		return fmt.Errorf("OpAMP requires an initialized SDK")
	}
	if err := (exporterSettings{ExporterConfig: ExporterConfig{
		Endpoint: c.Endpoint, Protocol: "http/protobuf",
	}}).validate(); err != nil {
		return fmt.Errorf("OpAMP endpoint must be an HTTPS URL without credentials, query or fragment")
	}
	if !strings.HasPrefix(c.Endpoint, "https://") {
		return fmt.Errorf("OpAMP requires an explicit HTTPS endpoint")
	}
	if c.Interval == 0 {
		c.Interval = 30 * time.Second
	}
	if c.Interval < time.Second {
		return fmt.Errorf("OpAMP interval must be at least one second")
	}
	c.Headers = c.Headers.Clone()
	for key, values := range c.Headers {
		if !httpguts.ValidHeaderFieldName(key) {
			return fmt.Errorf("invalid OpAMP header")
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return fmt.Errorf("invalid OpAMP header")
			}
		}
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.TLSConfig != nil {
		tlsConfig = c.TLSConfig.Clone()
		if tlsConfig.InsecureSkipVerify || tlsConfig.MinVersion != 0 && tlsConfig.MinVersion < tls.VersionTLS12 ||
			tlsConfig.MaxVersion != 0 && tlsConfig.MaxVersion < tls.VersionTLS12 {
			return fmt.Errorf("OpAMP requires verified TLS 1.2 or newer")
		}
		if tlsConfig.MinVersion == 0 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
		if tlsConfig.RootCAs != nil {
			tlsConfig.RootCAs = tlsConfig.RootCAs.Clone()
		}
	}
	if c.InstanceUID == ([16]byte{}) {
		if _, err := rand.Read(c.InstanceUID[:]); err != nil {
			return fmt.Errorf("cannot generate OpAMP instance ID")
		}
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: tlsConfig, TLSHandshakeTimeout: 10 * time.Second,
		MaxResponseHeaderBytes: 64 << 10}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	opamp := client.NewHTTP(quietOpAMPLogger{})
	capabilities := protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus |
		protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig |
		protobufs.AgentCapabilities_AgentCapabilities_ReportsHeartbeat
	if err := opamp.SetCapabilities(&capabilities); err != nil {
		return fmt.Errorf("cannot initialize OpAMP capabilities")
	}
	description := &protobufs.AgentDescription{IdentifyingAttributes: []*protobufs.KeyValue{
		opampAttribute("service.name", c.ServiceName),
	}, NonIdentifyingAttributes: []*protobufs.KeyValue{
		opampAttribute("telemetry.distro.name", "edot-go-unofficial"),
		opampAttribute("telemetry.distro.version", Version),
		opampAttribute("telemetry.sdk.language", "go"),
	}}
	if c.Environment != "" {
		description.IdentifyingAttributes = append(description.IdentifyingAttributes,
			opampAttribute("deployment.environment.name", c.Environment))
	}
	if err := opamp.SetAgentDescription(description); err != nil {
		return fmt.Errorf("cannot initialize OpAMP agent description")
	}
	baseline := RuntimeConfig{SampleRatio: new(s.sampler.delegate.Load().ratio),
		SendingTraces: new(s.traces.Load()), SendingMetrics: new(s.metrics.Load()), SendingLogs: new(s.logs.Load())}
	sampleOverridden := false
	report := func(err error) {
		if c.OnError != nil {
			c.OnError(err)
		}
	}
	settings := types.StartSettings{
		OpAMPServerURL: c.Endpoint, Header: c.Headers, Client: httpClient,
		InstanceUid: types.InstanceUid(c.InstanceUID), MaxMessageSize: 64 << 10,
		HeartbeatInterval: &c.Interval,
		Callbacks: types.Callbacks{
			CheckRedirect: func(*http.Request, []*http.Request, []*http.Response) error {
				return fmt.Errorf("OpAMP redirects are disabled")
			},
			OnOpampConnectionSettings: func(context.Context, *protobufs.OpAMPConnectionSettings) error {
				return fmt.Errorf("remote OpAMP connection changes are unsupported")
			},
			OnConnectionSettings: func(context.Context, *protobufs.ConnectionSettingsOffers) error {
				return fmt.Errorf("remote telemetry connection changes are unsupported")
			},
			OnCommand: func(context.Context, *protobufs.ServerToAgentCommand) error {
				return fmt.Errorf("remote commands are unsupported")
			},
			OnConnectFailed: func(context.Context, error) { report(fmt.Errorf("OpAMP connection failed")) },
			OnError:         func(context.Context, *protobufs.ServerErrorResponse) { report(fmt.Errorf("OpAMP server error")) },
			OnMessage: func(messageContext context.Context, msg *types.MessageData) {
				if msg.RemoteConfig == nil || ctx.Err() != nil {
					return
				}
				remote := msg.RemoteConfig
				status := &protobufs.RemoteConfigStatus{
					LastRemoteConfigHash: append([]byte{}, remote.ConfigHash...),
					Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED,
				}
				config, hasSampling, err := elasticRuntimeConfig(remote, baseline, sampleOverridden)
				if err == nil {
					body, _ := json.Marshal(config)
					err = s.applyRuntimeConfigJSON(messageContext, body)
				}
				if err == nil {
					sampleOverridden = hasSampling
					status.Status = protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED
				} else {
					status.ErrorMessage = "unsupported or invalid elastic runtime configuration"
					report(errors.New(status.ErrorMessage))
				}
				if err := opamp.SetRemoteConfigStatus(status); err != nil {
					report(fmt.Errorf("cannot report OpAMP configuration status"))
				}
			},
		},
	}
	if err := opamp.Start(ctx, settings); err != nil {
		return fmt.Errorf("cannot start OpAMP client")
	}
	<-ctx.Done()
	stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := opamp.Stop(stop); err != nil {
		return errors.Join(ctx.Err(), fmt.Errorf("OpAMP client shutdown failed"))
	}
	return ctx.Err()
}

func opampAttribute(key, value string) *protobufs.KeyValue {
	return &protobufs.KeyValue{Key: key, Value: &protobufs.AnyValue{
		Value: &protobufs.AnyValue_StringValue{StringValue: value},
	}}
}

func elasticRuntimeConfig(remote *protobufs.AgentRemoteConfig, baseline RuntimeConfig, resetSampling bool) (RuntimeConfig, bool, error) {
	fail := fmt.Errorf("invalid elastic runtime configuration")
	if len(remote.ConfigHash) == 0 || len(remote.ConfigHash) > 256 || remote.Config == nil || len(remote.Config.ConfigMap) != 1 {
		return RuntimeConfig{}, false, fail
	}
	file := remote.Config.ConfigMap["elastic"]
	if file == nil || len(file.Body) > 16<<10 || file.ContentType != "" && file.ContentType != "application/json" {
		return RuntimeConfig{}, false, fail
	}
	values, err := decodeElasticConfig(file.Body)
	if err != nil {
		return RuntimeConfig{}, false, fail
	}
	result := baseline
	if !resetSampling {
		result.SampleRatio = nil
	}
	hasSampling := false
	for key, value := range values {
		switch key {
		case "send_traces", "send_metrics", "send_logs":
			if value != "true" && value != "false" {
				return RuntimeConfig{}, false, fail
			}
			enabled := value == "true"
			switch key {
			case "send_traces":
				result.SendingTraces = &enabled
			case "send_metrics":
				result.SendingMetrics = &enabled
			case "send_logs":
				result.SendingLogs = &enabled
			}
		case "sampling_rate":
			ratio, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 1 {
				return RuntimeConfig{}, false, fail
			}
			result.SampleRatio, hasSampling = &ratio, true
		default:
			return RuntimeConfig{}, false, fail
		}
	}
	return result, hasSampling, nil
}

// decodeElasticConfig rejects duplicate keys and non-string values so that
// configuration interpretation is unambiguous across protocol participants.
func decodeElasticConfig(body []byte) (map[string]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("elastic configuration must be an object")
	}
	values := map[string]string{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid elastic configuration key")
		}
		key, ok := token.(string)
		if _, exists := values[key]; !ok || exists {
			return nil, fmt.Errorf("invalid or duplicate elastic configuration key")
		}
		var value *string
		if decoder.Decode(&value) != nil || value == nil {
			return nil, fmt.Errorf("elastic configuration values must be strings")
		}
		values[key] = *value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("invalid elastic configuration object")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("trailing elastic configuration content")
	}
	return values, nil
}
