#requires -Version 5.1
[CmdletBinding()]
param([string]$WorkspaceRoot = '')
$ErrorActionPreference = 'Stop'
if (-not $WorkspaceRoot) { $WorkspaceRoot = Split-Path -Parent $PSScriptRoot }
. (Join-Path $WorkspaceRoot 'tools/release-geodata.ps1')
. (Join-Path $WorkspaceRoot 'tools/release-download.ps1')
Add-Type -AssemblyName System.IO.Compression
$source = Join-Path $WorkspaceRoot 'tools/prepare-release-components.ps1'
# Load the exact production function ASTs, not an altered implementation. Transport
# below is the sole mock boundary, with no network or real PE execution.
$tokens = $null; $errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($source, [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw 'Production script parse failed.' }
foreach ($function in $ast.FindAll({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] }, $false)) {
    Invoke-Expression $function.Extent.Text
}
$runBase = '.cache/tests/ci-release/20261123-2103/components'
$run = New-RCDirectory (Get-ReleaseGeoWorkspaceRoot $WorkspaceRoot) ($runBase + '/' + [guid]::NewGuid().ToString('N'))
$script:assertions = [Collections.Generic.List[string]]::new()
$script:requests = [Collections.Generic.List[string]]::new()
$script:mode = 'normal'
$script:assets = @{}
$script:fixture = 0
$geoBatch = '.cache/downloads/release-geodata-fixture'

