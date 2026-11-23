#requires -Version 5.1
[CmdletBinding()]
param(
    [string]$WorkspaceRoot = '',
    [string]$PowerShellPath = 'powershell.exe'
)
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($WorkspaceRoot)) { $WorkspaceRoot = Split-Path -Parent $PSScriptRoot }
# Fixtures only; never executes either copied launcher, or a mihomo core.
$workspace = [IO.Path]::GetFullPath($WorkspaceRoot)
$checker = Join-Path $workspace 'tools/check-workspace.ps1'
$sourceBinary = Join-Path $workspace 'MihomoLite.exe'
if (-not (Test-Path -LiteralPath $checker -PathType Leaf)) { throw 'Checker script is missing.' }
if (-not (Test-Path -LiteralPath $sourceBinary -PathType Leaf)) { throw 'A versioned root binary is needed as a read-only fixture source.' }
$script:RunRoot = Join-Path $workspace ('.cache/tests/build-pipeline-verification/' + [Guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($script:RunRoot)
$script:RunPrefix = [IO.Path]::GetFullPath($script:RunRoot) + [IO.Path]::DirectorySeparatorChar
$fixture = Join-Path $script:RunRoot 'fixture'
$stage = Join-Path $script:RunRoot 'zip-stage'
[void][IO.Directory]::CreateDirectory((Join-Path $fixture 'cmd/mihomolite'))
[void][IO.Directory]::CreateDirectory((Join-Path $fixture '.cache/tests'))
[void][IO.Directory]::CreateDirectory((Join-Path $stage 'MihomoLite'))
[IO.File]::Copy($sourceBinary, (Join-Path $fixture 'MihomoLite.exe'))
[IO.File]::Copy($sourceBinary, (Join-Path $stage 'MihomoLite/MihomoLite.exe'))
[void][IO.Directory]::CreateDirectory((Join-Path $stage 'MihomoLite/runtime'))
[void][IO.Directory]::CreateDirectory((Join-Path $stage 'MihomoLite/data/ui'))
[void][IO.Directory]::CreateDirectory((Join-Path $stage 'MihomoLite/docs'))
foreach ($relative in @('runtime/mihomo.exe', 'data/geoip.metadb', 'data/geosite.dat', 'data/geoip.dat', 'data/ui/index.html', 'example.yaml', 'README.md', 'docs/DEVELOPMENT.md', 'docs/RELEASE.md', 'docs/CHANGELOG.md')) {
    [IO.File]::WriteAllText((Join-Path $stage ('MihomoLite/' + $relative)), 'fixture only; never execute')
}
$binary = Get-Item -LiteralPath $sourceBinary
$version = $binary.VersionInfo.ProductVersion.Trim()
$binaryHash = (Get-FileHash -LiteralPath $sourceBinary -Algorithm SHA256).Hash.ToLowerInvariant()
$binarySize = $binary.Length
$sourceText = 'package main' + [Environment]::NewLine + 'const AppVersion = "' + $version + '"' + [Environment]::NewLine
[IO.File]::WriteAllText((Join-Path $fixture 'cmd/mihomolite/types.go'), $sourceText)
[IO.File]::WriteAllText((Join-Path $stage 'MihomoLite/launcher.example.json'), '{"listen":"127.0.0.1:17653"}')
$script:Passes = 0
$script:Failures = 0
function Assert-Case([string]$Name, [bool]$ShouldPass, [string]$ExpectedText, [switch]$Unpublished, [switch]$GeoData, [switch]$Payload) {
    $arguments = @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $checker, '-WorkspaceRoot', $fixture)
    if ($Unpublished) { $arguments += '-AllowUnpublished' }
    if ($GeoData) { $arguments += '-RequireGeoData' }
    if ($Payload) { $arguments += '-VerifyPayload' }
    $output = (& $PowerShellPath @arguments 2>&1 | Out-String)
    $code = $LASTEXITCODE
    $good = (($code -eq 0) -eq $ShouldPass) -and ([string]::IsNullOrEmpty($ExpectedText) -or $output.Contains($ExpectedText))
    if ($good) { $script:Passes++; Write-Output "PASS: $Name" }
    else { $script:Failures++; Write-Output "FAIL: $Name (exit $code)"; Write-Output $output }
    [IO.File]::AppendAllText((Join-Path $script:RunRoot 'results.log'), "CASE: $Name; exit=$code; passed=$good" + [Environment]::NewLine + $output + [Environment]::NewLine)
}
function Remove-FixturePath([string]$Path) {
    $absolute = [IO.Path]::GetFullPath($Path)
    if (-not $absolute.StartsWith($script:RunPrefix, [StringComparison]::OrdinalIgnoreCase)) { throw "Refusing to remove a non-fixture path: $absolute" }
    if (Test-Path -LiteralPath $absolute) { Remove-Item -LiteralPath $absolute -Recurse -Force }
}
function Write-Json([string]$Path, $Value) { [IO.File]::WriteAllText($Path, ($Value | ConvertTo-Json -Depth 20)) }

Assert-Case 'unpublished workspace can omit manifests and release' $true 'Workspace constraints OK' -Unpublished
Assert-Case 'strict mode requires a publication' $false 'CURRENT.json is required'
Assert-Case 'payload verification cannot silently skip unpublished releases' $false '-VerifyPayload requires an adopted published release' -Unpublished -Payload
$release = Join-Path $fixture 'release'
[void][IO.Directory]::CreateDirectory($release)
$zipName = 'MihomoLite-v' + $version + '-' + $binaryHash.Substring(0, 12) + '-win-x64.zip'
$zipPath = Join-Path $release $zipName
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
# .NET Framework CreateFromDirectory emits backslashes; use the release packager's explicit canonical names.
$archive = [IO.Compression.ZipFile]::Open($zipPath, [IO.Compression.ZipArchiveMode]::Create)
try {
    foreach ($file in Get-ChildItem -LiteralPath $stage -Recurse -File) {
        $name = $file.FullName.Substring($stage.Length + 1).Replace('\', '/')
        [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($archive, $file.FullName, $name, [IO.Compression.CompressionLevel]::Fastest) | Out-Null
    }
} finally { $archive.Dispose() }
$originalZip = Join-Path $script:RunRoot 'baseline.zip'
[IO.File]::Copy($zipPath, $originalZip)
$currentPath = Join-Path $fixture 'CURRENT.json'
$manifestPath = Join-Path $release 'current.json'
$createdAt = [DateTimeOffset]::UtcNow.ToString('o')
function Reset-Manifests {
    $zipHash = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $zipSize = (Get-Item -LiteralPath $zipPath).Length
    $current = [ordered]@{
        schemaVersion = 1; version = $version; buildId = ($version + '-' + $binaryHash.Substring(0, 12));
        binary = [ordered]@{ path = 'MihomoLite.exe'; sha256 = $binaryHash; size = $binarySize };
        provenance = 'adopted-existing-binary'; sourceVerified = $false;
        release = [ordered]@{ path = ('release/' + $zipName); sha256 = $zipHash; manifest = 'release/current.json' };
        recordedAt = $createdAt
    }
    $manifest = [ordered]@{
        version = $version; buildId = $current.buildId;
        binary = [ordered]@{ path = 'MihomoLite/MihomoLite.exe'; sha256 = $binaryHash; size = $binarySize };
        artifact = [ordered]@{ path = $zipName; sha256 = $zipHash; size = $zipSize }; createdAt = $createdAt
    }
    Write-Json $currentPath $current
    Write-Json $manifestPath $manifest
}
function Reset-Zip {
    Remove-FixturePath $zipPath
    [IO.File]::Copy($originalZip, $zipPath)
    Reset-Manifests
}
function Remove-ZipEntry([string]$Name) {
    $archive = [IO.Compression.ZipFile]::Open($zipPath, [IO.Compression.ZipArchiveMode]::Update)
    try {
        $entries = @($archive.Entries | Where-Object { $_.FullName.Replace('\', '/') -ceq $Name })
        if ($entries.Count -ne 1) { throw 'Fixture ZIP entry is not unique.' }
        $entries[0].Delete()
    } finally { $archive.Dispose() }
    Reset-Manifests
}
function Add-ZipEntry([string]$Name, [string]$Text) {
    $archive = [IO.Compression.ZipFile]::Open($zipPath, [IO.Compression.ZipArchiveMode]::Update)
    try {
        $entry = $archive.CreateEntry($Name)
        $writer = New-Object IO.StreamWriter ($entry.Open())
        try { $writer.Write($Text) } finally { $writer.Dispose() }
    } finally { $archive.Dispose() }
    Reset-Manifests
}
Reset-Manifests
Assert-Case 'matching manifests and package including safe example' $true 'Workspace constraints OK'
Assert-Case 'both geo databases satisfy the new release policy' $true 'Workspace constraints OK' -GeoData
foreach ($name in @('DEVELOPMENT.md','RELEASE.md','CHANGELOG.md')) {
    Remove-ZipEntry ('MihomoLite/docs/' + $name)
    Assert-Case ('public package rejects missing ' + $name) $false ('missing required complete-package payload: ''MihomoLite/docs/' + $name + '''')
    Add-ZipEntry ('MihomoLite/docs/' + $name) ''
    Assert-Case ('public package rejects empty ' + $name) $false ('required payload file is empty: ''MihomoLite/docs/' + $name + '''')
    Reset-Zip
}
foreach ($name in @('DEVELOPMENT.md','RELEASE.md','CHANGELOG.md')) { Remove-ZipEntry ('MihomoLite/docs/' + $name) }
Assert-Case 'package cannot omit both public docs and historical UPDATE' $false 'missing required complete-package payload'
Add-ZipEntry 'MihomoLite/UPDATE.md' 'fixture historical update'
Assert-Case 'historical UPDATE package may omit public docs and setup' $true 'Workspace constraints OK' -GeoData
Add-ZipEntry 'MihomoLite/setup.ps1' 'fixture historical setup'
Assert-Case 'historical setup and UPDATE remain accepted' $true 'Workspace constraints OK' -GeoData
Reset-Zip
Add-ZipEntry 'MihomoLite/docs/private-history.md' 'fixture forbidden documentation'
Assert-Case 'unapproved documentation remains outside the payload whitelist' $false 'outside the payload whitelist'
Reset-Zip
foreach ($name in @('runtime/mihomo.exe','data/geoip.metadb','data/ui/index.html')) {
    Remove-ZipEntry ('MihomoLite/' + $name)
    Assert-Case ('required component remains mandatory: ' + $name) $false ('missing required complete-package payload: ''MihomoLite/' + $name + '''')
    Reset-Zip
}
foreach ($name in @('geosite.dat', 'geoip.dat')) {
    Reset-Zip
    Remove-ZipEntry ('MihomoLite/data/' + $name)
    Assert-Case ('new policy rejects missing ' + $name) $false ('missing required complete-package payload: ''MihomoLite/data/' + $name + '''') -GeoData
    $policyCurrent = [IO.File]::ReadAllText($currentPath) | ConvertFrom-Json
    $policyManifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
    $policyCurrent.release | Add-Member -NotePropertyName geoDataPolicy -NotePropertyValue 1
    $policyManifest | Add-Member -NotePropertyName geoDataPolicy -NotePropertyValue 1
    Write-Json $currentPath $policyCurrent
    Write-Json $manifestPath $policyManifest
    Assert-Case ('declared policy rejects missing ' + $name + ' without CLI flag') $false ('missing required complete-package payload: ''MihomoLite/data/' + $name + '''')
    Reset-Zip
    Remove-ZipEntry ('MihomoLite/data/' + $name)
    Add-ZipEntry ('MihomoLite/data/' + $name) ''
    Assert-Case ('new policy rejects empty ' + $name) $false ('required payload file is empty: ''MihomoLite/data/' + $name + '''') -GeoData
}
Reset-Zip
if ([version]$version -lt [version]'0.6.11') {
    Remove-ZipEntry 'MihomoLite/data/geosite.dat'
    Remove-ZipEntry 'MihomoLite/data/geoip.dat'
    Assert-Case 'pre-policy historic release is not retroactively republished' $true 'Workspace constraints OK'
    Reset-Zip
}
$unknown = Join-Path $fixture 'unknown.txt'
[IO.File]::WriteAllText($unknown, 'fixture')
Assert-Case 'unknown root file is rejected' $false 'unapproved file'
Remove-FixturePath $unknown
$history = Join-Path $fixture 'MihomoLite-v0.0.0.exe'
[IO.File]::WriteAllText($history, 'fixture')
Assert-Case 'historical root exe is rejected' $false 'MihomoLite-v0.0.0.exe'
Remove-FixturePath $history
$unknownCache = Join-Path $fixture '.cache/old-build'
[void][IO.Directory]::CreateDirectory($unknownCache)
Assert-Case 'unknown cache directory is rejected' $false 'unapproved directory'
Remove-FixturePath $unknownCache
$extraZip = Join-Path $release 'old.zip'
[IO.File]::WriteAllText($extraZip, 'fixture')
Assert-Case 'unreferenced release zip is rejected' $false 'old.zip'
Remove-FixturePath $extraZip
$current = [IO.File]::ReadAllText($currentPath) | ConvertFrom-Json
$current.binary.size = $binarySize + 1
Write-Json $currentPath $current
Assert-Case 'root manifest binary size mismatch is rejected' $false 'actual root binary size'
Reset-Manifests
$current = [IO.File]::ReadAllText($currentPath) | ConvertFrom-Json
$current.release.path = '../outside.zip'
Write-Json $currentPath $current
Assert-Case 'manifest traversal is rejected' $false 'unsafe path segment'
Reset-Manifests
[IO.File]::WriteAllText((Join-Path $fixture 'cmd/mihomolite/types.go'), 'const AppVersion = "999.0.0"')
Assert-Case 'source and EXE versions must match' $false 'does not match source AppVersion'
[IO.File]::WriteAllText((Join-Path $fixture 'cmd/mihomolite/types.go'), $sourceText)
Add-ZipEntry 'MihomoLite/../outside.txt' 'fixture'
Assert-Case 'ZIP traversal is rejected without extraction' $false 'unsafe path segment'
Reset-Zip
Add-ZipEntry 'OtherRoot/readme.txt' 'fixture'
Assert-Case 'ZIP naming root escape is rejected' $false 'outside the MihomoLite/ naming root'
Reset-Zip
Add-ZipEntry 'MihomoLite/launcher.json' '{"fake":"fixture only"}'
Assert-Case 'real-settings filename is forbidden in package' $false 'forbidden real configuration file'
Reset-Zip
Add-ZipEntry 'MihomoLite/data/profiles/example.yaml' 'fixture only'
Assert-Case 'profiles directory is forbidden in package' $false 'forbidden private/cache/log directory'
Reset-Zip
$archive = [IO.Compression.ZipFile]::Open($zipPath, [IO.Compression.ZipArchiveMode]::Update)
try {
    $launcherEntry = @($archive.Entries | Where-Object { $_.FullName.Replace('\', '/') -ceq 'MihomoLite/MihomoLite.exe' })
    if ($launcherEntry.Count -ne 1) { throw 'Fixture ZIP does not have a unique launcher entry.' }
    $launcherEntry[0].Delete()
    $entry = $archive.CreateEntry('MihomoLite/MihomoLite.exe')
    $writer = New-Object IO.StreamWriter ($entry.Open())
    try { $writer.Write('not the root binary') } finally { $writer.Dispose() }
} finally { $archive.Dispose() }
Reset-Manifests
Assert-Case 'ZIP launcher hash mismatch is rejected' $false 'ZIP launcher SHA-256 does not match'
Reset-Zip
$manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
$manifest.artifact.path = 'other.zip'
Write-Json $manifestPath $manifest
Assert-Case 'release manifest must reference the current artifact' $false 'artifact.path must be the current ZIP filename'
Reset-Manifests
$current = [IO.File]::ReadAllText($currentPath) | ConvertFrom-Json
$current.release = $null
Write-Json $currentPath $current
Remove-FixturePath $manifestPath
Remove-FixturePath $zipPath
Assert-Case 'promoted but unpackaged CURRENT passes only unpublished mode' $true 'Workspace constraints OK' -Unpublished
Assert-Case 'strict promoted-but-unpackaged check requests packaging' $false 'run tools/package.ps1'
Reset-Zip
Assert-Case 'final fixture remains a valid strict publication' $true 'Workspace constraints OK'

# Strong payload checks use only the synthetic package above and the read-only launcher copy.
# Establish expected records from the fixture source files, independently of the ZIP verifier.
$payloadFiles = @(Get-ChildItem -LiteralPath (Join-Path $stage 'MihomoLite') -Recurse -File | Sort-Object FullName | ForEach-Object {
    [ordered]@{path=$_.FullName.Substring($stage.Length + 1).Replace('\','/');size=[long]$_.Length;sha256=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()}
})
$sumPath = Join-Path $release 'SHA256SUMS.txt'
function Reset-PayloadMetadata {
    Reset-Manifests
    $manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
    $manifest | Add-Member -NotePropertyName files -NotePropertyValue $payloadFiles
    Write-Json $manifestPath $manifest
    $sumLines = @($manifest.artifact.sha256 + '  ' + $zipName) + @($payloadFiles | ForEach-Object { $_.sha256 + '  ' + $_.path })
    [IO.File]::WriteAllLines($sumPath, [string[]]$sumLines)
}
Reset-PayloadMetadata
Assert-Case 'single-pass payload verification accepts a complete synthetic release' $true 'Workspace constraints OK' -Payload
# Historical optional setup is not exempt from file records or SHA256SUMS verification.
$publicPayloadFiles = $payloadFiles
$legacyRecords = @()
foreach ($name in @('setup.ps1','UPDATE.md')) {
    $text = 'fixture historical ' + $name
    $legacyPath = Join-Path $script:RunRoot ('legacy-' + $name + '.txt')
    [IO.File]::WriteAllText($legacyPath, $text, (New-Object Text.UTF8Encoding($false)))
    $legacyRecords += [ordered]@{path=('MihomoLite/' + $name);size=[long](Get-Item -LiteralPath $legacyPath).Length;sha256=(Get-FileHash -LiteralPath $legacyPath -Algorithm SHA256).Hash.ToLowerInvariant()}
    Add-ZipEntry ('MihomoLite/' + $name) $text
}
foreach ($name in @('DEVELOPMENT.md','RELEASE.md','CHANGELOG.md')) { Remove-ZipEntry ('MihomoLite/docs/' + $name) }
$payloadFiles = @($publicPayloadFiles | Where-Object { $_.path -notlike 'MihomoLite/docs/*' }) + $legacyRecords
Reset-PayloadMetadata
Assert-Case 'historical setup and UPDATE pass full payload verification without public docs' $true 'Workspace constraints OK' -Payload
$manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
$setupRecord = @($manifest.files | Where-Object { $_.path -ceq 'MihomoLite/setup.ps1' })[0]
$setupRecord.sha256 = '0' * 64
Write-Json $manifestPath $manifest
Assert-Case 'historical setup SHA-256 mismatch is still rejected' $false 'ZIP file SHA-256 does not match manifest.files' -Payload
Reset-PayloadMetadata
$manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
$manifest.files = @($manifest.files | Where-Object { $_.path -cne 'MihomoLite/setup.ps1' })
Write-Json $manifestPath $manifest
Assert-Case 'historical setup missing payload record is still rejected' $false 'ZIP file is missing from manifest.files' -Payload
Reset-PayloadMetadata
$lines = @([IO.File]::ReadAllLines($sumPath) | Where-Object { -not $_.EndsWith('  MihomoLite/setup.ps1') })
[IO.File]::WriteAllLines($sumPath, [string[]]$lines)
Assert-Case 'historical setup missing checksum is still rejected' $false 'SHA256SUMS.txt is missing an entry' -Payload
$payloadFiles = $publicPayloadFiles
Reset-Zip
Reset-PayloadMetadata
foreach ($name in @('DEVELOPMENT.md','RELEASE.md','CHANGELOG.md')) {
    Remove-ZipEntry ('MihomoLite/docs/' + $name)
    $payloadFiles = @($publicPayloadFiles | Where-Object { $_.path -cne ('MihomoLite/docs/' + $name) })
    Reset-PayloadMetadata
    Assert-Case ('strong mode requires public doc even with matching records/checksums: ' + $name) $false 'missing required complete-package payload' -Payload
    $payloadFiles = $publicPayloadFiles
    Reset-Zip
    Reset-PayloadMetadata
}
$manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
$manifest.PSObject.Properties.Remove('files')
Write-Json $manifestPath $manifest
Assert-Case 'historical default does not require payload records' $true 'Workspace constraints OK'
Assert-Case 'payload verification requires the file array' $false 'files must be a non-empty array' -Payload
Reset-PayloadMetadata
$manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
$manifest.files = $manifest.files[0]
Write-Json $manifestPath $manifest
Assert-Case 'payload records cannot be a single JSON object' $false 'files must be a non-empty array' -Payload
Reset-PayloadMetadata
$manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
$manifest.files[0].sha256 = '0' * 64
Write-Json $manifestPath $manifest
Assert-Case 'payload SHA-256 mismatch is rejected' $false 'ZIP file SHA-256 does not match manifest.files' -Payload
Reset-PayloadMetadata
$manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
$manifest.files = @($manifest.files | Where-Object { $_.path -cne 'MihomoLite/README.md' })
Write-Json $manifestPath $manifest
Assert-Case 'payload omission is rejected' $false 'ZIP file is missing from manifest.files' -Payload
Reset-PayloadMetadata
$manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
$extra = [pscustomobject]@{path='MihomoLite/data/ui/absent.js';size=0;sha256=('0' * 64)}
$manifest.files = @($manifest.files) + @($extra)
Write-Json $manifestPath $manifest
Assert-Case 'extra payload manifest record is rejected' $false 'manifest.files entry is missing from ZIP' -Payload
Reset-PayloadMetadata
foreach ($caseCollision in @($false, $true)) {
    $manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
    $duplicate = [pscustomobject]@{path=$manifest.files[0].path;size=$manifest.files[0].size;sha256=$manifest.files[0].sha256}
    if ($caseCollision) { $duplicate.path = 'MihomoLite/' + $duplicate.path.Substring(11).ToUpperInvariant() }
    $manifest.files = @($manifest.files) + @($duplicate)
    Write-Json $manifestPath $manifest
    Assert-Case ('duplicate or case-colliding manifest record is rejected: ' + $caseCollision) $false 'duplicate or case-colliding path' -Payload
    Reset-PayloadMetadata
}
$manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
$manifest.files[0].size++
Write-Json $manifestPath $manifest
Assert-Case 'payload size mismatch is rejected' $false 'ZIP file size does not match manifest.files' -Payload
foreach ($badSize in @(-1, 1.5, '12', $true, [decimal]'9223372036854775808')) {
    Reset-PayloadMetadata
    $manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
    $manifest.files[0].size = $badSize
    Write-Json $manifestPath $manifest
    Assert-Case ('illegal payload size is rejected: ' + $badSize) $false 'size must be a non-negative Int64 integer' -Payload
}
foreach ($badPath in @('MihomoLite/../escape.js', 'OtherRoot/file.js', 'MihomoLite\README.md', 'MihomoLite/data/ui/')) {
    Reset-PayloadMetadata
    $manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
    $manifest.files[0].path = $badPath
    Write-Json $manifestPath $manifest
    Assert-Case ('illegal payload path is rejected: ' + $badPath) $false 'files[0].path' -Payload
}
Reset-PayloadMetadata
$manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
$manifest.files[0].sha256 = 'not-a-hash'
Write-Json $manifestPath $manifest
Assert-Case 'illegal payload SHA-256 is rejected' $false 'must be a lowercase SHA-256' -Payload
Reset-PayloadMetadata
Remove-FixturePath $sumPath
Assert-Case 'payload verification requires SHA256SUMS' $false 'SHA256SUMS.txt is required' -Payload
Reset-PayloadMetadata
$baselineSums = [IO.File]::ReadAllLines($sumPath)
foreach ($index in @(0, 1)) {
    $lines = [string[]]$baselineSums.Clone()
    $lines[$index] = ('0' * 64) + $lines[$index].Substring(64)
    [IO.File]::WriteAllLines($sumPath, $lines)
    $expected = if ($index -eq 0) { 'ZIP SHA-256 does not match the actual ZIP' } else { 'SHA-256 does not match manifest.files' }
    Assert-Case ('checksum mismatch is rejected for ZIP or payload: ' + $index) $false $expected -Payload
}
foreach ($index in @(0, 1)) {
    $lines = @(for ($i = 0; $i -lt $baselineSums.Count; $i++) { if ($i -ne $index) { $baselineSums[$i] } })
    [IO.File]::WriteAllLines($sumPath, [string[]]$lines)
    Assert-Case ('missing ZIP or payload checksum is rejected: ' + $index) $false 'SHA256SUMS.txt is missing an entry' -Payload
}
foreach ($caseCollision in @($false, $true)) {
    $extraLine = $baselineSums[1]
    if ($caseCollision) { $extraLine = $extraLine.Substring(0, 66) + $extraLine.Substring(66).ToUpperInvariant() }
    [IO.File]::WriteAllLines($sumPath, [string[]](@($baselineSums) + @($extraLine)))
    Assert-Case ('duplicate or case-colliding checksum is rejected: ' + $caseCollision) $false 'SHA256SUMS.txt contains a duplicate or case-colliding path' -Payload
}
[IO.File]::WriteAllLines($sumPath, [string[]](@($baselineSums) + @(('0' * 64) + '  MihomoLite/data/ui/unknown.js')))
Assert-Case 'unknown checksum entry is rejected' $false 'SHA256SUMS.txt contains an unknown path' -Payload
[IO.File]::WriteAllLines($sumPath, [string[]](@($baselineSums) + @('bad-hash  MihomoLite/README.md')))
Assert-Case 'malformed checksum entry is rejected' $false 'must contain a lowercase SHA-256 and one file path' -Payload
[IO.File]::WriteAllLines($sumPath, [string[]](@($baselineSums) + @(('0' * 64) + '  ../escape.zip')))
Assert-Case 'checksum traversal is rejected' $false 'unsafe path segment' -Payload
Reset-PayloadMetadata
foreach ($entryName in @('MihomoLite/README.md', 'MihomoLite/readme.md')) {
    Add-ZipEntry $entryName 'fixture duplicate'
    Reset-PayloadMetadata
    Assert-Case ('duplicate or case-colliding ZIP payload is rejected: ' + $entryName) $false 'ZIP contains a duplicate or case-colliding entry' -Payload
    Reset-Zip
}
Reset-PayloadMetadata
Remove-ZipEntry 'MihomoLite/data/geosite.dat'
Reset-PayloadMetadata
Assert-Case 'payload verification still requires both DAT files' $false 'missing required complete-package payload' -Payload
Reset-Zip
Reset-PayloadMetadata
$policyCurrent = [IO.File]::ReadAllText($currentPath) | ConvertFrom-Json
$policyCurrent.release | Add-Member -NotePropertyName geoDataPolicy -NotePropertyValue 1
Write-Json $currentPath $policyCurrent
Assert-Case 'payload verification preserves Geo policy metadata agreement' $false 'must agree on geoDataPolicy' -Payload
Reset-PayloadMetadata
Assert-Case 'final payload fixture remains valid' $true 'Workspace constraints OK' -Payload
Write-Output ("RESULT: {0} passed, {1} failed. Fixtures retained at {2}" -f $script:Passes, $script:Failures, $script:RunRoot)
if ($script:Failures -gt 0) { exit 1 }
exit 0
