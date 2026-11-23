#requires -Version 5.1
[CmdletBinding()]
param(
    [string]$WorkspaceRoot = '',
    [switch]$IncludeLauncherExample,
    [string]$GeoDataRoot,
    [string]$NoticeManifest,
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
# The notices generator owns trusted origins; package only its explicitly declared text/JSON records.
# Never enumerate LICENSES to discover packaging inputs (it may contain unrelated private files).
function Get-PackageNoticePath {
    param($Value)
    if ($Value -isnot [string] -or $Value -cnotmatch '^LICENSES/(?:[A-Za-z0-9][A-Za-z0-9._-]*/)*[A-Za-z0-9][A-Za-z0-9._-]*\.(?:txt|md|json)$') { throw 'Notice license path must be a canonical LICENSES/ text or JSON path.' }
    foreach ($part in $Value.Split('/')) {
        if ($part -in @('.', '..') -or $part -match '[. ]$' -or $part -match '^(?i:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\..*)?$') { throw 'Unsafe notice license path segment.' }
    }
    return Get-PackagePath $Value
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
function Assert-PackageLicenseIndex {
    param($Records)
    $indexPath = Get-PackageNoticePath 'LICENSES/manifest.json'
    $indexFile = Get-Item -LiteralPath $indexPath
    if ($indexFile.Length -le 0 -or $indexFile.Length -gt 4194304) { throw 'LICENSES/manifest.json must be non-empty and at most 4 MiB.' }
    $index = [IO.File]::ReadAllText($indexPath) | ConvertFrom-Json
    if ($index -isnot [Management.Automation.PSCustomObject] -or ($index.schemaVersion -isnot [int] -and $index.schemaVersion -isnot [long]) -or $index.schemaVersion -ne 1 -or $index.records -isnot [Array] -or $index.records.Count -eq 0) { throw 'LICENSES/manifest.json must have schemaVersion=1 and complete records excluding itself.' }
    $indexed = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    foreach ($entry in $index.records) {
        $null = Get-PackageNoticePath $entry.path
        if ($entry.path -ceq 'LICENSES/manifest.json' -or -not $indexed.Add($entry.path)) { throw 'Invalid self-referential or duplicate license index record.' }
        $matching = @($Records | Where-Object { $_.path -ceq $entry.path })
        if ($matching.Count -ne 1 -or $entry.sha256 -cne $matching[0].sha256 -or ($entry.size -isnot [int] -and $entry.size -isnot [long]) -or $entry.size -ne $matching[0].size) { throw "License index does not match notice records: $($entry.path)" }
    }
    foreach ($entry in $Records) {
        if ($entry.path -cne 'THIRD_PARTY_NOTICES.md' -and $entry.path -cne 'LICENSES/manifest.json' -and -not $indexed.Contains($entry.path)) { throw "License index is missing a notice record: $($entry.path)" }
    }
}
function Get-PackageNoticeInputs {
    param([string]$ManifestPath)
    if ([string]::IsNullOrWhiteSpace($ManifestPath) -or $ManifestPath -cnotmatch '^\.cache/downloads/release-notices-[A-Za-z0-9._-]+/notices\.json$') { throw '-NoticeManifest must be an explicit .cache/downloads/release-notices-<id>/notices.json path.' }
    $path = Get-PackagePath $ManifestPath
    $manifestRecord = Get-PackageFileRecord $path $ManifestPath
    if ($manifestRecord.size -le 0) { throw 'Notice manifest must be non-empty.' }
    $notice = [IO.File]::ReadAllText($path) | ConvertFrom-Json
    if ($notice -isnot [Management.Automation.PSCustomObject] -or ($notice.schemaVersion -isnot [int] -and $notice.schemaVersion -isnot [long]) -or $notice.schemaVersion -ne 1 -or $notice.status -cne 'verified-ready' -or $notice.redistributionReady -isnot [bool] -or -not $notice.redistributionReady -or $notice.records -isnot [Array] -or $notice.records.Count -lt 2) { throw 'Notice manifest must have verified-ready status and a non-empty complete records array.' }
    $records = @(); $seen = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    foreach ($record in $notice.records) {
        if ($record -isnot [Management.Automation.PSCustomObject] -or $record.path -isnot [string]) { throw 'Invalid notice file record.' }
        $inputPath = if ($record.path -ceq 'THIRD_PARTY_NOTICES.md') { Get-PackagePath $record.path } else { Get-PackageNoticePath $record.path }
        if (-not $seen.Add($record.path)) { throw 'Duplicate or case-colliding notice file record.' }
        if ($record.sha256 -isnot [string] -or $record.sha256 -cnotmatch '^[0-9a-f]{64}$' -or ($record.size -isnot [int] -and $record.size -isnot [long]) -or $record.size -le 0) { throw 'Notice file SHA-256 and size must be valid and non-empty.' }
        $actual = Get-PackageFileRecord $inputPath $record.path
        if ($actual.sha256 -cne $record.sha256 -or $actual.size -ne $record.size) { throw "Notice file SHA-256/size mismatch: $($record.path)" }
        $records += $actual
    }
    if (-not $seen.Contains('LICENSES/manifest.json')) { throw 'Notice manifest is missing LICENSES/manifest.json.' }
    Assert-PackageLicenseIndex $records
    if (-not $seen.Contains('THIRD_PARTY_NOTICES.md')) { throw 'Notice manifest is missing THIRD_PARTY_NOTICES.md.' }
    if (@($records | Where-Object { $_.path -cmatch '^LICENSES/.+\.(txt|md)$' }).Count -eq 0) { throw 'Notice manifest must declare non-empty full license text (.txt or .md).' }
    if ($notice.sourceArchives -isnot [Array]) { throw 'Notice sourceArchives must be an explicit array.' }
    $sources = @(); $sourceSeen = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    $batch = $ManifestPath.Substring(0, $ManifestPath.LastIndexOf('/') + 1)
    foreach ($archive in $notice.sourceArchives) {
        if ($archive -isnot [Management.Automation.PSCustomObject] -or $archive.path -isnot [string] -or -not $archive.path.StartsWith($batch, [StringComparison]::Ordinal)) { throw 'Notice source archive must be in the same verified public download batch.' }
        if ($archive.repository -cnotin @('MetaCubeX/mihomo','Zephyruso/zashboard','MetaCubeX/meta-rules-dat') -or $archive.commit -isnot [string] -or $archive.commit -cnotmatch '^[0-9a-f]{40}$' -or $archive.url -cne ('https://github.com/' + $archive.repository + '/archive/' + $archive.commit + '.tar.gz')) { throw 'Notice source archive has unapproved upstream identity.' }
        $name = $archive.path.Substring($batch.Length)
        if ($name -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._-]*\.(?:tar\.gz|zip)$' -or $name -match '^(?i:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])\.' -or -not $sourceSeen.Add($archive.path)) { throw 'Invalid or duplicate notice source archive path.' }
        if ($archive.sha256 -isnot [string] -or $archive.sha256 -cnotmatch '^[0-9a-f]{64}$' -or ($archive.size -isnot [int] -and $archive.size -isnot [long]) -or $archive.size -le 0) { throw 'Invalid notice source archive SHA-256/size.' }
        $actual = Get-PackageFileRecord (Get-PackagePath $archive.path) $archive.path
        if ($actual.sha256 -cne $archive.sha256 -or $actual.size -ne $archive.size) { throw 'Notice source archive SHA-256/size mismatch.' }
        $sources += $archive
    }
    return [pscustomobject]@{records=$records;manifest=$manifestRecord;sourceArchives=$sources}
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
    $noticeSelection = Get-PackageNoticeInputs $NoticeManifest
    $noticeInputs = @($noticeSelection.records)
    Write-Host ('Reusing verified local Geo data: ' + $geoSelection.root)
    # Public releases exclude local setup and internal history; ship only public documentation.
    $fixedInputs = @('MihomoLite.exe','runtime/mihomo.exe','data/geoip.metadb','example.yaml','README.md','docs/DEVELOPMENT.md','docs/RELEASE.md','docs/CHANGELOG.md')
    if (Test-Path -LiteralPath (Get-PackagePath 'docs/THIRD-PARTY.md') -PathType Leaf) { $fixedInputs += 'docs/THIRD-PARTY.md' }
    $uiRoot = Get-PackagePath 'data/ui'
    if (-not (Test-Path -LiteralPath (Get-PackagePath 'data/ui/index.html') -PathType Leaf)) { throw 'data/ui/index.html is required.' }
    $uiFiles = @(Get-PackageSafeFiles $uiRoot)
    $inputRecords = @($fixedInputs | ForEach-Object { Get-PackageFileRecord (Get-PackagePath $_) $_ }) + $geoDataInputs + $noticeInputs
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
    Assert-PackageInputsUnchanged ($inputRecords + @($versionRecord, $noticeSelection.manifest) + @($noticeSelection.sourceArchives) + @($geoDataInputs | ForEach-Object { $_.checksum }))
    $newRelease = @(Get-ReleaseEntries $releaseDirectory)
    if ($newRelease.Count -ne $oldRelease.Count) { throw 'Release directory changed during staging.' }
    Assert-PackageInputsUnchanged $oldRelease
    if (Test-Path -LiteralPath $finalZip) { throw 'Package appeared during staging; refusing overwrite.' }
    $createdAt = [datetime]::UtcNow.ToString('o')
    $releaseManifest = [ordered]@{schemaVersion=1;version=$version;buildId=$buildId;binary=[ordered]@{path='MihomoLite/MihomoLite.exe';sha256=$binary.sha256;size=$binary.size};artifact=$artifact;createdAt=$createdAt;provenance=$provenance.provenance;sourceVerified=$provenance.sourceVerified;tests=$provenance.tests;files=@($payloadRecords | Sort-Object path);buildInfo=$info}
    $current = [ordered]@{schemaVersion=1;version=$version;buildId=$buildId;binary=$binary;provenance=$provenance.provenance;sourceVerified=$provenance.sourceVerified;tests=$provenance.tests;release=[ordered]@{path=('release/' + $zipName);sha256=$artifact.sha256;manifest='release/current.json'};recordedAt=$createdAt}
    $current.release.geoDataPolicy = 1; $releaseManifest.geoDataPolicy = 1
    $current.release.releaseNoticePolicy = 1; $releaseManifest.releaseNoticePolicy = 1
    $current.release.noticeManifest = $noticeSelection.manifest; $releaseManifest.noticeManifest = $noticeSelection.manifest
    $releaseManifest.noticeFiles = @($noticeInputs | Sort-Object path)
    $releaseManifest.sourceArchives = @($noticeSelection.sourceArchives)
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
