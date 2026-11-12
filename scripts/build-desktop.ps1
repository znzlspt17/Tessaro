$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$resource = Join-Path $root 'cmd/desktop/jev_desktop.syso'
if (Test-Path -LiteralPath $resource) { throw "Resource already exists: $resource" }
try {
    Push-Location $root
    go run github.com/akavel/rsrc@v0.10.2 -manifest cmd/desktop/JevCanCode.manifest -o $resource
    if ($LASTEXITCODE -ne 0) { throw 'Resource compilation failed' }
    New-Item -ItemType Directory -Force -Path (Join-Path $root 'dist') | Out-Null
    go build -ldflags '-H=windowsgui' -o (Join-Path $root 'dist/JevCanCode.exe') ./cmd/desktop
    if ($LASTEXITCODE -ne 0) { throw 'Desktop build failed' }
} finally {
    if (Test-Path -LiteralPath $resource) { Remove-Item -LiteralPath $resource }
    Pop-Location
}