function Assert-True([bool]$Condition, [string]$Label) {
    if (-not $Condition) { throw "Assertion failed: $Label" }
    $script:assertions.Add($Label)
    Write-Output "PASS $Label"
}
function Assert-Rejected([scriptblock]$Action, [string]$Pattern, [string]$Label) {
    $message = ''
    try { $null = & $Action } catch { $message = $_.Exception.Message }
    Assert-True ($message -ne '' -and $message -match $Pattern) $Label
    Assert-True ($message -notmatch 'SECRET|sig=|token=|https://objects') "$Label diagnostics redact secrets"
}
function Write-Json([string]$Path, $Value) {
    [IO.File]::WriteAllText($Path, ($Value | ConvertTo-Json -Depth 15), [Text.UTF8Encoding]::new($false))
}
function New-Root {
    $script:fixture++
    $root = Join-Path $run ('fixture-' + $script:fixture)
    $null = [IO.Directory]::CreateDirectory($root)
    $batch = New-RCDirectory $root $geoBatch
    foreach ($name in @('geosite.dat', 'geoip.dat')) {
        $path = Join-Path $batch $name
        [IO.File]::WriteAllText($path, 'Synthetic isolated Geo fixture: ' + $name)
        [IO.File]::WriteAllText(($path + '.sha256sum'), (Get-FileHash $path -Algorithm SHA256).Hash.ToLowerInvariant() + '  ' + $name)
    }
    $records = @(); [long]$assetId = 5000
    foreach ($name in @(Get-ReleaseGeoAssetNames)) {
        $assetId++
        $record = Get-RCRecord $root "$geoBatch/$name"
        $records += [pscustomobject]@{ id = $assetId; name = $name; size = $record.size; digest = 'sha256:' + $record.sha256 }
    }
    Write-Json (Join-Path $batch 'provenance.json') ([ordered]@{ schemaVersion = 1; verified = $true; status = 'verified-ready'; verifiedAt = '2026-11-23T13:03:00Z';
        geoDataRoot = $geoBatch; source = 'public-official-upstream'; repository = 'MetaCubeX/meta-rules-dat'; releaseId = 300;
        releaseTag = '20261123'; metadataUrl = 'https://api.github.com/repos/MetaCubeX/meta-rules-dat/releases/300';
        releaseMetadataStableAfterDownload = $true; assets = $records })
    return $root
}
function New-PE {
    $bytes = New-Object byte[] 512
    $bytes[0] = 0x4d; $bytes[1] = 0x5a
    [BitConverter]::GetBytes([int]128).CopyTo($bytes, 0x3c)
    $bytes[128] = 0x50; $bytes[129] = 0x45
    [BitConverter]::GetBytes([uint16]0x8664).CopyTo($bytes, 132)
    [BitConverter]::GetBytes([uint16]240).CopyTo($bytes, 148)
    [BitConverter]::GetBytes([uint16]2).CopyTo($bytes, 150)
    [BitConverter]::GetBytes([uint16]0x20b).CopyTo($bytes, 152)
    return ,$bytes
}
function New-Zip($Entries) {
    $memory = [IO.MemoryStream]::new()
    $zip = [IO.Compression.ZipArchive]::new($memory, [IO.Compression.ZipArchiveMode]::Create, $true)
    try {
        foreach ($item in $Entries) {
            $entry = $zip.CreateEntry($item.name, [IO.Compression.CompressionLevel]::Optimal)
            if ($null -ne $item.attributes) { $entry.ExternalAttributes = $item.attributes }
            if ($item.bytes.Length -gt 0) {
                $stream = $entry.Open()
                try { $stream.Write($item.bytes, 0, $item.bytes.Length) } finally { $stream.Dispose() }
            }
        }
    } finally { $zip.Dispose() }
    try { return ,$memory.ToArray() } finally { $memory.Dispose() }
}
function Set-Assets([string]$PanelRoot = 'dist/') {
    $script:assets = @{
        'MetaCubeX/mihomo' = [pscustomobject]@{ id = 100; tag = 'v1.19.34'; name = 'mihomo-windows-amd64-compatible-v1.19.34.zip'; bytes = (New-Zip @([pscustomobject]@{ name = 'mihomo-windows-amd64-compatible.exe'; bytes = (New-PE) })) }
        'Zephyruso/zashboard' = [pscustomobject]@{ id = 200; tag = 'v3.30.0'; name = 'dist-no-fonts.zip'; bytes = (New-Zip @(
            [pscustomobject]@{ name = $PanelRoot + 'index.html'; bytes = [Text.Encoding]::UTF8.GetBytes('<html>synthetic static panel</html>') },
            [pscustomobject]@{ name = $PanelRoot + 'assets/app.js'; bytes = [Text.Encoding]::UTF8.GetBytes('window.syntheticFixture = true;') },
            [pscustomobject]@{ name = $PanelRoot + 'THIRD_PARTY_NOTICES.md'; bytes = [Text.Encoding]::UTF8.GetBytes('synthetic official panel notices text') })) }
        'MetaCubeX/meta-rules-dat' = [pscustomobject]@{ id = 300; tag = '20261123'; name = 'geoip.metadb'; bytes = [Text.Encoding]::UTF8.GetBytes('synthetic metadb ONLY from fixed releaseId 300') }
    }
}
function Invoke-RCTransport {
    [CmdletBinding()]
    param([string]$Uri, [long]$MaxBytes, [string]$Destination = '')
    $script:requests.Add($Uri)
    if ($script:mode -eq 'disabled') { throw 'Network mock was disabled.' }
    if ($script:mode -eq 'transport-error') { throw 'https://objects.example/private?sig=SECRET&token=SECRET' }
    if ($script:mode -eq 'redirect-evil' -and $Destination) { return [pscustomobject]@{ statusCode = 302; location = 'https://evil.example/asset?sig=SECRET'; content = $null } }
    if ($script:mode -eq 'redirect-http' -and $Destination) { return [pscustomobject]@{ statusCode = 302; location = 'http://objects.githubusercontent.com/asset?sig=SECRET'; content = $null } }
    if ($script:mode -eq 'redirect-loop' -and $Destination) { return [pscustomobject]@{ statusCode = 302; location = 'https://objects.githubusercontent.com/object?sig=SECRET'; content = $null } }
    if ($script:mode -eq 'redirect-official' -and $Destination -and ([uri]$Uri).Host -eq 'github.com') {
        return [pscustomobject]@{ statusCode = 302; location = 'https://release-assets.githubusercontent.com/object?sig=SECRET'; content = $null }
    }
    $repository = ''
    foreach ($repo in $script:assets.Keys) { if ($Uri.Contains('/' + $repo + '/')) { $repository = $repo; break } }
    if (([uri]$Uri).Host -eq 'release-assets.githubusercontent.com' -or ([uri]$Uri).Host -eq 'objects.githubusercontent.com') { $repository = 'MetaCubeX/mihomo' }
    if (-not $repository) { throw 'Unexpected mock request.' }
    $fixtureAsset = $script:assets[$repository]
    if ($Destination) {
        $bytes = $fixtureAsset.bytes
        if ($script:mode -eq 'bad-download') { $bytes = [Text.Encoding]::UTF8.GetBytes('tampered') }
        if ($bytes.Length -gt $MaxBytes) { throw 'Synthetic size bound.' }
        $stream = [IO.File]::Open($Destination, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write)
        try { $stream.Write($bytes, 0, $bytes.Length) } finally { $stream.Dispose() }
        return [pscustomobject]@{ statusCode = 200; location = ''; content = $null }
    }
    $base = 'https://api.github.com/repos/' + $repository
    if ($Uri.Contains('/git/ref/tags/')) {
        $object = [pscustomobject]@{ sha = ('a' * 40); type = 'commit'; url = "$base/git/commits/$('a' * 40)" }
        if ($script:mode -eq 'bad-commit') { $object.sha = 'main' }
        if ($script:mode -eq 'annotated') { $object = [pscustomobject]@{ sha = ('b' * 40); type = 'tag'; url = "$base/git/tags/$('b' * 40)" } }
        if ($script:mode -eq 'changed-commit' -and @($script:requests | Where-Object { $_ -ceq $Uri }).Count -ge 2) {
            $object = [pscustomobject]@{ sha = ('c' * 40); type = 'commit'; url = "$base/git/commits/$('c' * 40)" }
        }
        $value = [pscustomobject]@{ ref = 'refs/tags/' + $fixtureAsset.tag; url = "$base/git/refs/tags/$($fixtureAsset.tag)"; object = $object }
    } elseif ($Uri.Contains('/git/tags/')) {
        $value = [pscustomobject]@{ sha = ('b' * 40); url = $Uri; object = [pscustomobject]@{ sha = ('a' * 40); type = 'commit'; url = "$base/git/commits/$('a' * 40)" } }
    } else {
        $sha = [Security.Cryptography.SHA256]::Create()
        try { $digest = 'sha256:' + [BitConverter]::ToString($sha.ComputeHash($fixtureAsset.bytes)).Replace('-', '').ToLowerInvariant() } finally { $sha.Dispose() }
        $asset = [pscustomobject]@{ id = $fixtureAsset.id * 10; name = $fixtureAsset.name; size = $fixtureAsset.bytes.Length; digest = $digest; state = 'uploaded';
            url = "$base/releases/assets/$($fixtureAsset.id * 10)"; browser_download_url = "https://github.com/$repository/releases/download/$($fixtureAsset.tag)/$($fixtureAsset.name)" }
        $selected = @($asset)
        switch ($script:mode) {
            'missing-digest' { $asset.digest = $null }
            'bad-digest-format' { $asset.digest = 'SHA256:' + ('a' * 64) }
            'oversize' { $asset.size = 1073741824 }
            'wrong-asset-url' { $asset.browser_download_url = 'https://evil.example/asset?token=SECRET' }
            'wrong-asset-id' { $asset.id = '10' }
            'duplicate-asset' { $selected = @($asset, $asset) }
            'changed-digest' { if ($Uri -match '/releases/[0-9]+$') { $asset.digest = 'sha256:' + ('e' * 64) } }
        }
        $releaseId = $fixtureAsset.id
        if ($script:mode -eq 'wrong-release-id' -and $Uri -match '/releases/[0-9]+$') { $releaseId++ }
        $value = [pscustomobject]@{ id = $releaseId; draft = $false; tag_name = $fixtureAsset.tag; url = "$base/releases/$releaseId";
            html_url = "https://github.com/$repository/releases/tag/$($fixtureAsset.tag)"; assets = $selected }
        if ($script:mode -eq 'draft') { $value.draft = $true }
        if ($script:mode -eq 'wrong-release-tag') { $value.tag_name = 'latest' }
    }
    $bytes = [Text.Encoding]::UTF8.GetBytes(($value | ConvertTo-Json -Depth 15))
    return [pscustomobject]@{ statusCode = 200; location = ''; content = $bytes }
}

