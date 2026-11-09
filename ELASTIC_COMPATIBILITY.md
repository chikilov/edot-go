# Elastic 버전별 설정·검증 구분

Go SDK는 버전별 fork 없이 공통 OTLP를 사용합니다. 아래 구분은 Cloud 설정·검증 도구의
수집 경로와 실제 검증 범위입니다. Elastic의 공식 SDK 지원 인증을 뜻하지 않습니다.

| 대상 | 기본 검증 프로필 | 수집 endpoint | 저장 형식 / 예상 metric 경로 | 이 프로젝트의 실검증 |
|---|---|---|---|---|
| Hosted 8.x | `8.x-apm` | 기존 APM URL | ECS / APM 변환 | **8.19.16** HTTP/protobuf·gRPC, 세 signal 저장·로그 연결 통과 |
| Hosted 9.0.0~9.5.2 | `9.0-9.5.2-managed` | Managed OTLP URL | OTel native / bulk 호환 경로 | Cloud 미검증 |
| Hosted 9.5.3 이상 9.x | `9.5.3+-managed` | Managed OTLP URL | OTel native / native metric 경로 | Cloud 미검증 |
| Serverless | `serverless-managed` | 프로젝트 Managed OTLP URL | OTel native / native metric 경로 | 미검증 |
| 로컬 9.5.6 + EDOT Collector | 별도 local runbook | Collector 4317/4318 | OTel native | 세 프로토콜, 실제 저장·로그 연결 및 장애 복구 통과 |

8.x APM은 이 비공식 SDK의 upstream OTLP **기존 배포 호환 경로**입니다.
Elastic의 공식 EDOT SDK를 APM Server에 직접 연결하는 공식 지원 구성이라는 뜻이 아닙니다.
Elastic이 권장하는 EDOT 신규 배포는 해당 버전의 Agent/Collector/Gateway 안내를 따르세요.

9.x의 metric 경로는 문서상 일반적인 내부 라우팅입니다. 버전에서 추론한 예상값이며
실제 backend 내부 처리 경로를 관측하거나 강제로 변경하는 기능이 아닙니다.
Managed OTLP의 제공 여부도 해당 배포에서 확인해야 합니다. URL을 자동 생성하지 않습니다.
9.0~9.5.2 호환 경로에서는 cumulative histogram이 지원되지 않으므로 DELTA를 사용합니다.
9.5.3+ 예시도 기존 SDK의 DELTA 기본값을 유지합니다.

## 예시 파일

- [Hosted 8.x APM](deploy/cloud/profiles/elastic-8-apm.env.example)
- [Hosted 9.0~9.5.2 Managed OTLP](deploy/cloud/profiles/elastic-9-compat-managed.env.example)
- [Hosted 9.5.3+ Managed OTLP](deploy/cloud/profiles/elastic-9-native-managed.env.example)
- [Serverless Managed OTLP](deploy/cloud/profiles/serverless-managed.env.example)

각 예시의 version은 자신의 실제 버전으로 바꿉니다. 기존 `.env`를 자동으로 덮어쓰지 않습니다.
`ELASTIC_STACK_VERSION`과 `ELASTIC_DEPLOYMENT`는 Python 검증 도구의 metadata이며,
Go SDK의 `ConfigFromEnv()`가 수집 프로토콜을 자동으로 바꾸는 환경변수는 아닙니다.

## 실행

설정 파일이나 인증정보 없이 프로필만 확인:

```sh
python3 deploy/cloud/verify.py --list-profiles
```

현재 8.19.16 배포를 명시해 파일 검사와 실연동을 수행:

```sh
python3 deploy/cloud/verify.py --elastic-version 8.19.16 --preflight
python3 deploy/cloud/verify.py --elastic-version 8.19.16 --protocol http/protobuf --protocol grpc
```

기본 경로는 버전으로 결정되므로 8.x에 `--target managed-otlp`를 함께 지정하면
telemetry를 보내기 전에 오류를 반환합니다. 9.x의 기존 APM을 의도적으로 유지하려는 경우에는
`--target apm`을 명시할 수 있으나 Cloud 9.x APM 실검증 기록은 없습니다.

Serverless는 `--deployment serverless`를 지정하고 Stack version을 지정하지 않습니다.
Hosted 9.5의 Managed OTLP 자동 선택은 `9.5.2` / `9.5.3` 경계 때문에 patch version까지 필요합니다.
그 외 major.minor 표기는 해당 minor 범위를 기대한다는 뜻입니다.
CLI 인자가 파일 metadata보다 우선하며, 기존 수동 `--target apm` 사용법도 유지합니다.

## 버전 검증과 출력

- `--preflight`는 offline 검사이며 `version_verified=false`입니다.
- 실연동에서 Elasticsearch/Kibana 조회 권한이 있으면 먼저 서버의 실제 version을 읽습니다.
- 기대 version과 실제 version이 다르면 telemetry 전송 전에 중단합니다.
- version을 지정하지 않았다면 조회된 version으로 프로필을 선택합니다.
- 조회할 수 없으면 선언된 version을 사용하고 미검증 상태를 표시합니다.
- version도 수동 target도 없고 조회도 안 되면 이전 동작의 Managed OTLP 기본값을 유지하되
  `unversioned-managed-otlp`와 미검증 경고를 표시합니다. 수집 경로 검증으로 간주하지 마세요.
- 결과의 `compatibility`에는 profile, 선언/실제 version, 검증 여부, 저장 형식,
  예상 metric 경로 및 알려진 실검증 version 목록이 포함됩니다.
- 버전 정보 외에 실제 endpoint·API Key·계정 식별자는 출력하지 않습니다.
- 7.x와 10.x 이후는 이 matrix 범위 밖이며 알려진 호환 구성을 임의로 추정하지 않습니다.

Elasticsearch의 `/_otlp` 직접 ingest는 이 Cloud 검증 도구의 target이 아닙니다.
Native ES OTLP endpoint는 9.2+ metrics, 9.5+ logs/traces preview 등 signal별 가용성이
다르며 Gateway enrichment 없는 직접 ingest를 완전한 APM 경로로 취급하지 않습니다.

## 근거

- [Managed OTLP 및 9.5.3 metric 경계](https://www.elastic.co/docs/reference/opentelemetry/managed-inputs/managed-otlp-endpoint)
- [Hosted 9.0+ Managed OTLP의 내부 호환 경로](https://www.elastic.co/observability-labs/blog/opentelemetry-histograms-elastic-cloud-hosted)
- [기존 APM Server OTLP intake](https://www.elastic.co/docs/solutions/observability/apm/opentelemetry-intake-api)
- [버전별 EDOT 배포 경로](https://www.elastic.co/docs/solutions/observability/get-started/opentelemetry/start-with-otel)
- [Elasticsearch native OTLP signal별 가용성](https://www.elastic.co/docs/manage-data/ingest/otlp-endpoint)
