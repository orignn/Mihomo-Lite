#requires -Version 5.1
[CmdletBinding()]
param(
    [string]$WorkspaceRoot = '',
    [switch]$IncludeLauncherExample,
    [string]$GeoDataRoot,
    [switch]$PassThru
)
$ErrorActionPreference = 'Stop'

function Get-PackagePath {
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
function New-PackageDirectory {
    param([string]$Relative, [switch]$Exclusive)
    $path = Get-PackagePath $Relative
    if ($Exclusive -and (Test-Path -LiteralPath $path)) { throw "Output already exists: $path" }
    if (-not (Test-Path -LiteralPath $path)) { New-Item -ItemType Directory -Path $path -Force | Out-Null }
    if (-not (Get-Item -LiteralPath $path).PSIsContainer) { throw "Not a directory: $path" }
    return $path
}
function Get-PackageFileRecord {
    param([string]$Path, [string]$Relative)
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.PSIsContainer) { throw "Expected a regular file: $Path" }
    return [ordered]@{path=$Relative.Replace('\','/');sha256=(Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant();size=[long]$item.Length}
}
function Get-PackageSafeFiles {
    param([string]$Directory)
    foreach ($item in (Get-ChildItem -LiteralPath $Directory -Force | Sort-Object Name)) {
        $safe = Get-PackagePath ($item.FullName.Substring($script:Workspace.Length + 1))
        if ($item.PSIsContainer) { Get-PackageSafeFiles $safe } else { $safe }
    }
}
function Write-PackageJson {
    param([string]$Path, $Value)
    $relative = $Path.Substring($script:Workspace.Length + 1)
    $safe = Get-PackagePath $relative
    $temp = Get-PackagePath ($relative + '.tmp-' + [guid]::NewGuid().ToString('N'))
    [IO.File]::WriteAllText($temp, ($Value | ConvertTo-Json -Depth 16) + [Environment]::NewLine, (New-Object Text.UTF8Encoding($false)))
    if (Test-Path -LiteralPath $safe) { [IO.File]::Replace($temp, $safe, [Management.Automation.Language.NullString]::Value) } else { [IO.File]::Move($temp, $safe) }
}
function Assert-PackageBuildInfo {
    param([string[]]$Info)
    $text = $Info -join [Environment]::NewLine
    if ($text -match '(?m)^\s*build\s+-tags(?:=|\s)' -or $text -match 'isolatedtest') { throw 'Tagged/debug binary cannot be packaged.' }
    foreach ($setting in @('GOOS=windows','GOARCH=amd64','CGO_ENABLED=0','-trimpath=true')) {
        if ($text -notmatch ('(?m)^\s*build\s+' + [regex]::Escape($setting) + '\s*$')) { throw "Missing release build setting: $setting" }
    }
}
function Get-ReleaseEntries {
    param([string]$ReleaseDirectory)
    $entries = @()
    foreach ($item in (Get-ChildItem -LiteralPath $ReleaseDirectory -Force | Sort-Object Name)) {
        $safe = Get-PackagePath ('release/' + $item.Name)
        if ($item.PSIsContainer) { throw "Unknown release directory must be reviewed/archived explicitly: $safe" }
        if ($item.Name -ceq 'README.md') { continue }
        if ($item.Name -notin @('current.json','SHA256SUMS.txt') -and $item.Name -notmatch '^MihomoLite-v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9a-f]{12})?-win-x64\.zip$') { throw "Unknown release file must be reviewed explicitly: $safe" }
        $entries += Get-PackageFileRecord $safe ('release/' + $item.Name)
    }
    return $entries
}
# Local batches are checked against both upstream sidecars on every use.
# Automatic selection is offline and never reads databases from the running instance.
function Get-PackageGeoDataInputs {
    param([string]$Root)
    $selection = Resolve-ReleaseGeoData -WorkspaceRoot $script:Workspace -GeoDataRoot $Root
    $selection.records
}
function Assert-PackageInputsUnchanged {
    param($Records)
    foreach ($record in $Records) {
        $actual = Get-PackageFileRecord (Get-PackagePath $record.path) $record.path
        if ($actual.sha256 -ne $record.sha256 -or $actual.size -ne $record.size) { throw "Packaging input changed: $($record.path)" }
    }
}
function Assert-NoKnownSkippedBuild {
    param([string]$BinaryHash)
    $buildDirectory = Get-PackagePath '.cache/build'
    if (-not (Test-Path -LiteralPath $buildDirectory -PathType Container)) { return }
    foreach ($item in (Get-ChildItem -LiteralPath $buildDirectory -Force)) {
        $safe = Get-PackagePath ($item.FullName.Substring($script:Workspace.Length + 1))
        if (-not $item.PSIsContainer) { continue }
        $manifestPath = Get-PackagePath ($item.FullName.Substring($script:Workspace.Length + 1) + '/build.json')
        if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { continue }
        $build = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
        if ($build.binary -and $build.binary.sha256 -eq $BinaryHash -and $build.tests.status -ne 'passed') { throw "Refusing a known untested/failed build: $manifestPath" }
    }
}
function Get-BinaryProvenance {
    param($Binary, [string]$Version)
    $result = [ordered]@{provenance='adopted-existing-binary';sourceVerified=$false;tests=[ordered]@{status='unknown';note='Existing binary adopted; this packaging operation does not run or assert tests.'};buildManifest=$null}
    $currentPath = Get-PackagePath 'CURRENT.json'
    if (-not (Test-Path -LiteralPath $currentPath -PathType Leaf)) { return $result }
    $current = [IO.File]::ReadAllText($currentPath) | ConvertFrom-Json
    if (-not $current.binary -or $current.binary.sha256 -ne $Binary.sha256) { return $result }
    if ($current.tests -and $current.tests.status -in @('skipped','failed','running','pending')) { throw 'CURRENT.json identifies this binary as untested/failed; publication is forbidden.' }
    if ($current.provenance -ne 'source-build') { return $result }
    if ($current.version -ne $Version -or $current.tests.status -ne 'passed' -or -not $current.sourceVerified) { throw 'Controlled build provenance is incomplete or untested.' }
    if (-not $current.buildManifest -or $current.buildManifest -notmatch '^\.cache/build/[^/\\]+/build\.json$') { throw 'Controlled build manifest must be inside .cache/build.' }
    $buildPath = Get-PackagePath $current.buildManifest
    $build = [IO.File]::ReadAllText($buildPath) | ConvertFrom-Json
    if ($build.provenance -ne 'source-build' -or -not $build.sourceVerified -or $build.version -ne $Version -or $build.binary.sha256 -ne $Binary.sha256 -or $build.tests.status -ne 'passed' -or -not $build.sourceManifest) { throw 'Controlled build manifest does not verify this binary.' }
    Assert-PackageInputsUnchanged $build.sourceManifest
    $result.provenance = 'source-build'; $result.sourceVerified = $true; $result.tests = $build.tests; $result.buildManifest = $current.buildManifest
    return $result
}
function Assert-ZipMatchesPayload {
    param([string]$ZipPath, [string]$PayloadPath, $Expected)
    $archive = [IO.Compression.ZipFile]::OpenRead($ZipPath)
    try {
        $found = @{}
        foreach ($entry in $archive.Entries) {
            if ($entry.FullName.EndsWith('/')) { continue }
            if (-not $entry.FullName.StartsWith('MihomoLite/', [StringComparison]::Ordinal) -or $entry.FullName.Contains('\') -or $entry.FullName -match '(^|/)\.\.?(/|$)') { throw "Unexpected ZIP entry: $($entry.FullName)" }
            if ($found.ContainsKey($entry.FullName)) { throw "Duplicate ZIP entry: $($entry.FullName)" }
            $stream = $entry.Open(); $sha = [Security.Cryptography.SHA256]::Create()
            try { $digest = [BitConverter]::ToString($sha.ComputeHash($stream)).Replace('-','').ToLowerInvariant() } finally { $stream.Dispose(); $sha.Dispose() }
            $found[$entry.FullName] = [ordered]@{sha256=$digest;size=[long]$entry.Length}
        }
        if ($found.Count -ne @($Expected).Count) { throw 'ZIP file count differs from the whitelist payload.' }
        foreach ($record in $Expected) {
            if (-not $found.ContainsKey($record.path) -or $found[$record.path].sha256 -ne $record.sha256 -or $found[$record.path].size -ne $record.size) { throw "ZIP entry hash/size mismatch: $($record.path)" }
        }
    } finally { $archive.Dispose() }
}

if ([string]::IsNullOrWhiteSpace($WorkspaceRoot)) { $WorkspaceRoot = Split-Path -Parent $PSScriptRoot }
$script:Workspace = [IO.Path]::GetFullPath($WorkspaceRoot).TrimEnd('\')
$previousLocation = Get-Location
$environmentNames = @('GOCACHE','GOMODCACHE','GOPATH','GOENV','GOFLAGS','GOWORK','GOTOOLCHAIN','GOPROXY','GOSUMDB')
$previousEnvironment = @{}
foreach ($name in $environmentNames) { $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
$metrics = $null; $metricsClock = $null; $activeStage = $null; $stage = $null; $packageReport = $null
try {
    if (-not (Test-Path -LiteralPath $script:Workspace -PathType Container)) { throw 'WorkspaceRoot does not exist.' }
    Set-Location -LiteralPath $script:Workspace
    . (Get-PackagePath 'tools/build-metrics.ps1')
    . (Get-PackagePath 'tools/release-geodata.ps1')
    $metrics = New-BuildMetrics; $metricsClock = [Diagnostics.Stopwatch]::StartNew()
    $activeStage = Start-BuildStage $metrics 'verify-inputs'
    $goCommand = Get-Command go -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $goCommand) { throw 'Go must be installed to verify binary build metadata; inspection is not silently skipped.' }
    $go = $goCommand.Source
    $env:GOCACHE = New-PackageDirectory '.cache/toolchain/gobuild'; $env:GOMODCACHE = New-PackageDirectory '.cache/toolchain/gomod'; $env:GOPATH = New-PackageDirectory '.cache/toolchain/gopath'
    $env:GOENV = 'off'; $env:GOFLAGS = ''; $env:GOWORK = 'off'; $env:GOTOOLCHAIN = 'local'; $env:GOPROXY = 'off'; $env:GOSUMDB = 'off'
    $rootBinaryPath = Get-PackagePath 'MihomoLite.exe'
    $binary = Get-PackageFileRecord $rootBinaryPath 'MihomoLite.exe'
    $typesPath = Get-PackagePath 'cmd/mihomolite/types.go'
    $versionMatch = [regex]::Match([IO.File]::ReadAllText($typesPath), '(?m)^const AppVersion = "([0-9]+\.[0-9]+\.[0-9]+)"\s*$')
    if (-not $versionMatch.Success) { throw 'Cannot determine AppVersion from source.' }
    $version = $versionMatch.Groups[1].Value
    $resource = (Get-Item -LiteralPath $rootBinaryPath).VersionInfo
    if ($resource.FileVersion -ne $version -or $resource.ProductVersion -ne $version) { throw 'Root binary resource versions do not match source AppVersion; rebuild explicitly instead of rewriting versions.' }
    $info = @(& $go version -m $rootBinaryPath 2>&1 | ForEach-Object { [string]$_ })
    if ($LASTEXITCODE -ne 0) { throw 'go version -m failed; root binary will not be packaged.' }
    Assert-PackageBuildInfo $info
    Assert-NoKnownSkippedBuild $binary.sha256
    $provenance = Get-BinaryProvenance $binary $version
    $hash12 = $binary.sha256.Substring(0,12)
    $buildId = $version + '-' + $hash12
    $zipName = 'MihomoLite-v' + $version + '-' + $hash12 + '-win-x64.zip'
    $releaseDirectory = New-PackageDirectory 'release'
    $finalZip = Get-PackagePath ('release/' + $zipName)
    if (Test-Path -LiteralPath $finalZip) { throw "Same launcher-hash package already exists; explicit review is required, never overwrite/reuse implicitly: $finalZip" }
    $oldRelease = @(Get-ReleaseEntries $releaseDirectory)
    $geoSelection = Resolve-ReleaseGeoData -WorkspaceRoot $script:Workspace -GeoDataRoot $GeoDataRoot
    $geoDataInputs = @($geoSelection.records)
    Write-Host ('Reusing verified local Geo data: ' + $geoSelection.root)
    # Public releases exclude local setup and internal history; ship only public documentation.
    $fixedInputs = @('MihomoLite.exe','runtime/mihomo.exe','data/geoip.metadb','example.yaml','README.md','docs/DEVELOPMENT.md','docs/RELEASE.md','docs/CHANGELOG.md')
    $uiRoot = Get-PackagePath 'data/ui'
    if (-not (Test-Path -LiteralPath (Get-PackagePath 'data/ui/index.html') -PathType Leaf)) { throw 'data/ui/index.html is required.' }
    $uiFiles = @(Get-PackageSafeFiles $uiRoot)
    $inputRecords = @($fixedInputs | ForEach-Object { Get-PackageFileRecord (Get-PackagePath $_) $_ }) + $geoDataInputs
    $inputRecords += @($uiFiles | ForEach-Object { Get-PackageFileRecord -Path $_ -Relative ($_.Substring($script:Workspace.Length + 1)) })
    $versionRecord = Get-PackageFileRecord $typesPath 'cmd/mihomolite/types.go'
    Stop-BuildStage $activeStage
    $activeStage = Start-BuildStage $metrics 'stage-payload'
    $stamp = [datetime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ')
    $stageRelative = '.cache/package/' + $buildId + '-' + $stamp
    $stage = New-PackageDirectory $stageRelative -Exclusive
    $payloadRelative = $stageRelative + '/MihomoLite'
    $payload = New-PackageDirectory $payloadRelative
    $packageReport = [ordered]@{schemaVersion=1;status='running';buildId=$buildId;geo=[ordered]@{root=$geoSelection.root;source=$geoSelection.source;downloaded=$false};timings=$metrics;artifact=$null;error=$null}
    $payloadRecords = @()
    foreach ($record in $inputRecords) {
        $source = Get-PackagePath $record.path
        $payloadPath = if ($record.payloadPath) { $record.payloadPath } else { $record.path }
        $destinationRelative = $payloadRelative + '/' + $payloadPath
        $destination = Get-PackagePath $destinationRelative
        New-PackageDirectory ([IO.Path]::GetDirectoryName($destinationRelative)) | Out-Null
        if (Test-Path -LiteralPath $destination) { throw "Staging file already exists: $destination" }
        Copy-Item -LiteralPath $source -Destination $destination
        $stagedRecord = Get-PackageFileRecord $destination ('MihomoLite/' + $payloadPath)
        if ($stagedRecord.sha256 -ne $record.sha256 -or $stagedRecord.size -ne $record.size) { throw "Copy mismatch: $($record.path)" }
        $payloadRecords += $stagedRecord
    }
    if ($IncludeLauncherExample) {
        $examplePath = Get-PackagePath ($payloadRelative + '/launcher.example.json')
        Write-PackageJson $examplePath ([ordered]@{listen='127.0.0.1:17653';configName='';autoStart=$false;closeToTray=$true;silentStart=$false})
        $payloadRecords += Get-PackageFileRecord $examplePath 'MihomoLite/launcher.example.json'
    }
    Stop-BuildStage $activeStage
    $activeStage = Start-BuildStage $metrics 'compress'
    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $stagedZip = Get-PackagePath ($stageRelative + '/' + $zipName)
    if (Test-Path -LiteralPath $stagedZip) { throw 'Staging ZIP already exists.' }
    # Explicit entry names avoid framework-dependent backslashes and include only hashed whitelist files.
    $zipArchive = [IO.Compression.ZipFile]::Open($stagedZip, [IO.Compression.ZipArchiveMode]::Create)
    try {
        foreach ($record in $payloadRecords) {
            $stagedFile = Get-PackagePath ($stageRelative + '/' + $record.path)
            [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($zipArchive, $stagedFile, $record.path, [IO.Compression.CompressionLevel]::Optimal) | Out-Null
        }
    } finally { $zipArchive.Dispose() }
    Stop-BuildStage $activeStage
    $activeStage = Start-BuildStage $metrics 'verify-package'
    Assert-ZipMatchesPayload $stagedZip $payload $payloadRecords
    $artifact = Get-PackageFileRecord $stagedZip $zipName
    Assert-PackageInputsUnchanged ($inputRecords + @($versionRecord) + @($geoDataInputs | ForEach-Object { $_.checksum }))
    $newRelease = @(Get-ReleaseEntries $releaseDirectory)
    if ($newRelease.Count -ne $oldRelease.Count) { throw 'Release directory changed during staging.' }
    Assert-PackageInputsUnchanged $oldRelease
    if (Test-Path -LiteralPath $finalZip) { throw 'Package appeared during staging; refusing overwrite.' }
    $createdAt = [datetime]::UtcNow.ToString('o')
    $releaseManifest = [ordered]@{schemaVersion=1;version=$version;buildId=$buildId;binary=[ordered]@{path='MihomoLite/MihomoLite.exe';sha256=$binary.sha256;size=$binary.size};artifact=$artifact;createdAt=$createdAt;provenance=$provenance.provenance;sourceVerified=$provenance.sourceVerified;tests=$provenance.tests;files=@($payloadRecords | Sort-Object path);buildInfo=$info}
    $current = [ordered]@{schemaVersion=1;version=$version;buildId=$buildId;binary=$binary;provenance=$provenance.provenance;sourceVerified=$provenance.sourceVerified;tests=$provenance.tests;release=[ordered]@{path=('release/' + $zipName);sha256=$artifact.sha256;manifest='release/current.json'};recordedAt=$createdAt}
    $current.release.geoDataPolicy = 1; $releaseManifest.geoDataPolicy = 1
    $releaseManifest.geoDataSource = [ordered]@{root=$geoSelection.root;source=$geoSelection.source;downloaded=$false}
    if ($provenance.buildManifest) { $current.buildManifest = $provenance.buildManifest; $releaseManifest.buildManifest = $provenance.buildManifest }
    Write-PackageJson (Join-Path $stage 'release-current.json') $releaseManifest
    Write-PackageJson (Join-Path $stage 'workspace-current.json') $current
    $sumPath = Get-PackagePath ($stageRelative + '/SHA256SUMS.txt')
    $sumLines = @($artifact.sha256 + '  ' + $zipName) + @($payloadRecords | Sort-Object path | ForEach-Object { $_.sha256 + '  ' + $_.path })
    [IO.File]::WriteAllText($sumPath, ($sumLines -join [Environment]::NewLine) + [Environment]::NewLine, (New-Object Text.UTF8Encoding($false)))
    Stop-BuildStage $activeStage
    $activeStage = Start-BuildStage $metrics 'publish'
    # Review every existing release entry before moving; README.md is deliberately retained.
    if ($oldRelease.Count -gt 0) {
        $archiveRelative = 'archive/releases/' + [datetime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ')
        $archiveDirectory = New-PackageDirectory $archiveRelative -Exclusive
        $moves = @()
        foreach ($record in $oldRelease) {
            $source = Get-PackagePath $record.path
            $destination = Get-PackagePath ($archiveRelative + '/' + [IO.Path]::GetFileName($record.path))
            if (Test-Path -LiteralPath $destination) { throw "Historical release would be overwritten: $destination" }
            $moves += [ordered]@{source=$source;destination=$destination}
        }
        Write-PackageJson (Join-Path $archiveDirectory 'archive.json') ([ordered]@{createdAt=$createdAt;files=$oldRelease})
        foreach ($move in $moves) { Move-Item -LiteralPath $move.source -Destination $move.destination }
    }
    if (Test-Path -LiteralPath $finalZip) { throw 'Final ZIP already exists; refusing overwrite.' }
    Move-Item -LiteralPath $stagedZip -Destination $finalZip
    $published = Get-PackageFileRecord $finalZip $zipName
    if ($published.sha256 -ne $artifact.sha256 -or $published.size -ne $artifact.size) { throw 'Published ZIP hash mismatch; previous releases remain archived.' }
    Write-PackageJson (Get-PackagePath 'release/current.json') $releaseManifest
    $releaseSumPath = Get-PackagePath 'release/SHA256SUMS.txt'
    if (Test-Path -LiteralPath $releaseSumPath) { throw 'SHA256SUMS.txt appeared during publication; refusing overwrite.' }
    Copy-Item -LiteralPath $sumPath -Destination $releaseSumPath
    Write-PackageJson (Get-PackagePath 'CURRENT.json') $current
    Stop-BuildStage $activeStage; $activeStage = $null
    Complete-BuildMetrics $metrics $metricsClock 'passed'
    $packageReport.status = 'passed'; $packageReport.artifact = $published
    Write-PackageJson (Join-Path $stage 'package.json') $packageReport
    Write-Host ('Package finished in {0:N2}s' -f ($metrics.durationMs / 1000))
    Write-Host "Package: $finalZip"
    Write-Host ('Launcher SHA-256: ' + $binary.sha256)
    Write-Host ('ZIP SHA-256: ' + $artifact.sha256)
    Write-Host ('Provenance: ' + $provenance.provenance + '; sourceVerified=' + $provenance.sourceVerified + '; tests=' + $provenance.tests.status)
    if ($PassThru) { [pscustomobject]@{artifact=('release/' + $zipName);manifest='release/current.json';report=($stageRelative + '/package.json');geo=$packageReport.geo;timings=$metrics} }
} catch {
    if ($metrics) { Stop-BuildStage $activeStage -Status failed; Complete-BuildMetrics $metrics $metricsClock 'failed' }
    if ($stage -and $packageReport) {
        $packageReport.status = 'failed'; $packageReport.error = $_.Exception.Message
        try { Write-PackageJson (Join-Path $stage 'package.json') $packageReport } catch { Write-Warning 'Could not persist package failure timings.' }
    }
    throw
} finally {
    foreach ($name in $environmentNames) { [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], 'Process') }
    Set-Location -LiteralPath $previousLocation.Path
}
