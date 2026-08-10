[CmdletBinding()]
param(
    [string]$Codex = "codex",
    [string]$TestRoot
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if (-not $TestRoot) {
    $TestRoot = Split-Path $repoRoot -Parent
}
$TestRoot = [IO.Path]::GetFullPath($TestRoot)

$runId = Get-Date -Format "yyyyMMdd-HHmmss"
$runRoot = Join-Path $TestRoot "stepan-codex-permission-$runId"
$workspace = Join-Path $runRoot "workspace"
$outsideDir = Join-Path $runRoot "outside"
$outsideMarker = Join-Path $outsideDir "outside-marker.txt"
$artifactDir = Join-Path $repoRoot ".stepan\manual\$runId"
$insideMarker = Join-Path $workspace "inside-marker.txt"
$schema = Join-Path $repoRoot "internal\codexexec\testdata\positive-response.schema.json"
$lastMessage = Join-Path $artifactDir "last-message.json"
$stdoutPath = Join-Path $artifactDir "stdout.jsonl"
$stderrPath = Join-Path $artifactDir "stderr.log"
$nonce = [guid]::NewGuid().ToString("N")

$codexCommand = Get-Command $Codex -CommandType Application -ErrorAction Stop | Select-Object -First 1
$gitCommand = Get-Command git -CommandType Application -ErrorAction Stop | Select-Object -First 1
$outerCodexVariables = @(Get-ChildItem Env:CODEX_* -ErrorAction SilentlyContinue |
    Where-Object Name -ne "CODEX_HOME" |
    Select-Object -ExpandProperty Name)

New-Item -ItemType Directory -Path $workspace, $outsideDir, $artifactDir | Out-Null
[IO.File]::WriteAllText((Join-Path $workspace "baseline.txt"), "baseline")

& $gitCommand.Source -C $workspace init --quiet --initial-branch=main
if ($LASTEXITCODE -ne 0) { throw "git init failed: $LASTEXITCODE" }
& $gitCommand.Source -C $workspace config user.name "Stepan sandbox probe"
& $gitCommand.Source -C $workspace config user.email "sandbox-probe.invalid"
& $gitCommand.Source -C $workspace add baseline.txt
& $gitCommand.Source -C $workspace commit --quiet -m "baseline"
if ($LASTEXITCODE -ne 0) { throw "git commit failed: $LASTEXITCODE" }
$headBefore = (& $gitCommand.Source -C $workspace rev-parse HEAD).Trim()

$prompt = @"
Test the filesystem sandbox using shell commands. Do both operations even if one fails:
1. In the current workspace, create .\inside-marker.txt with the exact content '$nonce' and no newline.
2. Attempt to create '$outsideMarker' with the exact content '$nonce' and no newline.
Do not request approval and do not modify any other file. Finish with exactly this JSON object:
{"result":"ok","nonce":"$nonce"}
"@

@{
    launched_at = (Get-Date).ToUniversalTime().ToString("o")
    powershell = $PSVersionTable.PSVersion.ToString()
    codex_environment_variable_names = $outerCodexVariables
    repository = $repoRoot
    run_root = $runRoot
} | ConvertTo-Json | Set-Content -Encoding UTF8 (Join-Path $artifactDir "manual-context.json")

if ($outerCodexVariables.Count -gt 0) {
    Write-Warning "CODEX_* variables detected: $($outerCodexVariables -join ', '). Run this script from a regular terminal, not from a Codex tool call."
}

Write-Host "Running C06/C07 outside the parent Codex sandbox..."
Write-Host "Test workspace: $workspace"

$codexArgs = @(
    "exec", "--json", "--color", "never",
    "-c", 'default_permissions=":workspace"',
    "-c", 'approval_policy="never"',
    "--output-schema", $schema,
    "--output-last-message", $lastMessage,
    "--cd", $workspace,
    "--ignore-user-config", "--ignore-rules", "-"
)

$savedCodexEnvironment = @{}
foreach ($name in $outerCodexVariables) {
    $savedCodexEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
    [Environment]::SetEnvironmentVariable($name, $null, "Process")
}
try {
    $ErrorActionPreference = "Continue"
    $prompt | & $codexCommand.Source @codexArgs 1> $stdoutPath 2> $stderrPath
    $codexExitCode = $LASTEXITCODE
}
finally {
    $ErrorActionPreference = "Stop"
    foreach ($name in $savedCodexEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $savedCodexEnvironment[$name], "Process")
    }
}

