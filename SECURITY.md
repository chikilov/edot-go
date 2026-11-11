# 보안 검토와 운영 조건

검토 기준일: 2026-11-10. 소스, Go 의존성/도구 체인, 인증된 HTTP 호출,
설정 직렬화, Git 이력 및 source 배포 경로를 검토했습니다.
이 문서는 침투 테스트 인증이나 취약점 부재 보증이 아닙니다.

## 확인한 문제와 v0.5.1 수정

| 문제 | 재현·영향 | 조치 |
|---|---|---|
| 오래된 Go/의존성 | Go 1.26.0 + x/net 0.59.0에서 govulncheck가 고유 ID 49개(호출 경로 보고 34개)를 탐지. 라이브러리와 예제 범위이며 모두 실제 악용 가능하다는 의미는 아님 | Go 최소 1.26.10, x/net 0.62.0으로 변경. 필요한 x/sys·x/text 전이 의존성 갱신 |
| HTTP redirect | 임시 local server와 가짜 키로 SDK의 여섯 HTTP 조합에서 307 replay를 재현. Python 302에서도 목적지에 Authorization 전달 가능 | SDK HTTP/protobuf·HTTP/JSON 및 Cloud/local 인증 조회에서 모든 redirect 차단. 최종 수집 URL을 직접 설정 |
| JSON 설정 로그 노출 | ExporterConfig를 json.Marshal 또는 slog JSON에 넣으면 endpoint·인증 헤더 노출 | MarshalJSON이 redacted 객체만 출력. 기존 fmt 마스킹 유지 |
| 응답 내용이 오류로 반사 | custom JSON parser의 오류에 서버가 보낸 잘못된 값이 들어갈 수 있음 | 자체 JSON/protobuf parse 및 content-encoding 오류에서 원문 제외 |
| env 변형/backup 유입 위험 | 실제 .env는 제외되어 있었지만 .env.backup 등의 실수에 대한 방어 부족. 실제 Git 유출은 발견하지 않음 | ignore 확대, source 패키저에서 tracked private env 변형 거부, 회귀 테스트 추가 |

HTTP client 통합은 TLS 검증, custom CA/mTLS 로딩, timeout·압축 설정을 유지합니다.
HTTP 응답은 guard로 최대 16 MiB까지 제한하며, upstream의 더 작은 제한이 적용될 수 있습니다.
기존 JSON decoded body 제한도 유지합니다. 응답 크기 제한은 방어 보강이며,
기존 protobuf exporter에 무제한 응답 취약점이 있었다는 의미가 아닙니다.

## 아직 필요한 운영 조치: 현재 검증 키의 과도한 권한

실제 key 자체를 기록하지 않고 `_has_privileges`로 확인한 결과, 검토 시 사용한
Cloud key는 `all`, `manage_security`, `manage_api_key`와 데이터 삭제/관리 권한을 갖고 있습니다.
운영 앱에 이 key를 배포하면 앱 또는 키 유출이 클러스터 관리·삭제로 확대될 수 있습니다.
이 권한은 코드 패치로 없어지지 않습니다.

- 앱에는 **수집 전용 key**를 사용합니다.
- 저장 확인 도구에는 **조회 전용 key**를 별도로 넣습니다.
- 기존 key의 사용처를 조사한 뒤, 앱 교체와 동작 확인을 거쳐 기존 광범위 key를 폐기하세요.
  이번 검토는 key 생성·권한 변경·취소를 자동 수행하지 않았습니다.
- Hosted APM 8.x용 검토 가능한 생성 body:
  [수집 key 예시](deploy/cloud/security/apm8-ingest-key.example.json),
  [조회 key 예시](deploy/cloud/security/apm8-read-key.example.json).
  발급 API는 Elasticsearch `POST /_security/api_key`이며 예시 파일은 실행되지 않습니다.
- 조회 index 패턴은 실제 검증용 data stream으로 더 좁히세요. 예시 `monitor`는 버전 조회에
  사용됩니다. 클러스터 권한을 주지 않으면 버전 자동 조회는 미검증으로 남을 수 있습니다.
- 조회 key 예시는 Elasticsearch 직접 조회용입니다. Kibana proxy 사용에는 별도 Kibana 접근
  권한이 필요할 수 있으므로 검증 편의를 위해 관리 권한을 추가하지 마세요.
- 위 role은 APM 8.x용입니다. Managed OTLP는 해당 Cloud 온보딩의 key 권한 모델을 따릅니다.

로컬 `.env`에서 두 key를 분리하는 형식:

```dotenv
OTEL_EXPORTER_OTLP_HEADERS='Authorization=ApiKey%20수집전용_encoded_key'
ELASTICSEARCH_URL='https://대상_Elasticsearch_호스트'
ELASTICSEARCH_API_KEY='조회전용_encoded_key'
```

`ELASTICSEARCH_API_KEY`에는 `ApiKey` 접두어 없이 Base64 `id:key`의 encoded 값을 넣습니다.
지정하면 버전/저장 조회에서만 쓰며 SDK subprocess에 전달하거나 출력하지 않습니다.
생략하면 서버 버전과 저장 조회를 건너뜁니다.
운영에서의 권장 구성은 두 key를 분리하는 것입니다. 실제 설정 파일은 자동 수정하지 않습니다.

