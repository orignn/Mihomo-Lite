#requires -Version 5.1
[CmdletBinding()]
param(
    [switch]$SkipTests,
    [switch]$Promote,
    [string]$NodePath,
    [switch]$PassThru
)
$ErrorActionPreference = 'Stop'

function Get-WorkspacePath {
    param([string]$Relative)
    $path = [IO.Path]::GetFullPath((Join-Path $script:Workspace $Relative))
    $prefix = $script:Workspace.TrimEnd('\') + '\'
    if ($path -ne $script:Workspace -and -not $path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) { throw "Path escapes workspace: $Relative" }
    $cursor = $path
    while ($cursor) {
        if (Test-Path -LiteralPath $cursor) {
            $item = Get-Item -LiteralPath $cursor -Force
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Reparse point is not allowed: $cursor" }
        }
        if ($cursor -eq $script:Workspace) { break }
        $cursor = [IO.Path]::GetDirectoryName($cursor)
    }
    return $path
}
function New-WorkspaceDirectory {
    param([string]$Relative, [switch]$Exclusive)
    $path = Get-WorkspacePath $Relative
    if ($Exclusive -and (Test-Path -LiteralPath $path)) { throw "Output already exists: $path" }
    if (-not (Test-Path -LiteralPath $path)) { New-Item -ItemType Directory -Path $path -Force | Out-Null }
    if (-not (Get-Item -LiteralPath $path).PSIsContainer) { throw "Not a directory: $path" }
    return $path
}
function Get-FileRecord {
    param([string]$Path, [string]$Relative)
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.PSIsContainer) { throw "Expected a regular file: $Path" }
    return [ordered]@{ path=$Relative.Replace('\','/'); sha256=(Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant(); size=[long]$item.Length }
}
function Get-SafeFiles {
    param([string]$Directory)
    foreach ($item in (Get-ChildItem -LiteralPath $Directory -Force | Sort-Object Name)) {
        $relative = $item.FullName.Substring($script:Workspace.Length + 1)
        $safe = Get-WorkspacePath $relative
        if ($item.PSIsContainer) { Get-SafeFiles $safe } else { $safe }
    }
}
function Get-SourceManifest {
    $paths = @('go.mod','go.sum','build.ps1','tools/package.ps1','tools/build-metrics.ps1','tools/build-release.ps1','tools/release-geodata.ps1','tools/prepare-geodata.ps1','tools/check-workspace.ps1','tests/build-pipeline.test.ps1','tests/package-pipeline.test.ps1','tests/release-geodata.test.ps1','tests/maintenance-safety.test.ps1','tests/package-geodata.test.ps1','tests/workspace-constraints.test.ps1','tests/opt005007008-ui.test.cjs','tests/runtime-settings.test.cjs','tests/profile-controls.test.cjs','tests/rule-selection.test.cjs','tests/unified-proxy-ui.test.cjs','tests/draft-protection.test.cjs','tests/frontend-harness.cjs','tests/browser-drafts-smoke.cjs','tests/browser-optimizations-smoke.cjs','tests/browser-connection-share-smoke.cjs','tests/browser-opt005007008-smoke.cjs','tools/update-version-resource.py')
    $files = @($paths | ForEach-Object { Get-WorkspacePath $_ }) + @(Get-SafeFiles (Get-WorkspacePath 'cmd/mihomolite'))
    return @($files | Sort-Object -Unique | ForEach-Object { Get-FileRecord -Path $_ -Relative ($_.Substring($script:Workspace.Length + 1)) })
}
function Assert-SourceUnchanged {
    param($Records)
    foreach ($record in $Records) {
        $path = Get-WorkspacePath $record.path
        $actual = Get-FileRecord $path $record.path
        if ($actual.sha256 -ne $record.sha256 -or $actual.size -ne $record.size) { throw "Source changed during build: $($record.path)" }
    }
    $now = @(Get-SourceManifest)
    if ($now.Count -ne @($Records).Count) { throw 'Source file set changed during build.' }
}
function Write-JsonAtomic {
    param([string]$Path, $Value)
    $relative = $Path.Substring($script:Workspace.Length + 1)
    $safe = Get-WorkspacePath $relative
    $temp = Get-WorkspacePath ($relative + '.tmp-' + [guid]::NewGuid().ToString('N'))
    [IO.File]::WriteAllText($temp, ($Value | ConvertTo-Json -Depth 16) + "`r`n", (New-Object Text.UTF8Encoding($false)))
    if (Test-Path -LiteralPath $safe) { [IO.File]::Replace($temp, $safe, [Management.Automation.Language.NullString]::Value) } else { [IO.File]::Move($temp, $safe) }
}
function Invoke-CheckedNative {
    param([string]$Executable, [string[]]$Arguments, [string]$LogPath, [switch]$Quiet)
    $oldPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        & $Executable @Arguments 2>&1 | Tee-Object -FilePath $LogPath | ForEach-Object { if (-not $Quiet) { Write-Host $_ } }
        $code = $LASTEXITCODE
    } finally { $ErrorActionPreference = $oldPreference }
    if ($code -ne 0) { throw "Native command failed (exit $code): $Executable $($Arguments -join ' ')" }
}
function Assert-ReleaseBuildInfo {
    param([string[]]$Info)
    $text = $Info -join "`n"
    if ($text -match '(?m)^\s*build\s+-tags(?:=|\s)' -or $text -match 'isolatedtest') { throw 'Tagged/debug binary cannot be promoted.' }
    foreach ($setting in @('GOOS=windows','GOARCH=amd64','CGO_ENABLED=0','-trimpath=true')) {
        if ($text -notmatch ('(?m)^\s*build\s+' + [regex]::Escape($setting) + '\s*$')) { throw "Missing release build setting: $setting" }
    }
}
function Assert-PromotionPackageAvailable {
    param([string]$Version, [string]$BinaryHash)
    $name = 'release/MihomoLite-v' + $Version + '-' + $BinaryHash.Substring(0,12) + '-win-x64.zip'
    if (Test-Path -LiteralPath (Get-WorkspacePath $name)) { throw 'Promotion refused: this launcher-hash package already exists; no current entry has been replaced.' }
}
function Assert-PromotionIdle {
    param([string]$RootBinary)
    $processes = @(Get-CimInstance -ClassName Win32_Process -ErrorAction Stop)
    foreach ($process in $processes) {
        if ($process.Name -ieq 'mihomo.exe') { throw "Promotion refused: mihomo is running (PID $($process.ProcessId))." }
        if ($process.Name -ieq 'MihomoLite.exe') {
            if (-not $process.ExecutablePath) { throw "Promotion refused: cannot resolve launcher PID $($process.ProcessId)." }
            $path = [IO.Path]::GetFullPath([string]$process.ExecutablePath)
            if ($path -ieq $RootBinary) { throw "Promotion refused: root launcher is running (PID $($process.ProcessId))." }
        }
    }
}

$script:Workspace = [IO.Path]::GetFullPath($PSScriptRoot).TrimEnd('\')
$previousLocation = Get-Location
$environmentNames = @('GOCACHE','GOMODCACHE','GOPATH','GOOS','GOARCH','GOAMD64','CGO_ENABLED','GOFLAGS','GOENV','GOWORK','GOTOOLCHAIN','GOPROXY','GOSUMDB','TEMP','TMP','NODE_OPTIONS','NODE_PATH')
# Inherited opt-in integration/probe variables must never turn default tests into real proxy/window tests.
$applicationEnvironmentNames = @([Environment]::GetEnvironmentVariables('Process').Keys | Where-Object { ([string]$_).StartsWith('MIHOMOLITE_', [StringComparison]::OrdinalIgnoreCase) })
$environmentNames = @($environmentNames + $applicationEnvironmentNames | Sort-Object -Unique)
$previousEnvironment = @{}
foreach ($name in $environmentNames) { $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
$manifest = $null
$manifestPath = $null
$metrics = $null; $metricsClock = $null; $activeStage = $null
try {
    Set-Location -LiteralPath $script:Workspace
    foreach ($name in $applicationEnvironmentNames) { [Environment]::SetEnvironmentVariable($name, $null, 'Process') }
    $env:NODE_OPTIONS = ''; $env:NODE_PATH = ''
    if ($env:OS -ne 'Windows_NT') { throw 'This release build requires Windows.' }
    if ($SkipTests -and $Promote) { throw '-Promote is forbidden with -SkipTests.' }
    . (Get-WorkspacePath 'tools/build-metrics.ps1')
    $metrics = New-BuildMetrics; $metricsClock = [Diagnostics.Stopwatch]::StartNew()
    $activeStage = Start-BuildStage $metrics 'prepare'
    $goCommand = Get-Command go -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $goCommand) { throw 'Go 1.23 or newer must be installed; no network/toolchain download is performed.' }
    $go = $goCommand.Source
    $versionSource = [IO.File]::ReadAllText((Get-WorkspacePath 'cmd/mihomolite/types.go'))
    $versionMatch = [regex]::Match($versionSource, '(?m)^const AppVersion = "([0-9]+\.[0-9]+\.[0-9]+)"\s*$')
    if (-not $versionMatch.Success) { throw 'Cannot determine AppVersion from source.' }
    $version = $versionMatch.Groups[1].Value
    $timestamp = [datetime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ')
    $buildId = $version + '-' + $timestamp
    $outputRelative = '.cache/build/' + $buildId
    $output = New-WorkspaceDirectory $outputRelative -Exclusive
    $binaryPath = Get-WorkspacePath ($outputRelative + '/MihomoLite.exe')
    $manifestPath = Get-WorkspacePath ($outputRelative + '/build.json')
    $testDirectory = New-WorkspaceDirectory ('.cache/tests/' + $buildId) -Exclusive
    $env:GOCACHE = New-WorkspaceDirectory '.cache/toolchain/gobuild'
    $env:GOMODCACHE = New-WorkspaceDirectory '.cache/toolchain/gomod'
    $env:GOPATH = New-WorkspaceDirectory '.cache/toolchain/gopath'
    $env:GOOS = 'windows'; $env:GOARCH = 'amd64'; $env:GOAMD64 = 'v1'; $env:CGO_ENABLED = '0'
    $env:GOFLAGS = ''; $env:GOENV = 'off'; $env:GOWORK = 'off'; $env:GOTOOLCHAIN = 'local'; $env:GOPROXY = 'off'; $env:GOSUMDB = 'off'
    $env:TEMP = $testDirectory; $env:TMP = $testDirectory
    $sourceManifest = @(Get-SourceManifest)
    Stop-BuildStage $activeStage; $activeStage = $null
    $testEntries = @(
        [ordered]@{name='go test ./cmd/mihomolite';status='pending'},
        [ordered]@{name='tests/runtime-settings.test.cjs';status='pending'},
        [ordered]@{name='tests/profile-controls.test.cjs';status='pending'},
        [ordered]@{name='tests/rule-selection.test.cjs';status='pending'},
        [ordered]@{name='tests/unified-proxy-ui.test.cjs';status='pending'},
        [ordered]@{name='tests/draft-protection.test.cjs';status='pending'},
        [ordered]@{name='tests/opt005007008-ui.test.cjs';status='pending'}
    )
    $manifest = [ordered]@{schemaVersion=1;version=$version;buildId=$buildId;status='started';binary=$null;sourceManifest=$sourceManifest;tests=[ordered]@{status='pending';entries=$testEntries;summary=[ordered]@{go=$null;node=@()}};timings=$metrics;provenance='source-build';sourceVerified=$false;createdAt=[datetime]::UtcNow.ToString('o');buildSettings=[ordered]@{GOOS='windows';GOARCH='amd64';GOAMD64='v1';CGO_ENABLED='0';trimpath=$true;ldflags='-s -w -H=windowsgui';tags=@();offline=$true};promotion=$null}
    Write-JsonAtomic $manifestPath $manifest
    if ($SkipTests) {
        $manifest.tests.status = 'skipped'
        foreach ($entry in $testEntries) { $entry.status = 'skipped' }
    } else {
        if ($NodePath) {
            $node = [IO.Path]::GetFullPath($NodePath)
            if (-not (Test-Path -LiteralPath $node -PathType Leaf)) { throw "Explicit -NodePath does not exist: $node" }
        } else {
            $nodeCommand = Get-Command node -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
            if (-not $nodeCommand) { throw 'Node.js is required for regression tests. Install it or supply -NodePath; tests are never silently skipped.' }
            $node = $nodeCommand.Source
        }
        $manifest.tests.status = 'running'
        for ($i = 0; $i -lt $testEntries.Count; $i++) {
            $entry = $testEntries[$i]; $entry.status = 'running'
            Write-JsonAtomic $manifestPath $manifest
            try {
                $activeStage = Start-BuildStage $metrics $entry.name
                if ($i -eq 0) {
                    $goLog = Join-Path $output 'go-test.jsonl'
                    Invoke-CheckedNative $go @('test','-json','./cmd/mihomolite') $goLog -Quiet
                    $summary = Get-GoRegressionSummary $goLog
                    $manifest.tests.summary.go = $summary
                    $summaryText = 'Go: {0} top-level PASS, {1} subtests PASS, {2} top-level SKIP; package {3:N3}s; cached={4}' -f $summary.topLevelPassed,$summary.subtestsPassed,$summary.topLevelSkipped,$summary.elapsedSeconds,$summary.cached
                    [IO.File]::WriteAllText((Join-Path $output 'go-test.log'), $summaryText + [Environment]::NewLine, (New-Object Text.UTF8Encoding($false)))
                    Write-Host $summaryText
                } else {
                    $nodeLog = Join-Path $output ('node-test-' + $i + '.log')
                    Invoke-CheckedNative $node @('--test','--test-reporter=tap',$entry.name) $nodeLog -Quiet
                    $summary = Get-NodeRegressionSummary $nodeLog
                    $summary.name = $entry.name
                    $manifest.tests.summary.node += $summary
                    Write-Host ('Node: {0}: {1} PASS, {2} SKIP' -f $entry.name,$summary.passed,$summary.skipped)
                }
                Stop-BuildStage $activeStage; $activeStage = $null
                $entry.status = 'passed'
            } catch { $entry.status = 'failed'; $manifest.tests.status = 'failed'; throw }
        }
        $manifest.tests.status = 'passed'
    }
    Assert-SourceUnchanged $sourceManifest
    $activeStage = Start-BuildStage $metrics 'compile'
    Invoke-CheckedNative $go @('build','-trimpath','-ldflags','-s -w -H=windowsgui','-o',$binaryPath,'./cmd/mihomolite') (Join-Path $output 'go-build.log')
    Stop-BuildStage $activeStage
    $activeStage = Start-BuildStage $metrics 'verify-build'
    $info = @(& $go version -m $binaryPath 2>&1 | ForEach-Object { [string]$_ })
    if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect built binary with go version -m.' }
    Assert-ReleaseBuildInfo $info
    Assert-SourceUnchanged $sourceManifest
    $resourceVersion = (Get-Item -LiteralPath $binaryPath).VersionInfo.FileVersion
    if ($resourceVersion -ne $version) { throw "Binary resource version '$resourceVersion' differs from AppVersion '$version'; no source/resource versions are rewritten." }
    $manifest.binary = Get-FileRecord $binaryPath ($outputRelative + '/MihomoLite.exe')
    $manifest.buildInfo = $info
    $manifest.sourceVerified = $true
    $manifest.status = 'built'
    Stop-BuildStage $activeStage; $activeStage = $null
    Write-JsonAtomic $manifestPath $manifest
    if ($Promote) {
        if ($manifest.tests.status -ne 'passed') { throw 'Only a tested build can be promoted.' }
        Assert-PromotionPackageAvailable $version $manifest.binary.sha256
        $activeStage = Start-BuildStage $metrics 'promote'
        $rootBinary = Get-WorkspacePath 'MihomoLite.exe'
        $currentPath = Get-WorkspacePath 'CURRENT.json'
        Assert-PromotionIdle $rootBinary
        $promotionStamp = [datetime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ')
        $archiveRelative = 'archive/promotions/' + $promotionStamp
        $archiveDirectory = New-WorkspaceDirectory $archiveRelative -Exclusive
        $archiveBinary = Get-WorkspacePath ($archiveRelative + '/MihomoLite.exe')
        if (Test-Path -LiteralPath $archiveBinary) { throw "Historical promotion would be overwritten: $archiveBinary" }
        $stagedBinary = Get-WorkspacePath ($outputRelative + '/promotion-MihomoLite.exe')
        if (Test-Path -LiteralPath $stagedBinary) { throw "Promotion staging already exists: $stagedBinary" }
        Copy-Item -LiteralPath $binaryPath -Destination $stagedBinary
        if ((Get-FileHash -LiteralPath $stagedBinary -Algorithm SHA256).Hash.ToLowerInvariant() -ne $manifest.binary.sha256) { throw 'Promotion staging hash mismatch.' }
        Assert-PromotionIdle $rootBinary
        $hadRoot = Test-Path -LiteralPath $rootBinary -PathType Leaf
        if ($hadRoot) {
            $oldRoot = Get-FileRecord $rootBinary 'MihomoLite.exe'
            Write-JsonAtomic (Join-Path $archiveDirectory 'promotion.json') ([ordered]@{createdAt=[datetime]::UtcNow.ToString('o');previousBinary=$oldRoot;replacedBy=$manifest.binary.sha256})
            Assert-PromotionIdle $rootBinary
            $null = Get-WorkspacePath 'MihomoLite.exe'
            $null = Get-WorkspacePath ($archiveRelative + '/MihomoLite.exe')
            if (Test-Path -LiteralPath $archiveBinary) { throw 'Promotion archive appeared; refusing overwrite.' }
            if ((Get-FileHash -LiteralPath $rootBinary -Algorithm SHA256).Hash.ToLowerInvariant() -ne $oldRoot.sha256) { throw 'Root binary changed before promotion; refusing replacement.' }
            Move-Item -LiteralPath $rootBinary -Destination $archiveBinary
        }
        try {
            if (Test-Path -LiteralPath $rootBinary) { throw 'Root binary appeared during promotion; refusing to overwrite it.' }
            Move-Item -LiteralPath $stagedBinary -Destination $rootBinary
        } catch {
            if ($hadRoot -and -not (Test-Path -LiteralPath $rootBinary)) { Move-Item -LiteralPath $archiveBinary -Destination $rootBinary }
            throw
        }
        $rootRecord = Get-FileRecord $rootBinary 'MihomoLite.exe'
        if ($rootRecord.sha256 -ne $manifest.binary.sha256) { throw 'Promoted root hash mismatch.' }
        $current = [ordered]@{schemaVersion=1;version=$version;buildId=($version + '-' + $rootRecord.sha256.Substring(0,12));binary=$rootRecord;provenance='source-build';sourceVerified=$true;buildManifest=($outputRelative + '/build.json');tests=$manifest.tests;release=$null;recordedAt=[datetime]::UtcNow.ToString('o')}
        Write-JsonAtomic $currentPath $current
        $manifest.promotion = [ordered]@{status='promoted';archive=($(if ($hadRoot) { $archiveRelative + '/MihomoLite.exe' } else { $null }));recordedAt=[datetime]::UtcNow.ToString('o')}
        Write-JsonAtomic $manifestPath $manifest
        Write-Host 'Promoted tested binary. Release is unpublished; run tools/package.ps1 explicitly.'
    }
    Stop-BuildStage $activeStage; $activeStage = $null
    Complete-BuildMetrics $metrics $metricsClock 'passed'
    Write-JsonAtomic $manifestPath $manifest
    Write-Host ('Build finished in {0:N2}s' -f ($metrics.durationMs / 1000))
    Write-Host "Build output: $binaryPath"
    Write-Host "Build manifest: $manifestPath"
    if ($PassThru) { [pscustomobject]@{manifest=($outputRelative + '/build.json');binary=$manifest.binary;tests=$manifest.tests;timings=$metrics;promoted=[bool]$Promote} }
} catch {
    if ($metrics) { Stop-BuildStage $activeStage -Status failed; Complete-BuildMetrics $metrics $metricsClock 'failed' }
    if ($manifest -and $manifestPath) {
        $manifest.status = 'failed'; $manifest.error = $_.Exception.Message
        if ($manifest.tests.status -in @('pending','running')) { $manifest.tests.status = 'failed' }
        try { Write-JsonAtomic $manifestPath $manifest } catch { Write-Warning 'Could not persist failure manifest.' }
    }
    throw
} finally {
    foreach ($name in $environmentNames) { [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], 'Process') }
    Set-Location -LiteralPath $previousLocation.Path
}
