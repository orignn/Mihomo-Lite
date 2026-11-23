#requires -Version 5.1
<#
.SYNOPSIS
Prepares official release-only components in an independent clean workspace.
.DESCRIPTION
Default is offline: revalidate an existing verified release-components cache batch.
-Refresh is the only network opt-in. This does not build/promote a launcher, run a
core, change proxy settings, copy user data, publish tags or create Releases.
Directory SHA256: ordinal path order; UTF8 lines "sha256  size  path\n".
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$WorkspaceRoot,
    [Parameter(Mandatory)][string]$GeoDataRoot,
    [string]$CoreVersion = 'v1.19.34',
    [string]$PanelVersion = 'v3.30.0',
    [switch]$Refresh,
    [switch]$PassThru
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'release-geodata.ps1')
. (Join-Path $PSScriptRoot 'release-download.ps1')
Add-Type -AssemblyName System.IO.Compression

function Assert-RCSegment {
    param([string]$Segment)
    if ($Segment -in @('', '.', '..') -or $Segment -match '[\x00-\x1f<>:"|?*\\]|[. ]$|^(?:CON|PRN|AUX|NUL|COM[1-9\u00b9\u00b2\u00b3]|LPT[1-9\u00b9\u00b2\u00b3]|CONIN\$|CONOUT\$)(?:[ .]|$)') {
        throw 'Unsafe release path segment.'
    }
}

function Get-RCPath {
    param([string]$Root, [string]$Relative, [switch]$File, [switch]$AllowMissing)
    $null = Get-ReleaseGeoWorkspaceRoot $Root
    $parts = $Relative.Split('/')
    $path = $Root
    for ($i = 0; $i -lt $parts.Count; $i++) {
        Assert-RCSegment $parts[$i]
        $path = [IO.Path]::Combine($path, $parts[$i])
        $null = Get-ReleaseGeoItemAttributes $path -Directory:(-not $File -or $i -lt $parts.Count - 1) -AllowMissing:$AllowMissing
    }
    return $path
}

function New-RCDirectory {
    param([string]$Root, [string]$Relative)
    $path = Get-RCPath $Root $Relative -AllowMissing
    $null = [IO.Directory]::CreateDirectory($path)
    $null = Get-RCPath $Root $Relative
    return $path
}

function Write-RCNewFile {
    param([string]$Root, [string]$Relative, [byte[]]$Bytes)
    $parent = $Relative.Substring(0, $Relative.LastIndexOf('/'))
    $null = New-RCDirectory $Root $parent
    $path = Get-RCPath $Root $Relative -File -AllowMissing
    $out = [IO.File]::Open($path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $out.Write($Bytes, 0, $Bytes.Length) } finally { $out.Dispose() }
}

function Get-RCRecord {
    param([string]$Root, [string]$Relative)
    $path = Get-RCPath $Root $Relative -File
    $inputStream = [IO.File]::Open($path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        return [pscustomobject]@{ path = $Relative; size = $inputStream.Length; sha256 = [BitConverter]::ToString($sha.ComputeHash($inputStream)).Replace('-', '').ToLowerInvariant() }
    } finally { $sha.Dispose(); $inputStream.Dispose() }
}

function Get-RCDirectorySHA {
    param($Files)
    $byPath = @{}
    $paths = [Collections.Generic.List[string]]::new()
    foreach ($file in $Files) {
        if ($byPath.ContainsKey($file.path)) { throw 'Duplicate release file record.' }
        $byPath[$file.path] = $file
        $paths.Add($file.path)
    }
    $paths.Sort([StringComparer]::Ordinal)
    $text = [Text.StringBuilder]::new()
    foreach ($path in $paths) {
        $file = $byPath[$path]
        [void]$text.Append($file.sha256).Append('  ').Append(([long]$file.size).ToString([Globalization.CultureInfo]::InvariantCulture)).Append('  ').Append($path).Append([char]10)
    }
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($text.ToString()))).Replace('-', '').ToLowerInvariant() }
    finally { $sha.Dispose() }
}