## 남는 경계와 운영 설정

- `Config.OnExportError`와 `Diagnostics()`는 비밀정보를 제외합니다. upstream이 반환하는
  원래 오류와 기본 OTel error handler에는 URL·receiver 오류 메시지가 들어갈 수 있습니다.
  원본 오류 동작은 호환성을 위해 유지하므로 로그 정책을 별도로 설정해야 합니다.

```go
// 프로세스 전체 OTel 오류 보고 정책을 앱에서 명시적으로 선택하는 예입니다.
otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {
    log.Print("OpenTelemetry export failed; inspect sanitized SDK diagnostics")
}))
```

- `ExporterConfig`의 표준 fmt/JSON/slog 마스킹은 raw field 접근, 임의 reflection serializer,
  애플리케이션이 직접 기록한 헤더를 보호하지 않습니다.
- 로그 body·span 속성·HTTP URL·선택한 baggage는 앱 데이터입니다. API Key/토큰/개인정보를
  넣기 전에 마스킹하세요. baggage 복사 기본값은 비활성화이며 필요한 키만 allowlist로 지정합니다.
  stacktrace의 파일 경로가 민감하면 `StacktraceMinDuration=-1` 또는
  `EDOT_GO_SPAN_STACKTRACE_MIN_DURATION=-1`로 비활성화할 수 있습니다.
- Cloud 검증은 HTTPS를 요구하고 인증서 검증을 유지합니다. SDK 자체는 local Collector를
  위한 HTTP도 지원합니다. 원격 전송에는 HTTPS 또는 별도로 보호된 신뢰 네트워크를 사용하세요.
  호출자가 `TLSConfig.InsecureSkipVerify` 등으로 검증을 해제하지 않도록 관리해야 합니다.
- endpoint·인증 설정은 신뢰된 운영 설정입니다. 임의의 외부 요청에서 endpoint를 받아 넣는
  앱에 대한 SSRF 방어를 이 라이브러리가 대신하지 않습니다.
- local Compose는 loopback 전용 개발 환경이며 내부 통신은 HTTP입니다. Docker 관리 권한이
  있는 로컬 사용자는 컨테이너 설정의 비밀정보를 읽을 수 있습니다. 운영 배포 예제가 아닙니다.
- CI의 govulncheck/Gitleaks는 알려진 문제와 패턴을 탐지합니다. 미탐·새 취약점 가능성이 있고,
  컨테이너 OS 패키지·Cloud 조직/IAM 전체·다른 앱의 비밀정보 처리는 이번 스캔 범위 밖입니다.

## 검증 기록

- 가짜 인증정보만 사용한 redirect·JSON 로그 노출 회귀 테스트.
- 신뢰하지 않는 TLS 인증서 거절 및 custom CA 정상 연결 테스트.
- malformed JSON 응답 비노출, 응답 크기 제한, env archive 거부 테스트.
- 패치 후 govulncheck v1.9.0: `No vulnerabilities found.`
- Gitleaks v8.30.1 커밋 이력 검사: 발견 0건. 실제 Cloud/local `.env` 권한 0600 확인.
- 실제 local Elastic 세 프로토콜 및 Cloud 8.19.16 APM 두 프로토콜의 저장·ID 연결 회귀 확인.
- CI에서 pinned govulncheck/Gitleaks를 실행해 이후 검토를 반복합니다. 취약점 DB는 실행 시점 기준입니다.

공식 근거:
[Go 보안 패치 이력](https://go.dev/doc/devel/release),
[HTTP/2 관련 예: GO-2026-6611](https://pkg.go.dev/vuln/GO-2026-6611),
[Go vulnerability DB](https://vuln.go.dev/),
[APM 8.19 key 권한](https://www.elastic.co/guide/en/observability/8.19/apm-api-key.html).


For deployments that require encrypted OTLP transport, set
`Config.RequireSecureOTLP` or use `EDOT_GO_REQUIRE_SECURE_OTLP=true` with
`ConfigFromEnv`. Each enabled exporter must resolve to an explicit HTTPS
endpoint. This policy rejects insecure exporter settings, certificate
verification bypass and explicitly configured TLS versions below 1.2.

With strict OTLP enabled, a signal endpoint on a different HTTPS origin
requires its own explicit `ExporterConfig.Headers` when common headers are
configured. Set an empty map for an exporter that needs no headers.

OpAMP 추가 시점의 재검사에서 Go 표준 라이브러리 GO-2026-6859와
Kafka 경로의 압축 의존성 GO-2026-6865가 발견되어 최소 Go 1.26.11 및
github.com/klauspost/compress 1.20.2로 갱신했습니다.
OpAMP는 별도 인증 헤더, 인증서 검증을 수행하는 HTTPS 연결, 메시지 크기 제한,
리다이렉트 거부를 사용합니다. 원격 설정은 지원하는 런타임 제어 항목만 적용합니다.
