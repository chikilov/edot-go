// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"crypto/tls"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"time"
)

// ExporterConfig supplies explicit OTLP settings. Zero fields inherit settings.
// Nonzero signal fields override common code fields, then signal environment,
// common environment, and upstream defaults, in that order. Headers replace
// the entire lower-priority map; a non-nil empty map clears inherited headers.
// Common HTTP Endpoint is a base URL; signal Endpoint is a complete URL.
// TLSConfig must not be mutated during New or after handing it to this API.
type ExporterConfig struct {
	Protocol    string
	Endpoint    string
	Headers     map[string]string
	Timeout     time.Duration
	Compression string // "gzip" or "none"; empty inherits.
	TLSConfig   *tls.Config
}

// Redact credentials, endpoints, and certificate material when formatted.
func (ExporterConfig) String() string     { return "ExporterConfig{credentials and endpoint redacted}" }
func (c ExporterConfig) GoString() string { return c.String() }

type exporterSettings struct {
	ExporterConfig
	endpointIsSignal bool
}

func (c Config) exporterFor(signal string) exporterSettings {
	common := c.OTLP
	var specific ExporterConfig
	switch signal {
	case "TRACES":
		specific = c.TraceExporter
	case "METRICS":
		specific = c.MetricExporter
	case "LOGS":
		specific = c.LogExporter
	}
	r := exporterSettings{ExporterConfig: common}
	if specific.Protocol != "" {
		r.Protocol = specific.Protocol
	}
	if r.Protocol == "" {
		r.Protocol = protocolFor(signal)
	}
	if specific.Endpoint != "" {
		r.Endpoint = specific.Endpoint
		r.endpointIsSignal = true
	}
	if specific.Headers != nil {
		r.Headers = specific.Headers
	}
	r.Headers = maps.Clone(r.Headers)
	if specific.Timeout != 0 {
		r.Timeout = specific.Timeout
	}
	if specific.Compression != "" {
		r.Compression = specific.Compression
	}
	if specific.TLSConfig != nil {
		r.TLSConfig = specific.TLSConfig
	}
	if r.TLSConfig != nil {
		r.TLSConfig = r.TLSConfig.Clone()
		if r.TLSConfig.RootCAs != nil {
			r.TLSConfig.RootCAs = r.TLSConfig.RootCAs.Clone()
		}
		if r.TLSConfig.ClientCAs != nil {
			r.TLSConfig.ClientCAs = r.TLSConfig.ClientCAs.Clone()
		}
	}
	return r
}

func exporterSettingsFor(signal string, supplied []exporterSettings) exporterSettings {
	if len(supplied) > 0 {
		return supplied[0]
	}
	return (Config{}).exporterFor(signal)
}

func (s exporterSettings) endpoint(signal string) string {
	if s.Endpoint == "" || s.endpointIsSignal || s.Protocol == "grpc" {
		return s.Endpoint
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil {
		return s.Endpoint
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/" + strings.ToLower(signal)
	u.RawPath = ""
	return u.String()
}

func (s exporterSettings) validate() error {
	switch s.Protocol {
	case "http/protobuf", "http/json", "grpc":
	default:
		return fmt.Errorf("unsupported OTLP protocol; use http/protobuf, http/json, or grpc")
	}
	if s.Timeout < 0 {
		return fmt.Errorf("timeout must be positive or zero to inherit")
	}
	switch s.Compression {
	case "", "gzip", "none":
	default:
		return fmt.Errorf("unsupported compression; use gzip or none")
	}
	if s.Endpoint != "" {
		u, err := url.Parse(s.Endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("endpoint must be an http(s) URL without userinfo, query, or fragment")
		}
		if s.Protocol == "grpc" && u.Path != "" && u.Path != "/" {
			return fmt.Errorf("gRPC endpoint must not have a path")
		}
		if s.TLSConfig != nil && u.Scheme != "https" {
			return fmt.Errorf("TLSConfig requires an https endpoint")
		}
	}
	for k, v := range s.Headers {
		if k == "" || strings.ContainsAny(k, "()<>@,;:\\\"/[]?={} \t\r\n") || strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("invalid exporter header")
		}
	}
	return nil
}
