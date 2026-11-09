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
아래 예시는 Managed OTLP용입니다. Cloud 8.19의 APM endpoint 설정과 실제 검증은
[Cloud runbook](deploy/cloud/README.md)을 참고하세요. Elasticsearch API URL은 수집용 endpoint와 구분합니다.

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
OTLP exporter 환경변수와 resource 환경변수는 `New`에서도 읽으며, 명시한 exporter 코드 설정이 우선합니다.

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

트레이스 JSON 인코딩과 모든 gRPC 전송은 공식 Go exporter를 사용합니다. 고정 버전에서 메트릭·로그
HTTP exporter는 binary protobuf 전용이라, 이 두 신호는 HTTP 경계에서 OTLP JSON으로 변환합니다. 세 JSON 신호 모두 응답 검증 계층을 거칩니다.
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
시작 시에는 고정 크기 버퍼에 호출 주소(PC)만 보관하고, 종료 시 임계값 이상인 span에 한해 문자열로 변환합니다.
호출 주소 캡처와 문자열 변환은 공용 mutex 밖에서 수행하며 버퍼는 재사용합니다.
끝나지 않는 span은 종료 또는 SDK shutdown까지 보관되며 상한에 포함됩니다.
빠른 span도 호출 주소 수집 비용은 발생합니다. 측정 결과와 재현 방법은 [PERFORMANCE.md](PERFORMANCE.md)에 있습니다.
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

## 장애 검증 및 CI

장애 테스트는 실제 로컬 HTTP/gRPC 서버에 오류를 주입합니다. HTTP 429·503 및 gRPC Unavailable의
재시도, 영구 오류의 비재시도, 연결 실패와 느린 응답의 deadline, 지속 장애의 전송 제한,
큐 포화 시 애플리케이션 비차단 및 span 폐기, 전송 중 shutdown을 검사합니다.
기본 exporter backoff를 유지하므로 전체 테스트는 약 1~2분 걸릴 수 있습니다.

```bash
go test -race -timeout=3m ./...
```

GitHub Actions는 main push·pull request·수동 실행을 지원합니다. Linux·macOS에서 포맷·의존성·
race/커버리지·vet·빌드를 검사하고 커버리지를 14일 보관합니다. 별도 Linux job에서 벤치마크를
측정해 아티팩트로 남깁니다. 공유 runner의 시간 편차로 빌드를 실패시키지는 않습니다.
수집 데이터는 메모리 큐에 보관되므로 프로세스 종료나 큐 포화 시 유실될 수 있습니다.
장기 보관과 재시작 복구가 필요하면 Collector의 persistent queue를 별도로 구성하세요.

## 코드로 exporter 설정하기

```go
cfg := edotgo.DefaultConfig()
cfg.ServiceName = "orders-go"
cfg.OTLP = edotgo.ExporterConfig{
    Protocol: "http/protobuf",
    Endpoint: os.Getenv("ELASTIC_OTLP_ENDPOINT"),
    Headers: map[string]string{
        "Authorization": "ApiKey " + os.Getenv("ELASTIC_API_KEY"),
    },
    Timeout: 10 * time.Second,
    Compression: "gzip",
}
// 필요하면 특정 신호만 별도로 설정합니다.
cfg.LogExporter.Protocol = "http/json"
cfg.LogExporter.Endpoint = "https://json-collector.example/v1/logs"
cfg.OnExportError = func(event edotgo.ExportError) {
    // event에는 신호, 입력 건수, export/canceled/deadline 분류만 있습니다.
    // 이 콜백에서 telemetry를 다시 전송하거나 SDK를 flush/shutdown하지 마세요.
}
sdk, err := edotgo.New(ctx, cfg)
```

동일 필드의 우선순위는 **신호별 코드 → 공통 코드 → 신호별 환경변수 → 공통 환경변수 → 기본값**입니다.
`TraceExporter`, `MetricExporter`, `LogExporter`가 신호별 설정입니다. 값이 없는 필드는 상속하며,
Headers만 nil과 빈 map을 구분합니다. nil은 상속, 빈 map은 하위 우선순위의 모든 헤더 제거입니다.
인증 헤더는 병합하지 않고 선택된 map 전체로 교체합니다. 코드의 헤더 값은 percent encoding 없이 전달합니다.

공통 HTTP Endpoint는 base URL이며 신호별 `/v1/*` 경로가 붙습니다. 신호별 HTTP Endpoint는 완전한
경로를 지정합니다. gRPC는 경로가 없는 http(s) URL을 사용합니다. userinfo·query·fragment는 허용하지 않습니다.
`Timeout=0`과 빈 Protocol/Compression은 상속을 뜻합니다. 압축 해제는 `Compression="none"`입니다.
커스텀 CA 또는 mTLS는 `TLSConfig: &tls.Config{...}`로 지정합니다. SDK는 TLS 설정과 CA pool을 복제하며,
초기화 중 Config/map/TLS 자료를 동시에 수정하지 마세요. TLS 개인키와 signer 자체는 공유하는 불변 객체로 취급합니다.

