$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$resource = Join-Path $root 'internal/desktop/tessaro_desktop_test.syso'
if (Test-Path -LiteralPath $resource) { throw "Resource already exists: $resource" }
try {
    Push-Location $root
    go run github.com/akavel/rsrc@v0.10.2 -manifest cmd/desktop/Tessaro.manifest -o $resource
    if ($LASTEXITCODE -ne 0) { throw 'Resource compilation failed' }
    go test -count=1 ./internal/desktop -run '^TestNative(WindowDefaultValues|DecisionControls|LiveCodeScrollPreserved)$' -args -native-gui
    if ($LASTEXITCODE -ne 0) { throw 'Native desktop constructor test failed' }
} finally {
    if (Test-Path -LiteralPath $resource) { Remove-Item -LiteralPath $resource }
    Pop-Location
}
