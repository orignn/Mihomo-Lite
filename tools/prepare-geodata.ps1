#requires -Version 5.1
<#
.SYNOPSIS
Reuses a verified local Geo release batch, or explicitly prepares a new one.
.EXAMPLE
.\tools\prepare-geodata.ps1
.EXAMPLE
.\tools\prepare-geodata.ps1 -Refresh
#>
[CmdletBinding(DefaultParameterSetName = 'Reuse')]
param(
    [string]$WorkspaceRoot = '',
    [Parameter(ParameterSetName = 'Reuse')][string]$GeoDataRoot = '',
    [Parameter(Mandatory, ParameterSetName = 'Refresh')][switch]$Refresh
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'release-geodata.ps1')
if (-not $WorkspaceRoot) { $WorkspaceRoot = Split-Path -Parent $PSScriptRoot }

function Get-PrepareGeoRelease {
    param([string]$Release)
    $uri = 'https://api.github.com/repos/MetaCubeX/meta-rules-dat/releases/' + $Release
    try {
        $response = Invoke-WebRequest -Uri $uri -UseBasicParsing -MaximumRedirection 0 -TimeoutSec 60 -ErrorAction Stop -Verbose:$false -Debug:$false
        if ($response.StatusCode -ne 200) { throw 'Unexpected metadata status.' }
        $metadata = ConvertFrom-Json -InputObject $response.Content -ErrorAction Stop
    } catch {
        throw 'Unable to read official MetaCubeX release metadata.'
    }
    if (-not (Test-ReleaseGeoPositiveInteger $metadata.id) -or $metadata.draft -cne $false -or
        $metadata.tag_name -isnot [string] -or $metadata.tag_name -notmatch '\A[A-Za-z0-9][A-Za-z0-9._-]*\z') {
        throw 'Invalid official Geo release identity or tag.'
    }
    if ($Release -ne 'latest' -and [string]$metadata.id -cne $Release) { throw 'Official Geo release ID changed.' }
    $names = @(Get-ReleaseGeoAssetNames)
    $selected = @($metadata.assets | Where-Object { $_.name -cin $names })
    $assets = Get-ReleaseGeoOfficialAssets $selected
    $baseUrl = 'https://github.com/MetaCubeX/meta-rules-dat/releases/download/' + $metadata.tag_name + '/'
    foreach ($asset in $selected) {
        $expectedUrl = $baseUrl + $asset.name
        if ($asset.browser_download_url -isnot [string] -or $asset.browser_download_url -cne $expectedUrl -or $asset.state -cne 'uploaded') {
            throw 'An official Geo asset has an invalid download location or upload status.'
        }
        $assets[$asset.name] | Add-Member -NotePropertyName downloadUrl -NotePropertyValue $expectedUrl
    }
    return [pscustomobject]@{ id = [long]$metadata.id; tag = $metadata.tag_name; assets = $assets }
}

function Save-PrepareGeoAsset {
    param($Asset, [string]$Destination)
    $ProgressPreference = 'SilentlyContinue'
    try {
        Invoke-WebRequest -Uri $Asset.downloadUrl -OutFile $Destination -UseBasicParsing -MaximumRedirection 5 -TimeoutSec 300 -ErrorAction Stop -Verbose:$false -Debug:$false | Out-Null
    } catch {
        # A redirect can carry a signed query. Keep the raw web error out of the report.
        throw "Unable to download official Geo asset: $($Asset.name)"
    }
}

if (-not $Refresh) {
    Resolve-ReleaseGeoData -WorkspaceRoot $WorkspaceRoot -GeoDataRoot $GeoDataRoot
    return
}

$workspace = Get-ReleaseGeoWorkspaceRoot $WorkspaceRoot
$downloads = Get-ReleaseGeoPath $workspace '.cache/downloads' -AllowMissing
$release = Get-PrepareGeoRelease 'latest'
$stamp = [datetime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ', [Globalization.CultureInfo]::InvariantCulture)
$nonce = [guid]::NewGuid().ToString('N')
$batch = ".cache/downloads/release-geodata-$stamp-metacubex-$($release.id)-$nonce"
$stageBatch = '.cache/downloads/.geo-refresh-' + $nonce
[void][IO.Directory]::CreateDirectory($downloads)
$stage = Get-ReleaseGeoPath $workspace $stageBatch -AllowMissing
# The staging name stays outside the automatic candidate list until the batch is complete.
New-Item -Path $stage -ItemType Directory -ErrorAction Stop | Out-Null
$assetNames = @(Get-ReleaseGeoAssetNames)
foreach ($name in $assetNames) {
    $file = Get-ReleaseGeoPath $workspace "$stageBatch/$name" -File -AllowMissing
    if ($null -ne (Get-ReleaseGeoItemAttributes $file -AllowMissing)) { throw 'Unexpected existing Geo staging file.' }
    Save-PrepareGeoAsset $release.assets[$name] $file
}
$records = @(Get-ReleaseGeoBatchRecords $workspace $stageBatch)
Assert-ReleaseGeoOfficialDigests $records $release.assets

# Re-read the fixed release ID to detect replaced assets during the download.
$fixed = Get-PrepareGeoRelease ([string]$release.id)
if ($fixed.tag -cne $release.tag) { throw 'Official Geo release changed during download.' }
foreach ($name in $assetNames) {
    $start = $release.assets[$name]
    $finish = $fixed.assets[$name]
    if ($start.id -ne $finish.id -or $start.size -ne $finish.size -or $start.digest -cne $finish.digest) {
        throw 'Official Geo release assets changed during download.'
    }
}
$metadataUrl = 'https://api.github.com/repos/MetaCubeX/meta-rules-dat/releases/' + $release.id
$provenance = [ordered]@{
    schemaVersion = 1
    status = 'verified-ready'
    verified = $true
    verifiedAt = [datetime]::UtcNow.ToString('o')
    geoDataRoot = $batch
    source = 'public-official-upstream'
    repository = 'MetaCubeX/meta-rules-dat'
    releaseId = $release.id
    releaseTag = $release.tag
    metadataUrl = $metadataUrl
    releaseMetadataStableAfterDownload = $true
    assets = @($assetNames | ForEach-Object { $release.assets[$_] })
}
$proofPath = Get-ReleaseGeoPath $workspace "$stageBatch/provenance.json" -File -AllowMissing
$proof = [IO.File]::Open($proofPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
try {
    $bytes = [Text.Encoding]::UTF8.GetBytes(($provenance | ConvertTo-Json -Depth 6) + [Environment]::NewLine)
    $proof.Write($bytes, 0, $bytes.Length)
} finally { $proof.Dispose() }
$stage = Get-ReleaseGeoPath $workspace $stageBatch
$destination = Get-ReleaseGeoPath $workspace $batch -AllowMissing
if ($null -ne (Get-ReleaseGeoItemAttributes $destination -Directory -AllowMissing)) { throw 'Unexpected existing Geo release batch.' }
[IO.Directory]::Move($stage, $destination)
$result = Resolve-ReleaseGeoData -WorkspaceRoot $workspace -GeoDataRoot $batch
$result | Add-Member -NotePropertyName releaseId -NotePropertyValue $release.id
$result | Add-Member -NotePropertyName metadataUrl -NotePropertyValue $metadataUrl
$result