`New`는 환경변수를 변경하지 않으므로 서로 다른 설정의 SDK 인스턴스를 함께 만들 수 있습니다.
`ConfigFromEnv()`는 먼저 환경변수를 검증합니다. 잘못된 환경 설정을 코드로 대체하려면
`DefaultConfig()`부터 구성하세요. `%v`, `%+v`, `%#v`로 ExporterConfig를 출력하면 주소·인증 자료가 가려집니다.

## 인스턴스별 진단

```go
snapshot := sdk.Diagnostics()
// snapshot.Traces / Metrics / Logs:
// ExportCalls, SuccessfulBatches, FailedBatches, SuccessfulItems,
// ItemsInFailedBatches, DisabledDroppedItems, InFlightBatches,
// LastSuccessAt, LastFailureAt, LastFailureKind
```

- 전송 호출은 upstream 내부 재시도가 끝난 최종 export 결과 단위입니다.
- Items는 trace에서는 span, log에서는 record, metric에서는 data point 개수입니다.
- `DisabledDroppedItems`는 SetSending*로 전송을 끈 상태에서 실제 폐기한 입력 건수입니다.
- `ItemsInFailedBatches`는 실패한 호출에 들어 있던 건수입니다. 서버가 일부 수신했을 수 있으므로
  확정적인 유실 건수나 partial-success의 거절 건수로 해석하면 안 됩니다.
- `InFlightBatches`는 진행 중인 exporter 호출 수입니다. 내부 큐 깊이가 아닙니다.
- `Stack`은 현재 보관/예약 슬롯, 상한, 상한 때문에 스택을 생략한 누적 건수를 제공합니다.
  스택 생략은 span 자체의 폐기가 아닙니다.
- upstream 배치 API는 큐 깊이/포화 폐기 누적값을 공개하지 않아 `QueueDepthAvailable`과
  `QueueOverflowCountAvailable`은 false입니다. 진단이 전체 유실량을 집계한다고 보장하지 않습니다.

각 카운터는 atomic이며 동시 조회가 가능하지만, snapshot 전체가 하나의 트랜잭션은 아닙니다.
진단 API는 endpoint·헤더·payload·원문 오류를 저장하거나 출력하지 않으며 전역 OTel 오류 핸들러를 교체하지 않습니다.
기존 upstream 오류 보고는 그대로 동작하므로 해당 로그의 민감 정보 처리 정책은 별도로 설정하세요.
`OnExportError`는 신호별 exporter goroutine에서 동기 호출되며 여러 신호가 동시에 호출할 수 있습니다.
콜백 panic은 회수하고 `CallbackPanics`에 누적합니다. 콜백은 빠르고 동시성에 안전하게 작성하세요.
진단은 HTTP endpoint로 자동 공개되지 않으며 외부로 별도 전송하지 않습니다.

## Elastic Cloud 확인 명령

인증 정보를 로컬 환경에 설정하고 실행합니다. API Key를 저장소나 채팅에 넣지 마세요.

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT='https://<managed-otlp-endpoint>'
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
# OTEL_EXPORTER_OTLP_HEADERS는 로컬 secret 관리 도구나 shell에서 설정
# Authorization=ApiKey%20<API_KEY> 형식

