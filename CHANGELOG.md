# Changelog

## 0.5.1

Security patch. Go >=1.26.10 and golang.org/x/net v0.62.0 are required; x/sys
and x/text follow the selected x/net patch dependencies.

- Reject HTTP redirects for all OTLP HTTP signals and authenticated Cloud lookup
  requests to prevent replay of telemetry or credentials to redirect targets.
- Redact ExporterConfig JSON/structured logging and custom JSON parser errors.
- Preserve TLS verification, CA/mTLS loading, timeout/compression and retry behavior
  while applying a uniform guarded HTTP client. Existing stricter upstream body limits remain.
- Ignore private .env variants/backups and reject them from source releases.
- Add reproducible security regressions and CI vulnerability/secret scans.


- Add version-aware Cloud verification profiles and configuration examples for Hosted
  8.x APM, 9.0–9.5.2 Managed OTLP, 9.5.3+ Managed OTLP and Serverless.
- Report declared/observed versions and stop before export on mismatches; preserve
  version-neutral SDK behavior and existing explicit APM configuration.

## 0.5.0

- Parse pinned upstream experimental SDK queue metrics into sanitized processor
  observations, preserving availability and delta/cumulative temporality.
- Support current-depth cumulative observable up/down counters and gauges;
  reject delta queue changes as current depth and distinguish delayed log drops.
- Keep instance Diagnostics unchanged; no automatic globals/environment changes.
- Add real blocked-export queue saturation/drain tests and a synthetic local example.
- Add version-pinned private-module installation, release packaging and checksums.

## 0.4.0

- Add programmatic trace/log batching, queue capacity, export interval/timeout,
  and metric reader interval/timeout/data-point batch limits. Zero inherits upstream settings.
- Classify failed exports using the canceled caller context when upstream returns
  a custom timeout cause, while preserving the original returned error.
- Add an opt-in real Collector/network recovery soak with resource and indexed-data checks.
- Add private Cloud config examples and a credential-redacting connectivity verifier
  for Managed OTLP and existing APM Server endpoints (including Hosted 8.19).

## 0.3.3

- Reject programmatic endpoints with empty hosts, empty/out-of-range ports,
  or a bare query delimiter before constructing exporters. Error messages
  do not contain the endpoint. IPv6 and default ports remain supported.

## 0.3.2

- Shut down providers concurrently so a stalled trace exporter does not consume
  the metric/log shutdown deadline. Preserve first-call ownership and cached errors.
- Add a blocked-trace shutdown regression covering delivery of metrics and logs.

## 0.3.1

- Validate programmatic HTTP/gRPC headers before creating exporters, reject ambiguous
  case-insensitive duplicate names, and support gRPC raw binary metadata.
- Validate all HTTP/JSON success responses, including traces using the official native
  request encoder. HTML/unknown non-empty success bodies no longer count as delivery.
- Preserve empty-response and protobuf-response compatibility, including partial rejection reporting.
- Flush initialized providers concurrently under one caller deadline, so a slow trace
  exporter does not starve metrics and logs.
- Add regression tests for real binary metadata, malformed response/content types,
  protobuf partial success, and blocked-trace flush fairness.

Managed Elastic Cloud authentication/TLS and the licensed Service Map UI remain outside
local integration coverage. Version metadata does not imply an official Elastic distribution.
