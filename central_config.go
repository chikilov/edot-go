// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// RuntimeConfig updates only the settings present in the response.
type RuntimeConfig struct {
	SampleRatio    *float64 `json:"sample_ratio"`
	SendingTraces  *bool    `json:"sending_traces"`
	SendingMetrics *bool    `json:"sending_metrics"`
	SendingLogs    *bool    `json:"sending_logs"`
}

// PollCentralConfig polls a dedicated HTTPS JSON endpoint immediately and at
// interval until ctx is canceled. The caller owns the context and should run
// one poller per SDK. onError runs synchronously and receives sanitized errors.
func (s *SDK) PollCentralConfig(ctx context.Context, endpoint string, headers http.Header, interval time.Duration, onError func(error)) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("central configuration requires an HTTPS URL without userinfo or fragment")
	}
	if interval <= 0 {
		return fmt.Errorf("central configuration interval must be positive")
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	headers = headers.Clone()
	for ctx.Err() == nil {
		err := s.fetchRuntimeConfig(ctx, client, u.String(), headers)
		if err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

func (s *SDK) fetchRuntimeConfig(ctx context.Context, client *http.Client, endpoint string, headers http.Header) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("invalid central configuration request")
	}
	req.Header = headers.Clone()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("central configuration request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("central configuration returned HTTP %d", resp.StatusCode)
	}
	const limit = 16 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(body) > limit {
		return fmt.Errorf("central configuration response unreadable or too large")
	}
	return s.applyRuntimeConfigJSON(ctx, body)
}

func (s *SDK) applyRuntimeConfigJSON(ctx context.Context, body []byte) error {
	if len(body) > 16<<10 {
		return fmt.Errorf("central configuration document too large")
	}
	var config *RuntimeConfig
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || config == nil || decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid central configuration document")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if config.SampleRatio != nil {
		if s.sampler == nil {
			return fmt.Errorf("central configuration requires an initialized sampler")
		}
		if err := s.SetSampleRatio(*config.SampleRatio); err != nil {
			return err
		}
	}
	if config.SendingTraces != nil {
		s.SetSendingTraces(*config.SendingTraces)
	}
	if config.SendingMetrics != nil {
		s.SetSendingMetrics(*config.SendingMetrics)
	}
	if config.SendingLogs != nil {
		s.SetSendingLogs(*config.SendingLogs)
	}
	return nil
}
