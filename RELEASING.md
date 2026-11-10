# 설치·업그레이드와 릴리스

이 프로젝트는 Elastic이 지원하는 공식 SDK가 아닌 비공식 부분 포팅이며, Go 1.26.10 이상이 필요합니다.
현재 GitHub 저장소는 비공개입니다. 접근 가능한 GitHub 계정으로 Git 인증을 먼저 준비하세요.

## 버전 고정 설치

기존 Go 프로젝트에서 실행합니다. 기존 `GOPRIVATE` 목록이 있다면 아래 경로를 추가해 사용하세요.

```sh
GOPRIVATE=github.com/chikilov/edot-go go get github.com/chikilov/edot-go@v0.5.1
```

개인 계정에서 Git 인증을 준비할 때 GitHub CLI를 쓴다면 `gh auth setup-git`을 사용할 수 있습니다.
Go module 다운로드는 private 경로를 public proxy/checksum 서비스로 보내지 않도록 설정합니다.
접근 권한이 없으면 저장소 관리자로부터 권한을 받아야 합니다.

소스 ZIP/tar.gz를 받아 로컬에서 사용하는 경우 프로젝트의 go.mod에 로컬 replace를 설정할 수도 있습니다.

```sh
go mod edit -replace=github.com/chikilov/edot-go=/절대/경로/edot-go-v0.5.1
```

## 0.5.0 → 0.5.1 업그레이드

- 최소 Go 버전이 1.26.10으로 올라갑니다. 이전 도구 체인으로 만든 바이너리는 재빌드해야 합니다.
- HTTP redirect를 따라가지 않습니다. redirect URL 대신 최종 OTLP 수집 URL을 설정하세요.
- ExporterConfig의 JSON 직렬화는 인증정보를 마스킹합니다. 설정 저장용 serializer로 사용하지 마세요.
- 기존 `SDK.Diagnostics()`와 OTLP 프로토콜 동작은 유지합니다.
- 큐 관측은 선택 사항입니다. 사용하지 않으면 실험적 환경변수나 전역 provider 설정이 필요 없습니다.
- 새 `QueueDiagnosticsFromMetrics`를 쓸 때는 [수집 조건과 한계](QUEUE_DIAGNOSTICS.md)를 확인하세요.
- source-only 라이브러리 릴리스이며 설치용 agent 바이너리는 제공하지 않습니다.
- 배포 전에 사용 환경에서 smoke test와 정상 종료를 확인하세요. 이전 태그로 돌아가면 해당 버전에 없는 보안 수정도 함께 사라집니다.
  운영에서는 보안 패치가 포함된 버전으로 재빌드하고 정상 종료·전송 smoke test를 수행하세요.

## 유지관리자 배포 순서

1. `sdk.go` Version과 CHANGELOG를 맞추고 전체 검사·예제를 실행합니다.
2. 소스 commit을 main에 push하고 그 commit의 Linux/macOS CI가 통과했는지 확인합니다.
3. 그 commit에 annotated `vX.Y.Z` tag를 만들고 push합니다. 기존 tag를 이동시키지 않습니다.
4. 태그를 지정해 패키징합니다.

```sh
python3 scripts/package_release.py --ref v0.5.1 --output-dir /안전한/배포/디렉터리
```

패키징은 live 작업 폴더가 아닌 해당 commit의 `git archive`를 사용합니다.
실제 `.env`, untracked IDE 설정·개인 파일은 포함하지 않습니다. 산출물은 source ZIP,
source tar.gz, commit/version을 기록한 release.json, SHA256SUMS입니다.

5. GitHub Release에 위 파일과 검증 요약·알려진 제한을 첨부합니다.
6. 별도 임시 Go module에서 private Git 인증을 이용해 태그 설치와 `edotgo.Version`을 확인합니다.

검증 예:

```sh
shasum -a 256 -c SHA256SUMS
```

Release asset은 공급된 checksum과 함께 확인하세요. SHA256은 파일 변조/손상을 검출하는 데
쓰이며 별도의 서명·provenance attestation을 제공하는 것은 아닙니다.
