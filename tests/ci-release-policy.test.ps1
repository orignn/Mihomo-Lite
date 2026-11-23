#requires -Version 5.1
[CmdletBinding()]
param([string]$WorkspaceRoot = '', [string]$PowerShellPath = 'powershell.exe')
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($WorkspaceRoot)) { $WorkspaceRoot = Split-Path -Parent $PSScriptRoot }
$root = [IO.Path]::GetFullPath($WorkspaceRoot).TrimEnd('\')
$run = Join-Path $root ('.cache/tests/ci-release/20261123-2103/package-policy/' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $run | Out-Null
$script:passed = 0; $script:failed = 0
$checker = Join-Path $root 'tools/check-workspace.ps1'
# Packaging-policy integration needs an adopted launcher, CURRENT.json and a release manifest.
# In a source-only checkout (CI before the controlled build) these do not exist yet: skip explicitly
# instead of fabricating a binary identity, and let the workflow rerun this file after packaging.
$prepared = (Test-Path -LiteralPath (Join-Path $root 'MihomoLite.exe') -PathType Leaf) -and (Test-Path -LiteralPath (Join-Path $root 'CURRENT.json') -PathType Leaf)
if (-not $prepared) {
    $payload = [ordered]@{ status = 'skipped'; reason = 'source-only checkout: no adopted launcher or CURRENT.json; policy integration runs after the controlled build'; at = [datetime]::UtcNow.ToString('o') }
    [IO.File]::WriteAllText((Join-Path $run 'results.json'), ($payload | ConvertTo-Json -Depth 6), (New-Object Text.UTF8Encoding($false)))
    Write-Output ('SKIPPED: packaging-policy integration requires a prepared workspace; ' + (Join-Path $run 'results.json'))
    exit 0
}
$before = @('MihomoLite.exe','CURRENT.json','release/current.json','launcher.json' | ForEach-Object {
    $path = Join-Path $root $_
    if (Test-Path -LiteralPath $path -PathType Leaf) { [ordered]@{path=$_;sha256=(Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash} }
})
function Assert-True([bool]$Condition, [string]$Name) {
    if (-not $Condition) { $script:failed++; Write-Output ('FAIL: ' + $Name); return }
    $script:passed++; Write-Output ('PASS: ' + $Name)
}
function Assert-Throws([scriptblock]$Action, [string]$Pattern, [string]$Name) {
    $message = $null
    try { & $Action | Out-Null } catch { $message = $_.Exception.Message }
    Assert-True ($null -ne $message -and $message -match $Pattern) $Name
}
function Write-Json([string]$Path, $Value) { [IO.File]::WriteAllText($Path, ($Value | ConvertTo-Json -Depth 30), (New-Object Text.UTF8Encoding($false))) }
function Get-Record([string]$Path, [string]$Relative) {
    [ordered]@{path=$Relative;sha256=(Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant();size=[long](Get-Item -LiteralPath $Path).Length}
}
function Load-Functions([string]$File, [string[]]$Names) {
    $tokens=$null; $errors=$null
    $ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $root $File),[ref]$tokens,[ref]$errors)
    if ($errors.Count) { throw ('Parser errors: ' + $File) }
    $definitions = @($ast.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -in $Names}, $true))
    if ($definitions.Count -ne $Names.Count) { throw ('Cannot isolate helpers: ' + $File) }
    foreach ($definition in $definitions) { . ([scriptblock]::Create($definition.Extent.Text)) }
    # Export just these definitions to the caller script scope; never execute build/package bodies.
    foreach ($name in $Names) { Set-Item -LiteralPath ('Function:script:' + $name) -Value (Get-Item -LiteralPath ('Function:' + $name)).ScriptBlock }
}
Load-Functions 'tools/package.ps1' @('Get-PackagePath','Get-PackageNoticePath','Get-PackageFileRecord','Get-PackageNoticeInputs','Assert-PackageLicenseIndex','Assert-PackageInputsUnchanged')
$payload = Join-Path $run 'payload'
New-Item -ItemType Directory -Path (Join-Path $payload 'runtime'),(Join-Path $payload 'data/ui'),(Join-Path $payload 'docs'),(Join-Path $payload 'LICENSES/upstream'),(Join-Path $payload 'LICENSES/go') -Force | Out-Null
$sourceBinary = Join-Path $root 'MihomoLite.exe'
Copy-Item -LiteralPath $sourceBinary -Destination (Join-Path $payload 'MihomoLite.exe')
$version = (Get-Item -LiteralPath $sourceBinary).VersionInfo.ProductVersion.Trim()
$binaryRecord = Get-Record $sourceBinary 'MihomoLite.exe'
foreach ($relative in @('runtime/mihomo.exe','data/geoip.metadb','data/geosite.dat','data/geoip.dat','data/ui/index.html','example.yaml','README.md','docs/DEVELOPMENT.md','docs/RELEASE.md','docs/CHANGELOG.md','docs/THIRD-PARTY.md','THIRD_PARTY_NOTICES.md','LICENSES/upstream/LICENSE.txt','LICENSES/go/LICENSE.md')) {
    [IO.File]::WriteAllText((Join-Path $payload $relative), 'SYNTHETIC POLICY FIXTURE; NEVER EXECUTE; NOT REAL LICENSE EVIDENCE')
}
$licenseRecords = @('LICENSES/upstream/LICENSE.txt','LICENSES/go/LICENSE.md' | ForEach-Object { Get-Record (Join-Path $payload $_) $_ })
Write-Json (Join-Path $payload 'LICENSES/manifest.json') ([ordered]@{schemaVersion=1;records=$licenseRecords;sourceAssets=@();scope='synthetic-fixture-only'})
$noticeFiles = @('THIRD_PARTY_NOTICES.md','LICENSES/manifest.json' | ForEach-Object { Get-Record (Join-Path $payload $_) $_ }) + $licenseRecords
$noticeRelative = '.cache/downloads/release-notices-synthetic/notices.json'
New-Item -ItemType Directory -Path (Join-Path $payload '.cache/downloads/release-notices-synthetic') -Force | Out-Null
$noticePath = Join-Path $payload $noticeRelative
$noticeData = [ordered]@{schemaVersion=1;status='verified-ready';redistributionReady=$true;records=$noticeFiles;sourceArchives=@();scope='synthetic-fixture-only'}
Write-Json $noticePath $noticeData
$script:Workspace = $payload
$selection = Get-PackageNoticeInputs $noticeRelative
Assert-True ($selection.records.Count -eq 4) 'Explicit manifest selects notices, index and all full license texts'
[IO.File]::WriteAllText((Join-Path $payload 'LICENSES/unused-private.txt'), 'UNRELATED PRIVATE TEST MARKER; MUST NOT BE READ OR SHIPPED')
$selection = Get-PackageNoticeInputs $noticeRelative
Assert-True (@($selection.records | Where-Object { $_.path -match 'unused-private' }).Count -eq 0) 'LICENSES directory junk is never discovered as package input'
Assert-Throws { Get-PackageNoticeInputs '' } 'NoticeManifest' 'New package requires explicit notice manifest'
foreach ($path in @('launcher.json','.cache/tests/notices.json','.cache/downloads/release-notices-synthetic/../notices.json','C:/private/notices.json')) {
    Assert-Throws { Get-PackageNoticeInputs $path } 'NoticeManifest' ('Manifest source path whitelist rejects ' + $path)
}
foreach ($path in @('LICENSES/../launcher.json','LICENSES/a/../../secrets.txt','LICENSES/config.yaml','LICENSES/secret.exe','LICENSES/CON.txt','LICENSES/a./secret.txt','LICENSES/a\secret.txt')) {
    Assert-Throws { Get-PackageNoticePath $path } 'path' ('License source path whitelist rejects ' + $path)
}
foreach ($relative in @('THIRD_PARTY_NOTICES.md','LICENSES/upstream/LICENSE.txt','LICENSES/go/LICENSE.md','LICENSES/manifest.json')) {
    $path = Join-Path $payload $relative; $content = [IO.File]::ReadAllText($path)
    [IO.File]::WriteAllText($path, '')
    Assert-Throws { Get-PackageNoticeInputs $noticeRelative } 'mismatch' ('Packaging rejects empty ' + $relative)
    [IO.File]::WriteAllText($path, $content)
}
$noticeData.records[2].sha256 = '0' * 64; Write-Json $noticePath $noticeData
Assert-Throws { Get-PackageNoticeInputs $noticeRelative } 'mismatch' 'Packaging rejects fake notice file digest'
$noticeData.records[2].sha256 = $licenseRecords[0].sha256
# Recreate records from bytes because the negative mutation intentionally shared an object.
$noticeFiles = @('THIRD_PARTY_NOTICES.md','LICENSES/manifest.json','LICENSES/upstream/LICENSE.txt','LICENSES/go/LICENSE.md' | ForEach-Object { Get-Record (Join-Path $payload $_) $_ })
$noticeData.records = $noticeFiles; $noticeData.status = 'redistribution-blocked'; Write-Json $noticePath $noticeData
Assert-Throws { Get-PackageNoticeInputs $noticeRelative } 'verified-ready' 'Packaging rejects blocked redistribution evidence'
$noticeData.status = 'verified-ready'; Write-Json $noticePath $noticeData
# Source archives are immutable public batch inputs, not installation ZIP records.
$archiveRelative = '.cache/downloads/release-notices-synthetic/mhCore-source-' + ('a' * 40) + '.tar.gz'
$archivePath = Join-Path $payload $archiveRelative
[IO.File]::WriteAllText($archivePath, 'SYNTHETIC PUBLIC SOURCE ARCHIVE; NOT AN ACTUAL ARCHIVE')
$archiveRecord = Get-Record $archivePath $archiveRelative
$archiveRecord.component='mhCore'; $archiveRecord.repository='MetaCubeX/mihomo'; $archiveRecord.commit='a' * 40; $archiveRecord.url='https://github.com/MetaCubeX/mihomo/archive/' + ('a' * 40) + '.tar.gz'
$noticeData.sourceArchives = @($archiveRecord); Write-Json $noticePath $noticeData
$selected = Get-PackageNoticeInputs $noticeRelative
Assert-True ($selected.sourceArchives.Count -eq 1 -and $selected.records.Count -eq 4) 'Public source archives are verified separately, never copied to ZIP inputs'
$archiveRecord.sha256 = '0' * 64; Write-Json $noticePath $noticeData
Assert-Throws { Get-PackageNoticeInputs $noticeRelative } 'source archive SHA-256/size mismatch' 'Fake source archive SHA is rejected before packaging'
$archiveRecord.sha256 = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
foreach ($badSource in @('launcher.json','data/private.zip','.cache/tests/private.tar.gz','.cache/downloads/release-notices-other/private.zip','.cache/downloads/release-notices-synthetic/../private.zip')) {
    $archiveRecord.path=$badSource; Write-Json $noticePath $noticeData
    Assert-Throws { Get-PackageNoticeInputs $noticeRelative } 'source archive|public download' ('Source archive whitelist rejects '+$badSource)
}
$archiveRecord.path=$archiveRelative; $archiveRecord.repository='attacker/private'; Write-Json $noticePath $noticeData
Assert-Throws { Get-PackageNoticeInputs $noticeRelative } 'unapproved upstream' 'Source archive upstream identities are whitelisted'
$archiveRecord.repository='MetaCubeX/mihomo'; $noticeData.sourceArchives=@(); Write-Json $noticePath $noticeData
$noticeDescriptor = Get-Record $noticePath $noticeRelative
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$script:caseNumber = 0
function New-Case([switch]$Legacy) {
    $script:caseNumber++; $fixture = Join-Path $run ('case-' + $script:caseNumber)
    New-Item -ItemType Directory -Path (Join-Path $fixture 'release'),(Join-Path $fixture 'cmd/mihomolite') -Force | Out-Null
    Copy-Item -LiteralPath $sourceBinary -Destination (Join-Path $fixture 'MihomoLite.exe')
    [IO.File]::WriteAllText((Join-Path $fixture 'cmd/mihomolite/types.go'), ('package main' + [Environment]::NewLine + 'const AppVersion = "' + $version + '"'))
    $zipName = 'MihomoLite-v' + $version + '-' + $binaryRecord.sha256.Substring(0,12) + '-win-x64.zip'
    $zip = Join-Path $fixture ('release/' + $zipName)
    $files = @()
    $archive = [IO.Compression.ZipFile]::Open($zip, [IO.Compression.ZipArchiveMode]::Create)
    try {
        foreach ($file in Get-ChildItem -LiteralPath $payload -Recurse -File) {
            $relative = $file.FullName.Substring($payload.Length + 1).Replace('\','/')
            if ($relative.StartsWith('.cache/') -or $relative -eq 'LICENSES/unused-private.txt' -or ($Legacy -and ($relative.StartsWith('LICENSES/') -or $relative -eq 'THIRD_PARTY_NOTICES.md'))) { continue }
            $name = 'MihomoLite/' + $relative
            [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($archive, $file.FullName, $name, [IO.Compression.CompressionLevel]::Fastest) | Out-Null
            $files += Get-Record $file.FullName $name
        }
    } finally { $archive.Dispose() }
    $artifact = Get-Record $zip $zipName
    $current = [ordered]@{schemaVersion=1;version=$version;buildId=($version+'-'+$binaryRecord.sha256.Substring(0,12));binary=$binaryRecord;provenance='adopted-existing-binary';sourceVerified=$false;release=[ordered]@{path=('release/'+$zipName);sha256=$artifact.sha256;manifest='release/current.json'};recordedAt=[DateTimeOffset]::UtcNow.ToString('o')}
    $manifest = [ordered]@{schemaVersion=1;version=$version;buildId=$current.buildId;binary=[ordered]@{path='MihomoLite/MihomoLite.exe';sha256=$binaryRecord.sha256;size=$binaryRecord.size};artifact=$artifact;files=$files;provenance='adopted-existing-binary';sourceVerified=$false;createdAt=$current.recordedAt}
    if (-not $Legacy) { $current.release.releaseNoticePolicy=1; $current.release.noticeManifest=$noticeDescriptor; $manifest.releaseNoticePolicy=1; $manifest.noticeManifest=$noticeDescriptor; $manifest.noticeFiles=@($noticeFiles | ForEach-Object { [ordered]@{path=$_.path;sha256=$_.sha256;size=$_.size} }); $manifest.sourceArchives=@() }
    $case = [pscustomobject]@{fixture=$fixture;zip=$zip;current=$current;manifest=$manifest}
    Save-Case $case
    return $case
}
function Save-Case($Case) {
    $artifact = Get-Record $Case.zip $Case.manifest.artifact.path
    $Case.current.release.sha256 = $artifact.sha256; $Case.manifest.artifact = $artifact
    Write-Json (Join-Path $Case.fixture 'CURRENT.json') $Case.current
    Write-Json (Join-Path $Case.fixture 'release/current.json') $Case.manifest
    $sums = @($artifact.sha256 + '  ' + $artifact.path) + @($Case.manifest.files | ForEach-Object { $_.sha256 + '  ' + $_.path })
    [IO.File]::WriteAllText((Join-Path $Case.fixture 'release/SHA256SUMS.txt'), ($sums -join [Environment]::NewLine))
}
function Change-Zip($Case, [string]$Name, [AllowNull()]$Text) {
    $absolute = [IO.Path]::GetFullPath($Case.zip)
    if (-not $absolute.StartsWith($run + '\', [StringComparison]::OrdinalIgnoreCase)) { throw 'Refusing ZIP mutation outside isolated test run.' }
    $archive = [IO.Compression.ZipFile]::Open($absolute, [IO.Compression.ZipArchiveMode]::Update)
    try {
        foreach ($entry in @($archive.Entries | Where-Object { $_.FullName -ceq $Name })) { $entry.Delete() }
        if ($null -ne $Text) { $entry = $archive.CreateEntry($Name); $writer = New-Object IO.StreamWriter ($entry.Open()); try { $writer.Write($Text) } finally { $writer.Dispose() } }
    } finally { $archive.Dispose() }
    Save-Case $Case
}
function Assert-Checker($Case, [bool]$ShouldPass, [string]$Pattern, [string]$Name, [switch]$Strict) {
    $arguments = @('-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',$checker,'-WorkspaceRoot',$Case.fixture)
    if ($Strict) { $arguments += '-VerifyPayload' }
    $output = (& $PowerShellPath @arguments 2>&1 | Out-String); $code = $LASTEXITCODE
    $good = (($code -eq 0) -eq $ShouldPass) -and ([string]::IsNullOrEmpty($Pattern) -or $output.Contains($Pattern))
    Assert-True $good $Name
    [IO.File]::AppendAllText((Join-Path $run 'checker-results.log'), ('CASE: '+$Name+'; exit='+$code+'; passed='+$good+[Environment]::NewLine+$output+[Environment]::NewLine))
    if (-not $good) { Write-Output $output }
}
$case = New-Case -Legacy
Assert-Checker $case $true 'Workspace constraints OK' 'Historical no-notice package remains strictly verifiable' -Strict
Change-Zip $case 'MihomoLite/setup.ps1' 'SYNTHETIC LEGACY SETUP; NEVER EXECUTE'
Change-Zip $case 'MihomoLite/UPDATE.md' 'SYNTHETIC LEGACY HISTORY'
Assert-Checker $case $true 'Workspace constraints OK' 'Historical setup and UPDATE remain optional legacy compatibility'
$case = New-Case
Assert-Checker $case $true 'Workspace constraints OK' 'New policy package passes full automatic hash and index verification'
foreach ($relative in @('THIRD_PARTY_NOTICES.md','LICENSES/manifest.json','LICENSES/upstream/LICENSE.txt','LICENSES/go/LICENSE.md')) {
    $case = New-Case; Change-Zip $case ('MihomoLite/'+$relative) $null
    Assert-Checker $case $false 'missing' ('Metadata cannot hide missing ZIP ' + $relative)
    $case = New-Case; Change-Zip $case ('MihomoLite/'+$relative) ''
    Assert-Checker $case $false 'empty' ('Policy rejects empty ZIP ' + $relative)
}
$case = New-Case; Change-Zip $case 'MihomoLite/LICENSES/upstream/LICENSE.txt' 'FAKE LICENSE CONTENT'
Assert-Checker $case $false 'SHA-256 does not match' 'Policy automatically rejects wrong license SHA without VerifyPayload switch'
$case = New-Case; $case.manifest.files = @($case.manifest.files | Where-Object { $_.path -cne 'MihomoLite/LICENSES/go/LICENSE.md' }); Save-Case $case
Assert-Checker $case $false 'missing from manifest.files' 'Notice metadata cannot stand in for full payload record'
$case = New-Case; $case.manifest.noticeFiles = @($case.manifest.noticeFiles | Where-Object { $_.path -cne 'LICENSES/go/LICENSE.md' }); Save-Case $case
Assert-Checker $case $false 'missing from noticeFiles' 'Embedded index prevents omitting any license from release notice table'
$case = New-Case; $case.current.release.Remove('releaseNoticePolicy'); Save-Case $case
Assert-Checker $case $false 'agree on releaseNoticePolicy' 'Root and release policy flags must agree'
$case = New-Case; $case.manifest.releaseNoticePolicy = 2; Save-Case $case
Assert-Checker $case $false 'integer 1' 'Unknown release notice policy is refused'
$case = New-Case; $case.manifest.noticeManifest = [ordered]@{path='launcher.json';sha256=$noticeDescriptor.sha256;size=$noticeDescriptor.size}; Save-Case $case
Assert-Checker $case $false 'public notices cache path' 'Notice provenance path escape or private source is refused'
$case = New-Case; Change-Zip $case 'MihomoLite/LICENSES/extra.txt' 'UNDECLARED TEXT'
Assert-Checker $case $false 'outside the payload whitelist' 'ZIP LICENSES content requires an explicit notice record'
$case = New-Case; Change-Zip $case 'MihomoLite/setup.ps1' 'NEW PACKAGE LOCAL SETUP'
Assert-Checker $case $false 'outside the payload whitelist' 'New policy cannot use legacy setup allowance'
$case = New-Case; Change-Zip $case 'MihomoLite/UPDATE.md' 'NEW PACKAGE INTERNAL HISTORY'
Assert-Checker $case $false 'outside the payload whitelist' 'New policy cannot use legacy UPDATE allowance'
$case = New-Case; $sums = Join-Path $case.fixture 'release/SHA256SUMS.txt'; [IO.File]::WriteAllText($sums, $case.manifest.artifact.sha256+'  '+$case.manifest.artifact.path)
Assert-Checker $case $false 'missing an entry' 'New policy automatically requires every notice checksum sum'
$case = New-Case
New-Item -ItemType Directory -Path (Join-Path $case.fixture '.github/workflows'),(Join-Path $case.fixture 'LICENSES') -Force | Out-Null
[IO.File]::WriteAllText((Join-Path $case.fixture '.github/workflows/release.yml'), 'name: synthetic workflow')
[IO.File]::WriteAllText((Join-Path $case.fixture 'THIRD_PARTY_NOTICES.md'), 'ROOT SYNTHETIC NOTICE')
Assert-Checker $case $true 'Workspace constraints OK' 'Root allows exact GitHub source workflow and generated release notice paths'
[IO.File]::WriteAllText((Join-Path $case.fixture '.github/workflows/random.yml'), 'name: unapproved workflow')
Assert-Checker $case $false 'unapproved file' 'GitHub workflows cannot whitelist arbitrary YAML files'
$case = New-Case; New-Item -ItemType Directory -Path (Join-Path $case.fixture '.github/actions') -Force | Out-Null
Assert-Checker $case $false 'unapproved directory' 'GitHub directory cannot permit arbitrary subdirectories'
$case = New-Case; New-Item -ItemType Directory -Path (Join-Path $case.fixture 'random-root') -Force | Out-Null
Assert-Checker $case $false 'unapproved directory' 'Root whitelist is not broadened for unrelated directories'
$case = New-Case; $case.manifest.noticeFiles[0].sha256 = '0' * 64; Save-Case $case
Assert-Checker $case $false 'noticeFiles SHA-256/size does not match' 'Forged notice table SHA cannot bypass payload verification'
$case = New-Case; $case.manifest.noticeFiles[2].path='LICENSES/../launcher.json'; Save-Case $case
Assert-Checker $case $false 'unsafe path segment' 'Notice table cannot escape the license source whitelist'
$case = New-Case; $case.manifest.sourceArchives=@([ordered]@{path='data/private.zip';sha256=$archiveRecord.sha256;size=$archiveRecord.size;repository='MetaCubeX/mihomo';commit=('a'*40);url=$archiveRecord.url}); Save-Case $case
Assert-Checker $case $false 'same-batch public source archive' 'Published source archive metadata rejects private paths without reading them'
$case = New-Case; $case.manifest.Remove('releaseNoticePolicy'); $case.current.release.Remove('releaseNoticePolicy'); Save-Case $case
Assert-Checker $case $false 'Notice metadata requires' 'Removing both flags cannot disguise new notice metadata as a legacy package'
$case = New-Case; $case.manifest.noticeFiles=@($case.manifest.noticeFiles | Where-Object { $_.path -cne 'LICENSES/go/LICENSE.md' }); $case.manifest.files=@($case.manifest.files | Where-Object { $_.path -cne 'MihomoLite/LICENSES/go/LICENSE.md' }); Change-Zip $case 'MihomoLite/LICENSES/go/LICENSE.md' $null
Assert-Checker $case $false 'missing from noticeFiles' 'Removing both license bytes and metadata cannot bypass the embedded complete index'
$case = New-Case; Change-Zip $case 'MihomoLite/docs/THIRD-PARTY.md' $null; $case.manifest.files=@($case.manifest.files | Where-Object { $_.path -cne 'MihomoLite/docs/THIRD-PARTY.md' }); Save-Case $case
Assert-Checker $case $true 'Workspace constraints OK' 'Fourth public doc remains optional for historical fixtures with notice policy'
$case = New-Case; New-Item -ItemType Directory -Path (Join-Path $case.fixture '.github/workflows/release.yml') -Force | Out-Null
Assert-Checker $case $false 'unapproved directory' 'Fixed GitHub workflow name must be a source file, not a directory'
Load-Functions 'build.ps1' @('Get-WorkspacePath','Get-SafeFiles','Get-FileRecord','Get-SourceManifest','Assert-SourceUnchanged')
$script:Workspace = $root
$source = @(Get-SourceManifest)
Assert-True (@($source | Where-Object { $_.path -ceq 'tests/ci-release-policy.test.ps1' }).Count -eq 1) 'Build source manifest records the new release-policy test without Go reruns'
foreach ($relative in @('.github/workflows/release.yml','tools/build-ci-release.ps1','tools/publish-github-release.ps1','tools/prepare-release-notices.ps1','tools/prepare-release-components.ps1','tests/ci-release.test.ps1','tests/github-release.test.ps1')) {
    if (Test-Path -LiteralPath (Join-Path $root $relative) -PathType Leaf) { Assert-True (@($source | Where-Object { $_.path -ceq $relative }).Count -eq 1) ('Build source manifest covers present '+$relative) }
}
$legacySource = Join-Path $run 'historical-source-fixture'
$optional = @('tools/build-ci-release.ps1','tools/publish-github-release.ps1','tools/release-download.ps1','tools/release-components.ps1','tools/prepare-release-components.ps1','tools/release-notices.ps1','tools/prepare-release-notices.ps1','tests/ci-release-policy.test.ps1','tests/ci-release.test.ps1','tests/github-release.test.ps1','tests/release-components.test.ps1','tests/release-notices.test.ps1','.github/workflows/release.yml','docs/THIRD-PARTY.md')
foreach ($record in $source) {
    if ($optional -ccontains $record.path) { continue }
    $path = Join-Path $legacySource $record.path
    New-Item -ItemType Directory -Path ([IO.Path]::GetDirectoryName($path)) -Force | Out-Null
    [IO.File]::WriteAllText($path, 'HISTORICAL SOURCE MANIFEST HASH FIXTURE; NEVER BUILD')
}
$script:Workspace = $legacySource; $historical = @(Get-SourceManifest)
Assert-True ($historical.Count -gt 0 -and @($historical | Where-Object { $optional -ccontains $_.path }).Count -eq 0) 'Historical source fixture need not fabricate new release tools or workflow'
Assert-SourceUnchanged $historical
New-Item -ItemType Directory -Path (Join-Path $legacySource '.github/workflows') -Force | Out-Null
[IO.File]::WriteAllText((Join-Path $legacySource '.github/workflows/release.yml'), 'name: synthetic source-set change')
Assert-Throws { Assert-SourceUnchanged $historical } 'Source file set changed' 'Adding a key release input during a build invalidates its source manifest'
foreach ($record in $before) { Assert-True ((Get-FileHash -LiteralPath (Join-Path $root $record.path) -Algorithm SHA256).Hash -eq $record.sha256) ('Real protected metadata unchanged: '+$record.path) }
$summary = [ordered]@{passed=$script:passed;failed=$script:failed;run=$run;scope='synthetic license/index/ZIP policy fixtures, adopted EXE inspected only; no Go build/default test, network, launcher/core execution, proxy, registry or root publication'}
Write-Json (Join-Path $run 'results.json') $summary
Write-Output ('RESULT: '+$script:passed+' passed; '+$script:failed+' failed; '+$run)
if ($script:failed) { throw 'CI release policy fixture assertions failed.' }
