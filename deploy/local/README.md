# Local Elastic integration stack

Elasticsearch + Kibana + EDOT Collector 9.5.6, single node, free Basic license.
Based on [elastic/start-local](https://github.com/elastic/start-local/tree/0a56d370042dc6d5032026b0173c93e94f2f1676)
(Apache-2.0), adapted to bind all published ports to 127.0.0.1 and disable Kibana telemetry.

Requires Docker Compose v2.23.1+ (inline configs), a running Docker engine, Python 3,
and roughly 6–8 GB of Docker VM memory. Images may require several GB of disk space.

```bash
./deploy/local/start.sh
./deploy/local/stop.sh
```

Stopping preserves volumes. Do not delete `.env` while keeping an existing volume:
Elasticsearch stores the bootstrap password in its data; generating a different
password does not reset existing users. The local `.env` is gitignored and contains
random passwords. Keep it private and back it up together with local data if needed.

| Service | Address |
|---|---|
| Kibana | http://127.0.0.1:5601 |
| Elasticsearch | http://127.0.0.1:9200 |
| OTLP HTTP/protobuf or HTTP/JSON | http://127.0.0.1:4318 |
| OTLP gRPC | http://127.0.0.1:4317 |
| OpAMP (collector extension) | http://127.0.0.1:4320 |

Kibana login: user `elastic`; password is `ES_LOCAL_PASSWORD` in your local `.env`.
Elasticsearch and Kibana use authentication over local HTTP. OTLP input has no
receiver authentication and is bound only to loopback. This is a development stack,
not a production deployment or a simulation of Managed OTLP authentication/TLS.

## Verify the complete local path

```bash
python3 deploy/local/verify.py
```

This sends synthetic data over all three protocols, polls Elasticsearch for exact
trace/log relationships and application counters, and checks the Kibana service
inventory API. It reads the local .env privately, removes inherited OTLP settings
from each demo process, and prints no passwords. Initial aggregation can take a
minute; the script waits for ingestion. It is tied to the pinned Kibana version's
internal service-inventory API and does not test the licensed Service Map UI.
A yellow Elasticsearch status is expected on one node when replica shards cannot
be assigned; it does not by itself indicate failed ingestion.

## Send synthetic multi-service data

From the repository root, without setting any Elasticsearch password in the app:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318 \
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
OTEL_RESOURCE_ATTRIBUTES='deployment.environment.name=local,service.version=0.3.0' \
go run ./examples/localdemo
```

Repeat with `http/json`, or use `grpc` with `http://127.0.0.1:4317`.
Clear unrelated OTEL_EXPORTER_OTLP_* endpoint, header and protocol variables first
so signal-specific overrides do not route synthetic data to a different system.

The example sends six frontend→backend requests, including two synthetic HTTP 500
responses. It emits distributed spans, logs correlated by trace/span ID, and counters
for services `edot-go-local-frontend` and `edot-go-local-backend`.

In Kibana, use Observability → Applications to locate these services and inspect
traces and errors. Logs and application metrics should be searchable as well.
The Service Map UI requires an active Platinum-or-higher/trial license in Kibana
9.5.6; this setup deliberately uses Basic and does not activate a trial. Distributed
trace IDs and parent/child spans still allow frontend/backend correlation checks. For data just emitted, use a recent time range and allow ingestion/aggregation
time. The collector includes `elasticapm` processing and aggregation for APM views;
writing raw spans into Elasticsearch alone would not provide the same result.

For a single-service check, use `go run ./examples/cloudcheck` with the same local
OTLP endpoint. It reports exporter success; it does not query Elasticsearch or Kibana.
