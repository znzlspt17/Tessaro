$ErrorActionPreference = 'Stop'
# 2026-11-05: 프로젝트 전용 가상환경만 변경하고 시스템 드라이버는 보존한다.
$projectRoot = Split-Path -Parent $PSScriptRoot
Push-Location -LiteralPath $projectRoot
try {
    if (-not (Test-Path -LiteralPath '.venv-rocm72\Scripts\python.exe')) {
        uv venv --python 3.12 .venv-rocm72
        if ($LASTEXITCODE -ne 0) { throw 'Python environment creation failed' }
    }
    uv pip install --python .venv-rocm72\Scripts\python.exe -r runtime\requirements-rocm72.txt
    if ($LASTEXITCODE -ne 0) { throw 'ROCm dependency installation failed' }
    # FLA/Triton을 요구하는 기본 설치 대신 검증 대상의 eager PyTorch 경로를 명시한다.
    uv pip install --python .venv-rocm72\Scripts\python.exe --no-deps decider-ai==1.10.0
    if ($LASTEXITCODE -ne 0) { throw 'Decider installation failed' }
    .venv-rocm72\Scripts\python.exe -I runtime\gpu_probe.py
    if ($LASTEXITCODE -ne 0) { throw 'ROCm GPU probe failed' }
} finally {
    Pop-Location
}
