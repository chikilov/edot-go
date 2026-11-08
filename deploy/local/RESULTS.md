# Local integration results — 2026-11-09

Environment: macOS arm64; Docker Linux VM with 8 GiB memory; Elasticsearch,
Kibana and EDOT Collector 9.5.6-arm64. Single node, Basic license, loopback-only ports.

| Protocol | Verified distributed traces | Verified spans | Correlated logs | Synthetic HTTP 500 requests |
|---|---:|---:|---:|---:|
| HTTP/protobuf | 6 | 18 | 12 | 2 |
| gRPC/protobuf | 6 | 18 | 12 | 2 |
| HTTP/JSON | 6 | 18 | 12 | 2 |

For every trace, Elasticsearch queries confirmed three spans: frontend server,
frontend HTTP client and backend server. The backend's parent span belongs to the
frontend. Both log records have span IDs present in that same trace, and expected
INFO/ERROR severity. Each service's custom counter was found in Elasticsearch for
all three runs (at least three metric documents per service).

Kibana reported overall status `available`. Its service inventory API returned
`edot-go-local-frontend`, `edot-go-local-backend`, and the separate
`edot-go-cloudcheck` service. Both demo services showed transaction error rate
1/3, matching two deliberately failed requests out of six per protocol.

The collector also generated transaction, service-summary, service-transaction
and service-destination metric data streams, confirming APM enrichment and aggregation.
The initial single-service plus multi-service smoke tests stored 57 spans, 39 logs
and 57 application metric data points before additional verification traffic.
These initial totals are not fixed cluster totals: rerunning verification adds data.

Elasticsearch health was yellow because this is a single-node cluster with replica
shards; reads and writes succeeded. No trial was activated. The Service Map UI
requires a Platinum-or-higher/trial license in this Kibana version and was not tested.
This test does not validate Elastic Cloud Managed OTLP authentication, TLS, routing,
or production durability/load. Local OTLP receiver authentication is deliberately
absent, with its host ports bound only to 127.0.0.1.

Reproduce with `python3 deploy/local/verify.py` after `./deploy/local/start.sh`.
Passwords stay in the ignored local .env; no credentials are included in these results.