function Get-RCSpec {
    param([string]$CoreVersion, [string]$PanelVersion, $GeoProof)
    foreach ($version in @($CoreVersion, $PanelVersion)) {
        if ($version -cnotmatch '\Av[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?\z') { throw 'Component version must be an explicit safe semantic version tag.' }
    }
    return @(
        [pscustomobject]@{ name = 'core'; version = $CoreVersion; repository = 'MetaCubeX/mihomo'; assetName = "mihomo-windows-amd64-compatible-$CoreVersion.zip"; destination = 'runtime/mihomo.exe'; maxSize = 134217728; release = "tags/$CoreVersion" },
        [pscustomobject]@{ name = 'panel'; version = $PanelVersion; repository = 'Zephyruso/zashboard'; assetName = 'dist-no-fonts.zip'; destination = 'data/ui'; maxSize = 134217728; release = "tags/$PanelVersion" },
        [pscustomobject]@{ name = 'metadb'; version = $GeoProof.releaseTag; repository = 'MetaCubeX/meta-rules-dat'; assetName = 'geoip.metadb'; destination = 'data/geoip.metadb'; maxSize = 536870912; release = [string]$GeoProof.releaseId }
    )
}

function Get-RCRelease {
    param($Spec, [string]$Release)
    $base = 'https://api.github.com/repos/' + $Spec.repository
    $metadata = Get-RCJson "$base/releases/$Release"
    if ($metadata -isnot [pscustomobject] -or -not (Test-RCPositiveInteger $metadata.id) -or $metadata.draft -cne $false -or
        $metadata.tag_name -isnot [string] -or $metadata.tag_name -cnotmatch '\A[A-Za-z0-9][A-Za-z0-9._-]*\z' -or
        $metadata.tag_name -cne $Spec.version -or
        ($Release -cnotmatch '\Atags/' -and [string]$metadata.id -cne $Release)) { throw 'Official component release identity or tag is invalid.' }
    if ($metadata.url -cne "$base/releases/$($metadata.id)" -or
        $metadata.html_url -cne "https://github.com/$($Spec.repository)/releases/tag/$($Spec.version)") { throw 'Official release repository URL is invalid.' }
    $selected = @($metadata.assets | Where-Object { $_.name -ceq $Spec.assetName })
    if ($selected.Count -ne 1) { throw 'Official component release has a missing or duplicate selected asset.' }
    $asset = $selected[0]
    $url = "https://github.com/$($Spec.repository)/releases/download/$($Spec.version)/$($Spec.assetName)"
    if (-not (Test-RCPositiveInteger $asset.id) -or -not (Test-RCPositiveInteger $asset.size) -or $asset.size -gt $Spec.maxSize -or
        $asset.digest -isnot [string] -or $asset.digest -cnotmatch '\Asha256:[0-9a-f]{64}\z' -or
        $asset.state -cne 'uploaded' -or $asset.browser_download_url -cne $url -or $asset.url -cne "$base/releases/assets/$($asset.id)") {
        throw 'Official component asset identity, digest, size or URL is invalid.'
    }
    if (@($metadata.assets | Where-Object { $_.id -eq $asset.id }).Count -ne 1) { throw 'Official component asset ID is duplicated.' }
    return [pscustomobject]@{
        releaseId = [long]$metadata.id; releaseTag = $metadata.tag_name; metadataUrl = "$base/releases/$($metadata.id)"
        asset = [pscustomobject]@{ id = [long]$asset.id; name = $asset.name; size = [long]$asset.size; digest = $asset.digest; downloadUrl = $url }
    }
}

function Get-RCCommit {
    param([string]$Repository, [string]$Tag)
    $base = 'https://api.github.com/repos/' + $Repository
    $ref = Get-RCJson "$base/git/ref/tags/$Tag"
    if ($ref.ref -cne "refs/tags/$Tag" -or $ref.url -cne "$base/git/refs/tags/$Tag") { throw 'Official release tag ref is invalid.' }
    $object = $ref.object
    $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    for ($depth = 0; $depth -lt 5; $depth++) {
        if ($object.sha -isnot [string] -or $object.sha -cnotmatch '\A[0-9a-f]{40}\z' -or -not $seen.Add($object.sha) -or $object.type -cnotin @('tag', 'commit')) { throw 'Official tag commit identity is invalid.' }
        $kind = 'commits'
        if ($object.type -ceq 'tag') { $kind = 'tags' }
        if ($object.url -cne "$base/git/$kind/$($object.sha)") { throw 'Official tag object repository is invalid.' }
        if ($object.type -ceq 'commit') { return $object.sha }
        $tagObject = Get-RCJson $object.url
        if ($tagObject.sha -cne $object.sha -or $tagObject.url -cne $object.url) { throw 'Official annotated tag identity changed.' }
        $object = $tagObject.object
    }
    throw 'Official annotated tag nesting exceeds limit.'
}

