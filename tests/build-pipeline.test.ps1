#requires -Version 5.1
[CmdletBinding()]
param([switch]$ProbeActiveProcess)

$ErrorActionPreference = 'Stop'
$workspace = Split-Path -Parent $PSScriptRoot
$scopeLabel = 'orchestration-fixtures-only'
$encoding = New-Object Text.UTF8Encoding($false)
$cases = New-Object 'System.Collections.Generic.List[object]'

# Keep all fixtures, input logs and evidence under one exclusive cache run.
$runParent = $workspace
foreach ($segment in @('.cache', 'tests', 'build-pipeline-unit')) {
    $runParent = Join-Path $runParent $segment
    if (-not (Test-Path -LiteralPath $runParent)) {
        New-Item -ItemType Directory -Path $runParent | Out-Null
    }
    $item = Get-Item -LiteralPath $runParent -Force
    if (-not $item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw 'Fixture evidence requires an ordinary cache directory.'
    }
}
$runId = [datetime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ') + '-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$run = Join-Path $runParent $runId
New-Item -ItemType Directory -Path $run | Out-Null
$fixtures = Join-Path $run '.fixture'
$logs = Join-Path $run 'helper-logs'
New-Item -ItemType Directory -Path $fixtures, $logs | Out-Null

. (Join-Path $workspace 'tools/build-metrics.ps1')

function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}
function Assert-Throws([scriptblock]$Action, [string]$Pattern) {
    $failure = $null
    try { & $Action | Out-Null } catch { $failure = $_.Exception.Message }
    Assert-True ($failure -and $failure -match $Pattern) "Expected error matching '$Pattern'; observed '$failure'."
}
function Invoke-TestCase([string]$Name, [scriptblock]$Action) {
    try {
        & $Action
        $cases.Add([pscustomobject]@{ name = $Name; status = 'passed' })
        Write-Output ('PASS ' + $Name)
    } catch {
        $cases.Add([pscustomobject]@{ name = $Name; status = 'failed'; error = $_.Exception.Message })
        Write-Output ('FAIL {0} ({1})' -f $Name, $_.Exception.Message)
    }
}
function Write-Sample([string]$Name, [string[]]$Lines) {
    $path = Join-Path $logs $Name
    [IO.File]::WriteAllLines($path, $Lines, $encoding)
    $path
}
function Assert-Timings($Metrics, [string]$Status, [string[]]$Stages) {
    Assert-True ($Metrics.schemaVersion -eq 1 -and $Metrics.status -ceq $Status) "Unexpected timing status; expected '$Status'."
    Assert-True ($Metrics.startedAt -and $Metrics.completedAt -and $Metrics.durationMs -gt 0) 'Overall timing is incomplete.'
    Assert-True ([DateTimeOffset]$Metrics.completedAt -ge [DateTimeOffset]$Metrics.startedAt) 'Overall timing has reversed timestamps.'
    $actual = @($Metrics.stages | ForEach-Object { $_.name + '=' + $_.status })
    Assert-True (($actual -join ',') -ceq ($Stages -join ',')) ('Unexpected stages. Expected {0}, got {1}.' -f ($Stages -join ', '), ($actual -join ', '))
    foreach ($stage in $Metrics.stages) {
        Assert-True ($stage.startedAt -and $stage.completedAt -and $null -ne $stage.durationMs -and $stage.durationMs -ge 0) "Timing is incomplete for '$($stage.name)'."
        Assert-True ([DateTimeOffset]$stage.completedAt -ge [DateTimeOffset]$stage.startedAt) "Timing has reversed timestamps for '$($stage.name)'."
    }
}

Invoke-TestCase 'Timing helpers record successful and failed stages and totals' {
    foreach ($resultStatus in @('passed', 'failed')) {
        $metrics = New-BuildMetrics
        $clock = [Diagnostics.Stopwatch]::StartNew()
        $stage = Start-BuildStage $metrics 'test-only-stage'
        Assert-True ($metrics.status -ceq 'running' -and $stage.record.status -ceq 'running' -and $stage.clock.IsRunning) 'A new timing must be running.'
        Start-Sleep -Milliseconds 15
        Stop-BuildStage $stage -Status $resultStatus
        Complete-BuildMetrics $metrics $clock $resultStatus
        Assert-Timings $metrics $resultStatus @('test-only-stage=' + $resultStatus)
        Assert-True ($stage.record.durationMs -gt 0 -and -not $stage.clock.IsRunning -and -not $clock.IsRunning) 'Stopwatches did not record and stop.'
        $completedAt = $stage.record.completedAt
        Stop-BuildStage $stage -Status passed
        Assert-True ($stage.record.status -ceq $resultStatus -and $stage.record.completedAt -eq $completedAt) 'A second stop changed a completed stage.'
    }
}

