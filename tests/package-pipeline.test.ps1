#requires -Version 5.1
[CmdletBinding()]
param([string]$WorkspaceRoot = '')
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($WorkspaceRoot)) { $WorkspaceRoot = Split-Path -Parent $PSScriptRoot }
$root = [IO.Path]::GetFullPath($WorkspaceRoot)
$run = Join-Path $root ('.cache/tests/package-pipeline/' + [guid]::NewGuid().ToString('N'))
$fixture = Join-Path $run 'fixture'
New-Item -ItemType Directory -Path (Join-Path $fixture 'tools'),(Join-Path $fixture 'cmd/mihomolite'),(Join-Path $fixture 'data/ui'),(Join-Path $fixture 'runtime'),(Join-Path $fixture 'docs'),(Join-Path $fixture '.cache/downloads/synthetic-geodata') -Force | Out-Null
$passed = 0
function Assert-Case([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
    $script:passed++; Write-Host ('PASS: ' + $Message)
}
# Read-only launcher fixture is inspected by go version -m and never executed.
$original = (Get-FileHash -LiteralPath (Join-Path $root 'MihomoLite.exe') -Algorithm SHA256).Hash
$binary = Get-Item -LiteralPath (Join-Path $root 'MihomoLite.exe')
$version = $binary.VersionInfo.ProductVersion.Trim()
[IO.File]::Copy($binary.FullName, (Join-Path $fixture 'MihomoLite.exe'))
foreach ($relative in @('tools/build-release.ps1','tools/build-metrics.ps1','tools/release-geodata.ps1','tools/package.ps1','tools/check-workspace.ps1')) {
    [IO.File]::Copy((Join-Path $root $relative), (Join-Path $fixture $relative))
}
[IO.File]::WriteAllText((Join-Path $fixture 'cmd/mihomolite/types.go'), ('package main' + [Environment]::NewLine + 'const AppVersion = "' + $version + '"' + [Environment]::NewLine))
foreach ($relative in @('runtime/mihomo.exe','data/geoip.metadb','data/ui/index.html','example.yaml','README.md','docs/DEVELOPMENT.md','docs/RELEASE.md','docs/CHANGELOG.md')) {
    [IO.File]::WriteAllText((Join-Path $fixture $relative), 'SYNTHETIC PACKAGING FIXTURE; NEVER EXECUTE; NOT A RELEASE')
}
$geoRoot = '.cache/downloads/synthetic-geodata'
foreach ($name in @('geosite.dat','geoip.dat')) {
    $path = Join-Path $fixture ($geoRoot + '/' + $name)
    [IO.File]::WriteAllText($path, ('Synthetic geodata: ' + $name))
    $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText(($path + '.sha256sum'), $hash + '  ' + $name)
}
# Synthetic permission texts and directory table; never sourced from private runtime files.
$noticeRelative = '.cache/downloads/release-notices-synthetic/notices.json'
New-Item -ItemType Directory -Path (Join-Path $fixture 'LICENSES'),(Join-Path $fixture '.cache/downloads/release-notices-synthetic') -Force | Out-Null
$noticeRecords = @()
foreach ($relative in @('THIRD_PARTY_NOTICES.md','LICENSES/synthetic-license.txt')) {
    [IO.File]::WriteAllText((Join-Path $fixture $relative), 'SYNTHETIC PERMISSION TEXT; TEST FIXTURE ONLY; NOT A RELEASE')
    $file = Get-Item -LiteralPath (Join-Path $fixture $relative)
    $noticeRecords += [ordered]@{path=$relative;sha256=(Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant();size=[long]$file.Length}
}
[IO.File]::WriteAllText((Join-Path $fixture 'LICENSES/manifest.json'), ([ordered]@{schemaVersion=1;records=@($noticeRecords | Where-Object { $_.path -cne 'THIRD_PARTY_NOTICES.md' });sourceAssets=@()} | ConvertTo-Json -Depth 10))
$indexFile = Get-Item -LiteralPath (Join-Path $fixture 'LICENSES/manifest.json')
$noticeRecords += [ordered]@{path='LICENSES/manifest.json';sha256=(Get-FileHash -LiteralPath $indexFile.FullName -Algorithm SHA256).Hash.ToLowerInvariant();size=[long]$indexFile.Length}
[IO.File]::WriteAllText((Join-Path $fixture $noticeRelative), ([ordered]@{schemaVersion=1;status='verified-ready';redistributionReady=$true;records=$noticeRecords;sourceArchives=@();scope='synthetic-fixture-only'} | ConvertTo-Json -Depth 10))
# Duplicate only this synthetic source fixture to cover locally retained setup/internal history.
$localFixture = Join-Path $run 'fixture-with-local-setup'
Copy-Item -LiteralPath $fixture -Destination $localFixture -Recurse
foreach ($name in @('setup.ps1','UPDATE.md')) { [IO.File]::WriteAllText((Join-Path $localFixture $name), 'LOCAL ONLY SYNTHETIC FIXTURE; NEVER EXECUTE') }
$localSetupHash = (Get-FileHash -LiteralPath (Join-Path $localFixture 'setup.ps1') -Algorithm SHA256).Hash
function Assert-PublicPayload([string]$FixtureRoot, [string]$Artifact) {
    $manifest = [IO.File]::ReadAllText((Join-Path $FixtureRoot 'release/current.json')) | ConvertFrom-Json
    $excluded = @('MihomoLite/setup.ps1','MihomoLite/UPDATE.md')
    Assert-Case (@($manifest.files | Where-Object { $_.path -in $excluded }).Count -eq 0) 'New manifest excludes local setup and internal history'
    $sums = [IO.File]::ReadAllText((Join-Path $FixtureRoot 'release/SHA256SUMS.txt'))
    Assert-Case ($sums -notmatch 'MihomoLite/(setup\.ps1|UPDATE\.md)') 'New SHA256SUMS excludes local setup and internal history'
    $archive = [IO.Compression.ZipFile]::OpenRead((Join-Path $FixtureRoot $Artifact))
    try {
        $names = @($archive.Entries.FullName)
        Assert-Case (@($names | Where-Object { $_ -in $excluded }).Count -eq 0) 'New ZIP excludes local setup and internal history'
        Assert-Case (@($names | Where-Object { $_ -in @('MihomoLite/docs/DEVELOPMENT.md','MihomoLite/docs/RELEASE.md','MihomoLite/docs/CHANGELOG.md') }).Count -eq 3) 'New ZIP contains the complete public documentation set'
    } finally { $archive.Dispose() }
}
# No build manifest is fabricated: this is an adopted-existing-binary fixture,
# sourceVerified=false, with synthetic payload files. It is not EXE acceptance.
function Invoke-WebRequest { throw 'Unexpected network operation in offline package integration.' }
function Invoke-RestMethod { throw 'Unexpected network operation in offline package integration.' }
try {
    $result = & (Join-Path $fixture 'tools/build-release.ps1') -PackageOnly -GeoDataRoot $geoRoot -NoticeManifest $noticeRelative
    Assert-Case (-not (Test-Path -LiteralPath (Join-Path $fixture 'setup.ps1'))) 'Public-source fixture has no setup.ps1'
    Assert-PublicPayload $fixture $result.package.artifact
    Assert-Case ($result.status -eq 'passed' -and $result.mode -eq 'package' -and $null -eq $result.build) 'PackageOnly executes real packaging and consolidated verification without a build'
    $report = [IO.File]::ReadAllText((Join-Path $fixture $result.report)) | ConvertFrom-Json
    Assert-Case ($report.geo.source -eq 'local' -and -not $report.geo.downloaded) 'Local geodata is reused without network access'
    Assert-Case ($report.verification.status -eq 'passed' -and $report.verification.verifyPayload) 'Full payload and SHA256SUMS pass in the consolidated checker'
    Assert-Case ($report.protected.checked -eq 3 -and -not @($report.protected.changed).Count -and -not @($report.protected.added).Count -and -not @($report.protected.removed).Count) 'Synthetic runtime files remain unchanged'
    $packageReport = [IO.File]::ReadAllText((Join-Path $fixture $result.package.report)) | ConvertFrom-Json
    Assert-Case ($packageReport.status -eq 'passed' -and $packageReport.timings.status -eq 'passed' -and $packageReport.timings.durationMs -gt 0) 'Package report records successful total duration'
    Assert-Case ((@($packageReport.timings.stages.name) -join ',') -eq 'verify-inputs,stage-payload,compress,verify-package,publish') 'Package report records all five measured stages'
    $release = [IO.File]::ReadAllText((Join-Path $fixture 'release/current.json')) | ConvertFrom-Json
    Assert-Case ($release.provenance -eq 'adopted-existing-binary' -and -not $release.sourceVerified) 'Read-only executable fixture retains honest adopted-binary provenance'
    Assert-Case ($release.geoDataPolicy -eq 1 -and @($release.files | Where-Object { $_.path -in @('MihomoLite/data/geosite.dat','MihomoLite/data/geoip.dat') }).Count -eq 2) 'Both lowercase DAT payloads are present'
    $zip = Join-Path $fixture $result.package.artifact
    $hashBefore = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash
    $message = $null
    try { & (Join-Path $fixture 'tools/package.ps1') -WorkspaceRoot $fixture -GeoDataRoot $geoRoot -NoticeManifest $noticeRelative | Out-Null } catch { $message = $_.Exception.Message }
    Assert-Case ($message -match 'already exists') 'Existing same-hash package is still refused'
    Assert-Case ((Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash -eq $hashBefore) 'Refused duplicate does not overwrite the ZIP'
    Assert-Case ((Get-FileHash -LiteralPath (Join-Path $root 'MihomoLite.exe') -Algorithm SHA256).Hash -eq $original) 'Real launcher is unchanged'
    $localResult = & (Join-Path $localFixture 'tools/build-release.ps1') -PackageOnly -GeoDataRoot $geoRoot -NoticeManifest $noticeRelative
    $localReport = [IO.File]::ReadAllText((Join-Path $localFixture $localResult.report)) | ConvertFrom-Json
    Assert-Case ($localResult.status -eq 'passed' -and $localReport.verification.status -eq 'passed') 'Locally retained setup/internal history do not prevent public packaging or full verification'
    Assert-PublicPayload $localFixture $localResult.package.artifact
    Assert-Case ((Get-FileHash -LiteralPath (Join-Path $localFixture 'setup.ps1') -Algorithm SHA256).Hash -eq $localSetupHash) 'Packaging preserves locally retained setup.ps1'
    $summary = [ordered]@{passed=$passed;scope='real packaging/checker on synthetic payload and read-only adopted EXE; no build or EXE execution; no network/proxy/registry';pipeline=$result.report;run=$run}
    [IO.File]::WriteAllText((Join-Path $run 'results.json'),($summary|ConvertTo-Json -Depth 5),(New-Object Text.UTF8Encoding($false)))
    Write-Host ('RESULT: {0} passed; fixtures: {1}' -f $passed,$run)
} finally {
    Remove-Item -LiteralPath Function:Invoke-WebRequest,Function:Invoke-RestMethod
}
