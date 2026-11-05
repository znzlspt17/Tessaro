# Windows ROCm 로컬 모델

RX 9070 XT에서 Decider-4B를 Windows 네이티브로 실행하는 경로입니다. 시스템 Python·드라이버를 변경하지 않고 프로젝트의 `.venv-rocm72`에 설치합니다.

```powershell
.\runtime\setup.ps1
.venv-rocm72\Scripts\python.exe -I runtime\download_model.py
.venv-rocm72\Scripts\python.exe -I runtime\gpu_probe.py --model-dir runtime\models\decider-4b --repeats 3
.venv-rocm72\Scripts\python.exe runtime\local_server.py --model-dir runtime\models\decider-4b --port 8000
```

모델 다운로드는 약 8.4GB이며 공개 revision `585473dec4fa93f0325c2bf02dda7273b61f44c1`을 사용합니다. `token=False`로 인증 없이 받으며, 추론 시에는 offline 설정을 적용합니다. 모델 파일과 Python 환경은 Git 대상에서 제외됩니다. 이미 준비된 환경에서는 설치·다운로드를 반복할 필요가 없습니다.

서버는 `127.0.0.1:8000`에만 바인딩합니다. `GET /health`, `POST /v1/systemone`을 제공하며 한 요청의 Choice 질문 하나, 2~255개 후보만 지원합니다. 요청은 1MiB 이하이고 순차 처리합니다. Go CLI의 `local` ranker를 이 주소로 연결합니다. 서버 종료는 실행한 터미널에서 Ctrl+C로 합니다.

## 실행 경로

- ROCm 7.2 Windows 공식 wheel의 PyTorch 2.9.1, CPython 3.12
- `decider-ai==1.10.0`, `transformers==5.22.1`
- `device="cuda:0"`, `dtype=float16`, `use_graphs=False`
- FLA/Triton은 설치하지 않고 Transformers의 PyTorch 참조 구현 사용

`decider-ai`의 일반 설치는 `flash-linear-attention`을 요구합니다. 이 환경은 공식 소스에서 제공하는 eager 경로와 Transformers 참조 구현을 의도적으로 사용하므로 Decider 패키지만 `--no-deps`로 설치합니다. `uv pip check`의 FLA 미설치 지적은 이 경로의 알려진 의존성 차이이며, 일반 CUDA 그래프 서버나 최적화 경로를 검증했다는 의미가 아닙니다. CPU로 자동 전환하지 않습니다.

AMD 공식 지원 조합은 [Windows matrix](https://rocm.docs.amd.com/projects/radeon-ryzen/en/docs-7.2/docs/compatibility/compatibilityrad/windows/windows_compatibility.html)와 [설치 문서](https://rocm.docs.amd.com/projects/radeon-ryzen/en/docs-7.2/docs/install/installrad/windows/install-pytorch.html)를 기준으로 합니다. ROCm 7.1.4 Windows 배포는 확인되지 않아 사용하지 않았습니다. 드라이버 교체가 필요하면 시스템 변경 전에 별도 판단이 필요합니다.

## 2026-11-05 실제 확인

아래 수치는 지역 변수·비교 실행기 확장 이전 구현에서 측정한 기록입니다. 확장된 문법의 후보 수·시간과 직접 비교하지 마세요. 이번 확장 개발에서는 실제 모델 추론을 다시 실행하지 않습니다.

Windows 11 10.0.26300, RX 9070 XT, WMI 드라이버 `32.0.32015.2008`, Python 3.12.12에서 다음을 확인했습니다.

- PyTorch `2.9.1+rocmsdk20260116`, HIP `7.2.26024-f6f897bd3d`
- GPU 인식 및 4×4 float32 행렬곱의 정확한 결과
- Decider-4B 전체 파라미터가 `cuda:0`에 위치
- 정수 덧셈 코드 선택 문제를 3회 수행해 유효한 분포와 `add` 선택 확인
- 해당 짧은 질문에서 측정한 추론 시간은 첫 회 27.72초, 이후 8.42초·7.93초, PyTorch 최대 할당 메모리는 8,719,439,872바이트

이 세 번은 GPU 기능 검증이며 일반적인 성능·정확도 벤치마크가 아닙니다. 긴 코드 후보·많은 후보에서는 지연과 메모리가 달라집니다. HF 모델 카드는 v3로 표기돼 있지만 해당 revision의 런타임 응답 모델명은 `decider-4b-v2.1`이었습니다. 비교 보고서에는 revision과 응답 모델명을 모두 보존해야 합니다.

Jev의 실제 인증 호출은 수행하지 않았습니다. 실제 키는 사용자 관리 실행 환경에서만 취급합니다.

Go 후보 선택 CLI에서 위 GPU 서버에 연결해 예제 2문제를 처리한 결과도 확인했습니다. 두 문제 모두 정답이 1위였고 요청 실패는 없었습니다. 첫 문제 24.64초, 두 번째 9.19초였습니다. 후보와 정답이 수작업으로 작성된 두 문제뿐이므로, 이 결과를 모델의 일반적인 Go 이해 정확도로 해석하지 않습니다.

`examples/add.json`과 별도 최종 평가 파일로 Go→HTTP→GPU→Go AST→컴파일→실행의 전체 경로도 확인했습니다. Greedy와 Beam-3 모두 `return a + b`를 합성하고 공개·최종 테스트를 통과했습니다. 동일한 기본 예산에서 Greedy는 모델 호출 4회·후보 26개·42.28초, Beam-3는 호출 4회·후보 69개·50.82초였습니다. 단일 문제의 순차 실행 결과이며, 워밍업·캐시를 통제한 성능 비교나 Beam의 우수성 근거는 아닙니다.
