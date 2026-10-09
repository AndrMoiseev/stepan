[CmdletBinding()]
param(
    [ValidateRange(60, 3600)]
    [int]$TimeoutSeconds = 900
)

$ErrorActionPreference = "Stop"
$AgentCliName = "nessy"

trap {
    Write-FailureResult "runner_failed"
    exit 1
}

function Write-FailureResult {
    param([string]$FailureClass)
    [ordered]@{
        schema_version = 1
        selected = $false
        passed = $false
        provider = "nessy"
        executable_name = $AgentCliName
        os = "windows"
        arch = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
        assertions = @{}
        tool_inventory = @{
            requested_exact = @()
            behavior_observed = @()
            preflight_present = $false
        }
        forbidden_capabilities = @{ outcomes = @{}; acp_tool_events = 0; canaries_found = 0 }
        native_read = @{
            outcome = "not_observed"
            os_isolation_guaranteed = $false
        }
        failure_class = $FailureClass
        cross_compile_darwin_arm64 = "NOT_RUN"
        cross_compile_is_runtime_acceptance = $false
    } | ConvertTo-Json -Depth 10 -Compress
}

if (-not [Environment]::Is64BitOperatingSystem -or
    [Runtime.InteropServices.RuntimeInformation]::OSArchitecture -ne [Runtime.InteropServices.Architecture]::X64) {
    Write-FailureResult "unsupported_host"
    exit 2
}

$agentCommand = Get-Command -Name $AgentCliName -CommandType Application -ErrorAction SilentlyContinue |
    Select-Object -First 1
if (-not $agentCommand) {
    Write-FailureResult "executable_not_found"
    exit 2
}
$goCommand = Get-Command -Name go -CommandType Application -ErrorAction SilentlyContinue |
    Select-Object -First 1
if (-not $goCommand) {
    Write-FailureResult "go_not_found"
    exit 2
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$runRoot = Join-Path ([IO.Path]::GetTempPath()) ("stepan-nessy-conformance-" + [guid]::NewGuid().ToString("N"))
$resultPath = Join-Path $runRoot "result.json"
$testLog = Join-Path $runRoot "go-test.log"
$crossLog = Join-Path $runRoot "darwin-cross-compile.log"
$crossBinary = Join-Path $runRoot "nessyapp-darwin-arm64.test"
$savedEnvironment = @{}
$names = @("STEPAN_NESSY_REAL_CLI", "STEPAN_NESSY_RESULT", "GOOS", "GOARCH", "CGO_ENABLED")
foreach ($name in $names) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
}

$testExitCode = 1
$crossExitCode = 1
$outputObject = $null
$passed = $false
$json = ""
New-Item -ItemType Directory -Path $runRoot | Out-Null
try {
    [Environment]::SetEnvironmentVariable("STEPAN_NESSY_REAL_CLI", "1", "Process")
    [Environment]::SetEnvironmentVariable("STEPAN_NESSY_RESULT", $resultPath, "Process")

    $ErrorActionPreference = "Continue"
    & $goCommand.Source test -tags nessy_real_cli -run '^TestNessyRealCLIConformance$' `
        ./internal/agentruntime/nessyapp -count=1 -timeout ("{0}s" -f $TimeoutSeconds) *> $testLog
    $testExitCode = $LASTEXITCODE
    $ErrorActionPreference = "Stop"

    [Environment]::SetEnvironmentVariable("STEPAN_NESSY_REAL_CLI", $null, "Process")
    [Environment]::SetEnvironmentVariable("STEPAN_NESSY_RESULT", $null, "Process")
    [Environment]::SetEnvironmentVariable("GOOS", "darwin", "Process")
    [Environment]::SetEnvironmentVariable("GOARCH", "arm64", "Process")
    [Environment]::SetEnvironmentVariable("CGO_ENABLED", "0", "Process")

    $ErrorActionPreference = "Continue"
    & $goCommand.Source test -c -tags nessy_real_cli -o $crossBinary `
        ./internal/agentruntime/nessyapp *> $crossLog
    $crossExitCode = $LASTEXITCODE
    $ErrorActionPreference = "Stop"

    if (Test-Path -LiteralPath $resultPath -PathType Leaf) {
        $outputObject = Get-Content -Raw -LiteralPath $resultPath | ConvertFrom-Json
    }
    else {
        $outputObject = [pscustomobject]@{
            schema_version = 1
            selected = $true
            passed = $false
            provider = "nessy"
            executable_name = $AgentCliName
            os = "windows"
            arch = "amd64"
            assertions = @{}
            tool_inventory = [pscustomobject]@{ requested_exact = @(); behavior_observed = @(); preflight_present = $false }
            forbidden_capabilities = [pscustomobject]@{ outcomes = [pscustomobject]@{}; acp_tool_events = 0; canaries_found = 0 }
            native_read = [pscustomobject]@{ outcome = "not_observed"; os_isolation_guaranteed = $false }
            failure_class = "test_harness_failed"
        }
    }
    $outputObject | Add-Member -NotePropertyName test_exit_code -NotePropertyValue $testExitCode -Force
    $outputObject | Add-Member -NotePropertyName native_windows_runtime_executed -NotePropertyValue $true -Force
    $outputObject | Add-Member -NotePropertyName cross_compile_darwin_arm64 `
        -NotePropertyValue $(if ($crossExitCode -eq 0) { "PASS" } else { "FAIL" }) -Force
    $outputObject | Add-Member -NotePropertyName cross_compile_is_runtime_acceptance -NotePropertyValue $false -Force

    $passed = $testExitCode -eq 0 -and $crossExitCode -eq 0 -and $outputObject.passed -eq $true
    $outputObject.passed = $passed
    $json = $outputObject | ConvertTo-Json -Depth 10 -Compress
}
finally {
    foreach ($name in $names) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], "Process")
    }

    if (Test-Path -LiteralPath $runRoot -PathType Container) {
        $resolvedRunRoot = (Resolve-Path -LiteralPath $runRoot).Path
        $resolvedTempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
        if (-not $resolvedRunRoot.StartsWith($resolvedTempRoot, [StringComparison]::OrdinalIgnoreCase)) {
            throw "refusing to clean integration data outside the temporary directory"
        }
        Remove-Item -LiteralPath $resolvedRunRoot -Recurse -Force
    }
}

$json
if ($passed) { exit 0 }
exit 1
