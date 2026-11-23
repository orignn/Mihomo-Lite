#requires -Version 5.1
[CmdletBinding()]
param(
    [string]$WorkspaceRoot = '',
    [string]$Tag = '',
    [string]$SourceCommit = '',
    [string]$NodePath = '',
    [switch]$Refresh,
    [switch]$PassThru
)
$ErrorActionPreference = 'Stop'
if (-not $WorkspaceRoot) { $WorkspaceRoot = Split-Path -Parent $PSScriptRoot }

function Assert-CIPath([string]$Root, [string]$Relative, [switch]$AllowMissing) {
    if ([IO.Path]::IsPathRooted($Relative) -or $Relative -match '[\\:\x00-\x1f<>"|?*]' -or $Relative -match '(^|/)\.\.?(/|$)') { throw 'Unsafe CI release relative path.' }
    $full = [IO.Path]::GetFullPath((Join-Path $Root $Relative))
    if (-not $full.StartsWith($Root.TrimEnd([char[]]@('/','\')) + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw 'CI path escaped its dedicated workspace.' }
    for ($cursor = $full; $cursor; $cursor = Split-Path -Parent $cursor) {
        if (Test-Path -LiteralPath $cursor) {
            $item = Get-Item -LiteralPath $cursor -Force
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'CI release path contains a reparse point.' }
        }
    }
    if (-not $AllowMissing -and -not (Test-Path -LiteralPath $full -PathType Leaf)) { throw 'Required CI release file is missing.' }
    return $full
}
function Read-CIJson([string]$Path) {
    $text = [IO.File]::ReadAllText($Path)
    if ((Get-Command ConvertFrom-Json).Parameters.ContainsKey('DateKind')) { return $text | ConvertFrom-Json -DateKind String }
    return $text | ConvertFrom-Json
}
function Write-CINewText([string]$Path, [string]$Text) {
    $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $bytes = [Text.Encoding]::UTF8.GetBytes($Text); $stream.Write($bytes, 0, $bytes.Length) } finally { $stream.Dispose() }
}
function Copy-CIAsset([string]$Source, [string]$Output, [string]$Name, [string]$ExpectedHash, [long]$ExpectedSize) {
    if ($Name -notmatch '\A[A-Za-z0-9][A-Za-z0-9._-]*\z' -or $Name -match '[. ]$') { throw 'Unsafe release asset name.' }
    if ($ExpectedHash -cnotmatch '\A[0-9a-f]{64}\z' -or $ExpectedSize -le 0) { throw 'Release asset identity is incomplete.' }
    $item = Get-Item -LiteralPath $Source -Force
    if ($item.PSIsContainer -or $item.Length -ne $ExpectedSize -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Release asset is not the expected regular file.' }
    if ((Get-FileHash -LiteralPath $Source -Algorithm SHA256).Hash.ToLowerInvariant() -cne $ExpectedHash) { throw 'Release asset SHA-256 mismatch.' }
    $destination = Join-Path $Output $Name
    if (Test-Path -LiteralPath $destination) { throw 'Release asset collision; no existing asset is overwritten.' }
    Copy-Item -LiteralPath $Source -Destination $destination
    if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash.ToLowerInvariant() -cne $ExpectedHash) { throw 'Copied release asset SHA-256 mismatch.' }
    return [pscustomobject]@{ name=$Name; sha256=$ExpectedHash; size=$ExpectedSize }
}

$root = [IO.Path]::GetFullPath($WorkspaceRoot).TrimEnd([char[]]@('/','\'))
if (-not [IO.Path]::IsPathRooted($WorkspaceRoot) -or -not (Test-Path -LiteralPath $root -PathType Container)) { throw 'A dedicated absolute CI checkout is required.' }
$versionPath = Assert-CIPath $root 'cmd/mihomolite/types.go'
$match = [regex]::Match([IO.File]::ReadAllText($versionPath), '(?m)^const AppVersion = "([0-9]+\.[0-9]+\.[0-9]+)"\s*$')
if (-not $match.Success) { throw 'The source version marker is invalid.' }
$version = $match.Groups[1].Value
if (-not $Tag) { $Tag = 'v' + $version }
if ($Tag -cne ('v' + $version)) { throw 'Release tag must exactly match the source version; do not relabel another version.' }
if ($SourceCommit -and $SourceCommit -cnotmatch '\A[0-9a-f]{40}\z') { throw 'SourceCommit must be a full lowercase commit identity.' }
# Never turn a developer's day-to-day installation into a CI release workspace.
foreach ($relative in @('data','runtime','logs','launcher.json','CURRENT.json','MihomoLite.exe','LICENSES','THIRD_PARTY_NOTICES.md','release')) {
    $candidate = Assert-CIPath $root $relative -AllowMissing
    if (Test-Path -LiteralPath $candidate) { throw ('CI requires a clean checkout, not an existing installation: ' + $relative) }
}
foreach ($relative in @('tools/build-release.ps1','tools/prepare-geodata.ps1','tools/prepare-release-components.ps1','tools/prepare-release-notices.ps1','go.mod','go.sum')) { [void](Assert-CIPath $root $relative) }
$git = Get-Command git -CommandType Application -ErrorAction Stop | Select-Object -First 1
$top = (@(& $git.Source -C $root rev-parse --show-toplevel) -join '').Trim()
if ($LASTEXITCODE -ne 0 -or -not [string]::Equals([IO.Path]::GetFullPath($top).TrimEnd([char[]]@('/','\')), $root, [StringComparison]::OrdinalIgnoreCase)) { throw 'CI must build its own tracked checkout, not a parent repository.' }
$commit = (@(& $git.Source -C $root rev-parse HEAD) -join '').Trim()
if ($LASTEXITCODE -ne 0 -or $commit -cnotmatch '\A[0-9a-f]{40}\z' -or ($SourceCommit -and $SourceCommit -cne $commit)) { throw 'CI source commit does not match the selected revision.' }
& $git.Source -C $root diff --quiet HEAD --
if ($LASTEXITCODE -ne 0) { throw 'Tracked source changes are not a release identity.' }
$plan = [ordered]@{ version=$version; tag=$Tag; sourceCommit=$commit; refreshRequested=[bool]$Refresh; dedicatedWorkspaceRequired=$true; existingInstallationNeverUpdated=$true }
if (-not $Refresh) {
    Write-Host ('Verified clean release workspace and immutable source identity for ' + $Tag + ' (offline preflight only).')
    if ($PassThru) { [pscustomobject]$plan } else { $plan | ConvertTo-Json }
    return
}
$go = Get-Command go -CommandType Application -ErrorAction Stop | Select-Object -First 1
$previous = Get-Location
$envNames = @('GOCACHE','GOMODCACHE','GOPATH','GOENV','GOWORK','GOTOOLCHAIN','GOFLAGS','GOOS','GOARCH','CGO_ENABLED','GOPROXY','GOSUMDB')
$oldEnv = @{}
foreach ($name in $envNames) { $oldEnv[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
try {
    Set-Location -LiteralPath $root
    $env:GOCACHE = Join-Path $root '.cache/toolchain/gobuild'
    $env:GOMODCACHE = Join-Path $root '.cache/toolchain/gomod'
    $env:GOPATH = Join-Path $root '.cache/toolchain/gopath'
    $env:GOENV='off'; $env:GOWORK='off'; $env:GOTOOLCHAIN='local'; $env:GOFLAGS=''
    $env:GOOS='windows'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
    $env:GOPROXY='https://proxy.golang.org,direct'; $env:GOSUMDB='sum.golang.org'
    Write-Host 'Preparing pinned Go modules explicitly; later build stages are offline.'
    & $go.Source mod download
    if ($LASTEXITCODE -ne 0) { throw 'Pinned Go module preparation failed.' }
    $geo = & (Assert-CIPath $root 'tools/prepare-geodata.ps1') -WorkspaceRoot $root -Refresh
    if (-not $geo -or -not $geo.root) { throw 'Official double-DAT preparation did not produce a verified batch.' }
    $components = & (Assert-CIPath $root 'tools/prepare-release-components.ps1') -WorkspaceRoot $root -GeoDataRoot $geo.root -Refresh -PassThru
    if (-not $components -or -not $components.manifest) { throw 'Official components were not prepared.' }
    $notices = & (Assert-CIPath $root 'tools/prepare-release-notices.ps1') -WorkspaceRoot $root -ComponentManifest $components.manifest -Refresh -PassThru
    if (-not $notices -or -not $notices.manifest) { throw 'Release license materials did not produce a manifest.' }
    $noticeFile = Assert-CIPath $root $notices.manifest
    $noticeIdentity = Read-CIJson $noticeFile
    if ($noticeIdentity.status -cne 'verified-ready' -or $noticeIdentity.redistributionReady -cne $true) { throw 'Third-party release materials are not ready; no complete package may be published.' }
    $buildArguments = @{Publish=$true;GeoDataRoot=$geo.root;NoticeManifest=$notices.manifest}
    if ($NodePath) { $buildArguments.NodePath=$NodePath }
    $built = & (Assert-CIPath $root 'tools/build-release.ps1') @buildArguments
    if (-not $built -or $built.status -cne 'passed' -or -not $built.package) { throw 'Controlled full release build did not pass.' }
    $current = Read-CIJson (Assert-CIPath $root 'CURRENT.json')
    $manifest = Read-CIJson (Assert-CIPath $root 'release/current.json')
    if ($current.version -cne $version -or $manifest.version -cne $version -or $manifest.sourceVerified -cne $true -or $manifest.releaseNoticePolicy -ne 1 -or $manifest.geoDataPolicy -ne 1) { throw 'Full release identity, source evidence, licenses or double-DAT policy is missing.' }
    $id = [guid]::NewGuid().ToString('N')
    $artifactRelative = '.cache/package/ci-release-assets/' + $id
    $artifactDir = Assert-CIPath $root $artifactRelative -AllowMissing
    [void](New-Item -ItemType Directory -Path $artifactDir -ErrorAction Stop)
    $assets = [Collections.Generic.List[object]]::new()
    $zipRelative = 'release/' + $manifest.artifact.path
    $assets.Add((Copy-CIAsset (Assert-CIPath $root $zipRelative) $artifactDir $manifest.artifact.path $manifest.artifact.sha256 ([long]$manifest.artifact.size)))
    foreach ($name in @('SHA256SUMS.txt','current.json')) {
        $source = Assert-CIPath $root ('release/' + $name)
        $assets.Add((Copy-CIAsset $source $artifactDir $name (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash.ToLowerInvariant() (Get-Item -LiteralPath $source).Length))
    }
    foreach ($archive in @($noticeIdentity.sourceArchives)) {
        if ($archive.component -notmatch '\A[a-z][a-z0-9-]{0,63}\z' -or $archive.commit -cnotmatch '\A[0-9a-f]{40}\z' -or $archive.path -notmatch '\A\.cache/downloads/release-notices-[A-Za-z0-9._-]+/.*\.(?:tar\.gz|zip)\z') { throw 'Upstream source archive identity is unsafe.' }
        $suffix = if ($archive.path.EndsWith('.tar.gz', [StringComparison]::Ordinal)) { '.tar.gz' } else { '.zip' }
        $name = 'Source-' + $archive.component + '-' + $archive.commit.Substring(0,12) + $suffix
        $assets.Add((Copy-CIAsset (Assert-CIPath $root $archive.path) $artifactDir $name $archive.sha256 ([long]$archive.size)))
    }
    if (-not @($noticeIdentity.sourceArchives).Count) { throw 'Corresponding upstream source archives must accompany the release.' }
    $goSummary = $current.tests.summary.go
    $nodePass = ($current.tests.summary.node | Measure-Object -Property passed -Sum).Sum
    $lines = @('# Mihomo Lite ' + $Tag, '', 'Windows x64 complete portable package. Verify SHA-256 before extraction.', '', 'Source commit: ' + $commit, 'Launcher SHA-256: ' + $current.binary.sha256, '', 'Default Go tests: ' + $goSummary.topLevelPassed + ' top-level PASS, ' + $goSummary.subtestsPassed + ' subtests PASS, ' + $goSummary.topLevelSkipped + ' top-level SKIP.', 'Frontend tests: ' + $nodePass + ' PASS.', '', 'Skipped/native Windows, live system proxy, reboot and private-subscription scenarios are not claimed as accepted.', 'Third-party license notices are included in the ZIP; fixed upstream source archives are attached separately.', 'No user subscriptions, credentials, runtime snapshots, logs or caches are included.', '', 'Installation: extract the complete ZIP and run the launcher. Source-code archives are not installation packages.')
    $notesPath = Join-Path $artifactDir 'RELEASE_NOTES.md'
    Write-CINewText $notesPath (($lines -join [Environment]::NewLine) + [Environment]::NewLine)
    $assets.Add([pscustomobject]@{name='RELEASE_NOTES.md';sha256=(Get-FileHash -LiteralPath $notesPath -Algorithm SHA256).Hash.ToLowerInvariant();size=(Get-Item -LiteralPath $notesPath).Length})
    $sumsPath = Join-Path $artifactDir 'RELEASE_SHA256SUMS.txt'
    Write-CINewText $sumsPath ((@($assets | Sort-Object name | ForEach-Object { $_.sha256 + '  ' + $_.name }) -join [Environment]::NewLine) + [Environment]::NewLine)
    $assets.Add([pscustomobject]@{name='RELEASE_SHA256SUMS.txt';sha256=(Get-FileHash -LiteralPath $sumsPath -Algorithm SHA256).Hash.ToLowerInvariant();size=(Get-Item -LiteralPath $sumsPath).Length})
    $report = [ordered]@{schemaVersion=1;status='verified-ready';repository='orignn/Mihomo-Lite';tag=$Tag;version=$version;sourceCommit=$commit;launcherSHA256=$current.binary.sha256;package=$manifest.artifact.path;assets=@($assets);tests=$current.tests.summary;sourceVerified=$true;releaseNoticePolicy=1;geoDataPolicy=1;generatedAt=[datetime]::UtcNow.ToString('o')}
    Write-CINewText (Join-Path $artifactDir 'release-report.json') (($report | ConvertTo-Json -Depth 12) + [Environment]::NewLine)
    if ($env:GITHUB_OUTPUT) {
        foreach ($value in @('tag=' + $Tag, 'source_commit=' + $commit, 'artifact_dir=' + $artifactDir.Replace('\','/'))) {
            if ($value.Contains([char]10) -or $value.Contains([char]13)) { throw 'Multiline Actions outputs are forbidden.' }
            [IO.File]::AppendAllText($env:GITHUB_OUTPUT, $value + [Environment]::NewLine, [Text.Encoding]::UTF8)
        }
    }
    Write-Host ('Verified complete release artifacts: ' + $artifactRelative)
    if ($PassThru) { [pscustomobject]@{status='verified-ready';tag=$Tag;sourceCommit=$commit;artifactDirectory=$artifactDir;report=(Join-Path $artifactDir 'release-report.json')} }
} finally {
    Set-Location -LiteralPath $previous
    foreach ($name in $envNames) { [Environment]::SetEnvironmentVariable($name, $oldEnv[$name], 'Process') }
}