# Token headers are unit-tested without making a request or printing credentials.
$oldToken = $env:GITHUB_TOKEN
try {
    $env:GITHUB_TOKEN = 'SYNTHETIC_TOKEN_NEVER_PRINT'
    $apiHeaders = Get-RCRequestHeaders 'https://api.github.com/repos/MetaCubeX/mihomo/releases/100'
    Assert-True ($apiHeaders['Authorization'] -ceq ('Bearer ' + $env:GITHUB_TOKEN)) 'API metadata gets environment token only'
    $commitHeaders = Get-RCRequestHeaders 'https://api.github.com/repos/Zephyruso/zashboard/git/ref/tags/v3.30.0'
    Assert-True ($commitHeaders.ContainsKey('Authorization')) 'fixed tag API gets read token'
    $assetHeaders = Get-RCRequestHeaders 'https://github.com/MetaCubeX/mihomo/releases/download/v1.19.34/mihomo-windows-amd64-compatible-v1.19.34.zip' 'fixture.zip'
    Assert-True (-not $assetHeaders.ContainsKey('Authorization')) 'browser asset never receives API token'
    $objectHeaders = Get-RCRequestHeaders 'https://release-assets.githubusercontent.com/object?sig=synthetic' 'fixture.zip'
    Assert-True (-not $objectHeaders.ContainsKey('Authorization')) 'official object redirect never receives API token'
    Assert-Rejected { Get-RCRequestHeaders 'https://api.github.com/repos/attacker/unknown/releases/100' } 'fixed official' 'token blocked for non-fixed API repository'
    $env:GITHUB_TOKEN = $null
    Assert-True (-not (Get-RCRequestHeaders 'https://api.github.com/repos/MetaCubeX/mihomo/releases/100').ContainsKey('Authorization')) 'public API works without token'
} finally { $env:GITHUB_TOKEN = $oldToken }
Set-Assets
$root = New-Root
$script:mode = 'disabled'
Assert-Rejected { Invoke-PrepareReleaseComponents -WorkspaceRoot $root -GeoDataRoot $geoBatch } 'No matching verified' 'default offline refuses absent cache'
Assert-True ($script:requests.Count -eq 0) 'offline refusal makes zero requests'
Assert-Rejected { & $source -WorkspaceRoot 'relative' -GeoDataRoot $geoBatch } 'absolute' 'entry point requires absolute workspace'
Assert-Rejected { Invoke-PrepareReleaseComponents -WorkspaceRoot $root -GeoDataRoot '../data' } 'verified release batch' 'Geo traversal rejected'
Assert-Rejected { Invoke-PrepareReleaseComponents -WorkspaceRoot $root -GeoDataRoot $run } 'outside' 'absolute Geo outside workspace rejected'
foreach ($name in @('runtime', 'data', 'logs', 'launcher.json', 'CURRENT.json', 'MihomoLite.exe', 'CURRENT', 'MihomoLite')) {
    $dirty = New-Root
    $marker = Join-Path $dirty $name
    [IO.File]::WriteAllText($marker, 'untouched synthetic marker')
    Assert-Rejected { Invoke-PrepareReleaseComponents -WorkspaceRoot $dirty -GeoDataRoot $geoBatch -Refresh } 'clean isolated' "existing $name rejected"
    Assert-True ([IO.File]::ReadAllText($marker) -ceq 'untouched synthetic marker') "existing $name unchanged"
}
$script:mode = 'normal'; $script:requests.Clear()
$success = New-Root
$result = Invoke-PrepareReleaseComponents -WorkspaceRoot $success -GeoDataRoot (Join-Path $success $geoBatch) -Refresh -PassThru
$manifest = ConvertFrom-Json ([IO.File]::ReadAllText((Join-Path $success $result.manifest)))
Assert-True ($result.components.Count -eq 3 -and $manifest.verified -eq $true) 'three verified components returned'
Assert-True (($manifest.components.name -join ',') -ceq 'core,panel,metadb') 'manifest component names stable'
Assert-True ($manifest.files.Count -eq 5 -and $manifest.directorySHA256 -ceq (Get-RCDirectorySHA $manifest.files)) 'manifest global directory SHA exact'
foreach ($component in $result.components) {
    Assert-True ($component.sourceCommitSHA -ceq ('a' * 40) -and $component.asset.digest -match '^sha256:[0-9a-f]{64}$') "$($component.name) commit and official asset digest"
    foreach ($file in $component.files) { Assert-True ((Get-RCRecord $success $file.path).sha256 -ceq $file.sha256) "$($file.path) destination digest verified" }
    Assert-True ($component.directorySHA256 -ceq (Get-RCDirectorySHA $component.files)) "$($component.name) directory digest verified"
}
Assert-True (@($script:requests | Where-Object { $_ -match '/meta-rules-dat/releases/tags/|/meta-rules-dat/releases/latest' }).Count -eq 0) 'MetaDB never uses latest or independent tag release'
Assert-True (@($script:requests | Where-Object { $_ -ceq 'https://api.github.com/repos/MetaCubeX/meta-rules-dat/releases/300' }).Count -eq 2) 'MetaDB fixed Geo release ID reread'
Assert-True (-not [IO.File]::Exists((Join-Path $success 'data/geosite.dat'))) 'DAT preparation remains separate Geo interface'
Assert-Rejected { Invoke-PrepareReleaseComponents -WorkspaceRoot $success -GeoDataRoot $geoBatch } 'clean isolated' 'repeat cannot overwrite destination'

