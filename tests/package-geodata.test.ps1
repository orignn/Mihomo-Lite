#requires -Version 5.1
[CmdletBinding()]
param([string]$WorkspaceRoot = '')
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($WorkspaceRoot)) { $WorkspaceRoot = Split-Path -Parent $PSScriptRoot }
$root = (Resolve-Path -LiteralPath $WorkspaceRoot).Path
. (Join-Path $root 'tools/release-geodata.ps1')
$run = Join-Path $root ('.cache/tests/package-geodata/' + [guid]::NewGuid().ToString('N'))
$fixture = Join-Path $run 'fixture'
$script:Workspace = $fixture
New-Item -ItemType Directory -Path (Join-Path $fixture '.cache/downloads/release-geodata') -Force | Out-Null
$before = @('MihomoLite.exe','CURRENT.json','release/current.json','launcher.json' | ForEach-Object {
    [ordered]@{path=$_;sha256=(Get-FileHash -LiteralPath (Join-Path $root $_) -Algorithm SHA256).Hash}
})
$tokens=$null; $parseErrors=$null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $root 'tools/package.ps1'),[ref]$tokens,[ref]$parseErrors)
if ($parseErrors.Count) { throw 'Package script has parser errors.' }
$names = @('Get-PackagePath','Get-PackageFileRecord','Get-PackageGeoDataInputs','Assert-PackageInputsUnchanged','Assert-ZipMatchesPayload')
$functions = @($ast.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -in $names}, $true))
if ($functions.Count -ne $names.Count) { throw 'Cannot isolate the package validation helpers.' }
# Never executes the packaging body, real binaries, downloads or registry APIs.
foreach ($definition in $functions) { Invoke-Expression $definition.Extent.Text }
$script:passed = 0
function Assert-True([bool]$Condition,[string]$Message) {
    if (-not $Condition) { throw $Message }
    $script:passed++
    Write-Output ('PASS: ' + $Message)
}
function Assert-Throws([scriptblock]$Action,[string]$Pattern) {
    $message = $null
    try { & $Action | Out-Null } catch { $message = $_.Exception.Message }
    Assert-True ($null -ne $message -and $message -match $Pattern) ('Guard rejects: ' + $Pattern)
}
$geoRoot = '.cache/downloads/release-geodata'
Assert-Throws { Get-PackageGeoDataInputs $geoRoot } 'missing'
$sums = @{}
foreach ($name in @('geosite.dat','geoip.dat')) {
    $path = Join-Path $fixture ($geoRoot + '/' + $name)
    [IO.File]::WriteAllText($path, 'synthetic public database fixture: ' + $name)
    $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    $sums[$name] = $hash + '  ' + $name
    [IO.File]::WriteAllText(($path + '.sha256sum'), $sums[$name])
}
$records = @(Get-PackageGeoDataInputs $geoRoot)
Assert-True ($records.Count -eq 2) 'Both release databases are selected'
foreach ($record in $records) {
    Assert-True ($record.path.StartsWith('.cache/downloads/')) 'Source stays in download cache'
    Assert-True ($record.payloadPath -in @('data/geosite.dat','data/geoip.dat')) 'Payload uses the required database path'
    Assert-True ($record.checksum.sha256.Length -eq 64 -and $record.size -gt 0) 'Checksum source is recorded and payload is nonempty'
}
Assert-Throws { Get-PackageGeoDataInputs 'data' } 'runtime data is not a release source'
Assert-Throws { Get-PackageGeoDataInputs '.cache/downloads/../tests' } 'must be a verified release batch'
foreach ($name in @('geosite.dat','geoip.dat')) {
    $path = Join-Path $fixture ($geoRoot + '/' + $name)
    $original = [IO.File]::ReadAllText($path)
    [IO.File]::WriteAllText(($path + '.sha256sum'), 'not an upstream checksum')
    Assert-Throws { Get-PackageGeoDataInputs $geoRoot } 'SHA-256 does not match'
    [IO.File]::WriteAllText(($path + '.sha256sum'), ('0' * 64) + '  ' + $name)
    Assert-Throws { Get-PackageGeoDataInputs $geoRoot } 'SHA-256 does not match'
    [IO.File]::WriteAllText(($path + '.sha256sum'), $sums[$name])
    [IO.File]::WriteAllText($path, '')
    Assert-Throws { Get-PackageGeoDataInputs $geoRoot } 'Invalid release database/checksum size'
    [IO.File]::WriteAllText($path, $original + 'changed')
    Assert-Throws { Assert-PackageInputsUnchanged $records } 'Packaging input changed'
    [IO.File]::WriteAllText($path, $original)
}
$payload = Join-Path $fixture 'payload/MihomoLite'
New-Item -ItemType Directory -Path (Join-Path $payload 'data') -Force | Out-Null
$expected = @()
foreach ($record in $records) {
    $source = Get-PackagePath $record.path
    $destination = Join-Path $payload $record.payloadPath
    [IO.File]::Copy($source, $destination)
    $expected += Get-PackageFileRecord $destination ('MihomoLite/' + $record.payloadPath)
}
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$zip = Join-Path $run 'synthetic-geodata.zip'
$archive = [IO.Compression.ZipFile]::Open($zip, [IO.Compression.ZipArchiveMode]::Create)
try {
    foreach ($record in $expected) {
        [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($archive, (Join-Path $fixture ('payload/' + $record.path)), $record.path, [IO.Compression.CompressionLevel]::Fastest) | Out-Null
    }
} finally { $archive.Dispose() }
Assert-ZipMatchesPayload $zip $payload $expected
Assert-True ($expected.Count -eq 2) 'Synthetic ZIP contains both lowercase database entries with matching hashes'
Assert-PackageInputsUnchanged ($records + @($records | ForEach-Object { $_.checksum }))
foreach ($entry in $before) {
    Assert-True ((Get-FileHash -LiteralPath (Join-Path $root $entry.path) -Algorithm SHA256).Hash -eq $entry.sha256) ('Real file unchanged: ' + $entry.path)
}
[ordered]@{passed=$script:passed;scope='Isolated checksum, source, payload and ZIP tests; no real release generated';run=$run} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $run 'results.json') -Encoding utf8
Write-Output ('RESULT: ' + $script:passed + ' passed; fixtures: ' + $run)