$goBody = (@'
{"Action":"start","Package":"mihomolite/cmd/mihomolite"}
{"Action":"run","Package":"mihomolite/cmd/mihomolite","Test":"TestParent"}
{"Action":"run","Package":"mihomolite/cmd/mihomolite","Test":"TestParent/Child"}
{"Action":"run","Package":"mihomolite/cmd/mihomolite","Test":"TestParent/Child/Grandchild"}
{"Action":"pass","Package":"mihomolite/cmd/mihomolite","Test":"TestParent/Child/Grandchild","Elapsed":0.01}
{"Action":"pass","Package":"mihomolite/cmd/mihomolite","Test":"TestParent/Child","Elapsed":0.01}
{"Action":"run","Package":"mihomolite/cmd/mihomolite","Test":"TestParent/OptIn"}
{"Action":"skip","Package":"mihomolite/cmd/mihomolite","Test":"TestParent/OptIn","Elapsed":0}
{"Action":"pass","Package":"mihomolite/cmd/mihomolite","Test":"TestParent","Elapsed":0.02}
{"Action":"run","Package":"mihomolite/cmd/mihomolite","Test":"TestSolo"}
{"Action":"output","Package":"mihomolite/cmd/mihomolite","Test":"TestSolo","Output":"ok  \tmihomolite/cmd/mihomolite\t(cached)\n"}
{"Action":"pass","Package":"mihomolite/cmd/mihomolite","Test":"TestSolo","Elapsed":0}
{"Action":"run","Package":"mihomolite/cmd/mihomolite","Test":"TestOptIn"}
{"Action":"skip","Package":"mihomolite/cmd/mihomolite","Test":"TestOptIn","Elapsed":0}
'@) -split '\r?\n'
$goPassed = '{"Action":"pass","Package":"mihomolite/cmd/mihomolite","Elapsed":0.4}'
$goError = 'Go regression'
Invoke-TestCase 'Go JSON counts top-level passes, nested passes and top-level skips once' {
    $sample = Write-Sample 'go-passed.jsonl' ($goBody + @(
        '{"Action":"output","Package":"mihomolite/cmd/mihomolite","Output":"ok  \tmihomolite/cmd/mihomolite\t0.400s\n"}', $goPassed))
    $result = Get-GoRegressionSummary $sample
    Assert-True ($result.packagePassed -and $result.failures -eq 0 -and $result.elapsedSeconds -eq 0.4) 'Incorrect Go package result.'
    Assert-True ($result.topLevelPassed -eq 2 -and $result.subtestsPassed -eq 2) 'Incorrect Go pass breakdown.'
    Assert-True ($result.topLevelSkipped -eq 1 -and (@($result.skipped) -join ',') -ceq 'TestOptIn') 'A subtest skip affected the top-level count or list.'
    Assert-True (-not $result.cached) 'A regular test message containing (cached) was treated as a package cache hit.'
}
Invoke-TestCase 'Go JSON distinguishes an actual package cache hit' {
    $sample = Write-Sample 'go-cached.jsonl' ($goBody + @(
        '{"Action":"output","Package":"mihomolite/cmd/mihomolite","Output":"ok  \tmihomolite/cmd/mihomolite\t(cached)\n"}',
        '{"Action":"pass","Package":"mihomolite/cmd/mihomolite","Elapsed":0}'))
    $result = Get-GoRegressionSummary $sample
    Assert-True ($result.cached -and $result.packagePassed -and $result.elapsedSeconds -eq 0) 'Go cached package result was not detected.'
    Assert-True ($result.topLevelPassed -eq 2 -and $result.subtestsPassed -eq 2 -and $result.topLevelSkipped -eq 1) 'Cache status changed the Go totals.'
}
Invoke-TestCase 'Go JSON rejects a failing package or test' {
    $packageFail = Write-Sample 'go-package-failed.jsonl' ($goBody + '{"Action":"fail","Package":"mihomolite/cmd/mihomolite","Elapsed":0.4}')
    $testFail = Write-Sample 'go-test-failed.jsonl' ($goBody + @(
        '{"Action":"fail","Package":"mihomolite/cmd/mihomolite","Test":"TestBroken","Elapsed":0}', $goPassed))
    Assert-Throws { Get-GoRegressionSummary $packageFail } $goError
    Assert-Throws { Get-GoRegressionSummary $testFail } $goError
}
Invoke-TestCase 'Go JSON rejects malformed and incomplete logs' {
    $badJson = Write-Sample 'go-invalid.jsonl' ($goBody + @('{"Action":', $goPassed))
    $missingPackage = Write-Sample 'go-no-package-result.jsonl' $goBody
    $missingElapsed = Write-Sample 'go-no-elapsed.jsonl' ($goBody + '{"Action":"pass","Package":"mihomolite/cmd/mihomolite"}')
    Assert-Throws { Get-GoRegressionSummary $badJson } $goError
    Assert-Throws { Get-GoRegressionSummary $missingPackage } $goError
    Assert-Throws { Get-GoRegressionSummary $missingElapsed } 'Go package pass.*elapsed'
}

$nodeTap = @(
    'TAP version 13', 'ok 1 - first fixture', 'ok 2 - second fixture', 'ok 3 - opt-in fixture # SKIP',
    '1..3', '# tests 3', '# suites 0', '# pass 2', '# fail 0', '# cancelled 0',
    '# skipped 1', '# todo 0', '# duration_ms 19.625'
)
Invoke-TestCase 'Node TAP records exact totals, passes, failures, skips and duration' {
    $sample = Write-Sample 'node-passed.tap' $nodeTap
    $result = Get-NodeRegressionSummary $sample
    Assert-True ($result.tests -eq 3 -and $result.passed -eq 2 -and $result.failed -eq 0 -and $result.skipped -eq 1 -and $result.durationMs -eq 19.625) 'Incorrect Node TAP summary.'
    Assert-True ($result.cancelled -eq 0 -and $result.todo -eq 0) 'Incorrect Node cancelled or TODO totals.'
}
Invoke-TestCase 'Node TAP rejects a failing summary' {
    $sample = Write-Sample 'node-failed.tap' @(
        'TAP version 13', 'ok 1 - first fixture', 'not ok 2 - second fixture', '1..2',
        '# tests 2', '# suites 0', '# pass 1', '# fail 1', '# cancelled 0', '# skipped 0', '# todo 0', '# duration_ms 8.25')
    Assert-Throws { Get-NodeRegressionSummary $sample } 'Node regression'
}
Invoke-TestCase 'Node TAP rejects missing and inconsistent summary fields' {
    $truncated = Write-Sample 'node-truncated.tap' @('TAP version 13', '# tests 3', '# fail 0')
    $contradictory = Write-Sample 'node-contradictory.tap' ($nodeTap -replace '^# pass 2$', '# pass 1')
    $duplicate = Write-Sample 'node-duplicate.tap' ($nodeTap + '# pass 2')
    $fraction = Write-Sample 'node-fractional.tap' ($nodeTap -replace '^# pass 2$', '# pass 1.5')
    Assert-Throws { Get-NodeRegressionSummary $truncated } 'summary is incomplete'
    Assert-Throws { Get-NodeRegressionSummary $contradictory } 'consistent passing totals'
    Assert-Throws { Get-NodeRegressionSummary $duplicate } 'duplicate totals'
    Assert-Throws { Get-NodeRegressionSummary $fraction } 'whole numbers'
}
Invoke-TestCase 'Node TAP accounts for TODO separately and rejects cancelled tests' {
    $todo = Write-Sample 'node-todo.tap' ($nodeTap -replace '^ok 3 .+$', 'not ok 3 - pending fixture # TODO' -replace '^# skipped 1$', '# skipped 0' -replace '^# todo 0$', '# todo 1')
    $result = Get-NodeRegressionSummary $todo
    Assert-True ($result.tests -eq 3 -and $result.passed -eq 2 -and $result.todo -eq 1 -and $result.skipped -eq 0) 'A TODO affected the Node PASS or SKIP count.'
    $cancelled = Write-Sample 'node-cancelled.tap' ($nodeTap -replace '^ok 3 .+$', 'not ok 3 - cancelled fixture' -replace '^# skipped 1$', '# skipped 0' -replace '^# cancelled 0$', '# cancelled 1')
    Assert-Throws { Get-NodeRegressionSummary $cancelled } 'consistent passing totals'
}

# The copied production scripts resolve every child relative to this fixture.
# Each test-only child also checks its fixture marker before recording actions.
function Get-FixtureSettings([string]$Fixture) {
    $physicalPath = [IO.Path]::GetFullPath($Fixture)
    if ((Split-Path -Parent $physicalPath) -ine $fixtures) { throw 'Test-only stub was called outside its fixture.' }
    $settings = [IO.File]::ReadAllText((Join-Path $physicalPath 'fixture.json')) | ConvertFrom-Json
    if ($settings.scope -cne 'orchestration-fixtures-only') { throw 'Missing test-only fixture marker.' }
    $settings
}
function Add-FixtureCall([string]$Fixture, [string]$Name, [hashtable]$Flags = @{}) {
    $null = Get-FixtureSettings $Fixture
    $call = [ordered]@{ scope = 'orchestration-fixtures-only'; name = $Name; flags = $Flags }
    [IO.File]::AppendAllText((Join-Path $Fixture 'calls.jsonl'), (($call | ConvertTo-Json -Compress) + [Environment]::NewLine), $encoding)
}
function New-PipelineFixture([string]$Name, [string]$Failure = '', [switch]$ChangeProtectedFile) {
    $fixture = Join-Path $fixtures $Name
    New-Item -ItemType Directory -Path $fixture | Out-Null
    foreach ($directory in @('tools', 'data', 'runtime', 'logs', 'release')) {
        New-Item -ItemType Directory -Path (Join-Path $fixture $directory) | Out-Null
    }
    $settings = [ordered]@{ scope = $scopeLabel; failure = $Failure; changeProtectedFile = [bool]$ChangeProtectedFile }
    [IO.File]::WriteAllText((Join-Path $fixture 'fixture.json'), ($settings | ConvertTo-Json), $encoding)
    foreach ($relative in @('data/test-only.txt', 'runtime/mihomo.exe', 'logs/test-only.log', 'launcher.json', 'MihomoLite.exe', 'CURRENT.json', 'release/current.json')) {
        [IO.File]::WriteAllText((Join-Path $fixture $relative), 'test-only fixture A', $encoding)
    }
    foreach ($relative in @('tools/build-release.ps1', 'tools/build-metrics.ps1')) {
        Copy-Item -LiteralPath (Join-Path $workspace $relative) -Destination (Join-Path $fixture $relative)
    }
    $buildStub = @'
# Test-only build stub. Scope orchestration-fixtures-only.
[CmdletBinding()]
param([switch]$Promote, [switch]$PassThru, [switch]$SkipTests, [string]$NodePath)
$settings = Get-FixtureSettings $PSScriptRoot
Add-FixtureCall $PSScriptRoot 'build' @{ promote = [bool]$Promote; passThru = [bool]$PassThru; skipTests = [bool]$SkipTests; nodePath = $NodePath }
if ($settings.failure -ceq 'build') { throw 'test-only build failure' }
if ($settings.changeProtectedFile) {
    [IO.File]::WriteAllText((Join-Path $PSScriptRoot 'data/test-only.txt'), 'test-only fixture B')
}
if ($PassThru) {
    $fixtureTestStatus = if ($settings.failure -ceq 'regressions') { 'failed' } else { 'passed' }
    [pscustomobject]@{
        scope = 'orchestration-fixtures-only'
        provenance = 'test-only'
        sourceVerified = $false
        promoted = [bool]$Promote
        tests = @{ scope = 'orchestration-fixtures-only'; status = $fixtureTestStatus }
    }
}
'@
    $geoStub = @'
# Test-only Geo resolver stub. Scope orchestration-fixtures-only.
function Resolve-ReleaseGeoData {
    [CmdletBinding()]
    param([string]$WorkspaceRoot, [string]$GeoDataRoot)
    $settings = Get-FixtureSettings $WorkspaceRoot
    Add-FixtureCall $WorkspaceRoot 'geo' @{ requestedRoot = $GeoDataRoot }
    if ($settings.failure -ceq 'geo') { throw 'test-only Geo failure' }
    $selectedRoot = if ($GeoDataRoot) { $GeoDataRoot } else { '.cache/downloads/test-only-resolved' }
    [pscustomobject]@{ scope = 'orchestration-fixtures-only'; root = $selectedRoot; source = 'test-only'; records = @() }
}
'@
    $packageStub = @'
# Test-only package stub. Scope orchestration-fixtures-only.
[CmdletBinding()]
param([string]$GeoDataRoot, [switch]$PassThru)
$fixture = Split-Path -Parent $PSScriptRoot
$settings = Get-FixtureSettings $fixture
Add-FixtureCall $fixture 'package' @{ geoDataRoot = $GeoDataRoot; passThru = [bool]$PassThru }
if ($settings.failure -ceq 'package') { throw 'test-only package failure' }
$artifact = 'release/test-only-package.txt'
[IO.File]::WriteAllText((Join-Path $fixture $artifact), 'orchestration-fixtures-only')
if ($PassThru) { [pscustomobject]@{ scope = 'orchestration-fixtures-only'; artifact = $artifact } }
'@
    $checkStub = @'
# Test-only checker stub. Scope orchestration-fixtures-only.
[CmdletBinding()]
param([switch]$RequireGeoData, [switch]$VerifyPayload)
$fixture = Split-Path -Parent $PSScriptRoot
$settings = Get-FixtureSettings $fixture
Add-FixtureCall $fixture 'check' @{ requireGeoData = [bool]$RequireGeoData; verifyPayload = [bool]$VerifyPayload }
if ($settings.failure -ceq 'check') {
    Write-Output 'orchestration-fixtures-only checker failure'
    exit 23
}
if (-not (Test-Path -LiteralPath (Join-Path $fixture 'release/test-only-package.txt') -PathType Leaf)) { exit 24 }
Write-Output 'orchestration-fixtures-only checker passed'
exit 0
'@
    $stubs = @{
        'build.ps1' = $buildStub
        'tools/release-geodata.ps1' = $geoStub
        'tools/package.ps1' = $packageStub
        'tools/check-workspace.ps1' = $checkStub
    }
    foreach ($relative in $stubs.Keys) {
        [IO.File]::WriteAllText((Join-Path $fixture $relative), $stubs[$relative], $encoding)
    }
    $fixture
}
function Invoke-FixturePipeline {
    param(
        [string]$Fixture,
        [hashtable]$Flags = @{},
        [ValidateSet('empty', 'running-core', 'running-launcher', 'real')][string]$ProcessMode = 'empty',
        [string]$ExpectedError = '',
        [switch]$NoReport
    )
    $null = Get-FixtureSettings $Fixture
    if ($ProcessMode -ne 'real') {
        function Get-CimInstance {
            [CmdletBinding()]
            param([string]$ClassName)
            if ($ClassName -cne 'Win32_Process') { throw 'Unexpected CIM query in test-only fixture.' }
            if ($ProcessMode -ceq 'running-core') {
                [pscustomobject]@{ Name = 'mihomo.exe'; ProcessId = -1; ExecutablePath = (Join-Path $Fixture 'runtime/mihomo.exe') }
            } elseif ($ProcessMode -ceq 'running-launcher') {
                [pscustomobject]@{ Name = 'MihomoLite.exe'; ProcessId = -1; ExecutablePath = (Join-Path $Fixture 'MihomoLite.exe') }
            }
        }
    }
    function Stop-FixtureNetworkCall {
        Add-FixtureCall $Fixture 'unexpected-network'
        throw 'Network calls are forbidden in the test-only fixture.'
    }
    function Invoke-WebRequest { Stop-FixtureNetworkCall }
    function Invoke-RestMethod { Stop-FixtureNetworkCall }
    function Start-BitsTransfer { Stop-FixtureNetworkCall }
    function curl.exe { Stop-FixtureNetworkCall }
    function wget.exe { Stop-FixtureNetworkCall }

    $result = $null
    $failure = $null
    $priorExitCode = Get-Variable LASTEXITCODE -Scope Global -ErrorAction SilentlyContinue
    $savedExitCode = if ($priorExitCode) { $priorExitCode.Value }
    Push-Location -LiteralPath $Fixture
    try {
        try { $result = & (Join-Path $Fixture 'tools/build-release.ps1') @Flags }
        catch { $failure = $_.Exception.Message }
    } finally {
        Pop-Location
        if ($priorExitCode) { $global:LASTEXITCODE = $savedExitCode }
        else { Remove-Variable LASTEXITCODE -Scope Global -ErrorAction SilentlyContinue }
    }
    if ($ExpectedError) {
        Assert-True ($failure -and $failure -match $ExpectedError) "Expected pipeline error matching '$ExpectedError'; observed '$failure'."
    } else {
        Assert-True (-not $failure) "Unexpected pipeline error '$failure'."
        Assert-True (@($result).Count -eq 1 -and $result.status -ceq 'passed') 'Pipeline success did not return a single structured result.'
    }

    $tracePath = Join-Path $Fixture 'calls.jsonl'
    $trace = if (Test-Path -LiteralPath $tracePath) {
        [IO.File]::ReadLines($tracePath) | ForEach-Object { $_ | ConvertFrom-Json }
    }
    foreach ($call in @($trace | Where-Object { $null -ne $_ })) {
        Assert-True ($call.scope -ceq $scopeLabel) 'A child call lacks the test-only scope.'
    }
    $evidenceRoot = Join-Path $Fixture '.cache/tests/build-pipeline'
    $reports = @()
    if (Test-Path -LiteralPath $evidenceRoot -PathType Container) {
        $reports = @(Get-ChildItem -LiteralPath $evidenceRoot -Filter 'pipeline.json' -File -Recurse)
    }
    $report = $null
    $reportDirectory = $null
    if ($NoReport) {
        Assert-True ($reports.Count -eq 0) 'The invalid flag combination produced a pipeline run.'
    } else {
        Assert-True ($reports.Count -eq 1) 'Expected exactly one pipeline report.'
        $reportDirectory = $reports[0].DirectoryName
        $report = [IO.File]::ReadAllText($reports[0].FullName) | ConvertFrom-Json
        if ($ExpectedError) {
            Assert-True ($report.status -ceq 'failed' -and $report.error -eq $failure) 'Failure was not preserved in the pipeline report.'
        } else {
            Assert-True ($report.status -ceq 'passed' -and $report.mode -ceq $result.mode) 'Pipeline return value and report disagree.'
            Assert-True ([IO.Path]::GetFullPath((Join-Path $Fixture $result.report)) -ieq $reports[0].FullName) 'Pipeline returned the wrong report path.'
        }
    }
    [pscustomobject]@{ report = $report; trace = @($trace | Where-Object { $null -ne $_ }); reportDirectory = $reportDirectory }
}
function Assert-Calls($Outcome, [string[]]$Expected) {
    $actual = @($Outcome.trace | ForEach-Object { $_.name })
    Assert-True (($actual -join ',') -ceq ($Expected -join ',')) ('Expected calls [{0}], observed [{1}].' -f ($Expected -join ', '), ($actual -join ', '))
}

Invoke-TestCase 'Default pipeline invokes one build and leaves publication protected' {
    $fixture = New-PipelineFixture 'default'
    $nodePath = Join-Path $fixture 'node-test-only.exe'
    $outcome = Invoke-FixturePipeline $fixture -Flags @{ NodePath = $nodePath }
    Assert-Calls $outcome @('build')
    $flags = $outcome.trace[0].flags
    Assert-True ($flags.passThru -and -not $flags.promote -and -not $flags.skipTests -and $flags.nodePath -eq $nodePath) 'Default build received incorrect arguments.'
    $report = $outcome.report
    Assert-True ($report.mode -ceq 'build' -and $null -eq $report.geo -and $null -eq $report.package -and $null -eq $report.verification) 'Default mode did more than build and test.'
    Assert-True ($report.build.scope -ceq $scopeLabel -and $report.build.sourceVerified -eq $false) 'Build fixture claimed source verification.'
    Assert-True ($report.protected.checked -eq 7 -and $report.protected.changed.Count -eq 0) 'Default mode did not include publication in its protected snapshot.'
    Assert-Timings $report.timings 'passed' @('preflight=passed', 'build-and-test=passed', 'protected-files=passed')
}
Invoke-TestCase 'Publish invokes one promoted build, one package and one strict checker' {
    $fixture = New-PipelineFixture 'publish'
    $requestedGeo = '.cache/downloads/test-only-explicit'
    $outcome = Invoke-FixturePipeline $fixture -Flags @{ Publish = $true; GeoDataRoot = $requestedGeo }
    Assert-Calls $outcome @('geo', 'build', 'package', 'check')
    Assert-True ($outcome.trace[0].flags.requestedRoot -ceq $requestedGeo) 'Publish did not forward the Geo selection.'
    Assert-True ($outcome.trace[1].flags.promote -and $outcome.trace[1].flags.passThru -and -not $outcome.trace[1].flags.skipTests) 'Publish did not require a tested promotion.'
    Assert-True ($outcome.trace[2].flags.passThru -and $outcome.trace[2].flags.geoDataRoot -ceq $requestedGeo) 'Package did not receive the verified Geo fixture root.'
    Assert-True ($outcome.trace[3].flags.requireGeoData -and $outcome.trace[3].flags.verifyPayload) 'Checker did not receive both strict flags.'
    $report = $outcome.report
    Assert-True ($report.mode -ceq 'publish' -and $report.build.promoted -and $report.build.scope -ceq $scopeLabel -and -not $report.build.sourceVerified) 'Publish report has incorrect test-only build evidence.'
    Assert-True ($report.package.scope -ceq $scopeLabel -and $report.geo.source -ceq 'test-only' -and -not $report.geo.downloaded) 'Publish report has incorrect test-only package or Geo evidence.'
    Assert-True ($report.verification.status -ceq 'passed' -and $report.protected.checked -eq 4) 'Publish report lacks strict verification or protection evidence.'
    $checkLog = Join-Path $outcome.reportDirectory 'workspace-check.log'
    Assert-True ([IO.File]::ReadAllText($checkLog) -ceq 'orchestration-fixtures-only checker passed') 'Strict checker output was not retained.'
    Assert-Timings $report.timings 'passed' @('preflight=passed', 'local-geodata=passed', 'build-and-test=passed', 'package=passed', 'verify-publication=passed', 'protected-files=passed')
}
Invoke-TestCase 'PackageOnly resolves local Geo and packages without a build' {
    $fixture = New-PipelineFixture 'package-only'
    $outcome = Invoke-FixturePipeline $fixture -Flags @{ PackageOnly = $true }
    Assert-Calls $outcome @('geo', 'package', 'check')
    Assert-True ($outcome.report.mode -ceq 'package' -and $null -eq $outcome.report.build) 'PackageOnly recorded a build.'
    Assert-True ($outcome.trace[1].flags.geoDataRoot -ceq '.cache/downloads/test-only-resolved') 'PackageOnly did not pass through the resolved local Geo root.'
    Assert-Timings $outcome.report.timings 'passed' @('preflight=passed', 'local-geodata=passed', 'package=passed', 'verify-publication=passed', 'protected-files=passed')
}
Invoke-TestCase 'Geo failure blocks promotion and records a failed stage' {
    $fixture = New-PipelineFixture 'geo-fail' -Failure geo
    $outcome = Invoke-FixturePipeline $fixture -Flags @{ Publish = $true } -ExpectedError 'test-only Geo failure'
    Assert-Calls $outcome @('geo')
    Assert-Timings $outcome.report.timings 'failed' @('preflight=passed', 'local-geodata=failed')
}
Invoke-TestCase 'Build failure blocks package and checker and records a failed stage' {
    $fixture = New-PipelineFixture 'build-fail' -Failure build
    $outcome = Invoke-FixturePipeline $fixture -Flags @{ Publish = $true } -ExpectedError 'test-only build failure'
    Assert-Calls $outcome @('geo', 'build')
    Assert-Timings $outcome.report.timings 'failed' @('preflight=passed', 'local-geodata=passed', 'build-and-test=failed')
}
Invoke-TestCase 'A returned failed regression result also blocks publication' {
    $fixture = New-PipelineFixture 'regression-fail' -Failure regressions
    $outcome = Invoke-FixturePipeline $fixture -Flags @{ Publish = $true } -ExpectedError 'requires passing default regressions'
    Assert-Calls $outcome @('geo', 'build')
    Assert-Timings $outcome.report.timings 'failed' @('preflight=passed', 'local-geodata=passed', 'build-and-test=failed')
}
Invoke-TestCase 'Package failure blocks checker and records a failed stage' {
    $fixture = New-PipelineFixture 'package-fail' -Failure package
    $outcome = Invoke-FixturePipeline $fixture -Flags @{ Publish = $true } -ExpectedError 'test-only package failure'
    Assert-Calls $outcome @('geo', 'build', 'package')
    Assert-Timings $outcome.report.timings 'failed' @('preflight=passed', 'local-geodata=passed', 'build-and-test=passed', 'package=failed')
}
Invoke-TestCase 'Checker nonzero exit is logged and fails publication verification' {
    $fixture = New-PipelineFixture 'check-fail' -Failure check
    $outcome = Invoke-FixturePipeline $fixture -Flags @{ PackageOnly = $true } -ExpectedError 'publication verification failed'
    Assert-Calls $outcome @('geo', 'package', 'check')
    $checkLog = Join-Path $outcome.reportDirectory 'workspace-check.log'
    Assert-True ([IO.File]::ReadAllText($checkLog) -ceq 'orchestration-fixtures-only checker failure') 'Failing checker output was not retained.'
    Assert-Timings $outcome.report.timings 'failed' @('preflight=passed', 'local-geodata=passed', 'package=passed', 'verify-publication=failed')
}
Invoke-TestCase 'Same-size changes to a protected fixture file are detected' {
    $fixture = New-PipelineFixture 'protected-change' -ChangeProtectedFile
    $outcome = Invoke-FixturePipeline $fixture -ExpectedError 'Protected files changed'
    Assert-Calls $outcome @('build')
    Assert-True ((@($outcome.report.protected.changed) -join ',') -ceq 'data/test-only.txt') 'Protected file change was not identified precisely.'
    Assert-True ($outcome.report.protected.added.Count -eq 0 -and $outcome.report.protected.removed.Count -eq 0) 'A content change was misclassified.'
    Assert-Timings $outcome.report.timings 'failed' @('preflight=passed', 'build-and-test=passed', 'protected-files=failed')
}
Invoke-TestCase 'Publish refuses a running core or current launcher before any child' {
    foreach ($mode in @('running-core', 'running-launcher')) {
        $fixture = New-PipelineFixture $mode
        $outcome = Invoke-FixturePipeline $fixture -Flags @{ Publish = $true } -ProcessMode $mode -ExpectedError 'Publish refused: .*running'
        Assert-Calls $outcome @()
        Assert-Timings $outcome.report.timings 'failed' @('preflight=failed')
    }
}
if ($ProbeActiveProcess) {
    Invoke-TestCase 'Unmocked CIM refuses the actual running core from the fixture' {
        $fixture = New-PipelineFixture 'real-core-guard'
        $outcome = Invoke-FixturePipeline $fixture -Flags @{ Publish = $true } -ProcessMode real -ExpectedError 'Publish refused: mihomo is running'
        Assert-Calls $outcome @()
        Assert-Timings $outcome.report.timings 'failed' @('preflight=failed')
    }
}
Invoke-TestCase 'Incompatible and unsupported flags are rejected before any work' {
    $fixture = New-PipelineFixture 'bad-mode-combination'
    $outcome = Invoke-FixturePipeline $fixture -Flags @{ Publish = $true; PackageOnly = $true } -ExpectedError 'cannot be combined' -NoReport
    Assert-Calls $outcome @()
    $fixture = New-PipelineFixture 'unsupported-skip-tests'
    $outcome = Invoke-FixturePipeline $fixture -Flags @{ SkipTests = $true } -ExpectedError 'SkipTests' -NoReport
    Assert-Calls $outcome @()
}

Invoke-TestCase 'Promotion guard refuses an existing exact-hash package before replacement' {
    $fixture = New-PipelineFixture 'promotion-package-guard'
    $tokens = $null; $parseErrors = $null
    $ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $workspace 'build.ps1'), [ref]$tokens, [ref]$parseErrors)
    Assert-True ($parseErrors.Count -eq 0) 'Build script has parse errors.'
    $names = @('Get-WorkspacePath', 'Assert-PromotionPackageAvailable')
    $definitions = @($ast.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -in $names}, $true))
    Assert-True ($definitions.Count -eq $names.Count) 'Could not isolate the promotion guard and its path helper.'
    foreach ($definition in $definitions) { Invoke-Expression $definition.Extent.Text }

    $savedWorkspace = $script:Workspace
    try {
        $script:Workspace = $fixture
        $version = '0.0.0'
        $hash = 'a' * 64
        $otherPackage = Join-Path $fixture 'release/MihomoLite-v0.0.0-bbbbbbbbbbbb-win-x64.zip'
        [IO.File]::WriteAllText($otherPackage, 'test-only different hash')
        Assert-PromotionPackageAvailable -Version $version -BinaryHash $hash
        $exactPackage = Join-Path $fixture 'release/MihomoLite-v0.0.0-aaaaaaaaaaaa-win-x64.zip'
        [IO.File]::WriteAllText($exactPackage, 'test-only exact hash')
        Assert-Throws { Assert-PromotionPackageAvailable -Version $version -BinaryHash $hash } 'Promotion refused: .*package already exists'
        Assert-True ([IO.File]::ReadAllText($exactPackage) -ceq 'test-only exact hash') 'Guard changed the existing package.'
    } finally {
        $script:Workspace = $savedWorkspace
    }
}

$failed = @($cases | Where-Object { $_.status -ceq 'failed' }).Count
$summary = [ordered]@{
    schemaVersion = 1
    scope = $scopeLabel
    powershell = $PSVersionTable.PSVersion.ToString()
    edition = $PSVersionTable.PSEdition
    realProcessProbe = [bool]$ProbeActiveProcess
    passed = $cases.Count - $failed
    failed = $failed
    cases = @($cases.ToArray())
}
$summaryPath = Join-Path $run 'test-results.json'
[IO.File]::WriteAllText($summaryPath, ($summary | ConvertTo-Json -Depth 8) + [Environment]::NewLine, $encoding)
Write-Output ('{0} | PowerShell {1} | {2} passed, {3} failed | {4}' -f $scopeLabel, $summary.powershell, $summary.passed, $summary.failed, $summaryPath)
if ($failed) { throw 'Build pipeline fixture tests failed. See test-results.json.' }