# Test approved official redirect hops directly; its signed URL is not persisted.
$script:mode = 'redirect-official'; $script:requests.Clear()
$redirectRoot = New-Root
$redirectFile = Get-RCPath $redirectRoot '.cache/downloads/redirect.zip' -File -AllowMissing
Save-RCAsset $result.components[0].asset $redirectFile
Assert-RCAssetFile $redirectRoot '.cache/downloads/redirect.zip' $result.components[0].asset
Assert-True ($script:requests.Count -eq 2) 'official HTTPS object redirect accepted with bounded download'

# Offline reuse requires a clean new workspace with verified cache archives only.
$offline = New-Root
$cacheRelative = Split-Path -Parent $result.manifest
$offlineCache = New-RCDirectory $offline $cacheRelative.Replace('\', '/')
foreach ($file in Get-ChildItem -LiteralPath (Join-Path $success $cacheRelative) -File) { [IO.File]::Copy($file.FullName, (Join-Path $offlineCache $file.Name), $false) }
$script:mode = 'disabled'; $script:requests.Clear()
$offlineResult = Invoke-PrepareReleaseComponents -WorkspaceRoot $offline -GeoDataRoot $geoBatch -PassThru
Assert-True ($script:requests.Count -eq 0 -and $offlineResult.components.Count -eq 3) 'offline cache fully prepares with zero requests'
Assert-True ($offlineResult.components[2].files[0].sha256 -ceq $result.components[2].files[0].sha256) 'offline MetaDB only from matching verified release cache'

# Full unmocked script entry reuses only a local synthetic verified cache.
$entryRoot = New-Root
$entryCache = New-RCDirectory $entryRoot $cacheRelative.Replace('\', '/')
foreach ($file in Get-ChildItem -LiteralPath (Join-Path $success $cacheRelative) -File) { [IO.File]::Copy($file.FullName, (Join-Path $entryCache $file.Name), $false) }
$entryResult = & $source -WorkspaceRoot $entryRoot -GeoDataRoot $geoBatch -PassThru
Assert-True ($entryResult.components.Count -eq 3) 'actual production entry prepares synthetic offline cache'

foreach ($tamper in @('digest','repository','releaseId','sourceCommit','archive','verified-type')) {
    $tamperedRoot = New-Root
    $tamperedCache = New-RCDirectory $tamperedRoot $cacheRelative.Replace('\', '/')
    foreach ($file in Get-ChildItem -LiteralPath (Join-Path $success $cacheRelative) -File) { [IO.File]::Copy($file.FullName, (Join-Path $tamperedCache $file.Name), $false) }
    $cachedManifestPath = Join-Path $tamperedRoot $result.manifest
    $cachedManifest = ConvertFrom-Json ([IO.File]::ReadAllText($cachedManifestPath))
    switch ($tamper) {
        'verified-type' { $cachedManifest.verified = 1 }
        'digest' { $cachedManifest.components[0].asset.digest = 'sha256:' + ('e' * 64) }
        'repository' { $cachedManifest.components[0].repository = 'attacker/mihomo' }
        'releaseId' { $cachedManifest.components[2].releaseId = 301 }
        'sourceCommit' { $cachedManifest.components[0].sourceCommitSHA = 'main' }
        'archive' { [IO.File]::WriteAllText((Join-Path $tamperedRoot $cachedManifest.components[0].asset.path), 'tampered synthetic cached archive') }
    }
    Write-Json $cachedManifestPath $cachedManifest
    $script:mode = 'disabled'; $script:requests.Clear()
    Assert-Rejected { Invoke-PrepareReleaseComponents -WorkspaceRoot $tamperedRoot -GeoDataRoot $geoBatch } 'invalid|match|provenance' "offline cache $tamper rejected"
    Assert-True ($script:requests.Count -eq 0) "offline cache $tamper rejected without network"
    Assert-True (-not [IO.Directory]::Exists((Join-Path $tamperedRoot 'runtime'))) "offline cache $tamper writes no destination"
}
foreach ($mode in @('missing-digest','bad-digest-format','oversize','wrong-asset-url','wrong-asset-id','duplicate-asset','changed-digest','wrong-release-id','wrong-release-tag','draft','bad-download','bad-commit','changed-commit','transport-error','redirect-evil','redirect-http','redirect-loop')) {
    $script:mode = $mode; $script:requests.Clear(); $rejectRoot = New-Root
    Assert-Rejected { Invoke-PrepareReleaseComponents -WorkspaceRoot $rejectRoot -GeoDataRoot $geoBatch -Refresh } 'invalid|missing|changed|match|Unable|redirect|status|allowed' "$mode fails closed"
    Assert-True (-not [IO.Directory]::Exists((Join-Path $rejectRoot 'runtime')) -and -not [IO.Directory]::Exists((Join-Path $rejectRoot 'data'))) "$mode writes no destination"
}
$script:mode = 'normal'
foreach ($prefix in @('', 'dist-no-fonts/')) {
    Set-Assets $prefix
    $compatible = New-Root
    $compatibleResult = Invoke-PrepareReleaseComponents -WorkspaceRoot $compatible -GeoDataRoot $geoBatch -Refresh -PassThru
    Assert-True ([IO.File]::Exists((Join-Path $compatible 'data/ui/index.html'))) "safe panel root '$prefix' normalized"
}
Set-Assets
$script:mode = 'annotated'; $script:requests.Clear()
$annotatedRoot = New-Root
$annotated = Invoke-PrepareReleaseComponents -WorkspaceRoot $annotatedRoot -GeoDataRoot $geoBatch -Refresh -PassThru
Assert-True ($annotated.components[0].sourceCommitSHA -ceq ('a' * 40)) 'annotated tag resolves fixed commit'
$script:mode = 'normal'

# Full pipeline archive rejection must precede *all* destination writes.
foreach ($badArchive in @('core-extra-exe','core-invalid-PE','core-versioned-inner-name','panel-installer')) {
    Set-Assets
    switch ($badArchive) {
        'core-extra-exe' { $script:assets['MetaCubeX/mihomo'].bytes = New-Zip @([pscustomobject]@{ name = 'evil.exe'; bytes = (New-PE) }) }
        'core-invalid-PE' { $script:assets['MetaCubeX/mihomo'].bytes = New-Zip @([pscustomobject]@{ name = 'mihomo-windows-amd64-compatible.exe'; bytes = [Text.Encoding]::UTF8.GetBytes('not a PE') }) }
        'core-versioned-inner-name' { $script:assets['MetaCubeX/mihomo'].bytes = New-Zip @([pscustomobject]@{ name = 'mihomo-windows-amd64-compatible-v1.19.34.exe'; bytes = (New-PE) }) }
        'panel-installer' { $script:assets['Zephyruso/zashboard'].bytes = New-Zip @([pscustomobject]@{ name = 'dist/index.html'; bytes = @(1) }, [pscustomobject]@{ name = 'dist/postinstall.js'; bytes = @(1) }) }
    }
    $badArchiveRoot = New-Root
    Assert-Rejected { Invoke-PrepareReleaseComponents -WorkspaceRoot $badArchiveRoot -GeoDataRoot $geoBatch -Refresh } 'executable|PE|forbidden' "full pipeline $badArchive rejected"
    Assert-True (-not [IO.Directory]::Exists((Join-Path $badArchiveRoot 'runtime')) -and -not [IO.Directory]::Exists((Join-Path $badArchiveRoot 'data'))) "full pipeline $badArchive writes no destinations"
}
Set-Assets
$archiveRoot = New-Root
$panelSpec = (Get-RCSpec 'v1.19.34' 'v3.30.0' ([pscustomobject]@{ releaseId = 300; releaseTag = '20261123' }))[1]
$good = [pscustomobject]@{ name = 'dist/index.html'; bytes = [Text.Encoding]::UTF8.GetBytes('fixture') }
$cases = @(
    [pscustomobject]@{ label = 'zip traversal'; entries = @($good, [pscustomobject]@{ name = '../outside.js'; bytes = @(1) }) },
    [pscustomobject]@{ label = 'zip absolute'; entries = @($good, [pscustomobject]@{ name = '/outside.js'; bytes = @(1) }) },
    [pscustomobject]@{ label = 'zip backslash'; entries = @($good, [pscustomobject]@{ name = 'dist\evil.js'; bytes = @(1) }) },
    [pscustomobject]@{ label = 'zip ADS'; entries = @($good, [pscustomobject]@{ name = 'dist/app.js:stream'; bytes = @(1) }) },
    [pscustomobject]@{ label = 'zip reserved DOS'; entries = @($good, [pscustomobject]@{ name = 'dist/CON.js'; bytes = @(1) }) },
    [pscustomobject]@{ label = 'zip case duplicate'; entries = @($good, [pscustomobject]@{ name = 'dist/INDEX.html'; bytes = @(1) }) },
    [pscustomobject]@{ label = 'zip implicit case dirs'; entries = @($good, [pscustomobject]@{ name = 'dist/assets/a.js'; bytes = @(1) }, [pscustomobject]@{ name = 'dist/ASSETS/b.js'; bytes = @(1) }) },
    [pscustomobject]@{ label = 'zip mixed roots'; entries = @($good, [pscustomobject]@{ name = 'index.html'; bytes = @(1) }) },
    [pscustomobject]@{ label = 'zip symlink'; entries = @($good, [pscustomobject]@{ name = 'dist/link.js'; bytes = @(1); attributes = [int]-1577058304 }) },
    [pscustomobject]@{ label = 'zip file dir collision'; entries = @($good, [pscustomobject]@{ name = 'dist/assets'; bytes = @(1) }, [pscustomobject]@{ name = 'dist/assets/a.js'; bytes = @(1) }) },
    [pscustomobject]@{ label = 'zip node_modules'; entries = @($good, [pscustomobject]@{ name = 'dist/node_modules/a.js'; bytes = @(1) }) },
    [pscustomobject]@{ label = 'zip installer JS'; entries = @($good, [pscustomobject]@{ name = 'dist/postinstall.js'; bytes = @(1) }) },
    [pscustomobject]@{ label = 'zip arbitrary exe'; entries = @($good, [pscustomobject]@{ name = 'dist/evil.exe'; bytes = (New-PE) }) },
    [pscustomobject]@{ label = 'zip package manifest'; entries = @($good, [pscustomobject]@{ name = 'dist/package.json'; bytes = @(1) }) },
    [pscustomobject]@{ label = 'zip empty index'; entries = @([pscustomobject]@{ name = 'index.html'; bytes = @() }) }
)
foreach ($case in $cases) {
    $relative = '.cache/downloads/' + [guid]::NewGuid().ToString('N') + '.zip'
    Write-RCNewFile $archiveRoot $relative (New-Zip $case.entries)
    Assert-Rejected { Get-RCZipPayload $archiveRoot $relative $panelSpec } 'Unsafe|Duplicate|root|symlink|collision|forbidden|limit|case-colliding' $case.label
}
$huge = [pscustomobject]@{ name = 'index.html'; bytes = (New-Object byte[] 16777217) }
$relative = '.cache/downloads/oversize-entry.zip'
Write-RCNewFile $archiveRoot $relative (New-Zip @($huge))
Assert-Rejected { Get-RCZipPayload $archiveRoot $relative $panelSpec } 'size' 'ZIP entry expanded size capped'
$entryList = [Collections.Generic.List[object]]::new()
for ($i = 0; $i -lt 10001; $i++) { $entryList.Add([pscustomobject]@{ name = "x$i.txt"; bytes = @(1) }) }
$relative = '.cache/downloads/too-many.zip'
Write-RCNewFile $archiveRoot $relative (New-Zip $entryList)
Assert-Rejected { Get-RCZipPayload $archiveRoot $relative $panelSpec } 'entry count' 'ZIP entry count capped'
foreach ($change in @('MZ','PE','machine','optional','DLL')) {
    $pe = New-PE
    switch ($change) { 'MZ' { $pe[0] = 0 }; 'PE' { $pe[128] = 0 }; 'machine' { $pe[132] = 0 }; 'optional' { $pe[152] = 0 }; 'DLL' { $pe[151] = 0x20 } }
    Assert-Rejected { Assert-RCPE $pe } 'PE' "PE $change statically rejected"
}
$writeRoot = New-Root
Write-RCNewFile $writeRoot '.cache/downloads/existing.txt' ([Text.Encoding]::UTF8.GetBytes('original'))
Assert-Rejected { Write-RCNewFile $writeRoot '.cache/downloads/existing.txt' @(2) } 'exist' 'CreateNew never overwrites'
Assert-True ([IO.File]::ReadAllText((Join-Path $writeRoot '.cache/downloads/existing.txt')) -ceq 'original') 'existing destination remains unchanged'
$junctionRoot = New-Root
$outside = Join-Path $run 'junction-target'; $null = [IO.Directory]::CreateDirectory($outside)
$junction = Join-Path $junctionRoot 'alias'
New-Item -ItemType Junction -Path $junction -Target $outside | Out-Null
Assert-Rejected { Get-RCPath $junctionRoot 'alias/nested/evil.txt' -File -AllowMissing } 'Reparse' 'destination reparse ancestor rejected'
Assert-Rejected { Invoke-PrepareReleaseComponents -WorkspaceRoot $junction -GeoDataRoot $geoBatch } 'Reparse' 'workspace reparse ancestor rejected'
Assert-True (@(Get-ChildItem -LiteralPath $outside -Force).Count -eq 0) 'reparse target untouched'

$summary = [ordered]@{ schemaVersion = 1; status = 'passed'; powershellVersion = $PSVersionTable.PSVersion.ToString(); scriptHashes = @($source, (Join-Path $WorkspaceRoot 'tools/release-download.ps1'), $PSCommandPath | ForEach-Object { [pscustomobject]@{ path = $_; sha256 = (Get-FileHash -LiteralPath $_ -Algorithm SHA256).Hash.ToLowerInvariant() } }); assertionCount = $script:assertions.Count; assertions = $script:assertions.ToArray();
    network = 'all mocked'; realCoreExecution = $false; runtimeOriginalFilesUsed = $false; versions = @('v1.19.34','v3.30.0'); fixtureRoot = $run; componentManifest = (Join-Path $success $result.manifest) }
Write-Json (Join-Path $run 'results.json') $summary
Write-Output "Completed $($script:assertions.Count) isolated mocked assertions; retained evidence: $run"
