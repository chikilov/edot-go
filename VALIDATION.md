# Validation — 2026-11-06

Environment: macOS arm64, Go 1.26.0 (automatically selected from go.mod).

- `go test -race ./...`: PASS
- `go vet ./...`: PASS
- `go build ./...`: PASS
- `go test -race -cover ./...`: PASS; SDK package coverage 89.1%; example coverage 0%.
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
handshake was not exercised. Programmatic gRPC TLS with a configured CA is
now covered by version 0.3.0 tests. Managed OTLP ingestion, Kibana APM
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

Version 0.3.0 configuration and diagnostics:
- Full local race/coverage suite: PASS (86.541s); SDK coverage 89.1%.
- Follow-up race tests cover gRPC deadline/canceled classification and stack-capacity diagnostics.
- Explicit code settings override common/signal environment values without changing process environment.
- HTTP/protobuf and HTTP/JSON code endpoints, complete signal URLs, header replacement,
  empty-header clearing, compression disabling, and timeout override: PASS.
- HTTP/protobuf, HTTP/JSON, and gRPC with code-provided TLS CA config: PASS.
- Signal/common setting precedence, header snapshot copying, TLS config/CA pool cloning,
  invalid settings and formatted-config redaction: PASS.
- Per-instance success/failure/disabled-drop/item/in-flight counters, timestamps,
  sanitized callbacks, callback-panic isolation, and concurrent snapshots: PASS.
- Exact upstream batch queue depth and overflow totals remain unavailable, explicitly flagged in snapshots.
- Cloudcheck command builds; no real Elastic Cloud credentials or account were used.
  Cloud ingestion and Kibana UI verification remain pending local access configuration.

Local Elastic integration — 2026-11-09:
- Elasticsearch/Kibana/EDOT 9.5.6-arm64 started successfully with Basic license.
- All three protocols passed real ingestion and frontend/backend correlation checks.
- Kibana overall availability and APM service inventory were verified via API.
- Detailed synthetic-data assertions and limitations are in deploy/local/RESULTS.md.
- Managed Cloud authentication/TLS and licensed Service Map UI remain unverified.

Version 0.3.1 hardening — 2026-11-09:
- Regression tests first reproduced invalid programmatic header acceptance,
  HTML/non-JSON success acknowledgement, and metrics/log starvation during a blocked trace flush.
- Full race/coverage suite passed (79.005s; 88.8% SDK coverage at that run).
- Additional focused race tests passed for protobuf-response compatibility and partial rejection across all signals.
- Real gRPC binary metadata round trips, case-insensitive duplicate header rejection,
  HTTP token/control-byte validation, and shared-deadline flush fairness are covered.
- `go vet ./...`, `go build ./...`, `go mod tidy -diff`, and `go mod verify`: PASS.
- `deploy/local/verify.py`: PASS against the running Elastic 9.5.6 stack after changes;
  all protocol trace/metric/log correlation checks passed.
- Kibana Discover UI showed a synthetic ERROR log, its trace ID, and a linked waterfall
  with frontend/backend services and three spans. Screenshot retained locally outside the repository.
- Code header validation does not replace upstream environment header parsing.
  Cloud Managed OTLP and licensed Service Map UI remain outside this verification.

Version 0.3.2 shutdown hardening — 2026-11-09:
- Reproduced metric/log starvation when a blocked trace shutdown consumed the shared deadline.
- Concurrent provider shutdown regression passed three race-enabled repetitions;
  cached deadline errors remain stable on subsequent calls.
- Full `go test -race -timeout=3m ./...`: PASS (88.407s).
- `go vet ./...` and `go build ./...`: PASS.
- Local Elastic integration: PASS for HTTP/protobuf, gRPC, and HTTP/JSON,
  including per-protocol 6 traces, 18 spans, 12 correlated logs and metric checks.
- Shutdown still waits for provider calls and caches the first caller result;
  it does not guarantee persistence or retry after a timeout.

