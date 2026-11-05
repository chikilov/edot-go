# Validation — 2026-11-06

Environment: macOS arm64, Go 1.26.0 (automatically selected from go.mod).

- `go test -race ./...`: PASS
- `go vet ./...`: PASS
- `go build ./...`: PASS
- `go test -race -cover ./...`: PASS; SDK package coverage 82.5%; example coverage 0%.
  Coverage run explicitly placed the Go 1.26.0 bin directory first in PATH;
  the host Go 1.25.5 binary otherwise caused a tool-version mismatch during
  coverage instrumentation of the example. Normal tests/build/vet passed with
  automatic toolchain selection.

Version 0.2.0 protocol additions:
- HTTP/protobuf, HTTP/JSON, and gRPC/protobuf: all three signals exported to local receivers.
- Mixed protocols: traces over HTTP/protobuf, metrics over gRPC, logs over HTTP/JSON.
- Signal-specific endpoint and protocol precedence, gRPC auth metadata.
- HTTP/JSON gzip and HTTPS with a configured CA certificate.
- JSON partial-success handling, malformed JSON, and HTTP failure responses.
- JSON hex trace/span/exemplar IDs, numeric enums, 64-bit integer precision,
  and ordinary bytes remaining base64 encoded.

Tests also cover actual OTLP/HTTP protobuf export to a local receiver, endpoint
paths, percent-encoded auth headers, resource precedence/environment migration,
span callsite threshold, baggage allowlists, slog trace correlation, delta
counter export, dynamic root sampling/remote-parent decisions, concurrent sampler
updates, invalid configuration, SDK disable, HTTP context propagation, and all
three signal sending gates.

No Elastic Cloud credentials were used. HTTP/JSON acceptance by Elastic Cloud
was not tested. mTLS certificate loading is implemented but a full mutual-TLS
handshake and gRPC TLS were not exercised by these tests. Managed OTLP ingestion, Kibana APM
rendering, profiling, remote central configuration, real workload overhead,
resilience under prolonged outages, and non-macOS platforms were not validated.
The example compiles but has not been exercised against an Elastic Cloud account.
