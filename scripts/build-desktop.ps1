$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$resource = Join-Path $root 'cmd/desktop/tessaro_desktop.syso'
if (Test-Path -LiteralPath $resource) { throw "Resource already exists: $resource" }
try {
    Push-Location $root
    go run github.com/akavel/rsrc@v0.10.2 -manifest cmd/desktop/Tessaro.manifest -o $resource
    if ($LASTEXITCODE -ne 0) { throw 'Resource compilation failed' }
    New-Item -ItemType Directory -Force -Path (Join-Path $root 'dist') | Out-Null
    go build -ldflags '-H=windowsgui' -o (Join-Path $root 'dist/Tessaro.exe') ./cmd/desktop
    if ($LASTEXITCODE -ne 0) { throw 'Desktop build failed' }
} finally {
    if (Test-Path -LiteralPath $resource) { Remove-Item -LiteralPath $resource }
    Pop-Location
}
