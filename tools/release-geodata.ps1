#requires -Version 5.1
# Dot-source this library for offline validation. Loading it defines functions only.

function Get-ReleaseGeoItemAttributes {
    param([string]$Path, [switch]$Directory, [switch]$AllowMissing)
    try {
        $attributes = [IO.File]::GetAttributes($Path)
    } catch [IO.FileNotFoundException], [IO.DirectoryNotFoundException] {
        if ($AllowMissing) { return }
        throw "Required Geo release path is missing: $Path"
    }
    if ($attributes -band [IO.FileAttributes]::ReparsePoint) {
        throw "Reparse point is not allowed: $Path"
    }
    $isDirectory = [bool]($attributes -band [IO.FileAttributes]::Directory)
    if ($Directory -and -not $isDirectory) { throw "Expected a directory: $Path" }
    if (-not $Directory -and $isDirectory) { throw "Expected a regular file: $Path" }
    return $attributes
}

function Get-ReleaseGeoWorkspaceRoot {
    param([string]$WorkspaceRoot)
    $windowsPath = [IO.Path]::DirectorySeparatorChar -eq [char]92
    if (-not [IO.Path]::IsPathRooted($WorkspaceRoot) -or ($windowsPath -and $WorkspaceRoot -notmatch '^[A-Za-z]:[/\\]')) {
        throw 'WorkspaceRoot must be an absolute local filesystem directory.'
    }
    $fullPath = [IO.Path]::GetFullPath($WorkspaceRoot)
    $volume = [IO.Path]::GetPathRoot($fullPath)
    if ($fullPath.Length -gt $volume.Length) { $fullPath = $fullPath.TrimEnd([char[]]@('/', '\')) }
    $ancestors = [Collections.Generic.Stack[string]]::new()
    for ($cursor = $fullPath; $cursor; $cursor = [IO.Path]::GetDirectoryName($cursor)) {
        $ancestors.Push($cursor)
    }
    foreach ($ancestor in $ancestors) {
        $null = Get-ReleaseGeoItemAttributes $ancestor -Directory
    }
    return $fullPath
}

function Get-ReleaseGeoPath {
    param([string]$WorkspaceRoot, [string]$RelativePath, [switch]$File, [switch]$AllowMissing)
    # Check each component before visiting its descendants.
    $segments = $RelativePath.Split('/')
    if ($segments.Count -lt 2 -or $segments[0] -cne '.cache' -or $segments[1] -cne 'downloads') {
        throw 'GeoDataRoot must be a verified release batch inside .cache/downloads; runtime data is not a release source.'
    }
    foreach ($segment in $segments) {
        if ($segment -in @('', '.', '..') -or $segment -match '[\x00-\x1f<>:"|?*\\]|[. ]$|^(?:CON|PRN|AUX|NUL|COM[1-9\u00b9\u00b2\u00b3]|LPT[1-9\u00b9\u00b2\u00b3]|CONIN\$|CONOUT\$)(?:[ .]|$)') {
            throw 'GeoDataRoot must be a verified release batch inside .cache/downloads with safe path segments.'
        }
    }
    $path = $WorkspaceRoot
    $missing = $false
    for ($i = 0; $i -lt $segments.Count; $i++) {
        $path = [IO.Path]::Combine($path, $segments[$i])
        if (-not $missing) {
            $directory = -not $File -or $i -lt ($segments.Count - 1)
            $attributes = Get-ReleaseGeoItemAttributes $path -Directory:$directory -AllowMissing:$AllowMissing
            $missing = $null -eq $attributes
        }
    }
    return $path
}

function Get-ReleaseGeoFileRecord {
    param([string]$WorkspaceRoot, [string]$RelativePath, [int]$TextLimit = 0)
    $path = Get-ReleaseGeoPath $WorkspaceRoot $RelativePath -File
    # Hash and parse each sidecar through the same open handle.
    $stream = [IO.File]::Open($path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    try {
        $size = $stream.Length
        if ($size -le 0 -or ($TextLimit -gt 0 -and $size -gt $TextLimit)) {
            throw "Invalid release database/checksum size: $RelativePath"
        }
        $sha = [Security.Cryptography.SHA256]::Create()
        try {
            $digest = [BitConverter]::ToString($sha.ComputeHash($stream)).Replace('-', '').ToLowerInvariant()
        } finally { $sha.Dispose() }
        $record = [ordered]@{ path = $RelativePath; sha256 = $digest; size = $size }
        if ($TextLimit -gt 0) {
            $stream.Position = 0
            $reader = [IO.StreamReader]::new($stream, [Text.Encoding]::UTF8, $true, 1024, $true)
            try { $record.text = $reader.ReadToEnd().Trim() } finally { $reader.Dispose() }
        }
        return $record
    } finally { $stream.Dispose() }
}

function Get-ReleaseGeoBatchRecords {
    param([string]$WorkspaceRoot, [string]$Batch)
    $null = Get-ReleaseGeoPath $WorkspaceRoot $Batch
    foreach ($name in @('geosite.dat', 'geoip.dat')) {
        $relative = "$Batch/$name"
        $record = Get-ReleaseGeoFileRecord $WorkspaceRoot $relative
        $checksum = Get-ReleaseGeoFileRecord $WorkspaceRoot ($relative + '.sha256sum') -TextLimit 1024
        $expression = '\A([0-9a-f]{64})(?:[ \t]+\*?' + [regex]::Escape($name) + ')?\z'
        $match = [regex]::Match($checksum.text, $expression, [Text.RegularExpressions.RegexOptions]::IgnoreCase)
        if (-not $match.Success -or $match.Groups[1].Value.ToLowerInvariant() -cne $record.sha256) {
            throw "Release database SHA-256 does not match upstream checksum: $name"
        }
        $checksum.Remove('text')
        $record.payloadPath = 'data/' + $name
        $record.checksum = $checksum
        $record
    }
}

function Test-ReleaseGeoPositiveInteger {
    param($Value)
    return ($Value -is [int] -or $Value -is [long]) -and $Value -gt 0
}

function Get-ReleaseGeoAssetNames {
    return @('geosite.dat', 'geosite.dat.sha256sum', 'geoip.dat', 'geoip.dat.sha256sum')
}

function Get-ReleaseGeoOfficialAssets {
    param($Assets)
    $names = @(Get-ReleaseGeoAssetNames)
    if (@($Assets).Count -ne $names.Count) { throw 'Official Geo evidence requires four assets from one release.' }
    $byName = @{}
    $ids = [Collections.Generic.HashSet[long]]::new()
    foreach ($asset in $Assets) {
        if ($asset -isnot [pscustomobject] -or $asset.name -isnot [string] -or $asset.name -cnotin $names -or $byName.ContainsKey($asset.name)) {
            throw 'Official Geo evidence has an unexpected or duplicate asset name.'
        }
        if (-not (Test-ReleaseGeoPositiveInteger $asset.id) -or -not $ids.Add([long]$asset.id) -or
            -not (Test-ReleaseGeoPositiveInteger $asset.size) -or $asset.digest -isnot [string] -or
            $asset.digest -notmatch '\Asha256:[0-9a-f]{64}\z' -or
            ($asset.name.EndsWith('.sha256sum', [StringComparison]::Ordinal) -and $asset.size -gt 1024)) {
            throw "Invalid official Geo asset identity, digest or size: $($asset.name)"
        }
        $byName[$asset.name] = [pscustomobject]@{
            name = $asset.name
            id = [long]$asset.id
            size = [long]$asset.size
            digest = $asset.digest.ToLowerInvariant()
        }
    }
    return $byName
}

function Assert-ReleaseGeoOfficialDigests {
    param($Records, [hashtable]$Assets)
    foreach ($record in $Records) {
        foreach ($file in @($record, $record.checksum)) {
            $name = [IO.Path]::GetFileName($file.path)
            $asset = $Assets[$name]
            if ($null -eq $asset -or $asset.size -ne $file.size -or $asset.digest -cne ('sha256:' + $file.sha256)) {
                throw "Geo release file does not match the official asset digest and size: $name"
            }
        }
    }
}

function Get-ReleaseGeoProvenance {
    param([string]$WorkspaceRoot, [string]$Batch)
    $directory = Get-ReleaseGeoPath $WorkspaceRoot $Batch
    $record = Get-ReleaseGeoFileRecord $WorkspaceRoot "$Batch/provenance.json" -TextLimit 262144
    try {
        if (-not $record.text.StartsWith('{', [StringComparison]::Ordinal)) { throw 'Expected an object.' }
        $provenance = ConvertFrom-Json -InputObject $record.text -ErrorAction Stop
    } catch { throw 'Invalid release Geo provenance JSON.' }
    if ($provenance.verified -isnot [bool] -or -not $provenance.verified -or
        $provenance.status -isnot [string] -or $provenance.status -cne 'verified-ready' -or
        $provenance.source -isnot [string] -or $provenance.source -cne 'public-official-upstream' -or
        $provenance.repository -isnot [string] -or $provenance.repository -ne 'MetaCubeX/meta-rules-dat' -or
        -not (Test-ReleaseGeoPositiveInteger $provenance.releaseId)) {
        throw 'Geo batch is missing verified public upstream provenance.'
    }
    $metadataUrl = 'https://api.github.com/repos/MetaCubeX/meta-rules-dat/releases/' + $provenance.releaseId
    if ($provenance.metadataUrl -isnot [string] -or $provenance.metadataUrl -cne $metadataUrl) {
        throw 'Geo provenance lacks the official fixed release URL.'
    }
    $assets = Get-ReleaseGeoOfficialAssets $provenance.assets
    $verifiedAt = [IO.Directory]::GetLastWriteTimeUtc($directory)
    $timestamp = $provenance.verifiedAt
    if ($timestamp -is [datetime]) {
        $verifiedAt = $timestamp.ToUniversalTime()
    } elseif ($timestamp -is [string]) {
        $parsed = [DateTimeOffset]::MinValue
        if ([DateTimeOffset]::TryParse($timestamp, [Globalization.CultureInfo]::InvariantCulture,
                    [Globalization.DateTimeStyles]::AssumeUniversal, [ref]$parsed)) { $verifiedAt = $parsed.UtcDateTime }
    }
    return [pscustomobject]@{
        root = $Batch
        isDefault = $Batch -ceq '.cache/downloads/release-geodata'
        verifiedAt = $verifiedAt
        releaseId = [long]$provenance.releaseId
        metadataUrl = $metadataUrl
        assets = $assets
    }
}

function Resolve-ReleaseGeoData {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$WorkspaceRoot, [string]$GeoDataRoot = '')
    $workspace = Get-ReleaseGeoWorkspaceRoot $WorkspaceRoot
    if ($GeoDataRoot) {
        $batch = $GeoDataRoot.Replace('\', '/').TrimEnd('/')
        if (-not $batch.StartsWith('.cache/downloads/', [StringComparison]::Ordinal)) {
            throw 'GeoDataRoot must be a verified release batch inside .cache/downloads; runtime data is not a release source.'
        }
        $records = @(Get-ReleaseGeoBatchRecords $workspace $batch)
        return [pscustomobject]@{ root = $batch; records = $records; source = 'local' }
    }

    $missingBatch = 'No verified public release Geo batch found in .cache/downloads. Run tools/prepare-geodata.ps1 -Refresh to download and verify the four official assets.'
    $downloads = Get-ReleaseGeoPath $workspace '.cache/downloads' -AllowMissing
    if ($null -eq (Get-ReleaseGeoItemAttributes $downloads -Directory -AllowMissing)) { throw $missingBatch }
    $candidates = @(
        foreach ($path in [IO.Directory]::EnumerateFileSystemEntries($downloads, 'release-geodata*')) {
            $name = [IO.Path]::GetFileName($path)
            if ($name -cne 'release-geodata' -and $name -cnotmatch '\Arelease-geodata-.+\z') { continue }
            try { Get-ReleaseGeoProvenance $workspace ('.cache/downloads/' + $name) } catch { continue }
        }
    )
    $ordered = $candidates | Sort-Object -Property @{ Expression = 'isDefault'; Descending = $true },
        @{ Expression = 'verifiedAt'; Descending = $true }, @{ Expression = 'root'; Descending = $true }
    foreach ($candidate in $ordered) {
        try {
            $records = @(Get-ReleaseGeoBatchRecords $workspace $candidate.root)
            Assert-ReleaseGeoOfficialDigests $records $candidate.assets
            return [pscustomobject]@{
                root = $candidate.root
                records = $records
                source = 'local'
                releaseId = $candidate.releaseId
                metadataUrl = $candidate.metadataUrl
            }
        } catch { continue }
    }
    throw $missingBatch
}
