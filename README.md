# EDOT Java → Go: portable SDK layer

Elastic EDOT Java의 플랫폼 독립적인 SDK 커스터마이징을 Go로 옮긴 **비공식 부분 포팅**입니다.
공식 EDOT Go SDK나 Java 에이전트 전체의 대체 구현은 아닙니다.
원본은 `elastic/opentelemetry` 문서 저장소가 아닌
[`elastic/elastic-otel-java`](https://github.com/elastic/elastic-otel-java)입니다.

기준 커밋: `be9688413a5442d4b0e04fa3d128943a465a8f35` (2026-11-04).
이식 기능과 미구현 항목은 [PORTING.md](PORTING.md)를 확인하세요.

## 요구사항

- Go **1.26 이상**. 고정한 OTel 의존성의 요구 버전입니다. `GOTOOLCHAIN=auto`이면 도구 체인이 자동 다운로드될 수 있습니다.
- Elastic Cloud의 **Managed OTLP Endpoint** 또는 선택한 OTLP 프로토콜을 받는 Collector.
- 전송 프로토콜: **http/protobuf**(기본값), **grpc**(protobuf), **http/json**. 세 신호 모두 지원합니다.

## Elastic Cloud로 실행

Kibana의 OpenTelemetry 온보딩 화면에서 제공하는 Managed OTLP 주소와 API Key를 사용합니다.
일반 Elasticsearch 주소나 기존 APM Server 주소를 대신 넣지 마세요.

```bash
export OTEL_SERVICE_NAME=orders-go
export OTEL_RESOURCE_ATTRIBUTES='service.version=1.0.0,deployment.environment.name=production'
export OTEL_EXPORTER_OTLP_ENDPOINT='https://<managed-otlp-endpoint>'
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_HEADERS='Authorization=ApiKey%20<API_KEY>'

go run ./examples/httpserver
```

다른 터미널에서 요청을 보냅니다.

```bash
curl http://127.0.0.1:8080/orders/123
curl http://127.0.0.1:8080/orders/error
```

트레이스·커스텀 메트릭·Go 런타임 메트릭·slog 로그를 전송합니다. Ctrl+C로 종료하면 버퍼를 비웁니다.
HTTP 경로는 `GET /orders/{id}`처럼 고정된 이름으로 계측해 메트릭 카디널리티 증가를 줄입니다.
호스트·Kubernetes 메트릭과 컨테이너 stdout 파일 수집은 이 SDK가 담당하지 않습니다.

Collector 경유 시 `OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318`로 바꾸고,
Cloud 인증은 Collector 쪽에서 설정할 수 있습니다.

## 앱에 적용

모듈 경로는 `github.com/chikilov/edot-go`입니다. 비공개 저장소 접근 권한이 있는 GitHub 계정으로
Git 인증을 설정한 뒤 사용하세요.

```bash
go env -w GOPRIVATE=github.com/chikilov/edot-go
go get github.com/chikilov/edot-go@main
```

이미 GOPRIVATE 값이 있다면 기존 패턴을 유지하면서 위 경로를 추가하세요.
다른 로컬 프로젝트에서 연결하려면 다음처럼 설정할 수 있습니다.

```bash
go mod edit -require=github.com/chikilov/edot-go@v0.0.0
go mod edit -replace=github.com/chikilov/edot-go=/absolute/path/to/edot-go
go mod tidy
```

```go
sdk, err := edotgo.NewFromEnv(ctx)
if err != nil {
    return err
}
// 종료 시 취소되지 않은 별도 context와 타임아웃으로 sdk.Shutdown을 호출하세요.

handler := sdk.HTTPHandler("GET /orders/{id}", yourHandler)
client := &http.Client{Transport: sdk.HTTPTransport(http.DefaultTransport)}
logger := sdk.Logger("orders")
logger.InfoContext(requestContext, "order received")

ctx, span := sdk.TracerProvider.Tracer("orders").Start(requestContext, "process-order")
defer span.End()
_ = ctx
_ = handler
_ = client
```

`New`는 전역 provider를 변경하지 않습니다. 기존 OTel API 사용자에게 전역 등록이 필요하면
앱 시작 시 한 번 `sdk.InstallGlobals()`를 호출하세요. 종료 시 이전 전역 provider로 복원하지 않습니다.
서로 다른 에이전트/SDK를 같은 요청에 중복 적용하면 중복 span이 발생할 수 있습니다.

## 설정

| 환경변수 | 기본값 / 의미 |
|---|---|
| `OTEL_SERVICE_NAME` | `unknown_service:go`, resource의 service.name보다 우선 |
| `OTEL_RESOURCE_ATTRIBUTES` | 표준 OTel resource 속성 |
| `OTEL_SDK_DISABLED` | `false`, 전체 전송과 런타임 계측 비활성화 |
| `OTEL_TRACES_EXPORTER`, `OTEL_METRICS_EXPORTER`, `OTEL_LOGS_EXPORTER` | `otlp`, `none`도 지원 |
| `OTEL_TRACES_SAMPLER` | `parentbased_traceidratio` |
| `OTEL_TRACES_SAMPLER_ARG` | `1.0`, 0~1 |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf`(기본값), `grpc`, `http/json`. 신호별 설정 우선 |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | upstream exporter 기본값, 운영 시 명시 권장 |
| `OTEL_EXPORTER_OTLP_HEADERS` | 인증 헤더. 값에 공백 등이 있으면 percent encoding 사용 |
| `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE` | `DELTA`, `CUMULATIVE`·`LOWMEMORY`도 지원 |
| `EDOT_GO_RUNTIME_METRICS_ENABLED` | `true`, Go 런타임 메트릭 |
| `EDOT_GO_SPAN_STACKTRACE_MIN_DURATION` | `5ms`, 음수로 비활성화 |
| `EDOT_GO_SPAN_ATTRIBUTES_COPY_FROM_BAGGAGE_INCLUDE` | 빈 값, 허용한 키만 span 속성에 복사 |
| `EDOT_GO_LOG_ATTRIBUTES_COPY_FROM_BAGGAGE_INCLUDE` | 빈 값, 허용한 키만 log 속성에 복사 |

추가 sampler: `always_on`, `always_off`, `traceidratio`, `parentbased_always_on`, `parentbased_always_off`.
Java의 `experimental_composite_parentbased_traceidratio`는 지원하지 않습니다.

신호별 `OTEL_EXPORTER_OTLP_{TRACES,METRICS,LOGS}_ENDPOINT`, `_HEADERS`, `_TIMEOUT`,
TLS 인증서, 압축 등은 upstream exporter 및 JSON 어댑터가 처리합니다. HTTP 공통 endpoint에는
`/v1/traces`, `/v1/metrics`, `/v1/logs`가 붙고 HTTP 신호별 endpoint는 완전한 경로를 지정합니다.
gRPC endpoint는 호스트와 포트를 지정하며 `/v1/*` 경로를 붙이지 않습니다.
표준 batch/metric reader 설정도 해당 Go SDK가 지원하는 범위에서 적용됩니다.
모든 Java 환경변수를 호환하는 범용 자동 설정기는 아닙니다. 설정은 초기화 시 읽습니다.

프로그램 설정은 `DefaultConfig()`를 수정한 뒤 `New(ctx, config)`에 전달하세요.
이 경우 `ConfigFromEnv()`를 호출하지 않으면 위 Config 관련 환경변수는 적용되지 않습니다.
OTLP exporter 환경변수와 resource 환경변수는 `New`에서도 읽습니다.

## 프로토콜 선택

공통 설정을 바꾸면 활성화된 트레이스·메트릭·로그에 적용됩니다.

```bash
# HTTP + binary protobuf (기본값)
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318

# gRPC + protobuf
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317

# HTTP + OTLP JSON
export OTEL_EXPORTER_OTLP_PROTOCOL=http/json
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
```

위 설정은 서로 대체하는 예시입니다. TLS를 사용하면 `https://` 주소와 필요한 인증서를 지정하세요.
Elastic Cloud에는 온보딩에서 제공하는 주소와 protobuf 기반 설정을 우선 사용하세요.
HTTP/JSON은 수신기가 해당 인코딩을 지원해야 하며, Elastic Cloud 직접 수신은 실계정으로 검증하지 않았습니다.
필요하면 JSON을 받는 Collector를 거쳐 Cloud로 protobuf를 전송할 수 있습니다.

신호별로 프로토콜과 주소를 다르게 지정할 수 있습니다.

```bash
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318
export OTEL_EXPORTER_OTLP_TRACES_PROTOCOL=grpc
export OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://collector:4317
export OTEL_EXPORTER_OTLP_LOGS_PROTOCOL=http/json
export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://collector:4318/v1/logs
```

트레이스 JSON과 모든 gRPC 전송은 공식 Go exporter를 사용합니다. 고정 버전에서 메트릭·로그
HTTP exporter는 binary protobuf 전용이라, 이 두 신호는 HTTP 경계에서 OTLP JSON으로 변환합니다.
기존 exporter의 배치·재시도·partial-success 처리를 유지하며, TLS CA/mTLS 인증서·인증 헤더·
신호별 timeout·gzip을 처리합니다. JSON 요청/응답의 디코딩된 본문은 각각 16 MiB로 제한됩니다.
JSON에서는 trace/span ID를 hex, enum을 숫자로 표현하고 64비트 정수 정밀도를 보존합니다.
잘못된 프로토콜은 초기화 오류를 반환하며 다른 프로토콜로 자동 전환하지 않습니다.

## 실행 중 제어

```go
err := sdk.SetSampleRatio(0.1) // ratio sampler에서만 가능, 새 root span에 적용
sdk.SetSendingTraces(false)
sdk.SetSendingMetrics(false)
sdk.SetSendingLogs(false)
_ = err
```

Parent-based sampler는 부모의 샘플링 결정을 유지합니다. 전송 중지는 **export 시점의 배치를 폐기**하며
지속 큐나 일시 보관 기능이 아닙니다. 이미 전송 중인 요청은 취소하지 않고, 수집 자체도 중단하지 않습니다.
일시 중지 중 큐에 들어간 데이터가 재개 후 전송될 수도 있습니다.
Kibana 중앙 설정(OpAMP)과 자동 연동되는 API는 아닙니다.

## 스택트레이스·baggage

스택트레이스는 span **시작 호출 위치**를 수집해 종료 시 임계값 이상인 span에 `code.stacktrace`로 추가합니다.
동시에 보관하는 호출 스택은 인스턴스당 최대 4096개이며 초과한 span은 스택 없이 전송됩니다.
끝나지 않는 span은 종료 또는 SDK shutdown까지 보관되며 상한에 포함됩니다.
기능을 켜면 빠른 span도 시작 시 스택 캡처 비용이 발생합니다. 부하 테스트 후 임계값/비활성화를 결정하세요.
원본 Java의 현재 및 legacy 스택트레이스 환경변수도 별칭으로 인식합니다.

Baggage 복사는 opt-in입니다. 예: `tenant.*`, `region`을 쉼표로 구분할 수 있습니다.
키 패턴은 Go `path.Match` 문법이며 `*`는 `/`를 넘지 않습니다. 로그에는 요청 context를 전달해야 합니다.
허용 목록은 **속성 복사만** 제어하며 W3C baggage의 네트워크 전파는 별도입니다.
tracecontext와 baggage 전파를 기본 제공합니다.

## 검증

```bash
go test -race ./...
go vet ./...
go build ./...
```

테스트는 로컬 HTTP 및 gRPC 수신기에 실제 요청을 전송해 세 프로토콜의 신호별 URL,
인증 헤더/메타데이터, resource 속성, 로그-트레이스 연결, delta 메트릭,
baggage와 스택트레이스를 검사합니다. JSON은 gzip·TLS·partial success·64비트 정수·hex ID도 검증합니다.
Elastic Cloud의 실제 계정에 대한 end-to-end 검증은 별도로 필요합니다.

Apache-2.0. 원본과 이식 범위의 출처는 [NOTICE](NOTICE), [PORTING.md](PORTING.md)에 기록했습니다.
