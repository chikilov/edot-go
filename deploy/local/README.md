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

Starting recreates containers to refresh inline configuration and secret mounts.
Stopping preserves volumes. Do not delete `.env` while keeping an existing volume:
Elasticsearch stores the bootstrap password in its data; generating a different
password does not reset existing users. The local `.env` is gitignored and contains
random passwords. Keep it private and back it up together with local data if needed.
On startup, `prepare.py` creates private files in the ignored `.env.secrets/` directory.
Compose mounts them for Elasticsearch bootstrap, curl authentication and the
Collector configuration overlay. Kibana loads its service password and
saved-object encryption key from a private, read-only configuration file. For an existing stack or after changing secret files, run
`docker compose --project-directory deploy/local up -d --force-recreate --wait`
to refresh the secret mounts. On Linux, the container users must have read access to
their mounted files. Keep that access limited to the intended user.

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

## 지속 부하와 장애 복구 검증

```sh
python3 deploy/local/soak.py --duration 300 --rate 25 --output /tmp/edot-soak.json
# 더 긴 검증: --duration 3600 (1시간), --rate는 프로토콜별 요청 수/초
```

실행 중 이 compose 프로젝트의 Collector가 중단·재시작됩니다. Elasticsearch와 Kibana는
계속 실행되지만 해당 Collector를 함께 쓰는 다른 앱의 전송에도 일시적인 영향이 있습니다.
도구는 종료 시 Collector를 복원하며 합성 데이터는 실행별 서비스 이름으로 구분합니다.

세 프로토콜을 동시에 구동하고 다음 순서를 검증합니다.

1. 60초 정상 부하 후 Collector를 30초 중단하고 재시작합니다.
2. 60초 이상 복구 상태를 유지한 뒤 임시 TCP relay의 기존 연결·신규 연결을 30초 차단합니다.
3. 네트워크를 복구한 뒤 남은 시간 동안 전송을 계속하고 마지막으로 flush합니다.
4. 각 signal에서 실패와 복구 후 성공 증가를 검사하고, 최종 trace/log의 span ID 연결과
   메트릭의 실제 Elasticsearch 저장을 확인합니다.

10초 간격으로 GC 후 Go live heap과 goroutine 수를 수집합니다. 워밍업 표본 대비
마지막 heap 증가 32 MiB, goroutine 증가 20, 개별 합성 호출 최대 250ms,
목표 생성량의 90%를 회귀 감지 기준으로 사용합니다. 이 값은 범용 성능 보장이 아닙니다.
실행 시간·부하·장애 길이에 따라 조정이 필요합니다. RSS, 컨테이너 메모리, 무손실 전달,
무한 기간의 누수 부재를 보장하지 않습니다. exporter timeout을 넘긴 batch는 손실될 수 있습니다.

최소 5분 실행은 장애 복구 회귀 검증이며, 24시간 수준의 운영 soak를 대체하지 않습니다.
보고서에는 합성 trace ID와 진단 통계만 들어가며 인증정보는 포함하지 않습니다.

## 장애 중 전송 대기 데이터 보존

Collector의 `elasticsearch/otel` exporter는 `file_storage`와 Compose
볼륨 `dev-collector-queue`를 사용합니다. 큐 한도는 전송 요청 1,000개이며
일시적인 전송 실패는 최대 60회 재시도하며 대기 간격은 최대 30초입니다. Collector를 다시
시작할 때 같은 볼륨을 유지해야 남은 요청을 복구할 수 있습니다.

디스크 큐에는 텔레메트리 내용이 저장됩니다. 볼륨 디렉터리는 Collector
사용자 전용 권한으로 초기화합니다. 큐 진입 전의 SDK·Collector 메모리
배치, 용량 초과, 영구 오류 및 재시도 횟수 초과는 손실 가능성이 있습니다.
전송 재시도로 중복이 생길 수 있습니다.

## Collector 큐 관측

```bash
python3 deploy/local/queue_status.py
python3 deploy/local/queue_status.py --file /path/to/collector-metrics.txt
```

적용된 로컬 스택은 Collector 메트릭을 `127.0.0.1:8888/metrics`에서
제공합니다. 도구는 exporter·신호별 큐 깊이, 용량, 사용률과 관측된 전송·
enqueue 실패 누적값만 출력합니다. 미관측 필드는 생략하며 사용률은
`null`로 표시합니다. 중복된 시계열은 합산하지 않고
`ambiguous_metrics`에 표시합니다. Collector 재시작 시 누적값이 초기화될
수 있고, 재시도 실패 횟수는 최종 데이터 손실량과 다릅니다.

Docker Desktop에서 `python3 deploy/local/verify_queue_replay.py`를 실행하면
별도 임시 컨테이너·볼륨과 합성 수신기로 큐 관측 및 강제 종료 후
재전송을 검증합니다. 테스트 자원은 종료 시 삭제됩니다.

Elasticsearch exporter의 `retry` 설정은 HTTP 요청과 bulk 응답의 재시도를
제어합니다. 현재 설정은 429·502·503·504 응답도 재시도합니다. 연결 오류
등의 전송 재시도는 중복 저장 가능성이 있어 조회 시 이를 고려해야 합니다.
