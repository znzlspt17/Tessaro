param(
    [Parameter(Mandatory = $true)][string]$ManifestPath,
    [Parameter(Mandatory = $true)][string]$OutputDir,
    [Parameter(Mandatory = $true)][ValidateRange(1, 2147483647)][int]$MaxQuestions,
    [ValidateRange(1, 1000000)][int]$MaxCalls = 64,
    [ValidateRange(1, 1000000)][int]$MaxCandidates = 8192,
    [ValidateRange(1, 1000000)][int]$MaxTokens = 256,
    [ValidateRange(1, 2147483647)][int]$TimeoutSeconds = 300,
    [ValidateRange(1, 256)][int]$DecisionBatchSize = 1,
    [switch]$Ablation,
    [switch]$AllowBudgetOverride,
    [switch]$PreflightOnly,
    [switch]$Execute
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$manifest = (Resolve-Path -LiteralPath $ManifestPath).Path
$data = Get-Content -LiteralPath $manifest -Raw -Encoding UTF8 | ConvertFrom-Json
$expected = @('strict-greedy', 'beam', 'lds', 'levin', 'phs', 'mcts')
$strategies = @($data.strategies)
if ($data.repeats -ne 3 -or $data.ordering -ne 'paired-cross-over-v1' -or @($strategies | Select-Object -Unique).Count -ne $strategies.Count) { throw 'Manifest requires three repeats, paired-cross-over-v1, and unique strategies.' }
if ($Ablation) {
    if ($DecisionBatchSize -le 1 -or $strategies.Count -eq 0 -or @($strategies | Where-Object { $_ -notin @('lds', 'levin', 'phs') }).Count -gt 0) { throw 'Batch ablation requires only lds, levin, or phs and a batch size greater than one.' }
} elseif ($DecisionBatchSize -ne 1 -or $strategies.Count -ne 6 -or @($expected | Where-Object { $_ -notin $strategies }).Count -ne 0) {
    throw 'Baseline requires all six research strategies and batch size one.'
}
$catalog = Get-Content -LiteralPath (Join-Path $root 'examples/research/catalog.json') -Raw -Encoding UTF8 | ConvertFrom-Json
$records = @($catalog.records)
$problems = @($data.problems)
$split = switch ($problems.Count) { 40 { 'dev' } 20 { 'eval' } 60 { 'all' } default { throw 'Manifest must contain exactly a complete dev, eval, or all split.' } }
$selected = @($records | Where-Object { $split -eq 'all' -or $_.split -eq $split })
if ($selected.Count -ne $problems.Count -or @($problems.id | Select-Object -Unique).Count -ne $problems.Count) { throw 'Manifest problem set is invalid.' }
$overrides = [System.Collections.Generic.List[object]]::new()
foreach ($problem in $problems) {
    $record = @($selected | Where-Object id -eq $problem.id)
    if ($record.Count -ne 1) { throw 'Manifest problem is not in the selected catalog split.' }
    $base = Split-Path -Parent $manifest
    foreach ($field in @('spec', 'heldout')) {
        $actual = $problem.$field
        $expectedPath = $record[0].$field
        if ([string]::IsNullOrEmpty($actual) -and [string]::IsNullOrEmpty($expectedPath)) { continue }
        if ([string]::IsNullOrEmpty($actual) -or [string]::IsNullOrEmpty($expectedPath) -or [IO.Path]::GetFullPath((Join-Path $base $actual)) -ne [IO.Path]::GetFullPath((Join-Path (Join-Path $root 'examples/research') $expectedPath))) { throw "Manifest $field path differs from catalog." }
    }
    $specData = Get-Content -LiteralPath (Join-Path $base $problem.spec) -Raw -Encoding UTF8 | ConvertFrom-Json
    foreach ($pair in @(@('max_calls', $MaxCalls), @('max_candidates', $MaxCandidates), @('max_tokens', $MaxTokens), @('timeout_seconds', $TimeoutSeconds))) {
        $limit = $specData.budgets.($pair[0])
        if ($null -ne $limit -and $limit -gt 0 -and $pair[1] -gt $limit) { $overrides.Add([pscustomobject]@{ problem = $problem.id; budget = $pair[0]; original = $limit; override = $pair[1] }) }
    }
}
if ($overrides.Count -gt 0 -and -not $AllowBudgetOverride) { throw 'Requested budgets exceed a problem limit; inspect the manifest and explicitly use -AllowBudgetOverride for that experiment.' }
$out = [IO.Path]::GetFullPath($OutputDir)
$hash = (Get-FileHash -LiteralPath $manifest -Algorithm SHA256).Hash.ToLowerInvariant()
$argsList = @('run', '--kind', 'compare', '--manifest', $manifest, '--output', $out,
    '--max-questions', "$MaxQuestions", '--max-calls', "$MaxCalls", '--max-candidates', "$MaxCandidates",
    '--max-tokens', "$MaxTokens", '--timeout', "${TimeoutSeconds}s", '--return-policy', 'values-only-v1', '--json', '--progress')
if ($DecisionBatchSize -gt 1) { $argsList += @('--decision-batch-size', "$DecisionBatchSize") }
[pscustomobject]@{ mode = $(if ($Execute) { 'execute' } elseif ($PreflightOnly) { 'preflight' } else { 'dry-run' }); manifest = $manifest; manifest_sha256 = $hash; output = $out; split = $split; ordering = $data.ordering; strategies = $strategies; repeats = 3; decision_batch_size = $DecisionBatchSize; ablation = [bool]$Ablation; max_questions_global = $MaxQuestions; budget_overrides = @($overrides.ToArray()); command = @('go', 'run', './cmd/headless') + $argsList } | ConvertTo-Json -Depth 6 | Write-Host
if (-not $Execute -and -not $PreflightOnly) { exit 0 }
Push-Location $root
try {
    $preflight = & go run ./cmd/preflight -split $split
    if ($LASTEXITCODE -ne 0) { throw 'Preflight command failed.' }
    $parsedChecks = ($preflight -join "`n") | ConvertFrom-Json
    $checks = @($parsedChecks)
    if ($checks.Count -ne $selected.Count -or @($checks.id | Select-Object -Unique).Count -ne $selected.Count) { throw 'Preflight result count is invalid.' }
    foreach ($record in $selected) {
        $item = @($checks | Where-Object id -eq $record.id)
        if ($item.Count -ne 1) { throw 'Preflight result is missing or duplicated.' }
        $c = $item[0]
        if (-not $c.hashes_ok -or -not $c.frontier.reachable -or $c.validate_error -or @($c.excess | Where-Object { $_ }).Count -gt 0 -or $null -eq $c.public -or $c.public.status -ne 'completed' -or $c.public.total -le 0 -or $c.public.passed -ne $c.public.total) { throw "Preflight failed: $($record.id)" }
        if ($record.heldout) {
            if ($c.heldout_state -ne 'present' -or $null -eq $c.heldout -or $c.heldout.status -ne 'completed' -or $c.heldout.total -le 0 -or $c.heldout.passed -ne $c.heldout.total) { throw "Heldout preflight failed: $($record.id)" }
        } elseif ($c.heldout_state -ne 'exhaustive_public' -or $null -ne $c.heldout) { throw "Heldout state invalid: $($record.id)" }
    }
    if ($PreflightOnly) { Write-Host 'Preflight passed.'; exit 0 }
    & go run ./cmd/headless @argsList
    exit $LASTEXITCODE
} finally {
    Pop-Location
}
