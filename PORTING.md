# EDOT Java 기능 대응표

기준: https://github.com/elastic/elastic-otel-java/tree/be9688413a5442d4b0e04fa3d128943a465a8f35

이 프로젝트는 Java 소스 문법의 기계적 변환이 아닌 Go OTel SDK 기반 재구현입니다.
Java 에이전트 전체와 동일한 기능/동작/성능을 보장하지 않습니다.

| Java 소스/기능 | Go 구현 | 차이 / 상태 |
|---|---|---|
| `custom/.../ElasticAutoConfigurationCustomizerProvider.java`의 delta 기본값 | `sdk.go: temporalitySelector` | DELTA 기본값, CUMULATIVE·LOWMEMORY 선택, up/down 계열은 cumulative |
| 위 클래스의 deployment 환경 속성 이전 | `sdk.go: New` | legacy 값을 새 이름으로 복사, 새 이름이 있으면 유지 |
| `custom/.../ElasticDistroResource.java` | `sdk.go: New` | 공식 배포판 사칭 방지를 위해 distro 이름은 `edot-go-unofficial` |
| `custom/.../compositesampling/DynamicCompositeParentBasedTraceIdRatioBasedSampler.java` | `sampler.go`, `consistent_sampler.go` | opt-in consistent56, 원자적 비율 변경. Java contrib 1.61.0-alpha 기준 벡터 210개 대조 |
| `custom/.../dynamicconfig/Blockable{Span,Metric,LogRecord}Exporter.java` | `exporters.go` | export-time gate, 신호별 수동 API |
| Span Stacktrace 확장과 `custom/.../SpanStackTraceTest.java` | `processors.go: stackProcessor` | Go 시작 PC 수집·종료 시 지연 문자열화·버퍼 재사용, 4096 in-flight cap. Java 프레임 필터와 동일하지 않음 |
| Java baggage processor | `processors.go` | 명시적 allowlist로 span/log에 복사. Go glob 문법 |
| Java runtime telemetry | Go contrib runtime instrumentation | Go 런타임의 메트릭으로 대체, JVM 메트릭과 이름/의미 다름 |
| Java HTTP 자동 계측 | `HTTPHandler`, `HTTPTransport` | Go OTel HTTP 계측을 명시적으로 적용 |
| Java 로깅 프레임워크 연결 | `Logger`의 `otelslog` bridge | slog만 제공, 기존 stdout 로그 자동 수집 안 함 |
| Go exporter 코드 설정 | `exporter_config.go`, `protocol.go` | 공통/신호별 endpoint·인증 헤더·protocol·timeout·압축·TLS, 환경변수보다 우선 |
| Go 인스턴스 진단 | `diagnostics.go` | 전송 결과·전송 중지 폐기·스택 상한 생략·안전한 오류 콜백. 내부 배치 큐 수치는 미제공 |
| upstream SDK lifecycle / batch | `sdk.go`, upstream Go SDK | flush·shutdown·부분 초기화 실패 정리, 기본 전역 부작용 없음 |
| `ElasticUserAgentHeader.java` | upstream Go exporter User-Agent 유지 | Elastic 공식 agent로 식별하지 않음 |
| JVM bytecode agent·runtime attach·bootstrap | 미구현 | Go 런타임 구조에 맞는 별도 설계 필요 |
| gRPC 서비스 계측 | `GRPCServerOption`, `GRPCClientOption` | RPC 스팬·메트릭 및 trace context 전파 |
| JDBC에 대응하는 Go DB 계측 | `OpenSQLDB` | `database/sql` 드라이버 connector를 계측하며 SQL 문과 드라이버 오류 메시지는 스팬에서 제외 |
| Kafka 계측 | `KafkaOption` | franz-go 생산·소비 스팬, 브로커 통신 메트릭, trace context 전파 |
| 기타 메시징 클라이언트 계측 | 미구현 | 클라이언트별 연동 필요 |
| Inferred spans / async-profiler / JVMTI | 미구현 | Go pprof 데이터를 Java 방식으로 바꿀 수 없음 |
| Elastic Universal Profiling correlation | 미구현 | Go용 profiler correlation 별도 구현 필요 |
| 전용 중앙 설정 | `PollCentralConfig` | HTTPS JSON 폴링으로 비율과 신호별 전송 여부 적용 |
| OpAMP 원격 설정 | `RunOpAMP` | 공식 HTTP 클라이언트, elastic JSON의 샘플링·신호 전송 제어, 적용/실패 보고. 실제 Cloud 연동은 미검증 |
| 동적 instrumentation별 활성/비활성화 | 미구현 | 신호별 export gate와는 별개 |
| 리소스 detector 확장 | `Config.ResourceDetectors` | 명시적으로 전달한 OTel detector 실행, 환경변수 속성 우선 |
| AWS/GCP/Azure/CloudFoundry detector 기본 제공 | 미구현 | 앱이 선택한 detector를 추가 가능 |
| 선언형 YAML·SPI 확장·Java system properties | 미구현 | Go Config/API 및 명시된 환경변수만 지원 |
| OTLP gRPC | `protocol.go` | 공식 Go trace/metric/log gRPC exporter, 인증 메타데이터·TLS 지원 |
| OTLP HTTP/protobuf | `protocol.go` | 기존 기본값 유지, 공식 Go HTTP exporter |
| OTLP HTTP/JSON | `protocol.go`, `json_transport.go` | 트레이스는 native JSON 인코딩, 메트릭·로그는 HTTP 변환 계층. 세 신호 모두 응답 검증. OTLP hex ID·숫자 enum·64비트 정수 보존 |

## 사용한 기반 라이브러리

- OTel Go SDK 및 trace/metric/log HTTP·gRPC exporter: OTLP 전송·TLS·재시도·배치
- OTel Go contrib `otelhttp`: HTTP client/server 계측과 전파
- OTel Go contrib `otelslog`: 구조화 로그·trace/span ID 연계
- OTel Go contrib runtime: Go 런타임 메트릭

정확한 버전은 go.mod/go.sum에 고정되어 있습니다. Go 의존성은 Java 원본이 아닌 각각의 upstream 라이선스를 따릅니다.