function Assert-RCAssetFile {
    param([string]$Root, [string]$Relative, $Asset)
    $file = Get-RCRecord $Root $Relative
    if ($file.size -ne $Asset.size -or ('sha256:' + $file.sha256) -cne $Asset.digest) { throw 'Downloaded component does not match official SHA256 digest and size.' }
}

function Assert-RCPE {
    param([byte[]]$Bytes)
    if ($Bytes.Length -lt 256 -or $Bytes[0] -ne 0x4d -or $Bytes[1] -ne 0x5a) { throw 'Core is not a valid x64 PE image (MZ).' }
    $offset = [BitConverter]::ToInt32($Bytes, 0x3c)
    if ($offset -lt 64 -or $offset -gt $Bytes.Length - 24 -or $Bytes[$offset] -ne 0x50 -or $Bytes[$offset + 1] -ne 0x45 -or
        $Bytes[$offset + 2] -ne 0 -or $Bytes[$offset + 3] -ne 0 -or [BitConverter]::ToUInt16($Bytes, $offset + 4) -ne 0x8664) { throw 'Core is not a valid x64 PE image (PE/machine).' }
    $optionalSize = [BitConverter]::ToUInt16($Bytes, $offset + 20)
    $characteristics = [BitConverter]::ToUInt16($Bytes, $offset + 22)
    if ($optionalSize -lt 112 -or $offset + 24 + $optionalSize -gt $Bytes.Length -or
        [BitConverter]::ToUInt16($Bytes, $offset + 24) -ne 0x20b -or ($characteristics -band 2) -eq 0 -or ($characteristics -band 0x2000) -ne 0) { throw 'Core is not an executable PE32+ image.' }
}

