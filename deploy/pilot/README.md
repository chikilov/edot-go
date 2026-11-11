# 실제 서비스 파일럿 (대상 저장소 지정 후 실행)

현재 실제 서비스 저장소·스테이징 인스턴스가 아직 선택되지 않았습니다.
이 준비 파일이나 예제의 통과는 실제 서비스 24시간 관측 완료가 아닙니다.
키/권한은 변경하지 않습니다. 운영 서비스 재시작·배포는 자동 수행하지 않습니다.

## 적용 전 필요한 정보

- 서비스 저장소 경로, 서비스명, 실행/테스트 명령, 배포할 스테이징 환경.
- 현재 Go/toolchain과 프레임워크, HTTP/gRPC 계측 및 기존 OTel global provider 사용 여부.
- 서비스의 허용 CPU/RSS/지연 증가, 요청량, 정상 오류율, 샘플링 정책, 롤백 방법.
- Cloud 8.19.16은 현재 서버 보안 공지의 영향 범위에 있습니다. 8.19.24 패치 업그레이드를
  운영 절차로 검토하세요. 이 runbook은 Cloud 버전이나 자격증명을 변경하지 않습니다.

## 서비스 적용 순서

1. 서비스의 변경 전 테스트를 실행하고 동일 부하에서 CPU/RSS/latency 기준값을 수집합니다.
2. Go 1.26.11 이상에서 검증한 SDK 커밋을 고정해 적용합니다.
3. 서비스명/환경 속성을 설정하고 서버·클라이언트 계측, context를 전달한 slog 연결,
   별도 취소되지 않은 context를 쓰는 정상 종료를 붙입니다. 기존 계측과 중복되지 않게 합니다.
4. 기존 provider 사용을 확인한 후 global provider 변경 여부를 결정합니다.
   baggage·헤더·업무 payload에 비밀정보/개인정보를 추가하지 않습니다.
5. 기존 읽기 전용 경로로 trace ID와 로그 연결, 실제 metric 저장을 짧게 검증합니다.
6. 스테이징 인스턴스에서 24시간 이상 관측합니다. 업무 데이터를 생성하는 임의의 부하는 넣지 않습니다.
7. 기준값·서비스 SLO와 비교하고 장애/정상 종료·롤백은 운영자가 지정한 창에서 검증합니다.

## Snapshot 통합 예시

`snapshot.go.example`의 함수를 선택한 서비스 package에 통합합니다. 이 예시는 외부 진단
HTTP endpoint를 만들거나 global provider를 바꾸지 않습니다. 관측 파일은 서비스 계정만
읽는 위치에 `os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)`로 생성하고,
monitor context를 `context.WithTimeout(serviceContext, 24*time.Hour)`로 제한해 30초마다 기록합니다.
별도 goroutine에서 `recordTelemetryPilot`을 실행하고 오류는 원문 payload 없이 로컬에 보고합니다.
관측 종료는 서비스 종료 명령이 아니며 파일 close와 goroutine 종료 순서를 통합해야 합니다.
실제 서비스 경로를 확인한 뒤 실행 가능한 패치로 이 부분을 맞춥니다.

Snapshot은 runtime.ReadMemStats로 얻은 heap, goroutine, SDK 진단 통계를 기록합니다.
실제 앱 관측에 강제 GC를 넣지 않습니다. heap과 OS RSS는 서로 다른 값입니다.

## 24시간 외부 관측 도구

```sh
python3 deploy/pilot/observe.py \
  --pid SERVICE_PID \
  --snapshots /서비스전용경로/sdk-pilot.jsonl \
  --duration-seconds 86400 \
  --interval-seconds 60 \
  --output-dir /새로운전용경로/pilot-observation
```

현재 도구는 동일 OS/PID namespace의 native 서비스 PID를 대상으로 합니다. Docker Desktop 내부
PID를 macOS 호스트의 ps에 넣지 마세요. 컨테이너/Kubernetes 배포는 대상이 확인된 뒤 해당
runtime의 process/container metrics 수집 경로로 맞춰야 합니다. Snapshot PID도 선택한 PID와
일치해야 하며, 대상이 다른 파일을 실수로 결합하지 않습니다.

이 도구는 지정한 PID만 읽으며 프로세스를 실행/종료/재시작하지 않습니다.
PID 재사용, 프로세스 종료, 조기 중단을 구분하고 미완료 관측을 24시간 성공으로 표시하지 않습니다.
SDK 표본이 누락되면 duration_complete와 observation_complete를 구분합니다.
최근의 유효한 SDK snapshot만 허용하며 입력의 원문·관련 없는 필드는 출력하지 않습니다.
`ps %cpu`는 프로세스 실행 시간에 대한 평균이며 구간 CPU나 요청 p99 지연의 대체 값이 아닙니다.
요청량/오류율/p95·p99는 서비스의 기존 APM/부하계측에서 별도로 수집해야 합니다.

기존 `SDK.Diagnostics()`는 queue depth/overflow를 직접 관측하지 못합니다.
`QueueOverflowCountAvailable=false`는 드롭 0이 아닙니다. 실제 앱의 OTel meter 구성을 확인한 뒤
[upstream queue metrics](../../QUEUE_DIAGNOSTICS.md)를 연결하고, 미관측이면 해당 판정을 남깁니다.

## 통과 판단

- 24시간 관측의 실제 경과 시간과 데이터 누락 여부.
- 동등 부하 기준 대비 CPU/RSS/GC 이후 heap·goroutine 추세, 서비스 p95/p99·오류율.
- export 실패/실패 batch item, queue-full 및 sending-disable drop을 구분.
- 인증/TLS 오류, 영구 전송 실패, 비정상 메모리 증가, 업무 오류 증가가 있으면 확대 배포를 보류.
- 서비스 고유 SLO·baseline·queue 관측 없이 단순히 "24시간 PASS"로 판정하지 않습니다.