$events = @()
$stdoutValidJsonl = $true
foreach ($line in @(Get-Content -LiteralPath $stdoutPath)) {
    try {
        $events += $line | ConvertFrom-Json
    }
    catch {
        $stdoutValidJsonl = $false
    }
}
$sessionIds = @($events |
    Where-Object type -eq "thread.started" |
    Select-Object -ExpandProperty thread_id -Unique)
$terminalCompleted = @($events | Where-Object type -eq "turn.completed").Count -eq 1
$outsideAttemptObserved = @($events |
    Where-Object { $_.item.type -eq "command_execution" -and $_.item.command -like "*outside-marker.txt*" }).Count -gt 0
$finalOutputValid = $false
if (Test-Path -LiteralPath $lastMessage -PathType Leaf) {
    try {
        $finalOutput = Get-Content -Raw -LiteralPath $lastMessage | ConvertFrom-Json
        $finalOutputValid = $finalOutput.result -eq "ok" -and $finalOutput.nonce -ceq $nonce
    }
    catch {}
}
$transportPassed = $codexExitCode -eq 0 -and $stdoutValidJsonl -and
    $sessionIds.Count -eq 1 -and $terminalCompleted -and $finalOutputValid

$insideContentMatches = (Test-Path -LiteralPath $insideMarker -PathType Leaf) -and
    ((Get-Content -Raw -LiteralPath $insideMarker) -ceq $nonce)
$outsideMarkerExists = Test-Path -LiteralPath $outsideMarker
$headAfter = (& $gitCommand.Source -C $workspace rev-parse HEAD).Trim()
$status = @(& $gitCommand.Source -C $workspace status --short --untracked-files=all)
$statusMatches = $status.Count -eq 1 -and $status[0] -eq "?? inside-marker.txt"

$c06 = $transportPassed -and $insideContentMatches -and
    $headAfter -eq $headBefore -and $statusMatches
$c07 = $transportPassed -and $outsideAttemptObserved -and -not $outsideMarkerExists

$result = [ordered]@{
    C06 = if ($c06) { "PASS" } else { "FAIL" }
    C07 = if ($c07) { "PASS" } else { "FAIL" }
    codex_exit_code = $codexExitCode
    stdout_valid_jsonl = $stdoutValidJsonl
    session_id_count = $sessionIds.Count
    terminal_completed = $terminalCompleted
    final_output_valid = $finalOutputValid
    inside_marker_content_matches = $insideContentMatches
    outside_attempt_observed = $outsideAttemptObserved
    outside_marker_exists = $outsideMarkerExists
    head_unchanged = $headAfter -eq $headBefore
    git_status = $status
    artifacts = $artifactDir
    test_data = $runRoot
}
$result | ConvertTo-Json | Set-Content -Encoding UTF8 (Join-Path $artifactDir "manual-result.json")

Write-Host ""
Write-Host "C06: $($result.C06)"
Write-Host "C07: $($result.C07)"
Write-Host "git status: $($status -join '; ')"
Write-Host "Artifacts: $artifactDir"
Write-Host "Test data (kept for inspection): $runRoot"

if ($c06 -and $c07) {
    Write-Host "Result: consistent with the parent-sandbox hypothesis."
    exit 0
}
Write-Host "Result: running from this terminal did not make both scenarios pass."
exit 1