Version 0.3.3 endpoint validation — 2026-11-09:
- Regression tests reproduced acceptance of missing hosts, empty ports, ports 0/65536,
  and bare query delimiters across all three protocols.
- Invalid programmatic configuration now fails during New, before exporters start.
- Valid IPv6, omitted ports, and port 65535 remain accepted.
- Focused race tests covering endpoint validation and programmatic HTTP/gRPC export: PASS.
- Full `go test -race -timeout=3m ./...`: PASS (72.173s).
- `go vet ./...`, `go build ./...`, and diff whitespace checks: PASS.
- No transport changes; environment endpoint parsing remains upstream behavior.

Version 0.4.0 operations and recovery — 2026-11-09:
- New pipeline settings cover trace/log queue and batch sizes, interval and timeout,
  and periodic metric reader interval, timeout and data-point batch size.
- Focused race tests passed three repetitions: code overrides environment, zero
  inherits environment, real exported batches respect limits, all signals cancel
  under explicit processor/reader timeouts, and invalid values fail before startup.
- Those tests reproduced upstream custom timeout causes being classified as generic
  failures; diagnostics now retain the caller context cancellation classification.
- Full `go test -race -timeout=3m ./...`: PASS (81.741s); vet/build/mod verification PASS.
- Seven offline Cloud verifier regressions PASS, including secret redaction,
  inherited-route isolation, no shell evaluation, direct/proxy indexed correlation,
  and missing read privileges. Actionlint v1.7.12: PASS.
- Baseline SDK 0.3.3 real local soak: three concurrent protocols, 300 seconds,
  25 synthetic requests/sec/protocol, 7,500 events per signal/protocol generated.
  Collector stopped/restarted for 30 seconds and separate TCP connections were
  disconnected/refused for 30 seconds. Both recoveries passed for all signals.
  Post-recovery indexed traces/log IDs and application metrics were verified.
- Five-minute heap growth (GC live heap): HTTP/protobuf +56,720 bytes,
  gRPC -66,920 bytes, HTTP/JSON +200,952 bytes. Goroutine growth -6/-6/-12.
  Maximum synthetic emission duration 1.593/1.257/1.538 ms respectively.
  This is a bounded guardrail check, not proof of zero leaks or zero-loss delivery.
- A longer local soak is run separately; raw reports are local deliverables,
  not checked-in credentials or production data.
- Cloud Hosted APM (reported version 8.19.16): HTTP/protobuf and gRPC public TLS,
  API Key, all-signal exports, direct Elasticsearch indexing and trace/log ID
  correlation PASS. See deploy/cloud/RESULTS.md. Managed OTLP and Cloud HTTP/JSON
  remain unverified; the tested 8.x endpoint is APM Server.

Version 0.5.0 queue diagnostics and release — 2026-11-10:
- Added QueueDiagnosticsFromMetrics for pinned upstream experimental processor
  queue metrics. Output includes per-processor availability, capacity/depth,
  queue_full values, temporality and delayed log-overflow reporting.
- No environment/global mutations or collection/export in the library parser.
  Existing per-instance Diagnostics queue flags remain unavailable by design.
- Real trace/log blocked-export tests: queue capacity/depth 2, five further items
  produce three drops. Trace drops appear immediately; log drops only after
  dequeue/flush. Drained depth zero and cumulative drops three: PASS (3 race runs).
- Delta overflow labels, delta queue rejection, missing values, unknown scopes,
  arbitrary component names and unrelated-attribute redaction regressions: PASS.
- Local synthetic queuediagnostics example: saturated/drained snapshots PASS.
- Full race suite PASS (88.673s); vet/build/tidy-diff and whitespace checks PASS.
- Packaging smoke test on the prior immutable commit PASS; CI additionally checks
  repeated source packaging checksums on the release commit.
- Release packaging reads git archive, rejecting tracked private .env/IDE/cache
  files. Untracked personal files are never included.
