# Validation — 2026-11-06

Environment: macOS arm64, Go 1.26.0 (automatically selected from go.mod).

- `go test -race ./...`: PASS
- `go vet ./...`: PASS
- `go build ./...`: PASS
- `go test -race -cover ./...`: PASS; SDK package coverage 84.6%; example coverage 0%.
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

Version 0.2.1 performance/reliability/CI work:
- Full local `go test -race -coverprofile=coverage.out -timeout=3m ./...`: PASS (91.244s).
- Stack threshold, preserved attributes, capacity reuse and concurrent shutdown
  tests passed five repeated race-enabled runs.
- HTTP 429/503 and gRPC Unavailable retries; HTTP 400 and gRPC InvalidArgument
  permanent failures; unreachable endpoints; slow HTTP responses; persistent
  503 throttling; bounded shutdown during export: PASS.
- A blocked exporter with a full two-entry queue confirmed non-blocking span end,
  one dropped span, three exported spans and no retained stack captures.
- `go vet ./...`, `go build ./...`, `go mod tidy -diff`, `go mod verify`: PASS.
- CI workflow checked using actionlint v1.7.12: PASS.
- Linux/macOS race/coverage/vet/build and benchmark artifact jobs are configured
  in .github/workflows/ci.yml. Remote execution is visible in GitHub Actions.
- Before/after benchmark measurements and limitations are recorded in PERFORMANCE.md.

Outages are simulated locally; these tests do not establish durability across
process crashes, production load capacity, or availability during a real long outage.
