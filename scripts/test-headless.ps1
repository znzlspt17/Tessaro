param(
    [string]$PythonPath = 'python',
    [switch]$Native,
    [switch]$Race
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$steps = [System.Collections.Generic.List[object]]::new()
$oldSmoke = [Environment]::GetEnvironmentVariable('TESSARO_GPU_SMOKE', 'Process')

function Invoke-Check([string]$Name, [scriptblock]$Action) {
    $timer = [Diagnostics.Stopwatch]::StartNew()
    $code = 1
    try {
        & $Action | Out-Host
        $code = $LASTEXITCODE
        if ($null -eq $code) { $code = 0 }
    } catch {
        [Console]::Error.WriteLine("${Name}: execution failed")
    } finally {
        $timer.Stop()
    }
    $status = if ($code -eq 0) { 'passed' } else { 'failed' }
    $steps.Add([pscustomobject]@{ step = $Name; status = $status; duration_ms = $timer.ElapsedMilliseconds; exit_code = $code })
}

try {
    [Environment]::SetEnvironmentVariable('TESSARO_GPU_SMOKE', '0', 'Process')
    Push-Location $root
    Invoke-Check 'go-test' { & go test ./... }
    Invoke-Check 'python-unittest' { & $PythonPath -m unittest discover -s runtime -p test_local_worker.py }
    if ($Native) {
        Invoke-Check 'native-controls' { & (Join-Path $PSScriptRoot 'test-desktop.ps1'); $global:LASTEXITCODE = 0 }
    } else {
        $steps.Add([pscustomobject]@{ step = 'native-controls'; status = 'skipped'; duration_ms = 0; exit_code = $null })
    }
    if ($Race) {
        $cgo = & go env CGO_ENABLED
        $cgoCode = $LASTEXITCODE
        $cc = & go env CC
        $ccCode = $LASTEXITCODE
        if ($cgoCode -ne 0 -or $ccCode -ne 0) {
            $steps.Add([pscustomobject]@{ step = 'go-race'; status = 'failed'; duration_ms = 0; exit_code = 1; reason = 'go env failed' })
        } elseif ($cgo -ne '1') {
            $steps.Add([pscustomobject]@{ step = 'go-race'; status = 'skipped'; duration_ms = 0; exit_code = $null; reason = 'CGO_ENABLED is not 1' })
        } elseif ([string]::IsNullOrWhiteSpace($cc) -or -not (Get-Command ($cc -split '\s+')[0] -ErrorAction SilentlyContinue)) {
            $steps.Add([pscustomobject]@{ step = 'go-race'; status = 'skipped'; duration_ms = 0; exit_code = $null; reason = 'configured C compiler not found' })
        } else {
            Invoke-Check 'go-race' { & go test -race ./... }
        }
    }
} finally {
    Pop-Location
    [Environment]::SetEnvironmentVariable('TESSARO_GPU_SMOKE', $oldSmoke, 'Process')
}
$failed = @($steps | Where-Object status -eq 'failed').Count -gt 0
[pscustomobject]@{ status = $(if ($failed) { 'failed' } else { 'passed' }); steps = @($steps.ToArray()) } | ConvertTo-Json -Depth 5
if ($failed) { exit 1 }
exit 0
