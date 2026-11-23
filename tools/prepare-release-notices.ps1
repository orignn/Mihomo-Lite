#requires -Version 5.1
<#
.SYNOPSIS
Collects public third-party evidence in an isolated release workspace; never grants rights.
.DESCRIPTION
Refresh is the ONLY network mode. Without it, validates an existing immutable notices
batch and exact LICENSES inventory. A blocked manifest is returned as blocked, not ready.
GPL source archives are separate release assets, never installation ZIP records.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$WorkspaceRoot,
    [Parameter(Mandatory = $true)][string]$ComponentManifest,
    [string]$GoModCache = '.cache/toolchain/gomod',
    [string]$NoticesManifest = '',
    [switch]$Refresh,
    [switch]$PassThru
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2.0

function Get-NoticeProperty($Object, [string]$Name, $Default = $null) {
    if ($null -ne $Object -and $null -ne $Object.PSObject.Properties[$Name]) { return $Object.$Name }
    return $Default
}
function Assert-NoticeItem([string]$Path, [switch]$Directory, [switch]$Missing) {
    try { $a = [IO.File]::GetAttributes($Path) }
    catch [IO.FileNotFoundException], [IO.DirectoryNotFoundException] { if ($Missing) { return }; throw 'Required notices input is missing.' }
    if ($a -band [IO.FileAttributes]::ReparsePoint) { throw 'Reparse points are forbidden in notices paths.' }
    if ([bool]($a -band [IO.FileAttributes]::Directory) -ne [bool]$Directory) { throw 'Unexpected notices file/directory type.' }
    return $a
}
function Get-NoticeRoot([string]$Root) {
    if (-not [IO.Path]::IsPathRooted($Root) -or ([IO.Path]::DirectorySeparatorChar -eq [char]92 -and $Root -notmatch '^[A-Za-z]:[/\\]')) {
        throw 'WorkspaceRoot must be an absolute local release workspace.'
    }
    $p = [IO.Path]::GetFullPath($Root).TrimEnd([char[]]@('/', [char]92))
    $s = New-Object 'Collections.Generic.Stack[string]'
    for ($q = $p; $q; $q = [IO.Path]::GetDirectoryName($q)) { $s.Push($q) }
    foreach ($q in $s) { $null = Assert-NoticeItem $q -Directory }
    $sourceRoot = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot)).TrimEnd([char[]]@('/', [char]92))
    if ([IO.File]::Exists((Join-Path $p 'CURRENT.json')) -or [IO.File]::Exists((Join-Path $p 'MihomoLite.exe')) -or [IO.File]::Exists((Join-Path $p 'launcher.json')) -or [IO.Directory]::Exists((Join-Path $p 'logs'))) {
        throw 'Use an isolated source-clone release workspace, not the daily workspace.'
    }
    return $p
}
function Get-NoticePath([string]$Relative, [switch]$Directory, [switch]$Missing) {
    if ([string]::IsNullOrWhiteSpace($Relative) -or [IO.Path]::IsPathRooted($Relative) -or $Relative.Contains([string][char]92)) { throw 'Notices paths must be safe workspace-relative slash paths.' }
    $p = $script:NoticeRoot
    $segments = $Relative.Split('/')
    for ($i = 0; $i -lt $segments.Count; $i++) {
        $v = $segments[$i]
        if ($v -in @('', '.', '..') -or $v -match '[\x00-\x1f<>:"|?*\\]|[. ]$|^(?i:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:[ .]|$)') { throw 'Unsafe notices path segment.' }
        $p = [IO.Path]::Combine($p, $v)
        $null = Assert-NoticeItem $p -Directory:($Directory -or $i -lt ($segments.Count - 1)) -Missing:$Missing
    }
    return $p
}
function Get-NoticeHash([byte[]]$Bytes) {
    $h = [Security.Cryptography.SHA256]::Create()
    try { return [BitConverter]::ToString($h.ComputeHash($Bytes)).Replace('-', '').ToLowerInvariant() } finally { $h.Dispose() }
}
function Get-NoticeRecord([string]$Relative) {
    $p = Get-NoticePath $Relative
    $stream = [IO.File]::Open($p, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    try {
        if ($stream.Length -le 0) { throw 'An evidence file is empty.' }
        $h = [Security.Cryptography.SHA256]::Create()
        try { $hash = [BitConverter]::ToString($h.ComputeHash($stream)).Replace('-', '').ToLowerInvariant() } finally { $h.Dispose() }
        return [pscustomobject]@{ path = $Relative; sha256 = $hash; size = $stream.Length }
    } finally { $stream.Dispose() }
}
function Read-NoticeText([string]$Relative) {
    $p = Get-NoticePath $Relative
    if ((Get-Item -LiteralPath $p).Length -gt 8388608) { throw 'Notices text input exceeds the safe size limit.' }
    return [IO.File]::ReadAllText($p, [Text.Encoding]::UTF8)
}
function Write-NoticeBytes([string]$Relative, [byte[]]$Bytes) {
    $p = Get-NoticePath $Relative -Missing
    $parent = [IO.Path]::GetDirectoryName($p)
    [void][IO.Directory]::CreateDirectory($parent)
    $null = Get-NoticePath $Relative -Missing
    $s = [IO.File]::Open($p, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $s.Write($Bytes, 0, $Bytes.Length) } finally { $s.Dispose() }
}
function Write-NoticeText([string]$Relative, [string]$Text) {
    Write-NoticeBytes $Relative ([Text.Encoding]::UTF8.GetBytes($Text))
}
function Assert-NoticeHash($Expected) {
    $path = [string](Get-NoticeProperty $Expected 'path' '')
    $sha = Get-NoticeProperty $Expected 'sha256' ''
    $size = Get-NoticeProperty $Expected 'size' 0
    if ($sha -isnot [string] -or $sha -cnotmatch '^[a-f0-9]{64}$' -or $size -le 0) { throw 'Invalid notices record digest/size.' }
    $actual = Get-NoticeRecord $path
    if ($actual.sha256 -cne $sha -or $actual.size -ne $size) { throw 'Notices input or evidence hash/size mismatch.' }
}
function Get-NoticeTree([string]$Relative) {
    $p = Get-NoticePath $Relative -Directory
    foreach ($child in @(Get-ChildItem -LiteralPath $p -Force)) {
        if ($child.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Reparse points are forbidden in evidence trees.' }
        $r = $Relative + '/' + $child.Name
        if ($child.PSIsContainer) { Get-NoticeTree $r } else { $null = Get-NoticePath $r; $r }
    }
}
function Test-NoticeLicenseName([string]$Name) {
    return $Name -cmatch '^(?i:LICENSE|LICENCE|NOTICE|COPYING|COPYRIGHT)(?:[._-][A-Za-z0-9._-]+)?$'
}
function Get-NoticeModules {
    $mod = Read-NoticeText 'go.mod'
    if ($mod -match '(?m)^\s*(replace|exclude)\b' -or $mod -match '/\*') { throw 'Module replace/exclude and unsupported go.mod syntax are forbidden.' }
    $locks = @{}
    foreach ($line in (Read-NoticeText 'go.sum').Split([char]10)) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        if ($line.Trim() -notmatch '^([A-Za-z0-9][A-Za-z0-9._~!/+-]*) (v[0-9][A-Za-z0-9.+-]*)(/go\.mod)? (h1:[A-Za-z0-9+/]{43}=)$') { throw 'Malformed module lock entry.' }
        $name = $Matches[1]; $version = $Matches[2]; $gomod = $Matches[3]; $sum = $Matches[4]
        if ($name -notmatch '^[a-z0-9][a-z0-9.-]*\.[a-z][A-Za-z0-9._~!/+-]*/' -or $name.Contains('..') -or $name.Contains('!') -or $name.Contains('//')) { throw 'Non-public or unsafe module path.' }
        if (-not $gomod) {
            $key = $name + '@' + $version
            if ($locks.ContainsKey($key)) { throw 'Duplicate module content lock.' }
            $locks[$key] = [pscustomobject]@{ module = $name; version = $version; goSum = $sum }
        }
    }
    $inRequire = $false
    foreach ($line in $mod.Split([char]10)) {
        $line = ($line -replace '//.*$', '').Trim()
        if ($line -eq 'require (') { $inRequire = $true; continue }
        if ($inRequire -and $line -eq ')') { $inRequire = $false; continue }
        if ($inRequire -or $line -match '^require ') {
            $line = $line -replace '^require\s+', ''
            if ($line -eq '') { continue }
            if ($line -notmatch '^(\S+)\s+(v\S+)$' -or -not $locks.ContainsKey($Matches[1] + '@' + $Matches[2])) { throw 'A required module has no pinned content hash in go.sum.' }
        }
    }
    if ($locks.Count -eq 0) { throw 'No public module content locks found.' }
    return @($locks.Values | Sort-Object module, version)
}
function Convert-NoticeModuleEscape([string]$Name) {
    return [regex]::Replace($Name, '[A-Z]', { param($m) '!' + $m.Value.ToLowerInvariant() })
}
function Get-NoticeModuleEvidence($Modules) {
    if ($GoModCache -cnotmatch '^\.cache/toolchain/[A-Za-z0-9._/-]+$') { throw 'GOMODCACHE must be inside this release workspace .cache/toolchain.' }
    $null = Get-NoticePath $GoModCache -Directory
    foreach ($m in $Modules) {
        $escaped = Convert-NoticeModuleEscape $m.module
        $base = $GoModCache + '/' + $escaped + '@' + $m.version
        $null = Get-NoticePath $base -Directory
        if ((Read-NoticeText ($base + '/go.mod')) -match '(?m)^\s*replace\b') { throw 'Module replacement declarations are not accepted.' }
        $ziphash = $GoModCache + '/cache/download/' + $escaped + '/@v/' + $m.version + '.ziphash'
        if ((Read-NoticeText $ziphash).Trim() -cne $m.goSum) { throw 'Local module cache ziphash differs from go.sum.' }
        $licenseFiles = @(Get-NoticeTree $base | Where-Object { Test-NoticeLicenseName ([IO.Path]::GetFileName($_)) })
        if (@($licenseFiles | Where-Object { [IO.Path]::GetFileName($_) -match '^(?i:LICENSE|LICENCE|COPYING)(?:[._-]|$)' }).Count -eq 0) { throw 'A pinned Go module has no local LICENSE/LICENCE/COPYING evidence.' }
        $id = (Get-NoticeHash ([Text.Encoding]::UTF8.GetBytes($m.module + '@' + $m.version))).Substring(0, 20)
        $files = @()
        foreach ($file in $licenseFiles) {
            $relative = $file.Substring($base.Length + 1)
            $target = 'LICENSES/go-' + $id + '/' + (Get-NoticeHash ([Text.Encoding]::UTF8.GetBytes($relative))).Substring(0, 16) + '.txt'
            $files += [pscustomobject]@{ input = (Get-NoticeRecord $file); path = $target; originalPath = $relative }
        }
        [pscustomobject]@{ module = $m.module; version = $m.version; goSum = $m.goSum; cacheVerification = 'go.sum matches local ziphash; not an independent sumdb authenticity attestation'; files = $files; lockInputs = @((Get-NoticeRecord ($base + '/go.mod')), (Get-NoticeRecord $ziphash)) }
    }
}
function Invoke-NoticeGitHub([string]$Url, [switch]$Missing) {
    if (-not $Refresh) { throw 'Network requires explicit Refresh.' }
    if ($Url -cnotmatch '^https://api\.github\.com/repos/(?:MetaCubeX/(?:mihomo|meta-rules-dat)|Zephyruso/zashboard|Loyalsoldier/(?:v2ray-rules-dat|geoip|domain-list-custom)|blackmatrix7/ios_rule_script|xishang0128/rules|XIU2/TrackersListCollection|v2fly/domain-list-community|gfwlist/gfwlist|crazy-max/WindowsSpyBlocker|gaoyifan/china-operator-ip|felixonmars/dnsmasq-china-list)/[A-Za-z0-9._/?=&%+-]+$') { throw 'Only fixed official public GitHub evidence URLs are allowed.' }
    try {
        $headers = @{ 'User-Agent' = 'MihomoLite-release-notices'; Accept = 'application/vnd.github+json' }
        if ($env:GITHUB_TOKEN) { $headers.Authorization = 'Bearer ' + $env:GITHUB_TOKEN }
        $r = Invoke-WebRequest -Uri $Url -UseBasicParsing -MaximumRedirection 0 -TimeoutSec 60 -Headers $headers -Verbose:$false -Debug:$false
        if ($r.StatusCode -ne 200) { throw 'Unexpected GitHub evidence response.' }
        return ConvertFrom-Json -InputObject $r.Content
    } catch {
        $response = Get-NoticeProperty $_.Exception 'Response'
        if ($Missing -and (Get-NoticeProperty $response 'StatusCode' 0) -eq 404) { return }
        throw 'Unable to read fixed public GitHub evidence (raw/signed errors suppressed).'
    }
}
function Get-NoticeDocument([string]$Repository, [string]$Commit, [string]$File, [string]$Slug) {
    if ($Commit -cnotmatch '^[a-f0-9]{40}$') { throw 'License source must be a fixed full commit.' }
    $url = 'https://api.github.com/repos/' + $Repository + '/contents/' + $File + '?ref=' + $Commit
    $r = Invoke-NoticeGitHub $url -Missing
    if ($null -eq $r) { return [pscustomobject]@{ repository = $Repository; commit = $Commit; originalPath = $File; url = $url; missing = $true } }
    if ((Get-NoticeProperty $r 'encoding') -cne 'base64' -or (Get-NoticeProperty $r 'type') -cne 'file' -or (Get-NoticeProperty $r 'path') -cne $File) { throw 'Unexpected public source evidence format.' }
    try { $bytes = [Convert]::FromBase64String($r.content) } catch { throw 'Malformed public evidence encoding.' }
    if ($bytes.Length -eq 0 -or $bytes.Length -gt 8388608) { throw 'Public evidence text exceeds size limits or is empty.' }
    $name = (Get-NoticeHash ([Text.Encoding]::UTF8.GetBytes($File))).Substring(0, 16) + '.txt'
    if ($File -eq 'LICENSE') { $name = 'LICENSE.txt' }
    elseif ($File -eq 'README.md') { $name = 'README.md' }
    $path = 'LICENSES/' + $Slug + '/' + $name
    Write-NoticeBytes $path $bytes
    return [pscustomobject]@{ repository = $Repository; commit = $Commit; originalPath = $File; url = $url; path = $path; missing = $false }
}
function Save-NoticeArchive([string]$Repository, [string]$Commit, [string]$Component, [string]$Batch) {
    $url = 'https://github.com/' + $Repository + '/archive/' + $Commit + '.tar.gz'
    $relative = $Batch + '/' + $Component + '-source-' + $Commit + '.tar.gz'
    $p = Get-NoticePath $relative -Missing
    if ([IO.File]::Exists($p)) { throw 'Existing source archive must not be overwritten.' }
    # Only GitHub/codeload redirects, without credentials, can receive this request.
    $uri = $url
    for ($i = 0; $i -lt 5; $i++) {
        if ($uri -cnotmatch '^https://(?:github\.com/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/archive/[a-f0-9]{40}\.tar\.gz|codeload\.github\.com/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/tar.gz/[a-f0-9]{40})$') { throw 'Source archive redirect left fixed public GitHub hosts.' }
        $request = [Net.HttpWebRequest]::Create($uri)
        $request.AllowAutoRedirect = $false; $request.Timeout = 120000; $request.UserAgent = 'MihomoLite-release-notices'
        try { $response = $request.GetResponse() } catch { throw 'Unable to download fixed public source archive.' }
        try {
            if ([int]$response.StatusCode -in @(301,302,303,307,308)) { $uri = [string]$response.Headers['Location']; continue }
            if ([int]$response.StatusCode -ne 200) { throw 'Unexpected source archive response.' }
            $input = $response.GetResponseStream()
            $output = [IO.File]::Open($p, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
            try {
                $buffer = New-Object byte[] 65536; $total = 0L
                while (($n = $input.Read($buffer, 0, $buffer.Length)) -gt 0) {
                    $total += $n; if ($total -gt 268435456) { throw 'Source archive exceeds 256 MiB safe limit.' }
                    $output.Write($buffer, 0, $n)
                }
            } finally { $output.Dispose(); $input.Dispose() }
            $record = Get-NoticeRecord $relative
            return [pscustomobject]@{ path = $record.path; sha256 = $record.sha256; size = $record.size; component = $Component; repository = $Repository; commit = $Commit; url = $url }
        } finally { $response.Dispose() }
    }
    throw 'Too many source archive redirects.'
}

function Get-NoticePolicy([string]$Url, [string]$Path) {
    if (-not $Refresh -or $Url -cnotin @('https://www.maxmind.com/en/geolite2/eula','https://creativecommons.org/licenses/by-sa/4.0/legalcode.txt')) { throw 'Only fixed official license-policy URLs are allowed in Refresh.' }
    try { $r = Invoke-WebRequest -Uri $Url -UseBasicParsing -MaximumRedirection 0 -TimeoutSec 60 -Verbose:$false -Debug:$false }
    catch { throw 'Unable to obtain required official GeoLite license policy.' }
    if ($r.StatusCode -ne 200 -or $r.Content.Length -eq 0 -or $r.Content.Length -gt 8388608) { throw 'Invalid official license-policy response.' }
    Write-NoticeText $Path ([string]$r.Content)
    return [pscustomobject]@{ url = $Url; path = $Path; retrievedAt = [datetime]::UtcNow.ToString('o'); format = 'original public policy response' }
}
function Assert-NoticeInputPath([string]$Path) {
    if ($Path -cin @('go.mod','go.sum','LICENSE') -or $Path -ceq $ComponentManifest) { return }
    if ($Path.StartsWith($GoModCache + '/', [StringComparison]::Ordinal) -and ($Path -cmatch '/(?:go\.mod|LICENSE[A-Za-z0-9._-]*|LICENCE[A-Za-z0-9._-]*|NOTICE[A-Za-z0-9._-]*|COPYING[A-Za-z0-9._-]*|COPYRIGHT[A-Za-z0-9._-]*|v[A-Za-z0-9.+-]+\.ziphash)$')) { return }
    if ($Path -cmatch '^\.cache/downloads/release-geodata-[A-Za-z0-9._-]+/(?:provenance\.json|geosite\.dat|geoip\.dat|geosite\.dat\.sha256sum|geoip\.dat\.sha256sum)$') { return }
    throw 'Notice input references a non-public or unapproved workspace path.'
}
$script:NoticeRoot = Get-NoticeRoot $WorkspaceRoot
if ($ComponentManifest -cnotmatch '^\.cache/downloads/release-components[^/]*/components\.json$') { throw 'ComponentManifest must be a relative release-components batch components.json.' }
$componentInput = Get-NoticeRecord $ComponentManifest
$componentData = ConvertFrom-Json -InputObject (Read-NoticeText $ComponentManifest)
if ($componentData.schemaVersion -ne 1 -or $componentData.status -cne 'verified-ready' -or $componentData.verified -cne $true) { throw 'Component manifest must be verified-ready.' }
$moduleLocks = @(Get-NoticeModules)
$modInput = Get-NoticeRecord 'go.mod'; $sumInput = Get-NoticeRecord 'go.sum'
$goEvidence = @(Get-NoticeModuleEvidence $moduleLocks)
$requiredInputs = @($componentInput,$modInput,$sumInput)
foreach ($m in $goEvidence) { $requiredInputs += $m.lockInputs; foreach ($f in $m.files) { $requiredInputs += $f.input } }
$geoRoot = [string](Get-NoticeProperty $componentData 'geoDataRoot' '')
if ($geoRoot -cnotmatch '^\.cache/downloads/release-geodata-[A-Za-z0-9._-]+$') { throw 'A same-batch public Geo provenance directory is required.' }
$geoProof = ConvertFrom-Json -InputObject (Read-NoticeText ($geoRoot + '/provenance.json'))
if ($geoProof.schemaVersion -ne 1 -or $geoProof.status -cne 'verified-ready' -or $geoProof.verified -cne $true -or $geoProof.repository -cne 'MetaCubeX/meta-rules-dat' -or $geoProof.releaseId -ne $componentData.geoReleaseId) { throw 'Geo provenance disagrees with component batch.' }
foreach ($f in @('provenance.json','geosite.dat','geoip.dat','geosite.dat.sha256sum','geoip.dat.sha256sum')) { $requiredInputs += Get-NoticeRecord ($geoRoot + '/' + $f) }
if (-not $Refresh) {
    if (-not $NoticesManifest) {
        $downloads = Get-NoticePath '.cache/downloads' -Directory
        $candidates = @()
        foreach ($dir in @(Get-ChildItem -LiteralPath $downloads -Directory -Force)) {
            if ($dir.Name -cmatch '^release-notices-[A-Za-z0-9._-]+$') {
                $r = '.cache/downloads/' + $dir.Name + '/notices.json'
                $null = Get-NoticePath ('.cache/downloads/' + $dir.Name) -Directory
                if ([IO.File]::Exists((Get-NoticePath $r -Missing))) { $candidates += $r }
            }
        }
        if ($candidates.Count -ne 1) { throw 'Specify NoticesManifest when zero or multiple local batches exist.' }
        $NoticesManifest = $candidates[0]
    }
    if ($NoticesManifest -cnotmatch '^\.cache/downloads/release-notices-[A-Za-z0-9._-]+/notices\.json$') { throw 'Unsafe notices manifest location.' }
    $manifest = ConvertFrom-Json -InputObject (Read-NoticeText $NoticesManifest)
    if ($manifest.schemaVersion -ne 1 -or $manifest.status -cnotin @('verified-ready','redistribution-blocked') -or $manifest.redistributionReady -isnot [bool] -or ($manifest.status -ceq 'verified-ready') -ne $manifest.redistributionReady) { throw 'Invalid readiness schema.' }
    $inputSeen = @{}
    foreach ($record in @($manifest.inputs)) {
        Assert-NoticeInputPath $record.path
        if ($inputSeen.ContainsKey($record.path)) { throw 'Duplicate notices input record.' }; $inputSeen[$record.path] = $true
        Assert-NoticeHash $record
    }
    foreach ($r in $requiredInputs) {
        if (@($manifest.inputs | Where-Object { $_.path -ceq $r.path -and $_.sha256 -ceq $r.sha256 -and $_.size -eq $r.size }).Count -ne 1) { throw 'Notices are stale for these component/source/license inputs.' }
    }
    $seen = @{}
    foreach ($record in @($manifest.records)) {
        if ($record.path -cnotin @('THIRD_PARTY_NOTICES.md','LICENSES/sources.json','LICENSES/manifest.json','LICENSES/SOURCE_OFFER.md') -and $record.path -cnotmatch '^LICENSES/[a-z0-9][a-z0-9-]*/[A-Za-z0-9._-]+\.(?:txt|md|json)$') { throw 'Notices record is outside the exact license payload whitelist.' }
        if ($seen.ContainsKey($record.path)) { throw 'Duplicate notices payload record.' }; $seen[$record.path] = $true
        Assert-NoticeHash $record
    }
    foreach ($p in @('THIRD_PARTY_NOTICES.md','LICENSES/sources.json','LICENSES/manifest.json','LICENSES/SOURCE_OFFER.md')) { if (-not $seen.ContainsKey($p)) { throw 'Required notice files are missing from records.' } }
    foreach ($r in @(Get-NoticeTree 'LICENSES')) { if (-not $seen.ContainsKey($r)) { throw 'Unlisted LICENSES payload is forbidden.' } }
    $index = ConvertFrom-Json -InputObject (Read-NoticeText 'LICENSES/manifest.json')
    $indexed = @($manifest.records | Where-Object { $_.path.StartsWith('LICENSES/', [StringComparison]::Ordinal) -and $_.path -cne 'LICENSES/manifest.json' })
    if ($index.schemaVersion -ne 1 -or @($index.records).Count -ne $indexed.Count) { throw 'License index disagrees with cache manifest.' }
    foreach ($r in $indexed) { if (@($index.records | Where-Object { $_.path -ceq $r.path -and $_.sha256 -ceq $r.sha256 -and $_.size -eq $r.size }).Count -ne 1) { throw 'License index record mismatch.' } }
    if (@($index.sourceArchives).Count -ne @($manifest.sourceArchives).Count) { throw 'License index source archive count mismatch.' }
    foreach ($a in @($manifest.sourceArchives)) {
        if ($a.path -cnotmatch '^\.cache/downloads/release-notices-[A-Za-z0-9._-]+/(?:core|panel|metadb)-source-[a-f0-9]{40}\.tar\.gz$' -or $a.commit -cnotmatch '^[a-f0-9]{40}$' -or $a.repository -cnotin @('MetaCubeX/mihomo','Zephyruso/zashboard','MetaCubeX/meta-rules-dat') -or $a.url -cne ('https://github.com/' + $a.repository + '/archive/' + $a.commit + '.tar.gz')) { throw 'Invalid fixed source archive evidence.' }
        if (@($index.sourceArchives | Where-Object { $_.path -ceq $a.path -and $_.sha256 -ceq $a.sha256 -and $_.size -eq $a.size -and $_.commit -ceq $a.commit -and $_.repository -ceq $a.repository -and $_.url -ceq $a.url }).Count -ne 1) { throw 'License index source archive mismatch.' }
        Assert-NoticeHash $a
    }
    $manifest | Add-Member -NotePropertyName manifestPath -NotePropertyValue $NoticesManifest -Force
    $manifest | Add-Member -NotePropertyName manifest -NotePropertyValue $NoticesManifest -Force
    return $manifest
}
if ($NoticesManifest) { throw 'Refresh creates a new batch; NoticesManifest is offline-only.' }
foreach ($r in @('THIRD_PARTY_NOTICES.md','LICENSES')) {
    $p = Get-NoticePath $r -Directory:($r -eq 'LICENSES') -Missing
    if ([IO.File]::Exists($p) -or [IO.Directory]::Exists($p)) { throw 'Refresh refuses existing notices output; use a fresh independent release workspace.' }
}
$batch = '.cache/downloads/release-notices-' + [datetime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ') + '-' + [guid]::NewGuid().ToString('N')
[void][IO.Directory]::CreateDirectory((Get-NoticePath $batch -Directory -Missing))
$inputs = $requiredInputs
foreach ($m in $goEvidence) { foreach ($f in $m.files) { Write-NoticeBytes $f.path ([IO.File]::ReadAllBytes((Get-NoticePath $f.input.path))) } }
$evidence = @(); $archives = @(); $issues = New-Object 'Collections.Generic.List[string]'; $limitations = New-Object 'Collections.Generic.List[string]'
$components = @(Get-NoticeProperty $componentData 'components' @())
$official = [ordered]@{ mhCore = 'MetaCubeX/mihomo'; Panel = 'Zephyruso/zashboard'; Geo = 'MetaCubeX/meta-rules-dat' }
foreach ($key in $official.Keys) {
    $repo = $official[$key]
    $matches = @($components | Where-Object { (Get-NoticeProperty $_ 'repository' '') -ceq $repo })
    if ($matches.Count -ne 1) { throw 'Component manifest must identify exactly one official core, panel, and Geo repository.' }
    $c = $matches[0]; $id = $c.releaseId; $tag = $c.releaseTag; $commit = $c.sourceCommitSHA
    if ($id -le 0 -or $tag -isnot [string] -or $tag -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$' -or $commit -cnotmatch '^[a-f0-9]{40}$') { throw 'Missing fixed component release ID/tag/commit.' }
    if ($key -eq 'Geo' -and ($id -ne $geoProof.releaseId -or $tag -cne $geoProof.releaseTag)) { throw 'Geo source release differs from DAT provenance batch.' }
    $meta = Invoke-NoticeGitHub ('https://api.github.com/repos/' + $repo + '/releases/' + $id)
    if ($meta.id -ne $id -or $meta.tag_name -cne $tag -or $meta.draft) { throw 'Component release metadata disagrees with the locked batch.' }
    $commitMeta = Invoke-NoticeGitHub ('https://api.github.com/repos/' + $repo + '/commits/' + $tag)
    if ($commitMeta.sha -cne $commit) { throw 'Release tag commit changed from component manifest.' }
    $slug = @{ mhCore = 'mhcore'; Panel = 'panel'; Geo = 'geo' }[$key]
    $docs = @()
    $tree = Invoke-NoticeGitHub ('https://api.github.com/repos/' + $repo + '/git/trees/' + $commit + '?recursive=1')
    if ($tree.truncated) { throw 'Upstream license tree listing was truncated.' }
    $files = @('LICENSE','NOTICE','COPYING','README.md','go.mod','go.sum')
    foreach ($entry in @($tree.tree)) { if ($entry.type -eq 'blob' -and (Test-NoticeLicenseName ([IO.Path]::GetFileName($entry.path))) -and $entry.path -cnotin $files) { $files += $entry.path } }
    foreach ($file in $files) { $docs += Get-NoticeDocument $repo $commit $file $slug }
    if (@($docs | Where-Object { $_.originalPath -in @('LICENSE','COPYING') -and -not $_.missing }).Count -eq 0) { $issues.Add($key + ': corresponding release has no LICENSE/COPYING; no permission can be inferred.') }
    if ($key -in @('mhCore','Geo')) { $archives += Save-NoticeArchive $repo $commit (@{ mhCore = 'core'; Geo = 'metadb' }[$key]) $batch }
    $evidence += [pscustomobject]@{ component = $key; repository = $repo; releaseId = $id; tag = $tag; commit = $commit; releaseUrl = 'https://api.github.com/repos/' + $repo + '/releases/' + $id; documents = $docs }
    $finish = Invoke-NoticeGitHub ('https://api.github.com/repos/' + $repo + '/commits/' + $tag)
    if ($finish.sha -cne $commit) { throw 'Release source tag moved during license preparation.' }
}
$geoSources = [ordered]@{
    'Loyalsoldier/v2ray-rules-dat' = 'master'; 'Loyalsoldier/geoip' = 'master'; 'Loyalsoldier/domain-list-custom' = 'master';
    'blackmatrix7/ios_rule_script' = 'master'; 'xishang0128/rules' = 'main'; 'XIU2/TrackersListCollection' = 'master';
    'v2fly/domain-list-community' = 'master'; 'gfwlist/gfwlist' = 'master'; 'crazy-max/WindowsSpyBlocker' = 'master';
    'gaoyifan/china-operator-ip' = 'master'; 'felixonmars/dnsmasq-china-list' = 'master'
}
$geo = @($evidence | Where-Object { $_.component -eq 'Geo' })[0]
$evidence += Get-NoticeDocument 'MetaCubeX/meta-rules-dat' $geo.commit '.github/workflows/run.yml' 'geo'
foreach ($repo in $geoSources.Keys) {
    $commitResult = Invoke-NoticeGitHub ('https://api.github.com/repos/' + $repo + '/commits/' + $geoSources[$repo]) -Missing
    if ($null -eq $commitResult) { $issues.Add('A named Geo source repository is unavailable: ' + $repo); continue }
    $commit = [string]$commitResult.sha
    $slug = 'geo-source-' + (Get-NoticeHash ([Text.Encoding]::UTF8.GetBytes($repo))).Substring(0, 12)
    $docs = @()
    foreach ($file in @('LICENSE','LICENSE.md','NOTICE','COPYING','README.md')) { $docs += Get-NoticeDocument $repo $commit $file $slug }
    if ($repo -eq 'xishang0128/rules') { foreach ($file in @('biliintl.list','sharepoint.list')) { $docs += Get-NoticeDocument $repo $commit $file $slug } }
    if (@($docs | Where-Object { $_.originalPath -in @('LICENSE','LICENSE.md','COPYING') -and -not $_.missing }).Count -eq 0) { $limitations.Add('No explicit license located for named factual domain-list source ' + $repo + '; public availability is not a blanket grant for copyrightable material.') }
    $evidence += [pscustomobject]@{ repository = $repo; commit = $commit; role = 'current public license/attribution research snapshot, not a claimed same-release generation input'; documents = $docs }
}
$evidence += Get-NoticePolicy 'https://www.maxmind.com/en/geolite2/eula' 'LICENSES/geolite/maxmind-eula.txt'
$evidence += Get-NoticePolicy 'https://creativecommons.org/licenses/by-sa/4.0/legalcode.txt' 'LICENSES/geolite/CC-BY-SA-4.0.txt'
$limitations.Add('Official upstream Geo release identifies published assets and generator source, not all historical immutable external input revisions. Current source-license snapshots are not proof of full bit-for-bit reproducibility.')
$limitations.Add('GPL corresponding-source directions identify the fixed official release source archive and preserved build/Go lock files. Required non-System-Library dependencies and generation inputs remain governed by their own notices; distributor must maintain equivalent source access, not merely link latest branches.')
$limitations.Add('blackmatrix7 README prohibits republication by public accounts/self-media; preserved verbatim for scope review. This GitHub application package is not a claim that repository GPL automatically licenses every incorporated dataset.')
$mainLicense = Get-NoticePath 'LICENSE' -Missing
if ([IO.File]::Exists($mainLicense)) { $inputs += Get-NoticeRecord 'LICENSE'; Write-NoticeBytes 'LICENSES/project/LICENSE.txt' ([IO.File]::ReadAllBytes($mainLicense)) }
else { $limitations.Add('Mihomo Lite has no project LICENSE in this source workspace. This does not select MIT/GPL or grant third parties additional rights in maintainer-owned source.') }
$ready = $issues.Count -eq 0
$status = 'redistribution-blocked'; if ($ready) { $status = 'verified-ready' }
$offer = @('# Corresponding source distribution arrangement','','Upload these exact fixed-commit source archives as separate GitHub Release assets next to the binary ZIP at no additional charge. Preserve equivalent access and clear source directions beside the binary for GPLv3 section 6(d). This is not a written-offer substitution. No upstream source was executed.','',
    'The official source archives include upstream build scripts and lock files. Public external source/data inputs listed there must remain accessible under their applicable terms. No full reproducibility or comprehensive rights-chain audit is claimed. The distributor remains responsible for complete Corresponding Source, notices and continued access.','')
foreach ($a in $archives) { $offer += '- ' + $a.component + ': ' + $a.url + ' | commit ' + $a.commit + ' | SHA-256 ' + $a.sha256 + ' | bytes ' + $a.size }
Write-NoticeText 'LICENSES/SOURCE_OFFER.md' ($offer -join [Environment]::NewLine)
$sources = [ordered]@{ schemaVersion = 1; componentManifest = $componentInput; geoProvenance = (Get-NoticeRecord ($geoRoot + '/provenance.json')); goModules = $goEvidence; upstream = $evidence; sourceArchives = $archives; blockingIssues = @($issues.ToArray()); limitations = @($limitations.ToArray()); reviewScope = 'official publisher declarations and named public sources; not a comprehensive copyright or reproducibility audit' }
Write-NoticeText 'LICENSES/sources.json' ($sources | ConvertTo-Json -Depth 30)
$notice = @('# Third-party notices','','Evidence preparation status: ' + $status + '. This is not legal advice or a comprehensive copyright/reproducibility audit.','',
    'Original license, notice, copyright, attribution and source-policy texts are retained unchanged under LICENSES. Author attributions are not invented. SHA-256 authenticity does not itself establish redistribution rights.','',
    'mhCore: MetaCubeX/mihomo (GNU GPL version 3; original text governs). Panel: Zephyruso/zashboard (MIT, including its original copyright). Geo: MetaCubeX/meta-rules-dat generator/license and separately attributed mixed input data; no claim that the repository GPL covers every database. Exact official release commits/IDs and sources are recorded in LICENSES/sources.json.','',
    'This product includes GeoLite Data created by MaxMind, available from https://www.maxmind.com. Copyrightable GeoLite database elements are governed by Creative Commons Attribution-ShareAlike 4.0 International (https://creativecommons.org/licenses/by-sa/4.0/). The upstream dataset is modified/combined/converted by the listed upstream projects; this distributor copies their published assets unchanged. Applicable MaxMind EULA and full CC license are included in LICENSES/geolite.','',
    'MaxMind EULA: promptly use updated GeoLite databases, cease use of and destroy old versions within thirty (30) days following updated release; attribution/notices must not be removed. Publisher must address updates and historical asset retention; a static historical ZIP is not represented as permanently compliant. No FCRA eligibility or household/person/street-address identification use is permitted.','',
    'Go dependency originals are collected only from the local pinned public module cache of this isolated release workspace, including nested/multiple LICENSE/NOTICE/COPYING/COPYRIGHT files. ZIP hash lock matching is recorded, not an independent sumdb authenticity claim.','',
    'GPL source archives are separate Release assets, not in the install ZIP: see LICENSES/SOURCE_OFFER.md for fixed commits, URLs, byte sizes and SHA-256. License scopes remain separate; this notice does not relicense maintainer-owned source or factual domain identifiers.','', '## Provenance and scope limitations','')
foreach ($v in $limitations) { $notice += '- ' + $v }
if (-not $ready) { $notice += ''; $notice += '## Publication blockers'; foreach ($v in $issues) { $notice += '- ' + $v } }
Write-NoticeText 'THIRD_PARTY_NOTICES.md' ($notice -join [Environment]::NewLine)
$licenseRecords = @(); foreach ($f in @(Get-NoticeTree 'LICENSES' | Sort-Object)) { $licenseRecords += Get-NoticeRecord $f }
Write-NoticeText 'LICENSES/manifest.json' ([ordered]@{ schemaVersion = 1; records = $licenseRecords; sourceArchives = $archives } | ConvertTo-Json -Depth 30)
$records = @(Get-NoticeRecord 'THIRD_PARTY_NOTICES.md') + $licenseRecords + @(Get-NoticeRecord 'LICENSES/manifest.json')
$manifest = [ordered]@{ schemaVersion = 1; status = $status; verified = $ready; redistributionReady = $ready; generatedAt = [datetime]::UtcNow.ToString('o'); componentManifest = $ComponentManifest; records = $records; inputs = $inputs; sourceArchives = $archives; blockingIssues = @($issues.ToArray()); limitations = @($limitations.ToArray()) }
$manifestPath = $batch + '/notices.json'
Write-NoticeText $manifestPath ($manifest | ConvertTo-Json -Depth 30)
$manifest.manifestPath = $manifestPath; $manifest.manifest = $manifestPath
[pscustomobject]$manifest