go run ./examples/cloudcheck
```

명령은 `edot-go-cloudcheck` 서비스의 synthetic trace·metric·연결된 로그를 한 번 전송합니다.
출력의 `trace_id`로 Kibana에서 검색해 데이터 표시와 로그 연결을 확인할 수 있습니다.
전송 실패가 감지되면 종료 코드가 0이 아니며, 진단 JSON에는 인증 정보가 없습니다.
Exporter 성공은 Kibana의 검색/집계/서비스 맵 표시 검증과 구분해야 합니다. 이 명령은 Kibana에
접속하지 않으며 단일 서비스의 요청이므로 서비스 간 map 연결 검증은 별도 다중 서비스 테스트가 필요합니다.

## 로컬 Elastic 클러스터

Cloud 계정 없이 Elasticsearch·Kibana·EDOT Collector를 함께 띄우고 검증할 수 있습니다.

```bash
./deploy/local/start.sh
python3 deploy/local/verify.py
./deploy/local/stop.sh
```

시작은 Basic 라이선스와 localhost 전용 포트를 사용하며, 중지는 데이터 볼륨을 보존합니다.
인증 정보는 Git에서 제외한 `deploy/local/.env`에만 저장합니다.
설명과 접속 주소는 [로컬 구성 안내](deploy/local/README.md)를 참고하세요.
로컬 테스트는 Managed OTLP의 Cloud 인증/TLS 경로나 라이선스가 필요한 Service Map 화면 검증을 대체하지 않습니다.

## 전송 경계 조건과 flush

코드로 지정한 `ExporterConfig.Headers`는 초기화 시 프로토콜별로 검사합니다.
HTTP는 RFC 형식의 헤더 이름/값을, gRPC는 ASCII metadata 이름과 non-binary 값 형식을 요구합니다.
gRPC의 `-bin` metadata는 raw bytes 문자열을 허용하며 전송 계층이 base64 인코딩합니다.
대소문자만 다른 동일 헤더를 한 map에 중복 지정하면 인증 값 선택이 모호하므로 오류를 반환합니다.
이 검사는 코드 설정에 적용됩니다. 환경변수 헤더 파싱은 upstream exporter의 동작을 따릅니다.
검증 오류에 헤더 값은 포함하지 않습니다.

HTTP/JSON exporter는 non-empty 2xx 응답이 JSON 또는 유효한 protobuf인지 검사합니다.
HTML 로그인 페이지나 잘못된 Content-Type을 성공으로 집계하지 않으며 partial-success 거절도 보고합니다.
호환성을 위해 빈 성공 응답과 binary protobuf 응답을 받는 동작은 유지합니다.
트레이스 요청의 JSON 인코딩은 계속 공식 exporter가 담당합니다.

`ForceFlush(ctx)`는 세 provider에 같은 deadline으로 병렬 flush를 요청합니다.
느린 트레이스 전송이 메트릭/로그의 flush 시간을 먼저 소모하지 않습니다.
모든 결과를 모으거나 deadline 도달 시 반환하며, 이미 진행 중인 upstream 취소 처리는
반환 뒤 마무리될 수 있습니다. flush 반환만으로 서버의 영구 저장이나 모든 비동기 전송 성공이
보장되지는 않으므로 `Diagnostics()`와 함께 확인하세요. `Shutdown(ctx)`도 세 provider의 종료를 병렬로 시작합니다.
각 종료 호출이 반환할 때까지 기다리며 첫 호출의 context와 결과를 공유합니다.
동시에 호출하거나 다시 호출해도 종료를 반복하지 않습니다.

코드로 지정하는 endpoint는 호스트가 필요하며, 포트를 명시하면 1–65535 범위여야 합니다.
빈 포트나 query 구분자(`?`)만 있는 URL도 초기화 시 거부합니다.
포트 생략과 IPv6 주소는 지원합니다. 이 검증은 코드 설정에 적용되며
환경변수 endpoint 파싱은 upstream exporter가 담당합니다.


### 배치·큐·메트릭 주기를 코드로 설정

```go
cfg := edotgo.DefaultConfig()
cfg.TraceBatch = edotgo.BatchConfig{
    MaxQueueSize:       4096,
    MaxExportBatchSize:  256,
    ExportInterval:     time.Second,
    ExportTimeout:      5 * time.Second,
}
cfg.LogBatch = cfg.TraceBatch
cfg.MetricReader = edotgo.MetricReaderConfig{
    Interval:           10 * time.Second,
    Timeout:            5 * time.Second,
    MaxExportBatchSize:  1000,
}
```

각 필드의 양수 값은 해당 upstream 환경변수/기본값보다 우선합니다. 0은 상속하며
음수는 활성화된 signal의 초기화 오류입니다. 큐와 batch 크기를 모두 코드로 지정했다면
batch가 큐보다 클 수 없습니다. 하나만 지정한 경우 나머지는 upstream에서 결정하며,
최종 batch 크기는 upstream 정책에 따라 큐 크기로 제한될 수 있습니다.
큐·batch 크기는 byte가 아닌 span/log record 개수이고, metric batch는 data point 개수입니다.
큐가 가득 찼을 때 앱을 막지 않고 드롭하는 기존 정책을 유지합니다.

`BatchConfig.ExportTimeout`과 `MetricReader.Timeout`은 processor/reader 제한입니다.
`ExporterConfig.Timeout`의 네트워크 전송 제한과는 별도이며 적용되는 context 중 짧은
deadline에 따라 취소될 수 있습니다. 종료 시에도 별도 timeout의 context를 사용하세요.

실제 Collector 중단·네트워크 차단 부하 검증은 [로컬 runbook](deploy/local/README.md),
Cloud 8.19 APM 및 Managed OTLP 설정은 [Cloud runbook](deploy/cloud/README.md)을 참고하세요.


### 큐 진단과 버전 설치

`QueueDiagnosticsFromMetrics`로 upstream 자체 메트릭의 processor별 큐 깊이·용량과
queue-full 드롭을 읽을 수 있습니다. 실험적 관측 활성화와 global meter 연결이 필요하며,
로그 드롭 보고 지연·delta/cumulative 차이를 구분합니다. 라이브러리는 전역 설정을 자동 변경하지 않습니다.
자세한 사용법은 [QUEUE_DIAGNOSTICS.md](QUEUE_DIAGNOSTICS.md)를 참고하세요.

태그를 고정한 설치·업그레이드와 source 배포 파일 검증은 [RELEASING.md](RELEASING.md)에 있습니다.


Elastic 8.x / 9.x / Serverless별 설정 예시와 버전 검증은
[ELASTIC_COMPATIBILITY.md](ELASTIC_COMPATIBILITY.md)에 구분했습니다.
