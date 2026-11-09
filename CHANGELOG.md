# Changelog

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
