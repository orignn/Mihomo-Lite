#requires -Version 5.1
[CmdletBinding()]
param(
    [string]$WorkspaceRoot = '',
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]*$')]
    [string]$Batch = '20261120-workspace-cleanup',
    [switch]$Apply
)
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($WorkspaceRoot)) { $WorkspaceRoot = Split-Path -Parent $PSScriptRoot }
$root = (Resolve-Path -LiteralPath $WorkspaceRoot).Path.TrimEnd('\', '/')
if (-not (Test-Path -LiteralPath (Join-Path $root 'go.mod') -PathType Leaf) -or
    -not (Test-Path -LiteralPath (Join-Path $root 'cmd\mihomolite\main.go') -PathType Leaf)) {
    throw 'This is not the Mihomo Lite source workspace.'
}
$archiveRoot = [IO.Path]::GetFullPath((Join-Path $root ('archive\' + $Batch)))
$plans = New-Object 'System.Collections.Generic.List[object]'
function Resolve-WithinWorkspace([string]$relative) {
    if ([IO.Path]::IsPathRooted($relative) -or $relative -match '(^|[\\/])[.][.]([\\/]|$)') { throw "Unsafe relative path: $relative" }
    $full = [IO.Path]::GetFullPath((Join-Path $root $relative))
    if (-not $full.StartsWith($root + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw "Path escapes workspace: $relative" }
    $cursor = $full
    while ($cursor.Length -ge $root.Length) {
        if (Test-Path -LiteralPath $cursor) {
            $item = Get-Item -LiteralPath $cursor -Force
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Path traverses a reparse point: $cursor" }
        }
        if ($cursor -eq $root) { break }
        $cursor = Split-Path -Parent $cursor
    }
    return $full
}
function Add-Plan([string]$source, [string]$destination, [string]$category) {
    $src = Resolve-WithinWorkspace $source
    $dst = Resolve-WithinWorkspace $destination
    if (-not (Test-Path -LiteralPath $src)) { return }
    if (Test-Path -LiteralPath $dst) { throw "Destination already exists; will not overwrite: $dst" }
    $item = Get-Item -LiteralPath $src -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Refuse reparse source: $src" }
    $plans.Add([pscustomobject]@{ source=$source.Replace('\','/'); destination=$destination.Replace('\','/'); category=$category; isDirectory=[bool]$item.PSIsContainer; sourceAbsolute=$src; destinationAbsolute=$dst })
}
foreach ($entry in @(Get-ChildItem -LiteralPath $root -Force)) {
    if (-not $entry.PSIsContainer -and ($entry.Name -match '^MihomoLite-v[0-9]+[.][0-9]+[.][0-9]+[.]exe$' -or $entry.Name -in @('mihomo-lite.exe','MihomoLite-Diagnostics.exe'))) {
        Add-Plan $entry.Name ('archive\' + $Batch + '\binaries\' + $entry.Name) 'historical-binary'
    }
}
foreach ($name in @('-index-check.js','.cacheprofile-edit-fixMihomoLite.exe','.cacheprofile-edit-fix','%SystemDrive%')) {
    Add-Plan $name ('archive\' + $Batch + '\misplaced\' + $name) 'misplaced-output'
}
Add-Plan 'ui' ('archive\' + $Batch + '\legacy-components\ui') 'legacy-panel-copy'
Add-Plan 'verification-v0.6.3' ('archive\' + $Batch + '\verification\v0.6.3') 'historical-verification'
if (Test-Path -LiteralPath (Join-Path $root 'release') -PathType Container) {
    foreach ($entry in @(Get-ChildItem -LiteralPath (Join-Path $root 'release') -Force)) {
        if ($entry.Name -match '^MihomoLite-v[0-9]+[.][0-9]+[.][0-9]+-win-x64[.]zip$' -or ($entry.PSIsContainer -and $entry.Name -match '^MihomoLite(-v[0-9]+[.][0-9]+[.][0-9]+)?$')) {
            Add-Plan ('release\' + $entry.Name) ('archive\' + $Batch + '\releases\' + $entry.Name) 'historical-release'
        }
    }
}
if (Test-Path -LiteralPath (Join-Path $root '.cache') -PathType Container) {
    foreach ($entry in @(Get-ChildItem -LiteralPath (Join-Path $root '.cache') -Force)) {
        if ($entry.PSIsContainer -and $entry.Name -in @('gobuild','gomod','gopath')) {
            Add-Plan ('.cache\' + $entry.Name) ('.cache\toolchain\' + $entry.Name) 'toolchain-cache'
        } elseif ($entry.Name -notin @('build','tests','toolchain','downloads','package','README.md')) {
            Add-Plan ('.cache\' + $entry.Name) ('archive\' + $Batch + '\test-runs\' + $entry.Name) 'historical-test-run'
        }
    }
}
# Print every resolved source and destination BEFORE any move.
foreach ($plan in $plans) { Write-Output ('PLAN [{0}] {1} -> {2}' -f $plan.category, $plan.sourceAbsolute, $plan.destinationAbsolute) }
Write-Output ('Planned moves: {0}; Apply: {1}' -f $plans.Count, [bool]$Apply)
if (-not $Apply -or $plans.Count -eq 0) { return }
if (Test-Path -LiteralPath $archiveRoot) { throw "Archive batch already exists: $archiveRoot; use a new batch." }
$protected = @('MihomoLite.exe','launcher.json','runtime\mihomo.exe')
$protectedFiles = @($protected | ForEach-Object { Get-Item -LiteralPath (Join-Path $root $_) -ErrorAction SilentlyContinue }) + @(Get-ChildItem -LiteralPath (Join-Path $root 'data') -Recurse -File -Force)
$before = @($protectedFiles | ForEach-Object {
    [pscustomobject]@{ path=$_.FullName.Substring($root.Length + 1).Replace('\','/'); size=$_.Length; sha256=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
})
# Refuse to relocate any running executable, or browser/test process using a candidate tree.
$processes = @(Get-CimInstance Win32_Process)
foreach ($plan in $plans) {
    foreach ($process in $processes) {
        if ($process.ProcessId -eq $PID) { continue }
        if ($process.ExecutablePath -and ($process.ExecutablePath -eq $plan.sourceAbsolute -or $process.ExecutablePath.StartsWith($plan.sourceAbsolute + '\', [StringComparison]::OrdinalIgnoreCase))) { throw "Source is used by PID $($process.ProcessId): $($plan.sourceAbsolute)" }
        if ($process.Name -match '^(msedge|chrome|firefox|mihomo.*|MihomoLite.*|go|node)[.]exe$' -and $process.CommandLine -and
            ($process.CommandLine.IndexOf($plan.sourceAbsolute, [StringComparison]::OrdinalIgnoreCase) -ge 0 -or $process.CommandLine.IndexOf($plan.sourceAbsolute.Replace('\','/'), [StringComparison]::OrdinalIgnoreCase) -ge 0)) {
            throw "Source is referenced by active test/browser PID $($process.ProcessId): $($plan.sourceAbsolute)"
        }
    }
}
New-Item -ItemType Directory -Path $archiveRoot | Out-Null
$encoding = New-Object Text.UTF8Encoding($false)
function Save-JSON([string]$path, $value) { [IO.File]::WriteAllText($path, ($value | ConvertTo-Json -Depth 8), $encoding) }
Save-JSON (Join-Path $archiveRoot 'protected-before.json') $before
$ledger = New-Object 'System.Collections.Generic.List[object]'
function Get-Signature([string]$path) {
    $item = Get-Item -LiteralPath $path -Force
    if (-not $item.PSIsContainer) { return [pscustomobject]@{ fileCount=1; bytes=$item.Length; sha256=(Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant(); treeMetadataSHA256=$null } }
    $files = @(Get-ChildItem -LiteralPath $path -File -Recurse -Force | Sort-Object FullName)
    $lines = @($files | ForEach-Object { $_.FullName.Substring($path.Length).Replace('\','/') + ':' + $_.Length })
    $sha = [Security.Cryptography.SHA256]::Create()
    try { $hash = [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes(($lines -join [Environment]::NewLine)))).Replace('-','').ToLowerInvariant() } finally { $sha.Dispose() }
    $bytes = ($files | Measure-Object Length -Sum).Sum
    if ($null -eq $bytes) { $bytes = 0 }
    return [pscustomobject]@{ fileCount=$files.Count; bytes=$bytes; sha256=$null; treeMetadataSHA256=$hash }
}
foreach ($plan in $plans) {
    $signature = Get-Signature $plan.sourceAbsolute
    $record = [pscustomobject]@{ source=$plan.source; destination=$plan.destination; category=$plan.category; signature=$signature; status='planned'; movedAt=$null }
    $ledger.Add($record)
    Save-JSON (Join-Path $archiveRoot 'moves.json') @($ledger.ToArray())
    $parent = Split-Path -Parent $plan.destinationAbsolute
    New-Item -ItemType Directory -Path $parent -Force | Out-Null
    # Revalidate intended absolute targets and no-clobber immediately before moving.
    if ((Resolve-WithinWorkspace $plan.source) -ne $plan.sourceAbsolute -or (Resolve-WithinWorkspace $plan.destination) -ne $plan.destinationAbsolute -or (Test-Path -LiteralPath $plan.destinationAbsolute)) { throw 'Move target changed or exists.' }
    Write-Output ('MOVE {0} -> {1}' -f $plan.sourceAbsolute, $plan.destinationAbsolute)
    Move-Item -LiteralPath $plan.sourceAbsolute -Destination $plan.destinationAbsolute -ErrorAction Stop
    $record.status = 'moved'
    $record.movedAt = [DateTime]::UtcNow.ToString('o')
    Save-JSON (Join-Path $archiveRoot 'moves.json') @($ledger.ToArray())
    $after = Get-Signature $plan.destinationAbsolute
    if ($signature.fileCount -ne $after.fileCount -or $signature.bytes -ne $after.bytes -or $signature.sha256 -ne $after.sha256 -or $signature.treeMetadataSHA256 -ne $after.treeMetadataSHA256) { throw "Post-move signature differs: $($plan.destinationAbsolute)" }
    $record.status = 'verified'
    Save-JSON (Join-Path $archiveRoot 'moves.json') @($ledger.ToArray())
}
$changed = @($before | Where-Object {
    $path = Resolve-WithinWorkspace $_.path
    -not (Test-Path -LiteralPath $path -PathType Leaf) -or (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -ne $_.sha256
})
Save-JSON (Join-Path $archiveRoot 'protected-verification.json') ([pscustomobject]@{ checkedAt=[DateTime]::UtcNow.ToString('o'); checkedFiles=$before.Count; unchanged=($changed.Count -eq 0); changedPaths=@($changed | ForEach-Object {$_.path}); note='No protected files are written by this script; a running application may modify its own data.' })
if ($changed.Count -gt 0) { throw 'Protected files changed during organization; inspect protected-verification.json. No attempt was made to overwrite them.' }
Write-Output ('Completed and verified {0} moves. Protected files unchanged: {1}.' -f $ledger.Count, $before.Count)
