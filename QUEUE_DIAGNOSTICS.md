# Upstream 큐 진단

`QueueDiagnosticsFromMetrics(*metricdata.ResourceMetrics)`는 upstream SDK 자체 메트릭을
processor별 큐 깊이·용량·queue-full 드롭 관측값으로 변환합니다. 현재 고정된 OTel Go
1.47.0의 실험적 메트릭 이름과 instrumentation scope를 검증해 처리합니다.

```go
var data metricdata.ResourceMetrics
if err := reader.Collect(ctx, &data); err != nil {
    return err
}
queues := edotgo.QueueDiagnosticsFromMetrics(&data)
```

## 수집 조건

- 프로세서를 만들기 전에 `OTEL_GO_X_OBSERVABILITY=true`를 설정합니다.
- upstream은 **프로세서를 생성할 때의 전역 OTel MeterProvider**에 메트릭을 연결합니다.
  해당 provider에 reader/exporter가 있어야 합니다. 기존 real provider가 연결돼 있으면
  나중에 다른 global provider를 설치해도 기존 프로세서가 자동으로 옮겨지지 않습니다.
- reader/exporter가 받은 `ResourceMetrics`를 위 함수에 전달합니다.
- 라이브러리 함수는 환경변수·전역 provider를 변경하지 않으며 네트워크 전송도 하지 않습니다.
  진단값은 수집 표본 시점 기준이며 concurrent queue 연산과 원자적인 snapshot은 아닙니다.

## 실행 예제

```sh
OTEL_GO_X_OBSERVABILITY=true go run ./examples/queuediagnostics
```

이 예제는 로컬 합성 receiver만 사용하고 **진단 전용 MeterProvider를 명시적으로 전역에 설치**합니다.
앱 메트릭은 보내지 않으며, 종료 시 이전 provider를 복원합니다. 운영 앱에 이 예제의 전역 설정을
그대로 옮기면 기본 global meter로 생성한 앱 메트릭의 경로도 바뀔 수 있으므로,
기존 metrics provider/reader 구성을 유지한 채 수집 표본을 파싱하는 방식으로 통합하세요.

예상 관측:

| 상태 | Trace 큐 깊이/용량 | Trace 드롭 | Log 큐 깊이/용량 | Log 드롭 |
|---|---|---|---|---|
| 포화 | 2/2 | 3 | 2/2 | 아직 보고되지 않음 |
| 배출 후 | 0/2 | 3 | 0/2 | 3 |

## 값의 의미와 한계

- `Component`는 upstream이 생성한 `batching_span_processor/N` 또는
  `batching_log_processor/N`입니다. 생성 순서로 특정 `SDK` 인스턴스에 귀속시키지 않습니다.
- 깊이는 내보내는 중인 batch를 제외합니다. metric signal에는 동일한 batch queue가 없어
  이 API에서 임의의 metric 큐 깊이를 만들지 않습니다.
- `DepthAvailable`, `CapacityAvailable`, `OverflowCountAvailable`이 false이면 미관측입니다.
  미관측 드롭 0을 무손실 전달의 증거로 사용하지 마세요.
- `OverflowTemporality`는 `cumulative` 또는 `delta`입니다. cumulative는 reader의 누적
  관측 범위이며 프로세스 전체 수명의 완전한 손실 수를 보장하지 않습니다.
  delta를 자동 누적하지 않습니다.
- queue.size/capacity는 upstream observable up/down counter의 cumulative 값이나 gauge만
  해석합니다. delta up/down 값은 현재 깊이로 해석하지 않습니다.
- 로그 queue-full 드롭은 다음 dequeue/flush 때 보고되어 지연될 수 있습니다.
  `OverflowReportingDelayed`가 true이면 포화 상태에서 아직 누락된 드롭이 있을 수 있습니다.
- 일반 export 실패, 보내기 비활성화로 인한 드롭, 큐 초과 드롭은 다른 값입니다.
- 기존 `SDK.Diagnostics()`는 인스턴스 단위이고 이 API는 수집된 processor 메트릭 단위입니다.
  기존 인스턴스 snapshot의 큐 available 플래그는 계속 false입니다.
- 실험적 upstream schema가 바뀌면 이름을 확인해 parser와 실제 queue 테스트를 함께 갱신합니다.
  resource·payload·관련 없는 속성은 출력하지 않습니다.