function Get-RCZipPayload {
    param([string]$Root, [string]$Relative, $Spec)
    $path = Get-RCPath $Root $Relative -File
    $inputStream = [IO.File]::Open($path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    $zip = $null
    try {
        $zip = [IO.Compression.ZipArchive]::new($inputStream, [IO.Compression.ZipArchiveMode]::Read, $true)
        if ($zip.Entries.Count -le 0 -or $zip.Entries.Count -gt 10000) { throw 'Component ZIP entry count exceeds limit.' }
        $names = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
        $files = [Collections.Generic.List[object]]::new()
        $directories = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
        [long]$total = 0
        foreach ($entry in $zip.Entries) {
            $name = $entry.FullName
            $dir = $name.EndsWith('/', [StringComparison]::Ordinal)
            $trimmed = $name.TrimEnd('/')
            if ($name.Length -gt 240 -or $trimmed -ceq '' -or ($dir -and $name -cne ($trimmed + '/'))) { throw 'Unsafe component ZIP path.' }
            foreach ($segment in $trimmed.Split('/')) { Assert-RCSegment $segment }
            if (-not $names.Add($trimmed)) { throw 'Duplicate or case-colliding component ZIP path.' }
            $unixType = ([long]$entry.ExternalAttributes -shr 16) -band 0xf000
            if (($entry.ExternalAttributes -band 0x400) -ne 0 -or $unixType -notin @(0, 0x8000, 0x4000) -or
                ($unixType -eq 0x4000 -and -not $dir) -or ($unixType -eq 0x8000 -and $dir)) { throw 'Component ZIP contains symlink, reparse or special entry.' }
            if ($dir) {
                if ($entry.Length -ne 0) { throw 'Component ZIP directory has a payload.' }
                [void]$directories.Add($trimmed)
                continue
            }
            [long]$limit = 16777216
            if ($Spec.name -ceq 'core') { $limit = 134217728 }
            $total += $entry.Length
            if ($entry.Length -le 0 -or $entry.Length -gt $limit -or $total -gt 268435456 -or
                $entry.CompressedLength -le 0 -or $entry.Length -gt $entry.CompressedLength * 1000) { throw 'Component ZIP decompressed size exceeds limits.' }
            $files.Add([pscustomobject]@{ name = $name; entry = $entry })
        }
        $parentSpellings = @{}
        foreach ($name in $names) {
            $cursor = $name
            if ($directories.Contains($cursor)) {
                if ($parentSpellings.ContainsKey($cursor) -and $parentSpellings[$cursor] -cne $cursor) { throw 'Component ZIP has a case-colliding implicit directory.' }
                $parentSpellings[$cursor] = $cursor
            }
            while ($cursor.Contains('/')) {
                $cursor = $cursor.Substring(0, $cursor.LastIndexOf('/'))
                if ($names.Contains($cursor) -and -not $directories.Contains($cursor)) { throw 'Component ZIP file/directory collision.' }
                if ($parentSpellings.ContainsKey($cursor) -and $parentSpellings[$cursor] -cne $cursor) { throw 'Component ZIP has a case-colliding implicit directory.' }
                $parentSpellings[$cursor] = $cursor
            }
        }
        $prefix = ''
        if ($Spec.name -ceq 'core') {
            # Official assets drop the release-version suffix from the inner executable:
            # mihomo-windows-amd64-compatible-<version>.zip -> mihomo-windows-amd64-compatible.exe
            $coreBase = [IO.Path]::GetFileNameWithoutExtension($Spec.assetName)
            $versionSuffix = '-' + [string]$Spec.version
            if ($coreBase.EndsWith($versionSuffix, [StringComparison]::Ordinal)) { $coreBase = $coreBase.Substring(0, $coreBase.Length - $versionSuffix.Length) }
            $expectedCore = $coreBase + '.exe'
            if ($files.Count -ne 1 -or $directories.Count -ne 0 -or $files[0].name -cne $expectedCore) { throw 'Core ZIP must contain only its official single executable.' }
        } else {
            $indices = @($files | Where-Object { $_.name -ceq 'index.html' -or $_.name -ceq 'dist/index.html' -or $_.name -ceq 'dist-no-fonts/index.html' })
            if ($indices.Count -ne 1 -or @($files | Where-Object { [IO.Path]::GetFileName($_.name) -ieq 'index.html' }).Count -ne 1) { throw 'Panel ZIP must have one supported official static root index.html.' }
            if ($indices[0].name -cne 'index.html') { $prefix = $indices[0].name.Substring(0, $indices[0].name.Length - 'index.html'.Length) }
            foreach ($directory in $directories) {
                if ($prefix -and $directory -cne $prefix.TrimEnd('/') -and -not $directory.StartsWith($prefix, [StringComparison]::Ordinal)) { throw 'Panel ZIP has an unrelated directory root.' }
                if ($directory -match '(?i)(^|/)(node_modules|node|scripts|\.git)(/|$)') { throw 'Panel ZIP contains forbidden tool directory.' }
            }
        }
        $payload = [Collections.Generic.List[object]]::new()
        # Validate all entry bytes before any destination file or runtime/data directory is created.
        foreach ($file in $files) {
            $destination = $Spec.destination
            if ($Spec.name -ceq 'panel') {
                if (-not $file.name.StartsWith($prefix, [StringComparison]::Ordinal)) { throw 'Panel ZIP has multiple resource roots.' }
                $relative = $file.name.Substring($prefix.Length)
                if ($relative -match '(?i)(^|/)(node_modules|node|scripts|\.git)(/|$)' -or
                    $relative -match '(?i)(^|/)(package(?:-lock)?\.json|(?:pre|post)?install\.[^/]+|setup\.[^/]+|vite\.config\.[^/]+|.*\.(?:exe|dll|ps1|bat|cmd|msi|sh|cjs|mjs))$' -or
                    [IO.Path]::GetExtension($relative).ToLowerInvariant() -notin @('.html', '.js', '.css', '.json', '.svg', '.png', '.jpg', '.jpeg', '.webp', '.gif', '.ico', '.woff', '.woff2', '.ttf', '.otf', '.txt', '.md', '.webmanifest', '.map', '.avif', '.wasm')) { throw 'Panel ZIP contains non-static or forbidden executable/tool content.' }
                # JavaScript is static browser content only; it is never evaluated by this tool.
                $destination += '/' + $relative
            }
            $stream = $file.entry.Open()
            $memory = [IO.MemoryStream]::new()
            try {
                $buffer = New-Object byte[] 65536
                [long]$read = 0
                while (($count = $stream.Read($buffer, 0, $buffer.Length)) -gt 0) {
                    $read += $count
                    if ($read -gt $file.entry.Length) { throw 'ZIP entry exceeds declared size.' }
                    $memory.Write($buffer, 0, $count)
                }
                if ($read -ne $file.entry.Length) { throw 'ZIP entry is truncated.' }
                $bytes = $memory.ToArray()
            } finally { $memory.Dispose(); $stream.Dispose() }
            if ($Spec.name -ceq 'core') { Assert-RCPE $bytes }
            $payload.Add([pscustomobject]@{ path = $destination; bytes = $bytes })
        }
        return ,$payload.ToArray()
    } catch [IO.InvalidDataException] { throw 'Component ZIP is invalid.' }
    finally { if ($zip) { $zip.Dispose() }; $inputStream.Dispose() }
}

function Get-RCCachedComponents {
    param([string]$Root, $Specs, [long]$GeoReleaseId)
    $downloads = Get-RCPath $Root '.cache/downloads'
    $candidates = @(Get-ChildItem -LiteralPath $downloads -Directory -Force | Where-Object { $_.Name -cmatch '\Arelease-components-[A-Za-z0-9-]+\z' } | Sort-Object Name -Descending)
    foreach ($candidate in $candidates) {
        $relative = '.cache/downloads/' + $candidate.Name
        $path = Get-RCPath $Root "$relative/components.json" -File
        if ((Get-Item -LiteralPath $path).Length -gt 8388608) { throw 'Cached component manifest exceeds size limit.' }
        try { $manifest = ConvertFrom-Json ([IO.File]::ReadAllText($path)) -ErrorAction Stop }
        catch { throw 'Invalid cached component manifest.' }
        if ($manifest.schemaVersion -isnot [int] -or $manifest.schemaVersion -ne 1 -or $manifest.verified -isnot [bool] -or $manifest.verified -cne $true -or $manifest.status -cne 'verified-ready' -or
            -not (Test-RCPositiveInteger $manifest.geoReleaseId) -or @($manifest.components).Count -ne 3) { throw 'Cached components lack verified provenance.' }
        if ($manifest.geoReleaseId -ne $GeoReleaseId) { continue }
        if ($manifest.components[0].version -cne $Specs[0].version -or $manifest.components[1].version -cne $Specs[1].version) { continue }
        for ($i = 0; $i -lt 3; $i++) {
            $component = $manifest.components[$i]
            $spec = $Specs[$i]
            $asset = $component.asset
            $expectedPath = "$relative/$($spec.assetName)"
            if ($component.name -cne $spec.name -or $component.repository -cne $spec.repository -or $component.version -cne $spec.version -or
                $component.releaseTag -cne $spec.version -or -not (Test-RCPositiveInteger $component.releaseId) -or
                ($spec.name -ceq 'metadb' -and $component.releaseId -ne $GeoReleaseId) -or
                $component.metadataUrl -cne "https://api.github.com/repos/$($spec.repository)/releases/$($component.releaseId)" -or
                $component.sourceCommitSHA -isnot [string] -or $component.sourceCommitSHA -cnotmatch '\A[0-9a-f]{40}\z' -or
                $component.releaseMetadataStableAfterDownload -isnot [bool] -or $component.releaseMetadataStableAfterDownload -cne $true -or $asset.path -cne $expectedPath -or $asset.name -cne $spec.assetName -or
                -not (Test-RCPositiveInteger $asset.id) -or -not (Test-RCPositiveInteger $asset.size) -or $asset.size -gt $spec.maxSize -or
                $asset.digest -isnot [string] -or $asset.digest -cnotmatch '\Asha256:[0-9a-f]{64}\z' -or
                $asset.downloadUrl -cne "https://github.com/$($spec.repository)/releases/download/$($spec.version)/$($spec.assetName)") { throw 'Cached component identity, source or asset is invalid.' }
            Assert-RCAssetFile $Root $asset.path $asset
        }
        return ,@($manifest.components)
    }
    throw 'No matching verified local component cache; use explicit -Refresh in the isolated release workspace.'
}

function Invoke-PrepareReleaseComponents {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$WorkspaceRoot, [Parameter(Mandatory)][string]$GeoDataRoot,
        [string]$CoreVersion = 'v1.19.34', [string]$PanelVersion = 'v3.30.0', [switch]$Refresh, [switch]$PassThru)
    $root = Get-ReleaseGeoWorkspaceRoot $WorkspaceRoot
    foreach ($item in @(Get-ChildItem -LiteralPath $root -Force)) {
        if ($item.Name -in @('runtime', 'data', 'logs', 'launcher', 'launcher.json', 'CURRENT', 'CURRENT.json', 'MihomoLite', 'MihomoLite.exe')) { throw 'WorkspaceRoot is not a clean isolated release workspace; protected runtime or launcher already exists.' }
    }
    $geo = $GeoDataRoot.Replace('\', '/').TrimEnd('/')
    if ([IO.Path]::IsPathRooted($GeoDataRoot)) {
        $geoFull = [IO.Path]::GetFullPath($GeoDataRoot).Replace('\', '/').TrimEnd('/')
        $rootPrefix = $root.Replace('\', '/') + '/'
        if (-not $geoFull.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'GeoDataRoot is outside the isolated workspace.' }
        $geo = $geoFull.Substring($rootPrefix.Length)
    }
    $resolved = Resolve-ReleaseGeoData -WorkspaceRoot $root -GeoDataRoot $geo
    $proofPath = Get-RCPath $root "$geo/provenance.json" -File
    $proof = ConvertFrom-Json ([IO.File]::ReadAllText($proofPath))
    if ($proof.schemaVersion -isnot [int] -or $proof.schemaVersion -ne 1 -or $proof.geoDataRoot -cne $geo -or $proof.repository -cne 'MetaCubeX/meta-rules-dat' -or
        $proof.releaseMetadataStableAfterDownload -isnot [bool] -or $proof.releaseMetadataStableAfterDownload -cne $true -or $proof.releaseTag -isnot [string] -or $proof.releaseTag -cnotmatch '\A[A-Za-z0-9][A-Za-z0-9._-]*\z') { throw 'GeoDataRoot must be a verified prepare-geodata -Refresh batch with stable release identity.' }
    $specs = @(Get-RCSpec $CoreVersion $PanelVersion $proof)
    $cached = $null
    if (-not $Refresh) { $cached = Get-RCCachedComponents $root $specs $proof.releaseId }
    $id = [datetime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ') + '-' + [guid]::NewGuid().ToString('N')
    $batch = '.cache/downloads/release-components-' + $id
    $stage = '.cache/downloads/.components-' + $id
    $null = New-RCDirectory $root $stage
    $components = [Collections.Generic.List[object]]::new()
    for ($i = 0; $i -lt $specs.Count; $i++) {
        $spec = $specs[$i]
        if ($Refresh) {
            $release = Get-RCRelease $spec $spec.release
            $commit = Get-RCCommit $spec.repository $spec.version
            $asset = $release.asset
        } else {
            $release = $cached[$i]
            $commit = $release.sourceCommitSHA
            $asset = [pscustomobject]@{ id = $release.asset.id; name = $release.asset.name; size = $release.asset.size; digest = $release.asset.digest; downloadUrl = $release.asset.downloadUrl }
        }
        $downloadRelative = "$stage/$($spec.assetName)"
        $download = Get-RCPath $root $downloadRelative -File -AllowMissing
        if ($Refresh) { Save-RCAsset $asset $download }
        else {
            $source = Get-RCPath $root $cached[$i].asset.path -File
            [IO.File]::Copy($source, $download, $false)
        }
        Assert-RCAssetFile $root $downloadRelative $asset
        if ($Refresh) {
            $fixed = Get-RCRelease $spec ([string]$release.releaseId)
            if ($fixed.releaseTag -cne $release.releaseTag -or $fixed.asset.id -ne $asset.id -or $fixed.asset.size -ne $asset.size -or
                $fixed.asset.digest -cne $asset.digest -or (Get-RCCommit $spec.repository $spec.version) -cne $commit) { throw 'Official component release or source tag changed during download.' }
        }
        $asset | Add-Member -NotePropertyName path -NotePropertyValue "$batch/$($asset.name)"
        $components.Add([pscustomobject]@{ name = $spec.name; version = $spec.version; repository = $spec.repository; releaseId = $release.releaseId;
            releaseTag = $release.releaseTag; sourceCommitSHA = $commit; metadataUrl = $release.metadataUrl; releaseMetadataStableAfterDownload = $true;
            asset = $asset; destination = $spec.destination; files = @(); directorySHA256 = '' })
    }
    # Validate every archive/PE before even creating runtime/ or data/.
    $payload = [Collections.Generic.List[object]]::new()
    for ($i = 0; $i -lt $specs.Count; $i++) {
        $spec = $specs[$i]
        $downloadRelative = "$stage/$($spec.assetName)"
        if ($spec.name -ceq 'metadb') {
            $bytes = [IO.File]::ReadAllBytes((Get-RCPath $root $downloadRelative -File))
            $payload.Add([pscustomobject]@{ path = $spec.destination; bytes = $bytes; component = $i })
        } else {
            foreach ($file in (Get-RCZipPayload $root $downloadRelative $spec)) {
                $payload.Add([pscustomobject]@{ path = $file.path; bytes = $file.bytes; component = $i })
            }
        }
    }
    $paths = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($file in $payload) {
        if (-not $paths.Add($file.path)) { throw 'Duplicate destination component path.' }
        $path = Get-RCPath $root $file.path -File -AllowMissing
        if ([IO.File]::Exists($path) -or [IO.Directory]::Exists($path)) { throw 'Component destination exists; never overwrite.' }
    }
    # Revalidate the same Geo batch immediately before committing destinations.
    $null = Resolve-ReleaseGeoData -WorkspaceRoot $root -GeoDataRoot $geo
    $expected = @{}
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        foreach ($file in $payload) {
            $expected[$file.path] = [pscustomobject]@{ size = $file.bytes.Length; sha256 = [BitConverter]::ToString($sha.ComputeHash($file.bytes)).Replace('-', '').ToLowerInvariant() }
            Write-RCNewFile $root $file.path $file.bytes
        }
    } finally { $sha.Dispose() }
    $allFiles = [Collections.Generic.List[object]]::new()
    for ($i = 0; $i -lt $components.Count; $i++) {
        $files = @($payload | Where-Object { $_.component -eq $i } | ForEach-Object { Get-RCRecord $root $_.path })
        foreach ($file in $files) {
            if ($file.size -ne $expected[$file.path].size -or $file.sha256 -cne $expected[$file.path].sha256) { throw 'Destination component changed during preparation.' }
        }
        $components[$i].files = $files
        $components[$i].directorySHA256 = Get-RCDirectorySHA $files
        foreach ($file in $files) { $allFiles.Add($file) }
    }
    $manifest = [ordered]@{ schemaVersion = 1; status = 'verified-ready'; verified = $true; id = $id; createdAt = [datetime]::UtcNow.ToString('o');
        geoDataRoot = $geo; geoReleaseId = [long]$proof.releaseId; components = $components.ToArray(); files = $allFiles.ToArray(); directorySHA256 = Get-RCDirectorySHA $allFiles }
    Write-RCNewFile $root "$stage/components.json" ([Text.Encoding]::UTF8.GetBytes(($manifest | ConvertTo-Json -Depth 12) + [char]10))
    $sourceDirectory = Get-RCPath $root $stage
    $targetDirectory = Get-RCPath $root $batch -AllowMissing
    if ([IO.Directory]::Exists($targetDirectory)) { throw 'Component cache batch exists; never overwrite.' }
    [IO.Directory]::Move($sourceDirectory, $targetDirectory)
    if ($PassThru) { return [pscustomobject]@{ manifest = "$batch/components.json"; components = $components.ToArray() } }
    Write-Output "Prepared verified release components: $batch/components.json"
}

Invoke-PrepareReleaseComponents @PSBoundParameters
