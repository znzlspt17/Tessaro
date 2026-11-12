# 토큰 합성 문제 40개

`specs/`는 자연어 의도·입력 이름·타입/리터럴 허용 목록·공개 사례를 담습니다. 함수 이름·서명·정답 코드는 포함하지 않습니다. `heldout/`은 제출 후보를 고정한 뒤에만 읽습니다. `references.json`의 참조 프로그램은 개발 테스트와 가설 검증의 평가 계층에서만 읽습니다. 전체 함수 생성에는 전달하지 않으며, 다음 토큰 평가에는 현재 앞부분만 전달합니다.

기존 산술·비교·불리언·분기·계산·스코프 24개에 문자열·슬라이스·map·반복문 각 4개를 추가했습니다. map 순회 순서에 답이 의존하는 문제는 없습니다. Boolean-only 5문제는 모든 입력을 공개한 `exhaustive_public` 평가이며 별도 heldout을 두지 않습니다. 그 밖의 문제는 `sampled_holdout`입니다. 예제의 heldout은 저장소에 공개되므로 실제 미공개 일반화 평가를 대신하지 않습니다.

참조와 동등한 입출력을 만드는 해는 지역 변수·반복문 사용 여부와 무관하게 인정합니다. `scope-*` 통과만으로 지역 변수 생성 능력이 검증됐다고 해석하지 마세요. 지역 선언과 제어 흐름은 결정적인 통합 테스트로 별도 확인합니다. 참조의 지역 변수 이름은 도달성 테스트에서 엔진의 `v0`, `v1` 이름으로 바꿔 검사합니다.

```powershell
go test ./internal/tokenverify -run TestTokenDatasetReferences -count=1
go test ./internal/tokensynth -run TestDatasetReferenceFrontiers -count=1
# 실제 모델 비교는 Tessaro의 비교 탭에서 compare-live.json을 엽니다.
```

설정 파일 경로는 설정이 있는 디렉터리를 기준으로 해석합니다.

| 파일 | 내용 |
|---|---|
| `compare-smoke.json` | fake, 2문제 × 3전략 |
| `compare.json` | fake, 40문제 × 3전략 |
| `compare-jev.json` | Jev, 40문제 × 3전략, 사용자가 실행 |
| `compare-live.json` | Jev·로컬 Decider, 40문제 × 3전략, 사용자가 실행 |
| `hypothesis.json` | Jev 1.14.0의 도달성·다음 토큰 판단·전체 생성 평가, 사용자가 실행 |

fake는 고정 순위기로 정확도를 측정하는 모델이 아닙니다. 테스트 전용 scripted ranker는 지정된 정답 경로를 선택하여 토큰 조립과 검증을 확인하지만 제품 실행에는 포함되지 않습니다. 실제 모델 성능·요청 비용·GPU 속도는 이번 개발 검증에서 측정하지 않습니다. 기존 AST 설정·과거 결과는 별도 비교 기준으로 유지합니다.
