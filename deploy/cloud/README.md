# Elastic Cloud 접속 준비

버전별 프로필·검증 상태는 [ELASTIC_COMPATIBILITY.md](../../ELASTIC_COMPATIBILITY.md)를 먼저 확인하세요.
`--elastic-version 8.19.16` 또는 파일의 `ELASTIC_STACK_VERSION`으로 경로를 구분할 수 있습니다.
실제 `.env`는 자동으로 수정하지 않습니다.

## Hosted 8.19의 APM endpoint

Managed OTLP 항목이 없는 8.x 배포는 기존 APM endpoint로 검증합니다.
Cloud Manage의 **APM**에서 URL을 복사합니다. 대상 Kibana의
**Applications/APM → Settings → Agent keys**에서 `event:write` 권한으로
APM agent key를 생성하고 **Base64 encoded** 값을 복사합니다.
서버에서 API key 인증이 활성화되어 있어야 하며, 임의의 Elasticsearch 쓰기 권한 key와 다릅니다.

`.env.apm.example`을 참고해 기존 `.env`의 endpoint와 header를 수정합니다.
기존 APM secret token이 있다면 `Authorization=Bearer%20...`도 지원합니다.
서버의 기존 token을 재설정할 필요는 없습니다.

```sh
python3 deploy/cloud/verify.py --target apm --preflight
python3 deploy/cloud/verify.py --target apm --protocol http/protobuf --protocol grpc
```

8.19 키 생성 안내: https://www.elastic.co/guide/en/observability/8.19/apm-api-key.html

## Managed OTLP endpoint (지원되는 Cloud 배포)

1. Elastic Cloud 대상 배포/프로젝트의 **Add data → OpenTelemetry** 안내에서
   Managed OTLP endpoint와 해당 안내에서 생성한 수집용 API Key를 복사합니다.
2. 저장소 루트에서 실행합니다.

```sh
cp deploy/cloud/.env.example deploy/cloud/.env
chmod 600 deploy/cloud/.env
```

3. 로컬 편집기로 `deploy/cloud/.env`의 두 placeholder를 교체합니다.

| 변수 | 넣을 값 |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | 안내 화면의 `https://...` Managed OTLP URL |
| `OTEL_EXPORTER_OTLP_HEADERS` | `Authorization=ApiKey%20` 뒤에 수집용 API Key의 encoded 값 |

`ApiKey` 뒤의 `%20`은 공백입니다. API Key를 다시 base64 인코딩하지 않습니다.
API Key 생성 API의 JSON을 직접 사용한다면 `api_key` 단독 값이 아닌 `encoded` 값을 씁니다.
Cloud 계정 관리용 API Key, Elasticsearch URL, Kibana URL과 혼동하지 마세요.
별도 CA나 클라이언트 인증서 없이 공인 TLS 인증서를 사용하는 연결이 기본입니다.

4. 저장소 루트에서 다음 명령을 실행합니다. 직접 작성한 로컬 설정만 source 하세요.

```sh
(
  set -a
  . ./deploy/cloud/.env
  set +a
  go run ./examples/cloudcheck
)
```

이 명령은 합성 트레이스·메트릭·로그를 Cloud로 전송하고 trace ID와 진단 통계를 출력합니다.
SDK의 전송 성공은 Elasticsearch에 저장된 데이터의 직접 조회 검증과 구분합니다.
출력된 trace ID로 Cloud UI에서 트레이스를 찾고 연결된 로그와 메트릭을 확인할 수 있습니다.
기존 signal-specific OTLP 환경변수가 있으면 공통 설정보다 우선하므로 먼저 확인하세요.
`.env`는 Git에서 제외되며 실제 값을 채팅·이슈·스크린샷으로 공유할 필요가 없습니다.

공식 안내: https://www.elastic.co/docs/reference/opentelemetry/managed-inputs/managed-otlp-endpoint

### 설정 파일을 실행하지 않는 검증 도구

```sh
python3 deploy/cloud/verify.py --preflight
python3 deploy/cloud/verify.py --protocol http/protobuf --protocol grpc
```

이 도구는 `.env`를 데이터로 읽고 기존 OTLP 환경변수의 영향을 제거합니다.
실패 시 exporter의 raw 출력을 숨겨 endpoint나 인증정보 노출을 피합니다.
`http/json`은 대상 endpoint의 지원 여부를 별도로 확인하려는 경우에만 명시적으로 선택합니다.
프로토콜별 성공 여부와 trace ID를 출력하며, 실제 저장 확인은 Cloud에서 별도로 수행해야 합니다.

### Cloud Elasticsearch의 실제 저장 확인 (선택)

로컬 `.env`에 `ELASTICSEARCH_URL='https://Cloud의_Elasticsearch_URL'`을 추가하면
기존 API Key로 대상 trace ID의 트레이스·로그와 합성 메트릭을 조회합니다.
APM endpoint는 `traces-apm*`, `logs-apm*`, `metrics-apm*`에서 확인하며,
Managed OTLP는 `*.otel-*` 데이터 스트림을 조회합니다.
트레이스와 로그의 span/transaction ID 연결도 검사합니다.
기존 key에 read 권한이 없으면 권한을 변경하거나 새 key를 만들지 않고 미검증 사유를 보고합니다.
APM Bearer secret token은 Elasticsearch 조회에 사용할 수 없습니다.
보고서의 `result`는 전송 결과이며, 저장 확인은 `indexed_data_verified`와 `storage`로 구분합니다.
조회 URL을 명시했는데 저장 검증을 완료하지 못하면 명령은 nonzero로 종료합니다.

Elasticsearch URL 대신 `KIBANA_URL='https://대상_Kibana_호스트'`로 설정하면
Kibana console proxy를 통해 동일한 검색을 수행합니다. `/app/...` 경로·query·fragment는
제외한 origin 주소만 지정합니다. 기존 key에 Kibana 및 데이터 조회 권한이 있어야 합니다.
두 URL을 모두 설정하면 Elasticsearch 직접 조회가 우선합니다.
실제 로컬 파일의 URL이나 인증정보는 도구가 자동 변경하지 않습니다.

검증 기록: [RESULTS.md](RESULTS.md).

### 수집·조회 자격증명 분리

운영 앱에는 수집 전용 key를 사용하고, 조회용 key는 검증 도구에만 설정합니다.
`.env`의 `ELASTICSEARCH_API_KEY`에 조회 전용 key의 Base64 encoded 값을 넣으면
Elasticsearch/Kibana 버전·저장 조회에서만 사용합니다. `ApiKey` 접두어는 붙이지 않습니다.
조회 key는 SDK subprocess에 전달되지 않습니다. 생략하면 서버 버전과 저장 조회를 건너뜁니다. 전송 성공과 저장 확인 여부는 각각 출력합니다.
최소 권한 예시와 현재 운영 검토 결과는 [SECURITY.md](../../SECURITY.md)를 참고하세요.

The verifier builds a temporary binary without the configured OTLP credentials,
then supplies the OTLP configuration only to that binary with strict TLS enabled.
