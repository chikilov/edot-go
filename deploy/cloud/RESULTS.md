# Cloud APM integration verification — 2026-11-09

Target: existing Elastic Cloud Hosted APM Server; user-reported Stack 8.19.16.
Managed OTLP was unavailable on this 8.x deployment and was not tested.

| Protocol | TLS + API Key export | Indexed trace | Correlated log | Application metric |
|---|---|---|---|---|
| HTTP/protobuf | PASS | PASS | PASS | PASS |
| gRPC/protobuf | PASS | PASS | PASS | PASS |

For each protocol, SDK diagnostics reported one successful batch/item per signal
and zero failed batches. Using the same locally configured API Key, read-only
Elasticsearch searches through the supplied Kibana console proxy, then repeated
direct Elasticsearch searches using the latest user-supplied URL/key, confirmed:

- One trace document and one log document for the newly generated trace ID.
- Matching trace transaction/span ID and log span ID.
- The `cloudcheck.requests` metric for `edot-go-cloudcheck` in the verification window.

Synthetic trace IDs:

- HTTP/protobuf: `2f8f0254488267d61d1e95744df83747`
- gRPC: `7db408390abc374899ea19a37c5e24d7`

The earlier failed authentication attempt used superseded configuration; the user
replaced the endpoint/key before this successful run. No permissions were changed.
No real endpoint, API Key, account identifier, or production payload is included here.

This verifies public TLS, API Key authentication, export and actual indexed data.
It does not verify mTLS, Managed OTLP, HTTP/JSON on APM Server, Service Map UI,
or production load behavior of the Cloud endpoint.
