#requires -Version 5.1
[CmdletBinding()]
param([string]$WorkspaceRoot = '')
$ErrorActionPreference = 'Stop'
if (-not $WorkspaceRoot) { $WorkspaceRoot = Split-Path -Parent $PSScriptRoot }
. (Join-Path $WorkspaceRoot 'tools/release-geodata.ps1')
$prepare = Join-Path $WorkspaceRoot 'tools/prepare-geodata.ps1'
$run = Get-ReleaseGeoWorkspaceRoot $WorkspaceRoot
foreach ($segment in @('.cache', 'tests', 'build-pipeline-geodata')) {
    $run = Join-Path $run $segment
    $null = Get-ReleaseGeoItemAttributes $run -Directory -AllowMissing
    $null = [IO.Directory]::CreateDirectory($run)
    $null = Get-ReleaseGeoItemAttributes $run -Directory
}
$run = Join-Path $run ([guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $run | Out-Null
$fixture = Join-Path $run 'fixture'
$refreshFixture = Join-Path $run 'refresh-fixture'
$null = [IO.Directory]::CreateDirectory($fixture)
$null = [IO.Directory]::CreateDirectory($refreshFixture)

$script:assertions = [Collections.Generic.List[string]]::new()
$script:mockRequests = [Collections.Generic.List[string]]::new()
$script:mockMode = 'disabled'
$script:mockAssets = @()
$metadataBase = 'https://api.github.com/repos/MetaCubeX/meta-rules-dat/releases/'
$downloadBase = 'https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/'

function Assert-True([bool]$Condition, [string]$Label) {
    if (-not $Condition) { throw $Label }
    $script:assertions.Add($Label)
    Write-Output ('PASS ' + $Label)
}

function Assert-Rejected([scriptblock]$Action, [string]$Pattern, [string]$Label) {
    $failure = $null
    try { $null = & $Action } catch { $failure = $_.Exception.Message }
    Assert-True ($failure -and $failure -match $Pattern) $Label
}

function Write-FixtureJson([string]$Path, $Value) {
    [IO.File]::WriteAllText($Path, ($Value | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
}

function New-GeoFixture([string]$Root, [string]$Batch, [long]$Id, [string]$VerifiedAt = '') {
    $path = Join-Path $Root $Batch
    $null = [IO.Directory]::CreateDirectory($path)
    foreach ($name in @('geosite.dat', 'geoip.dat')) {
        $data = Join-Path $path $name
        [IO.File]::WriteAllText($data, "Synthetic Geo fixture $Id $name")
        $digest = (Get-FileHash -LiteralPath $data -Algorithm SHA256).Hash
        $sidecar = $digest
        if ($name -eq 'geosite.dat') { $sidecar += '  *' + $name }
        [IO.File]::WriteAllText(($data + '.sha256sum'), $sidecar + [Environment]::NewLine)
    }
    $assetId = $Id * 10
    $assets = @(
        foreach ($name in @(Get-ReleaseGeoAssetNames)) {
            $assetId++
            $file = Get-Item -LiteralPath (Join-Path $path $name)
            [pscustomobject]@{
                name = $name
                id = $assetId
                size = $file.Length
                digest = 'sha256:' + (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
                state = 'uploaded'
                browser_download_url = $downloadBase + $name
            }
        }
    )
    if ($VerifiedAt) {
        Write-FixtureJson (Join-Path $path 'provenance.json') ([ordered]@{
            schemaVersion = 1
            status = 'verified-ready'
            verified = $true
            verifiedAt = $VerifiedAt
            source = 'public-official-upstream'
            repository = 'MetaCubeX/meta-rules-dat'
            releaseId = $Id
            metadataUrl = $metadataBase + $Id
            assets = $assets
        })
    }
    return $assets
}

function Get-FixtureSnapshot([string]$Root) {
    return @(
        Get-ChildItem -LiteralPath $Root -File -Force -Recurse | Sort-Object FullName | ForEach-Object {
            $_.FullName + ':' + (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash
        }
    ) -join [Environment]::NewLine
}

# Replace Invoke-WebRequest in the entry point's caller scope. Every request
# is served by the test fixture, including errors with a synthetic signed URL.
function Invoke-WebRequest {
    [CmdletBinding()]
    param([string]$Uri, [string]$OutFile, [switch]$UseBasicParsing, [int]$MaximumRedirection, [int]$TimeoutSec)
    $mockRequests.Add($Uri)
    if ($mockMode -eq 'disabled') { throw 'The offline entry point attempted a network request.' }
    if ($Uri -in @(($metadataBase + 'latest'), ($metadataBase + '901'))) {
        if ($OutFile -or $MaximumRedirection -ne 0) { throw 'Unexpected metadata request options.' }
        $assets = @($mockAssets | ForEach-Object { $_.PSObject.Copy() })
        if ($mockMode -eq 'changed-release' -and $Uri.EndsWith('/901')) { $assets[0].id += 10000 }
        if ($mockMode -eq 'missing-asset') { $assets = @($assets | Where-Object { $_.name -ne 'geoip.dat' }) }
        return [pscustomobject]@{ StatusCode = 200; Content = ([ordered]@{ id = 901; tag_name = 'latest'; draft = $false; assets = $assets } | ConvertTo-Json -Depth 7) }
    }
    $name = $Uri.Substring($Uri.LastIndexOf('/') + 1)
    if (-not $OutFile -or $Uri -cne ($downloadBase + $name) -or $name -cnotin @(Get-ReleaseGeoAssetNames)) { throw 'Unexpected mock URL or output path.' }
    if ($mockMode -eq 'download-error') { throw 'Failed redirect https://release-assets.githubusercontent.com/mock?sig=SYNTHETIC_SECRET' }
    $bytes = [IO.File]::ReadAllBytes((Join-Path $mockFiles $name))
    if ($mockMode -eq 'digest-mismatch' -and $name -eq 'geoip.dat.sha256sum') {
        # Same valid digest text, different bytes, so the local DAT/sidecar
        # comparison succeeds while the official sidecar digest check fails.
        $bytes = [Text.Encoding]::UTF8.GetBytes(([Text.Encoding]::UTF8.GetString($bytes)).ToLowerInvariant())
    }
    [IO.File]::WriteAllBytes($OutFile, $bytes)
}

$status = 'failed'
try {
    $base = '.cache/downloads'
    $default = "$base/release-geodata"
    $old = "$base/release-geodata-old"
    $new = "$base/release-geodata-new"
    $untrusted = "$base/release-geodata-untrusted"
    $null = New-GeoFixture $fixture $untrusted 101
    $null = New-GeoFixture $fixture 'data' 102
    $hits = @(Resolve-ReleaseGeoData -WorkspaceRoot $fixture -GeoDataRoot '.cache\downloads\release-geodata-untrusted\')
    Assert-True ($hits.Count -eq 1 -and $hits[0].root -ceq $untrusted -and $hits[0].source -ceq 'local') 'Explicit legacy batch returns one normalized local result'
    $result = $hits[0]
    Assert-True (($result.records.payloadPath -join ',') -ceq 'data/geosite.dat,data/geoip.dat') 'Release records name both required ZIP destinations'
    Assert-True (@($result.records | Where-Object { $_.size -gt 0 -and $_.sha256 -cmatch '^[0-9a-f]{64}$' -and $_.checksum.path -ceq ($_.path + '.sha256sum') -and $_.checksum.size -gt 0 -and $_.checksum.sha256 -cmatch '^[0-9a-f]{64}$' -and -not $_.checksum.Contains('text') }).Count -eq 2) 'Both records carry data and sidecar SHA-256 and size'
    Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot $fixture } 'No verified public' 'Automatic selection rejects legacy-only and runtime fixtures'
    Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot 'relative' } 'absolute local' 'Relative workspace roots are rejected'
    Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot $fixture -GeoDataRoot 'data' } 'runtime data is not a release source' 'Runtime Geo data cannot be selected'
    foreach ($invalid in @("$base/../data", "$base//release-geodata-untrusted", "$base/release-geodata-untrusted.", "$base/CON/file", (Join-Path $fixture $untrusted))) {
        Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot $fixture -GeoDataRoot $invalid } 'must be a verified release batch' ('Forbidden batch path ' + $invalid)
    }

    $null = New-GeoFixture $fixture $new 104 '2026-11-21T01:00:00Z'
    $null = New-GeoFixture $fixture $old 103 '2026-11-19T09:00:00+08:00'
    $null = New-GeoFixture $fixture $default 105 '2026-11-17T01:00:00Z'
    $snapshot = Get-FixtureSnapshot $fixture
    Assert-True ((Resolve-ReleaseGeoData -WorkspaceRoot $fixture).root -ceq $default) 'Verified default batch takes precedence'
    Assert-True ((& $prepare -WorkspaceRoot $fixture).root -ceq $default) 'Prepare defaults to local batch reuse'
    Assert-True ($script:mockRequests.Count -eq 0 -and (Get-FixtureSnapshot $fixture) -ceq $snapshot) 'Local selection and prepare perform no network calls or fixture writes'

    $proofPath = Join-Path $fixture "$default/provenance.json"
    [IO.File]::WriteAllText($proofPath, '{')
    Assert-True ((Resolve-ReleaseGeoData -WorkspaceRoot $fixture).root -ceq $new) 'Invalid default provenance is skipped and newest verification time wins'
    $proofPath = Join-Path $fixture "$new/provenance.json"
    $proofText = [IO.File]::ReadAllText($proofPath)
    $proof = ConvertFrom-Json $proofText
    $proof.verified = 'true'
    Write-FixtureJson $proofPath $proof
    Assert-True ((Resolve-ReleaseGeoData -WorkspaceRoot $fixture).root -ceq $old) 'Verification uses the JSON boolean true'
    $proof.verified = $true
    $proof.metadataUrl = 'https://example.invalid/releases/104'
    Write-FixtureJson $proofPath $proof
    Assert-True ((Resolve-ReleaseGeoData -WorkspaceRoot $fixture).root -ceq $old) 'Untrusted upstream source is skipped'
    [IO.File]::WriteAllText($proofPath, $proofText)
    $newSum = Join-Path $fixture "$new/geoip.dat.sha256sum"
    $goodSum = [IO.File]::ReadAllText($newSum)
    [IO.File]::WriteAllText($newSum, $goodSum.ToLowerInvariant())
    Assert-True ((Resolve-ReleaseGeoData -WorkspaceRoot $fixture).root -ceq $old) 'Automatic selection checks the official digest of the sidecar itself'
    Assert-True ((Resolve-ReleaseGeoData -WorkspaceRoot $fixture -GeoDataRoot $new).root -ceq $new) 'Explicit mode preserves legacy DAT and sidecar validation'
    [IO.File]::WriteAllText($newSum, ('0' * 64))
    Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot $fixture -GeoDataRoot $new } 'SHA-256 does not match' 'Invalid explicit batch fails without switching to an older batch'
    [IO.File]::WriteAllText($newSum, (' ' * 1025))
    Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot $fixture -GeoDataRoot $new } 'Invalid release database/checksum size' 'Oversized sidecar is rejected'
    [IO.File]::WriteAllText($newSum, '')
    Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot $fixture -GeoDataRoot $new } 'Invalid release database/checksum size' 'Empty sidecar is rejected'
    $emptyData = Join-Path $fixture "$new/geosite.dat"
    [IO.File]::WriteAllText($emptyData, '')
    Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot $fixture -GeoDataRoot $new } 'Invalid release database/checksum size' 'Empty database is rejected'
    $split = "$base/release-geodata-split"
    $null = [IO.Directory]::CreateDirectory((Join-Path $fixture $split))
    [IO.File]::Copy((Join-Path $fixture "$old/geosite.dat"), (Join-Path $fixture "$split/geosite.dat"))
    [IO.File]::Copy((Join-Path $fixture "$old/geosite.dat.sha256sum"), (Join-Path $fixture "$split/geosite.dat.sha256sum"))
    Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot $fixture -GeoDataRoot $split } 'missing' 'An incomplete batch cannot borrow the other database'

    $plain = "$base/release-geodata-file"
    [IO.File]::WriteAllText((Join-Path $fixture $plain), 'file fixture')
    Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot $fixture -GeoDataRoot $plain } 'Expected a directory' 'File-valued batch directory is rejected'
    $cacheFile = Join-Path $run 'cache-file-fixture'
    $null = [IO.Directory]::CreateDirectory($cacheFile)
    [IO.File]::WriteAllText((Join-Path $cacheFile '.cache'), 'file fixture')
    Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot $cacheFile } 'Expected a directory' 'File-valued cache ancestor is rejected'
    $folderAsData = "$base/release-geodata-directory-data"
    $null = [IO.Directory]::CreateDirectory((Join-Path $fixture "$folderAsData/geosite.dat"))
    Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot $fixture -GeoDataRoot $folderAsData } 'Expected a regular file' 'Directory-valued database is rejected'

    if ([IO.Path]::DirectorySeparatorChar -eq [char]92) {
        $linked = "$base/release-geodata-junction"
        $linkedWorkspace = Join-Path $run 'workspace-junction'
        New-Item -ItemType Junction -Path (Join-Path $fixture $linked) -Target (Join-Path $fixture $old) | Out-Null
        New-Item -ItemType Junction -Path $linkedWorkspace -Target $fixture | Out-Null
        Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot $fixture -GeoDataRoot $linked } 'Reparse point' 'Relative batch junction is rejected'
        Assert-Rejected { Resolve-ReleaseGeoData -WorkspaceRoot $linkedWorkspace } 'Reparse point' 'Workspace junction is rejected'
        Assert-True ((Resolve-ReleaseGeoData -WorkspaceRoot $fixture).root -ceq $old) 'Automatic selection skips a junction candidate'
    }

    $script:mockAssets = @(New-GeoFixture $refreshFixture 'mock-upstream' 901)
    $script:mockFiles = Join-Path $refreshFixture 'mock-upstream'
    $script:mockMode = 'ok'
    $refreshed = & $prepare -WorkspaceRoot $refreshFixture -Refresh
    Assert-True ($refreshed.root -cmatch '^\.cache/downloads/release-geodata-.+' -and $refreshed.releaseId -eq 901 -and @($refreshed.records).Count -eq 2) 'Explicit mocked refresh creates a complete new published batch'
    Assert-True (($script:mockRequests -join ',') -ceq ((@($metadataBase + 'latest') + @(Get-ReleaseGeoAssetNames | ForEach-Object { $downloadBase + $_ }) + @($metadataBase + '901')) -join ',')) 'Mock refresh downloads four assets and rechecks the fixed release ID'
    $script:mockMode = 'disabled'
    Assert-True ((& $prepare -WorkspaceRoot $refreshFixture).root -ceq $refreshed.root -and $script:mockRequests.Count -eq 6) 'Newly verified batch is reusable offline'
    $previousBatch = Get-FixtureSnapshot (Join-Path $refreshFixture $refreshed.root)
    $script:mockMode = 'ok'
    $another = & $prepare -WorkspaceRoot $refreshFixture -Refresh
    Assert-True ($another.root -cne $refreshed.root -and (Get-FixtureSnapshot (Join-Path $refreshFixture $refreshed.root)) -ceq $previousBatch) 'A second refresh leaves the prior batch intact'
    $script:mockMode = 'digest-mismatch'
    Assert-Rejected { & $prepare -WorkspaceRoot $refreshFixture -Refresh } 'official asset digest' 'Refresh rejects a sidecar whose bytes differ from the official digest'
    $script:mockMode = 'changed-release'
    Assert-Rejected { & $prepare -WorkspaceRoot $refreshFixture -Refresh } 'changed during download' 'Refresh rejects replaced assets from the fixed release'
    $script:mockMode = 'missing-asset'
    Assert-Rejected { & $prepare -WorkspaceRoot $refreshFixture -Refresh } 'requires four assets' 'Refresh requires all four assets in the same release'
    $script:mockMode = 'download-error'
    Assert-Rejected { & $prepare -WorkspaceRoot $refreshFixture -Refresh } '\AUnable to download official Geo asset: geosite[.]dat\z' 'Refresh redacts the synthetic signed redirect in errors'
    $batchCount = @(Get-ChildItem -LiteralPath (Join-Path $refreshFixture $base) -Directory -Filter 'release-geodata-*').Count
    Assert-True ($batchCount -eq 2) 'Failed refresh attempts leave the two verified candidates intact'
    Assert-True ((Resolve-ReleaseGeoData -WorkspaceRoot $refreshFixture).root -ceq $another.root) 'Partial downloads are invisible to the local selector'
    $status = 'passed'
} finally {
    $summary = [ordered]@{
        status = $status
        passed = $script:assertions.Count
        assertions = $script:assertions.ToArray()
        powershell = $PSVersionTable.PSVersion.ToString()
        scope = 'Isolated Geo batch fixtures and mocked upstream requests'
        realNetworkRequests = 0
    }
    Write-FixtureJson (Join-Path $run 'results.json') $summary
    Write-Output ("RESULT {0} ({1} assertions) {2}" -f $status, $script:assertions.Count, $run)
}
