#requires -Version 5.1
<#
Offline verification tests for tools/publish-github-release.ps1.
Synthetic artifacts only; no GitHub request, no tag, no Release, no real package.
#>
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression | Out-Null
Add-Type -AssemblyName System.IO.Compression.FileSystem | Out-Null
$repoRoot = Split-Path -Parent $PSScriptRoot
$run = Join-Path $repoRoot ('.cache/tests/ci-release/' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $run -Force | Out-Null
$script = Join-Path $repoRoot 'tools/publish-github-release.ps1'
if (-not (Test-Path -LiteralPath $script -PathType Leaf)) { throw 'Missing publish-github-release.ps1' }
$passed = 0; $failed = 0; $results = @()
function Assert-Case([bool]$Condition, [string]$Name) {
    if ($Condition) { $script:passed++; Write-Host ('PASS ' + $Name) } else { $script:failed++; Write-Host ('FAIL ' + $Name) }
    $script:results += [ordered]@{ name = $Name; passed = [bool]$Condition }
}
function Get-HashBytes([byte[]]$Bytes) { $sha = [Security.Cryptography.SHA256]::Create(); try { return [BitConverter]::ToString($sha.ComputeHash($Bytes)).Replace('-','').ToLowerInvariant() } finally { $sha.Dispose() } }
function Get-HashFile([string]$Path) { return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() }
function New-TempZip([string]$Path, $Entries) {
    $archive = [IO.Compression.ZipFile]::Open($Path, [IO.Compression.ZipArchiveMode]::Create)
    try {
        foreach ($entry in $Entries) {
            $item = $archive.CreateEntry($entry.name, [IO.Compression.CompressionLevel]::Optimal)
            $stream = $item.Open()
            try { $bytes = [Text.Encoding]::UTF8.GetBytes([string]$entry.text); $stream.Write($bytes, 0, $bytes.Length) } finally { $stream.Dispose() }
        }
    } finally { $archive.Dispose() }
}
function New-Fixture([string]$Name, $Options) {
    $dir = Join-Path $script:run $Name
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    $commit = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
    $geoCommit = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
    $launcherHash = ('c' * 64)
    $tag = 'v0.6.18'
    $zipName = 'MihomoLite-' + $tag + '-' + $launcherHash.Substring(0,12) + '-win-x64.zip'
    $licenseUpstream = 'SYNTHETIC-UPSTREAM-LICENSE-TEXT-ONLY'
    $licenseGo = 'SYNTHETIC-GO-MODULE-LICENSE-TEXT'
    $indexRecords = @(
        [ordered]@{ path='LICENSES/upstream/LICENSE.txt'; sha256=(Get-HashBytes ([Text.Encoding]::UTF8.GetBytes($licenseUpstream))); size=[long][Text.Encoding]::UTF8.GetBytes($licenseUpstream).Length },
        [ordered]@{ path='LICENSES/go/LICENSE.md'; sha256=(Get-HashBytes ([Text.Encoding]::UTF8.GetBytes($licenseGo))); size=[long][Text.Encoding]::UTF8.GetBytes($licenseGo).Length }
    )
    if ($Options -and $Options.DropLicenseIndexEntry) { $indexRecords = @($indexRecords | Select-Object -First 1) }
    $indexText = [ordered]@{ schemaVersion = 1; records = $indexRecords; sourceArchives = @() } | ConvertTo-Json -Depth 6
    $entries = @(
        @{ name='MihomoLite/MihomoLite.exe'; text='SYNTHETIC-PE-PLACEHOLDER-NOT-EXECUTED' },
        @{ name='MihomoLite/runtime/mihomo.exe'; text='SYNTHETIC-CORE-PLACEHOLDER' },
        @{ name='MihomoLite/data/ui/index.html'; text='<html>synthetic</html>' },
        @{ name='MihomoLite/data/geosite.dat'; text='SYNTHETIC-GEOSITE' },
        @{ name='MihomoLite/data/geoip.dat'; text='SYNTHETIC-GEOIP' },
        @{ name='MihomoLite/data/geoip.metadb'; text='SYNTHETIC-METADB' },
        @{ name='MihomoLite/example.yaml'; text='rules: []' },
        @{ name='MihomoLite/README.md'; text='synthetic readme' },
        @{ name='MihomoLite/docs/DEVELOPMENT.md'; text='synthetic development' },
        @{ name='MihomoLite/docs/RELEASE.md'; text='synthetic release' },
        @{ name='MihomoLite/docs/CHANGELOG.md'; text='synthetic changelog' },
        @{ name='MihomoLite/docs/THIRD-PARTY.md'; text='synthetic third party policy' },
        @{ name='MihomoLite/THIRD_PARTY_NOTICES.md'; text='synthetic third party notices' },
        @{ name='MihomoLite/LICENSES/manifest.json'; text=$indexText },
        @{ name='MihomoLite/LICENSES/upstream/LICENSE.txt'; text=$licenseUpstream },
        @{ name='MihomoLite/LICENSES/go/LICENSE.md'; text=$licenseGo }
    )
    if ($Options -and $Options.ExtraPrivateEntry) { $entries += @{ name='MihomoLite/logs/launcher.log'; text='synthetic log' } }
    if ($Options -and $Options.ExtraUnlistedEntry) { $entries += @{ name='MihomoLite/tools/extra.ps1'; text='synthetic extra' } }
    $payload = @()
    foreach ($entry in $entries) {
        $bytes = [Text.Encoding]::UTF8.GetBytes([string]$entry.text)
        $payload += [ordered]@{ path = $entry.name; sha256 = (Get-HashBytes $bytes); size = [long]$bytes.Length }
    }
    $zipPath = Join-Path $dir $zipName
    New-TempZip $zipPath $entries
    if ($Options -and $Options.TamperZipContentSameSize) {
        $archive = [IO.Compression.ZipFile]::Open($zipPath, [IO.Compression.ZipArchiveMode]::Update)
        try {
            $target = $archive.Entries | Where-Object { $_.FullName -ceq 'MihomoLite/README.md' }
            $stream = $target.Open(); try { $bytes = [Text.Encoding]::UTF8.GetBytes('SYNTHETIC README'); $stream.Write($bytes, 0, $bytes.Length) } finally { $stream.Dispose() }
        } finally { $archive.Dispose() }
    }
    $zipAsset = [ordered]@{ name = $zipName; sha256 = (Get-HashFile $zipPath); size = [long](Get-Item -LiteralPath $zipPath).Length }
    if ($Options -and $Options.TamperZipBytes) { [IO.File]::AppendAllText($zipPath, 'x') }
    $zipRecord = [ordered]@{ path = $zipName; sha256 = $zipAsset.sha256; size = $zipAsset.size }
    $noticeFiles = @($payload | Where-Object { $_.path -ceq 'MihomoLite/THIRD_PARTY_NOTICES.md' -or $_.path -like 'MihomoLite/LICENSES/*' } | ForEach-Object { [ordered]@{ path = $_.path.Substring('MihomoLite/'.Length); sha256 = $_.sha256; size = $_.size } })
    $panelCommit = 'dddddddddddddddddddddddddddddddddddddddd'
    $sources = @(
        [ordered]@{ component='core'; repository='MetaCubeX/mihomo'; commit=$commit; url=('https://github.com/MetaCubeX/mihomo/archive/' + $commit + '.tar.gz'); sha256=''; size=[long]0; path='' },
        [ordered]@{ component='metadb'; repository='MetaCubeX/meta-rules-dat'; commit=$geoCommit; url=('https://github.com/MetaCubeX/meta-rules-dat/archive/' + $geoCommit + '.tar.gz'); sha256=''; size=[long]0; path='' },
        [ordered]@{ component='panel'; repository='Zephyruso/zashboard'; commit=$panelCommit; url=('https://github.com/Zephyruso/zashboard/archive/' + $panelCommit + '.tar.gz'); sha256=''; size=[long]0; path='' }
    )
    if ($Options -and $Options.BadSourceURL) { $sources[0].url = 'https://example.invalid/source.tar.gz' }
    $sourceAssets = @()
    foreach ($source in $sources) {
        if ($Options -and $Options.MissingSourceArchive -and $source.component -eq 'metadb') { continue }
        $name = 'Source-' + $source.component + '-' + $source.commit.Substring(0,12) + '.tar.gz'
        $path = Join-Path $dir $name
        [IO.File]::WriteAllText($path, ('SYNTHETIC-SOURCE-' + $source.component))
        $source.sha256 = Get-HashFile $path; $source.size = [long](Get-Item -LiteralPath $path).Length; $source.path = $path
        $sourceAssets += [ordered]@{ name = $name; sha256 = $source.sha256; size = $source.size }
    }
    $manifest = [ordered]@{
        schemaVersion = 1; version = '0.6.18'; buildId = '0.6.18-' + $launcherHash.Substring(0,12)
        binary = [ordered]@{ path = 'MihomoLite/MihomoLite.exe'; sha256 = $launcherHash; size = [long]1 }
        artifact = $zipRecord
        sourceVerified = $true; releaseNoticePolicy = 1; geoDataPolicy = 1
        files = $payload; noticeFiles = $noticeFiles
        sourceArchives = @($sources | ForEach-Object { [ordered]@{ component=$_.component; repository=$_.repository; commit=$_.commit; url=$_.url; sha256=$_.sha256; size=$_.size; path=$_.path } })
    }
    if ($Options -and $Options.DropNoticeFile) { $manifest.files = @($manifest.files | Where-Object { $_.path -cne 'MihomoLite/THIRD_PARTY_NOTICES.md' }) }
    $manifestPath = Join-Path $dir 'current.json'
    [IO.File]::WriteAllText($manifestPath, ($manifest | ConvertTo-Json -Depth 8))
    $sums = @($zipAsset.sha256 + '  ' + $zipName)
    foreach ($record in $manifest.files | Sort-Object { $_.path }) { $sums += ($record.sha256 + '  ' + $record.path) }
    [IO.File]::WriteAllText((Join-Path $dir 'SHA256SUMS.txt'), (($sums -join [Environment]::NewLine) + [Environment]::NewLine))
    [IO.File]::WriteAllText((Join-Path $dir 'RELEASE_NOTES.md'), ('Synthetic release notes for ' + $tag + [Environment]::NewLine))
    $assets = @($zipAsset) + @($sourceAssets) + @(
        [ordered]@{ name='SHA256SUMS.txt'; sha256=''; size=[long]0 },
        [ordered]@{ name='current.json'; sha256=''; size=[long]0 },
        [ordered]@{ name='RELEASE_NOTES.md'; sha256=''; size=[long]0 }
    )
    foreach ($asset in $assets) { if (-not $asset.sha256) { $asset.sha256 = Get-HashFile (Join-Path $dir $asset.name); $asset.size = [long](Get-Item -LiteralPath (Join-Path $dir $asset.name)).Length } }
    $releaseSums = @($assets | Sort-Object { $_.name } | ForEach-Object { $_.sha256 + '  ' + $_.name })
    [IO.File]::WriteAllText((Join-Path $dir 'RELEASE_SHA256SUMS.txt'), (($releaseSums -join [Environment]::NewLine) + [Environment]::NewLine))
    $assets += [ordered]@{ name='RELEASE_SHA256SUMS.txt'; sha256=(Get-HashFile (Join-Path $dir 'RELEASE_SHA256SUMS.txt')); size=[long](Get-Item -LiteralPath (Join-Path $dir 'RELEASE_SHA256SUMS.txt')).Length }
    $report = [ordered]@{
        schemaVersion = 1; status = 'verified-ready'; repository = 'orignn/Mihomo-Lite'; tag = $tag; version = '0.6.18'
        sourceCommit = $commit; launcherSHA256 = $launcherHash; package = $zipName; assets = $assets
        sourceVerified = $true; releaseNoticePolicy = 1; geoDataPolicy = 1
    }
    if ($Options -and $Options.WrongReportCommit) { $report.sourceCommit = ('d' * 40) }
    [IO.File]::WriteAllText((Join-Path $dir 'release-report.json'), ($report | ConvertTo-Json -Depth 8))
    if ($Options -and $Options.ExtraDirectoryFile) { [IO.File]::WriteAllText((Join-Path $dir 'unexpected.bin'), 'x') }
    return [pscustomobject]@{ dir = $dir; commit = $commit }
}
function Invoke-Publisher([string]$Dir, [string[]]$Arguments) {
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = @(& powershell.exe -NoProfile -ExecutionPolicy Bypass -File $script -ArtifactDirectory $Dir @Arguments 2>&1 | ForEach-Object { [string]$_ })
        $code = $LASTEXITCODE
    } finally { $ErrorActionPreference = $previous }
    return [pscustomobject]@{ exit = $code; text = ($output -join [Environment]::NewLine) }
}
function Assert-Reject([string]$Name, [string]$Pattern, $Options) {
    $fixture = New-Fixture ('bad-' + [guid]::NewGuid().ToString('N')) $Options
    $result = Invoke-Publisher $fixture.dir @('-ExpectedCommit', $fixture.commit)
    Assert-Case ($result.exit -ne 0 -and $result.text -match $Pattern) $Name
    if ($result.text -match '(?i)bearer|token=|password|[A-Z]:\\Users\\') { $script:failed++; Write-Host ('FAIL ' + $Name + ' leaked sensitive output') }
}
try {
    $good = New-Fixture 'good' $null
    $ok = Invoke-Publisher $good.dir @('-ExpectedCommit', $good.commit, '-ExpectedTag', 'v0.6.18', '-PassThru')
    Assert-Case ($ok.exit -eq 0 -and $ok.text -match 'Release assets verified offline for v0\.6\.18') 'Verified offline artifact set passes without network or publishing'
    Assert-Case ($ok.text -notmatch '(?i)invoke-restmethod|api\.github\.com') 'Offline verification makes no GitHub API request'

    $wrongCommit = Invoke-Publisher $good.dir @('-ExpectedCommit', ('e' * 40))
    Assert-Case ($wrongCommit.exit -ne 0 -and $wrongCommit.text -match 'does not match') 'Artifacts built for another commit are refused'
    $wrongTag = Invoke-Publisher $good.dir @('-ExpectedCommit', $good.commit, '-ExpectedTag', 'v9.9.9')
    Assert-Case ($wrongTag.exit -ne 0 -and $wrongTag.text -match 'does not match') 'Artifacts built for another tag are refused'
    $relative = Invoke-Publisher 'relative/path' @('-ExpectedCommit', $good.commit)
    Assert-Case ($relative.exit -ne 0 -and $relative.text -match 'absolute isolated directory') 'Relative artifact directory is refused'
    $fakePublish = Invoke-Publisher $good.dir @('-ExpectedCommit', $good.commit, '-Publish')
    Assert-Case ($fakePublish.exit -ne 0 -and $fakePublish.text -match 'official GitHub repository job token') 'Publishing is refused without the workflow job token guard'

    Assert-Reject 'Tampered ZIP bytes are refused' 'hash/size mismatch' @{ TamperZipBytes = $true }
    Assert-Reject 'ZIP payload changed at equal size is refused' 'ZIP payload digest mismatch' @{ TamperZipContentSameSize = $true }
    Assert-Reject 'ZIP with a private log path is refused' 'outside the public whitelist|Private data is forbidden' @{ ExtraPrivateEntry = $true }
    Assert-Reject 'ZIP with an unlisted extra script is refused' 'outside the public whitelist' @{ ExtraUnlistedEntry = $true }
    Assert-Reject 'Missing THIRD_PARTY_NOTICES.md payload is refused' 'License inventory mismatch|Required complete package file is absent' @{ DropNoticeFile = $true }
    Assert-Reject 'Missing corresponding source archive is refused' 'Corresponding source archive missing' @{ MissingSourceArchive = $true }
    Assert-Reject 'Unapproved upstream source URL is refused' 'Invalid fixed upstream source identity' @{ BadSourceURL = $true }
    Assert-Reject 'Unexpected file in the artifact directory is refused' 'Unexpected data in release artifact directory' @{ ExtraDirectoryFile = $true }
    Assert-Reject 'Incomplete license index coverage is refused' 'License inventory entry absent from index' @{ DropLicenseIndexEntry = $true }
    Assert-Reject 'Report fixed to an unexpected commit is refused' 'does not match the trusted repository/revision/tag' @{ WrongReportCommit = $true }
} finally {
    $payload = [ordered]@{ status = 'completed'; passed = $passed; failed = $failed; cases = $results; scope = 'offline synthetic verification only; no GitHub request, tag or Release'; at = [datetime]::UtcNow.ToString('o') }
    [IO.File]::WriteAllText((Join-Path $run 'results.json'), ($payload | ConvertTo-Json -Depth 8), (New-Object Text.UTF8Encoding($false)))
    Write-Host ('RESULT ' + (Join-Path $run 'results.json'))
}
if ($failed -gt 0) { throw ($failed.ToString() + ' offline publisher assertions failed.') }
Write-Host ('Completed ' + $passed + ' isolated offline publisher assertions.')
