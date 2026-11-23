#requires -Version 5.1
[CmdletBinding()]
param(
    [switch]$Publish,
    [switch]$PackageOnly,
    [string]$NodePath,
    [string]$GeoDataRoot
)
$ErrorActionPreference = 'Stop'
$script:PipelineRoot = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot)).TrimEnd('\')
function Get-PipelinePath {
    param([string]$Relative)
    if ([IO.Path]::IsPathRooted($Relative) -or @($Relative.Replace('\','/').Split('/') | Where-Object { $_ -in @('', '.', '..') }).Count) { throw 'Pipeline path must be a safe workspace-relative path.' }
    $path = [IO.Path]::GetFullPath((Join-Path $script:PipelineRoot $Relative))
    if (-not $path.StartsWith($script:PipelineRoot + '\', [StringComparison]::OrdinalIgnoreCase)) { throw 'Pipeline path escapes workspace.' }
    $cursor = $path
    while ($cursor) {
        if ((Test-Path -LiteralPath $cursor) -and ((Get-Item -LiteralPath $cursor -Force).Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Pipeline path traverses a reparse point.' }
        if ($cursor -eq $script:PipelineRoot) { break }
        $cursor = [IO.Path]::GetDirectoryName($cursor)
    }
    $path
}
function Assert-PipelineIdle {
    $rootBinary = Get-PipelinePath 'MihomoLite.exe'
    foreach ($process in @(Get-CimInstance -ClassName Win32_Process -ErrorAction Stop)) {
        if ($process.Name -ieq 'mihomo.exe') { throw 'Publish refused: mihomo is running; exit the application before publishing.' }
        if ($process.Name -ieq 'MihomoLite.exe' -and (-not $process.ExecutablePath -or [IO.Path]::GetFullPath([string]$process.ExecutablePath) -ieq $rootBinary)) { throw 'Publish refused: the current launcher is running or its path cannot be resolved.' }
    }
}
function Get-PipelineProtectedState {
    param([switch]$IncludePublication)
    $records = @{}; $unreadable = @()
    $targets = @('data','runtime','logs','launcher.json')
    if ($IncludePublication) { $targets += @('MihomoLite.exe','CURRENT.json','release') }
    foreach ($relative in $targets) {
        $path = Get-PipelinePath $relative
        if (-not (Test-Path -LiteralPath $path)) { continue }
        $pending = New-Object 'System.Collections.Generic.Stack[string]'
        $pending.Push($path)
        while ($pending.Count) {
            $candidate = $pending.Pop()
            $safe = Get-PipelinePath ($candidate.Substring($script:PipelineRoot.Length + 1))
            $item = Get-Item -LiteralPath $safe -Force
            if ($item.PSIsContainer) {
                foreach ($child in Get-ChildItem -LiteralPath $safe -Force) { $pending.Push($child.FullName) }
            } else {
                $key = $safe.Substring($script:PipelineRoot.Length + 1).Replace('\','/')
                try { $records[$key] = [ordered]@{size=[long]$item.Length;sha256=(Get-FileHash -LiteralPath $safe -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant()} }
                catch [IO.IOException] { $unreadable += $key }
            }
        }
    }
    [pscustomobject]@{files=$records;unreadable=$unreadable}
}
function Compare-PipelineProtectedState {
    param($Before, $After)
    $changed = @(); $added = @(); $removed = @()
    foreach ($key in $Before.files.Keys) {
        if (-not $After.files.ContainsKey($key)) { $removed += $key }
        elseif ($Before.files[$key].sha256 -cne $After.files[$key].sha256 -or $Before.files[$key].size -ne $After.files[$key].size) { $changed += $key }
    }
    foreach ($key in $After.files.Keys) { if (-not $Before.files.ContainsKey($key)) { $added += $key } }
    [ordered]@{checked=$Before.files.Count;changed=$changed;added=$added;removed=$removed;unreadableBefore=$Before.unreadable;unreadableAfter=$After.unreadable}
}
function Write-PipelineReport {
    param([string]$Path, $Report)
    $relative = $Path.Substring($script:PipelineRoot.Length + 1)
    $safe = Get-PipelinePath $relative
    $temporary = Get-PipelinePath ($relative + '.tmp-' + [guid]::NewGuid().ToString('N'))
    [IO.File]::WriteAllText($temporary, ($Report | ConvertTo-Json -Depth 30) + [Environment]::NewLine, (New-Object Text.UTF8Encoding($false)))
    if (Test-Path -LiteralPath $safe) { [IO.File]::Replace($temporary, $safe, [Management.Automation.Language.NullString]::Value) } else { [IO.File]::Move($temporary, $safe) }
}
if ($Publish -and $PackageOnly) { throw '-Publish and -PackageOnly cannot be combined.' }
. (Get-PipelinePath 'tools/build-metrics.ps1')
$metrics = New-BuildMetrics; $clock = [Diagnostics.Stopwatch]::StartNew(); $active = $null
$runRelative = '.cache/tests/build-pipeline/' + [datetime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ') + '-' + [guid]::NewGuid().ToString('N').Substring(0,8)
$run = Get-PipelinePath $runRelative
if (Test-Path -LiteralPath $run) { throw 'Pipeline evidence directory already exists.' }
New-Item -ItemType Directory -Path $run -Force | Out-Null
$reportPath = Join-Path $run 'pipeline.json'
$report = [ordered]@{schemaVersion=1;mode=$(if($Publish){'publish'}elseif($PackageOnly){'package'}else{'build'});status='running';timings=$metrics;geo=$null;build=$null;package=$null;verification=$null;protected=$null;error=$null}
try {
    $active = Start-BuildStage $metrics 'preflight'
    if ($Publish) { Assert-PipelineIdle }
    $protectPublication = -not ($Publish -or $PackageOnly)
    $before = Get-PipelineProtectedState -IncludePublication:$protectPublication
    Stop-BuildStage $active; $active = $null
    if ($Publish -or $PackageOnly) {
        $active = Start-BuildStage $metrics 'local-geodata'
        . (Get-PipelinePath 'tools/release-geodata.ps1')
        $geo = Resolve-ReleaseGeoData -WorkspaceRoot $script:PipelineRoot -GeoDataRoot $GeoDataRoot
        $report.geo = [ordered]@{root=$geo.root;source=$geo.source;downloaded=$false}
        Write-Host ('Reusing verified local Geo data: ' + $geo.root)
        Stop-BuildStage $active; $active = $null
    }
    if (-not $PackageOnly) {
        $active = Start-BuildStage $metrics 'build-and-test'
        $arguments = @{PassThru=$true}
        if ($Publish) { $arguments.Promote = $true }
        if ($NodePath) { $arguments.NodePath = $NodePath }
        $result = & (Get-PipelinePath 'build.ps1') @arguments
        if (-not $result -or $result.tests.status -ne 'passed') { throw 'Build pipeline requires passing default regressions.' }
        if ($Publish -and -not $result.promoted) { throw 'Publish requires the controlled build promotion.' }
        $report.build = $result
        Stop-BuildStage $active; $active = $null
    }
    if ($Publish -or $PackageOnly) {
        $active = Start-BuildStage $metrics 'package'
        $report.package = & (Get-PipelinePath 'tools/package.ps1') -GeoDataRoot $geo.root -PassThru
        if (-not $report.package) { throw 'Packaging did not return a result.' }
        Stop-BuildStage $active
        $active = Start-BuildStage $metrics 'verify-publication'
        $output = @(& (Get-PipelinePath 'tools/check-workspace.ps1') -RequireGeoData -VerifyPayload 2>&1)
        $exitCode = $LASTEXITCODE
        $output | ForEach-Object { Write-Host $_ }
        [IO.File]::WriteAllText((Join-Path $run 'workspace-check.log'), ($output -join [Environment]::NewLine), (New-Object Text.UTF8Encoding($false)))
        if ($exitCode -ne 0) { throw 'Consolidated publication verification failed; see workspace-check.log.' }
        $report.verification = [ordered]@{status='passed';requireGeoData=$true;verifyPayload=$true;log=($runRelative + '/workspace-check.log')}
        Stop-BuildStage $active; $active = $null
    }
    $active = Start-BuildStage $metrics 'protected-files'
    $after = Get-PipelineProtectedState -IncludePublication:$protectPublication
    $report.protected = Compare-PipelineProtectedState $before $after
    if ($report.protected.changed.Count -or $report.protected.added.Count -or $report.protected.removed.Count) { throw 'Protected files changed during the pipeline; review pipeline.json before continuing.' }
    Stop-BuildStage $active; $active = $null
    $report.status = 'passed'
    Complete-BuildMetrics $metrics $clock 'passed'
    Write-PipelineReport $reportPath $report
    Write-Host ('Pipeline finished in {0:N2}s; report: {1}' -f ($metrics.durationMs / 1000), $reportPath)
    [pscustomobject]@{report=($runRelative + '/pipeline.json');status='passed';mode=$report.mode;durationMs=$metrics.durationMs;build=$report.build;package=$report.package}
} catch {
    Stop-BuildStage $active -Status failed
    Complete-BuildMetrics $metrics $clock 'failed'
    $report.status = 'failed'; $report.error = $_.Exception.Message
    Write-PipelineReport $reportPath $report
    Write-Host ('Pipeline failed; report: ' + $reportPath)
    throw
}
