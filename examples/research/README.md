# Tessaro 연구 데이터

기존 토큰 문제 40개를 `dev`, 새 문제 20개를 `eval`로 구분한다. `eval`은 산술·비교, 불리언·분기, 문자열, 슬라이스, 맵·반복 각 4개다. 모두 `values-only-v1`을 사용한다. 이 저장소의 데이터와 heldout은 공개되어 있으므로 비공개 벤치마크가 아니다. 평가 사례로 튜닝하면 독립 평가로 주장할 수 없다.

새 저장소에서 검사나 연구를 시작하기 전에 프로젝트 루트에서 제외된 `dev/`와 `eval/` 데이터를 생성한다. 생성 명령은 기존 데이터와 catalog·비교 manifest도 다시 쓴다.

```powershell
go run ./cmd/preflight -generate
```

`compare-dev-jev.json`, `compare-eval-jev.json`, `compare-all-jev.json`은 Jev 1.14.0, 6개 전략, 세 번의 반복과 `paired-cross-over-v1` 교차 순서를 지정한다. `key_env`에는 환경 변수의 **이름**만 저장한다. 비용이 드는 호출은 별도 예산을 정하고 실행해야 한다. 비교 매니페스트에는 참조 소스 경로가 없다. `catalog.json`과 `*/references/`는 오프라인 데이터 검사 전용이며 모델 요청이나 정책 보상 입력으로 사용하지 않는다.

원본 40개 문제의 필드를 보존하고 반환 정책만 추가했다. 원본의 `exhaustive_public` 5개는 공개 사례가 전수이므로 heldout이 없다. 나머지 35개는 원본 heldout을 복사했다. 신규 20개는 각자 별도의 공개·heldout 사례를 가진다. 원본 참조의 계산 반환은 같은 블록에서 새 지역변수에 계산한 뒤 그 변수를 반환하도록 평가용으로만 변환한다. 카탈로그는 원본 소스와 복사된 spec, heldout, 변형 소스의 SHA-256을 기록한다.

다음 명령은 모델 호출 없이 해시, 원본과 변형의 공개·heldout 실행, 최종 타입 검사, 참조 토큰의 실제 `Next`/`Advance` 선택지 경로, 토큰·지역변수·문장 예산을 검사한다. 전체 검사는 컴파일을 여러 번 수행한다.

```powershell
go test ./internal/researchdata
go run ./cmd/preflight -id arithmetic-add
go run ./cmd/preflight -frontier-only -out frontiers.json
go run ./cmd/preflight -split eval -out preflight-eval.json
go run ./cmd/preflight -out preflight-all.json
```

차단 토큰과 prefix는 해당 **참조 경로**의 결과다. 차단만으로 문제 전체에 해답이 없다고 단정할 수 없다. 예산을 초과한 참조는 실제 제한과 초과 항목을 별도 기록한다. 필요한 경우 실행 결과 확인에만 진단용 완화 제한을 사용하며 `diagnostic_relaxed`로 표시한다. 숫자는 고정 참조 검사 결과이며 Jev 성능 측정치가 아니다.
