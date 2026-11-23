#requires -Version 5.1
[CmdletBinding()]
param(
    [string]$WorkspaceRoot = '',
    [switch]$AllowUnpublished,
    [switch]$RequireGeoData,
    [switch]$VerifyPayload
)

# Read-only: never runs a launcher/core, extracts an archive, or changes files.
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($WorkspaceRoot)) { $WorkspaceRoot = Split-Path -Parent $PSScriptRoot }
$script:Problems = New-Object 'System.Collections.Generic.List[string]'
function Add-Problem([string]$Message) { [void]$script:Problems.Add($Message) }
function Has-Property($Object, [string]$Name) {
    return $null -ne $Object -and $null -ne $Object.PSObject.Properties[$Name]
}
function Is-Object($Value) { return $Value -is [System.Management.Automation.PSCustomObject] }
function Is-Size($Value) {
    return ($Value -is [int] -or $Value -is [long] -or $Value -is [decimal] -or $Value -is [double]) -and $Value -ge 0 -and $Value -eq [math]::Floor([double]$Value)
}
function Test-HashValue($Value, [string]$Label) {
    if ($Value -isnot [string] -or $Value -cnotmatch '^[0-9a-f]{64}$') {
        Add-Problem "$Label must be a lowercase SHA-256 (64 hexadecimal characters)."
        return $false
    }
    return $true
}
function Test-IsoTime($Value, [string]$Label) {
    $parsed = [DateTimeOffset]::MinValue
    if ($Value -isnot [string] -or $Value -notmatch '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$' -or
        -not [DateTimeOffset]::TryParse($Value, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::None, [ref]$parsed)) {
        Add-Problem "$Label must be an ISO-8601 timestamp including a time zone."
    }
}
function Get-SafeSegments($Value, [string]$Label, [switch]$DirectoryEntry) {
    if ($Value -isnot [string] -or [string]::IsNullOrWhiteSpace($Value)) {
        Add-Problem "$Label must be a non-empty relative path."
        return $null
    }
    $name = $Value.Replace('\', '/')
    if ($DirectoryEntry) { $name = $name.TrimEnd('/') }
    if ($name -match '[\x00-\x1f<>:"|?*]' -or $name.StartsWith('/') -or [IO.Path]::IsPathRooted($name)) {
        Add-Problem "$Label is not a safe relative path: '$Value'."
        return $null
    }
    $parts = @($name.Split('/'))
    foreach ($part in $parts) {
        if ($part -eq '' -or $part -eq '.' -or $part -eq '..' -or $part -match '[. ]$' -or $part -match '^(?i:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\..*)?$') {
            Add-Problem "$Label contains an unsafe path segment: '$Value'."
            return $null
        }
    }
    return ($parts -join '/')
}
function Get-SafeWorkspacePath($Value, [string]$Label) {
    $relative = Get-SafeSegments $Value $Label
    if ($null -eq $relative) { return $null }
    try {
        $absolute = [IO.Path]::GetFullPath((Join-Path $script:Root $relative.Replace('/', [IO.Path]::DirectorySeparatorChar)))
        if (-not $absolute.StartsWith($script:RootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            Add-Problem "$Label escapes the workspace: '$Value'."
            return $null
        }
        $cursor = $script:Root
        foreach ($part in $relative.Split('/')) {
            $cursor = Join-Path $cursor $part
            if (Test-Path -LiteralPath $cursor) {
                $item = Get-Item -LiteralPath $cursor -Force
                if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                    Add-Problem "$Label traverses a reparse point: '$cursor'."
                    return $null
                }
            }
        }
        return $absolute
    } catch {
        Add-Problem ("Cannot resolve {0}: {1}" -f $Label, $_.Exception.Message)
        return $null
    }
}
function Read-JsonObject([string]$Path, [string]$Label) {
    try {
        if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { Add-Problem "$Label is missing: '$Path'."; return $null }
        $jsonText = [IO.File]::ReadAllText($Path)
        if ((Get-Command ConvertFrom-Json).Parameters.ContainsKey('DateKind')) { $value = $jsonText | ConvertFrom-Json -DateKind String }
        else { $value = $jsonText | ConvertFrom-Json }
        if (-not (Is-Object $value)) { Add-Problem "$Label must contain a JSON object."; return $null }
        return $value
    } catch {
        Add-Problem ("Cannot read {0}: {1}" -f $Label, $_.Exception.Message)
        return $null
    }
}
function Get-ActualHash([string]$Path, [string]$Label) {
    try { return (Get-FileHash -LiteralPath $Path -Algorithm SHA256 -ErrorAction Stop).Hash.ToLowerInvariant() }
    catch { Add-Problem ("Cannot hash {0} '{1}': {2}" -f $Label, $Path, $_.Exception.Message); return $null }
}
function Check-Whitelist([string]$Path, [string[]]$Directories, [string[]]$Files, [string]$Label) {
    try {
        foreach ($entry in @(Get-ChildItem -LiteralPath $Path -Force -ErrorAction Stop)) {
            if (($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                Add-Problem "$Label contains a reparse point: '$($entry.Name)'."
                continue
            }
            if ($entry.PSIsContainer) {
                if ($Directories -cnotcontains $entry.Name) { Add-Problem "$Label contains an unapproved directory: '$($entry.Name)'." }
            } elseif ($Files -cnotcontains $entry.Name) {
                Add-Problem "$Label contains an unapproved file: '$($entry.Name)'."
            }
        }
    } catch { Add-Problem ("Cannot list {0}: {1}" -f $Label, $_.Exception.Message) }
}
function Check-BinaryDescriptor($Descriptor, [string]$ExpectedPath, [string]$Label, [string]$ActualHash, $ActualSize) {
    if (-not (Is-Object $Descriptor)) { Add-Problem "$Label must be an object."; return }
    [void](Get-SafeSegments $Descriptor.path "$Label.path")
    if ($Descriptor.path -isnot [string] -or $Descriptor.path -cne $ExpectedPath) { Add-Problem "$Label.path must be '$ExpectedPath'." }
    if (Test-HashValue $Descriptor.sha256 "$Label.sha256") {
        if ($null -ne $ActualHash -and $Descriptor.sha256 -cne $ActualHash) { Add-Problem "$Label.sha256 does not match the actual root binary." }
    }
    if (-not (Is-Size $Descriptor.size)) { Add-Problem "$Label.size must be a non-negative integer." }
    elseif ($null -ne $ActualSize -and $Descriptor.size -ne $ActualSize) { Add-Problem "$Label.size is $($Descriptor.size); actual root binary size is $ActualSize." }
}
# Strong verification is opt-in so historical publications without files/sums remain readable by default.
function Get-PayloadRecords {
    $records = New-Object 'System.Collections.Generic.Dictionary[string,object]' ([StringComparer]::Ordinal)
    $seen = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    if (-not (Is-Object $manifest) -or $manifest.files -isnot [Array] -or $manifest.files.Count -eq 0) {
        Add-Problem 'release/current.json.files must be a non-empty array for -VerifyPayload.'
        return ,$records
    }
    $index = 0
    foreach ($record in $manifest.files) {
        $label = 'release/current.json.files[' + $index + ']'; $index++
        if (-not (Is-Object $record)) { Add-Problem "$label must be an object."; continue }
        $name = Get-SafeSegments $record.path "$label.path"
        if ($null -eq $name) { continue }
        if ($record.path -cne $name -or -not $name.StartsWith('MihomoLite/', [StringComparison]::Ordinal)) {
            Add-Problem "$label.path must be a canonical file path inside MihomoLite/."; continue
        }
        if (-not $seen.Add($name)) { Add-Problem "release/current.json.files contains a duplicate or case-colliding path: '$name'."; continue }
        $validHash = Test-HashValue $record.sha256 "$label.sha256"
        # ZIP lengths are signed Int64; reject booleans, strings, fractions and out-of-range JSON numbers.
        $validSize = (Is-Size $record.size) -and $record.size -le [long]::MaxValue -and
            ($record.size -isnot [double] -or $record.size -lt 9223372036854775808.0)
        if (-not $validSize) { Add-Problem "$label.size must be a non-negative Int64 integer." }
        if ($validHash -and $validSize) { $records.Add($name, $record) }
    }
    return ,$records
}
function Check-PayloadChecksums($Records) {
    $path = Get-SafeWorkspacePath 'release/SHA256SUMS.txt' 'release/SHA256SUMS.txt'
    if ($null -eq $path) { return }
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { Add-Problem 'release/SHA256SUMS.txt is required for -VerifyPayload.'; return }
    $seen = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    $lineNumber = 0
    try {
        foreach ($line in [IO.File]::ReadLines($path)) {
            $lineNumber++
            if ([string]::IsNullOrWhiteSpace($line)) { continue }
            $match = [regex]::Match($line, '\A([0-9a-f]{64})[ \t]+\*?(.+)\z')
            if (-not $match.Success) { Add-Problem "release/SHA256SUMS.txt line $lineNumber must contain a lowercase SHA-256 and one file path."; continue }
            $hash = $match.Groups[1].Value
            $rawName = $match.Groups[2].Value
            $name = Get-SafeSegments $rawName 'release/SHA256SUMS.txt path'
            if ($null -eq $name) { continue }
            if ($name -cne $rawName) { Add-Problem 'release/SHA256SUMS.txt paths must use canonical forward slashes.'; continue }
            if (-not $seen.Add($name)) { Add-Problem "release/SHA256SUMS.txt contains a duplicate or case-colliding path: '$name'."; continue }
            if ($name -ceq $expectedZipName) {
                if ($null -ne $releaseHash -and $hash -cne $releaseHash) { Add-Problem 'release/SHA256SUMS.txt ZIP SHA-256 does not match the actual ZIP.' }
            } elseif ($Records.ContainsKey($name)) {
                if ($hash -cne $Records[$name].sha256) { Add-Problem "release/SHA256SUMS.txt SHA-256 does not match manifest.files: '$name'." }
            } else { Add-Problem "release/SHA256SUMS.txt contains an unknown path: '$name'." }
        }
        foreach ($name in @($expectedZipName) + @($Records.Keys)) {
            if (-not $seen.Contains($name)) { Add-Problem "release/SHA256SUMS.txt is missing an entry: '$name'." }
        }
    } catch { Add-Problem ("Cannot validate release/SHA256SUMS.txt: {0}" -f $_.Exception.Message) }
}
function Check-Zip([string]$Path, [string]$ExpectedHash, $ExpectedSize) {
    $archive = $null
    try {
        Add-Type -AssemblyName System.IO.Compression -ErrorAction Stop
        Add-Type -AssemblyName System.IO.Compression.FileSystem -ErrorAction Stop
        $archive = [IO.Compression.ZipFile]::OpenRead($Path)
        $seen = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
        $executables = New-Object 'System.Collections.Generic.List[object]'
        $payloadSeen = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::Ordinal)
        $launcherHash = $null
        if ($VerifyPayload) { $payloadRecords = Get-PayloadRecords; Check-PayloadChecksums $payloadRecords }
        $requiredFiles = @('MihomoLite/MihomoLite.exe', 'MihomoLite/runtime/mihomo.exe', 'MihomoLite/data/geoip.metadb', 'MihomoLite/data/ui/index.html', 'MihomoLite/example.yaml', 'MihomoLite/README.md')
        $publicDocs = @('MihomoLite/docs/DEVELOPMENT.md', 'MihomoLite/docs/RELEASE.md', 'MihomoLite/docs/CHANGELOG.md')
        # Historical publications contain UPDATE.md; new public packages require the complete public docs set.
        $zipNames = @($archive.Entries | ForEach-Object { $_.FullName.Replace('\', '/') })
        if ($zipNames -ccontains 'MihomoLite/UPDATE.md') { $requiredFiles += 'MihomoLite/UPDATE.md' }
        else { $requiredFiles += $publicDocs }
        # Preserve pre-policy historical ZIPs; every newly generated package
        # declares geoDataPolicy=1, even if an existing version is repackaged.
        $baseVersion = [regex]::Match([string]$version, '^\d+\.\d+\.\d+').Value
        if ($VerifyPayload -or $RequireGeoData -or (Has-Property $current.release 'geoDataPolicy') -or (Has-Property $manifest 'geoDataPolicy') -or
            ($baseVersion -and [version]$baseVersion -ge [version]'0.6.11')) {
            $requiredFiles += @('MihomoLite/data/geosite.dat', 'MihomoLite/data/geoip.dat')
        }
        # Local setup is optional only for historical ZIP compatibility; strong mode still hashes every entry.
        $fixedFiles = $requiredFiles + $publicDocs + @('MihomoLite/setup.ps1', 'MihomoLite/UPDATE.md', 'MihomoLite/launcher.example.json', 'MihomoLite/LICENSE', 'MihomoLite/THIRD_PARTY_NOTICES.md', 'MihomoLite/data/geosite.dat', 'MihomoLite/data/geoip.dat', 'MihomoLite/data/Country.mmdb')
        foreach ($entry in $archive.Entries) {
            $isDirectory = $entry.FullName.EndsWith('/') -or $entry.FullName.EndsWith('\')
            $name = Get-SafeSegments $entry.FullName 'ZIP entry' -DirectoryEntry:$isDirectory
            if ($null -eq $name) { continue }
            if (-not $seen.Add($name)) { Add-Problem "ZIP contains a duplicate or case-colliding entry: '$name'." }
            if ($name -cne 'MihomoLite' -and -not $name.StartsWith('MihomoLite/', [StringComparison]::Ordinal)) {
                Add-Problem "ZIP entry is outside the MihomoLite/ naming root: '$name'."
                continue
            }
            if ($name -ceq 'MihomoLite' -and -not $isDirectory) { Add-Problem 'ZIP naming root MihomoLite must be a directory.' }
            $unixType = ($entry.ExternalAttributes -shr 16) -band 61440
            if ($unixType -eq 40960) { Add-Problem "ZIP contains a symbolic link: '$name'." }
            $parts = @($name.Split('/'))
            $leaf = $parts[$parts.Count - 1]
            foreach ($part in $parts) {
                if ($part -match '^(?i:\.cache|cache|logs|profiles|Crashpad|GPUCache|DawnWebGPUCache|DawnGraphiteCache|Code Cache|Local Storage|Session Storage|IndexedDB)$') { Add-Problem "ZIP contains a forbidden private/cache/log directory: '$name'."; break }
            }
            if ($leaf -match '^(?i:launcher\.json(?:\..*)?|profiles\.json(?:\..*)?|custom-rules\.json(?:\..*)?|source\.ya?ml(?:\..*)?|runtime\.ya?ml(?:\..*)?)$') {
                Add-Problem "ZIP contains a forbidden real configuration file: '$name'."
            }
            if ($leaf -match '^(?i:cache\.db(?:[.-].*)?|Cookies(?:[.-].*)?|History(?:[.-].*)?|Login Data(?:[.-].*)?|Web Data(?:[.-].*)?|Preferences|Secure Preferences|Local State)$') {
                Add-Problem "ZIP contains a forbidden browser/database file: '$name'."
            }
            if ($isDirectory) {
                if ($name -cnotin @('MihomoLite', 'MihomoLite/data', 'MihomoLite/runtime', 'MihomoLite/data/ui', 'MihomoLite/docs') -and -not $name.StartsWith('MihomoLite/data/ui/', [StringComparison]::Ordinal)) {
                    Add-Problem "ZIP contains a directory outside the payload whitelist: '$name'."
                }
            } else {
                $isUiAsset = $name.StartsWith('MihomoLite/data/ui/', [StringComparison]::Ordinal) -and $leaf -match '\.(?i:html|js|css|json|webmanifest|svg|png|jpg|jpeg|webp|gif|ico|woff|woff2|ttf|otf|txt|md|map|wasm)$'
                if ($fixedFiles -cnotcontains $name -and -not $isUiAsset) { Add-Problem "ZIP contains a file outside the payload whitelist: '$name'." }
                if ($requiredFiles -ccontains $name -and $entry.Length -eq 0) { Add-Problem "ZIP required payload file is empty: '$name'." }
                if ($VerifyPayload) {
                    [void]$payloadSeen.Add($name)
                    if ($entry.FullName -cne $name) { Add-Problem "ZIP file path must use canonical forward slashes: '$name'." }
                    if (-not $payloadRecords.ContainsKey($name)) { Add-Problem "ZIP file is missing from manifest.files: '$name'." }
                    elseif ($payloadRecords[$name].size -ne $entry.Length) { Add-Problem "ZIP file size does not match manifest.files: '$name'." }
                }
                # Each selected stream is opened once. Strong mode reuses the launcher's digest below.
                if ($VerifyPayload -or $name -ceq 'MihomoLite/MihomoLite.exe') {
                    $stream = $null; $sha = $null
                    try {
                        $stream = $entry.Open()
                        $sha = [Security.Cryptography.SHA256]::Create()
                        $entryHash = ([BitConverter]::ToString($sha.ComputeHash($stream))).Replace('-', '').ToLowerInvariant()
                        if ($name -ceq 'MihomoLite/MihomoLite.exe') { $launcherHash = $entryHash }
                        if ($VerifyPayload -and $payloadRecords.ContainsKey($name) -and $entryHash -cne $payloadRecords[$name].sha256) {
                            Add-Problem "ZIP file SHA-256 does not match manifest.files: '$name'."
                        }
                    } catch { Add-Problem ("Cannot hash ZIP file '{0}': {1}" -f $name, $_.Exception.Message) }
                    finally { if ($null -ne $sha) { $sha.Dispose() }; if ($null -ne $stream) { $stream.Dispose() } }
                }
            }
            if (-not $isDirectory -and $leaf -ieq 'MihomoLite.exe') {
                if ($name -cne 'MihomoLite/MihomoLite.exe') { Add-Problem "ZIP contains a launcher at an unexpected path: '$name'." }
                else { [void]$executables.Add($entry) }
            }
        }
        foreach ($required in $requiredFiles) {
            if (-not $seen.Contains($required) -or ($VerifyPayload -and -not $payloadSeen.Contains($required))) { Add-Problem "ZIP is missing required complete-package payload: '$required'." }
        }
        if ($VerifyPayload) {
            foreach ($name in $payloadRecords.Keys) {
                if (-not $payloadSeen.Contains($name)) { Add-Problem "manifest.files entry is missing from ZIP: '$name'." }
            }
        }
        if ($executables.Count -ne 1) { Add-Problem "ZIP must contain exactly one MihomoLite/MihomoLite.exe; found $($executables.Count)." }
        else {
            $entry = $executables[0]
            if ($null -ne $ExpectedSize -and $entry.Length -ne $ExpectedSize) { Add-Problem "ZIP launcher size is $($entry.Length); root binary size is $ExpectedSize." }
            if ($null -ne $ExpectedHash -and $null -ne $launcherHash -and $launcherHash -cne $ExpectedHash) { Add-Problem 'ZIP launcher SHA-256 does not match the root binary.' }
        }
    } catch { Add-Problem ("Cannot validate ZIP '{0}': {1}" -f $Path, $_.Exception.Message) }
    finally { if ($null -ne $archive) { $archive.Dispose() } }
}

try {
    $script:Root = [IO.Path]::GetFullPath($WorkspaceRoot).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    if (-not (Test-Path -LiteralPath $script:Root -PathType Container)) { throw "Workspace directory does not exist: '$WorkspaceRoot'." }
    if (((Get-Item -LiteralPath $script:Root -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'WorkspaceRoot must not itself be a reparse point.' }
    $script:RootPrefix = $script:Root + [IO.Path]::DirectorySeparatorChar
} catch { Write-Error $_.Exception.Message; exit 1 }

$rootDirectories = @('cmd', 'assets', 'tests', 'tools', 'docs', 'archive', '.cache', 'data', 'runtime', 'logs', 'release', '.git', '.vscode')
$rootFiles = @('MihomoLite.exe', '.gitignore', 'go.mod', 'go.sum', 'build.ps1', 'setup.ps1', 'example.yaml', 'README.md', 'UPDATE.md', 'AGENTS.md', 'WORKSPACE.md', 'CURRENT.json', 'launcher.json')
Check-Whitelist $script:Root $rootDirectories $rootFiles 'Workspace root'
$cachePath = Get-SafeWorkspacePath '.cache' '.cache'
if ($null -ne $cachePath -and (Test-Path -LiteralPath $cachePath -PathType Container)) {
    Check-Whitelist $cachePath @('build', 'tests', 'toolchain', 'downloads', 'package') @('README.md') '.cache'
}

$binaryPath = Get-SafeWorkspacePath 'MihomoLite.exe' 'Root binary'
$rootHash = $null
$rootSize = $null
$binaryInfo = $null
if ($null -ne $binaryPath) {
    if (-not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) { Add-Problem 'The sole root launcher MihomoLite.exe is missing.' }
    else {
        try { $binaryInfo = Get-Item -LiteralPath $binaryPath; $rootSize = $binaryInfo.Length }
        catch { Add-Problem ("Cannot inspect root binary: {0}" -f $_.Exception.Message) }
        $rootHash = Get-ActualHash $binaryPath 'root binary'
    }
}
$sourcePath = Get-SafeWorkspacePath 'cmd/mihomolite/types.go' 'AppVersion source'
$version = $null
if ($null -ne $sourcePath) {
    try {
        $source = [IO.File]::ReadAllText($sourcePath)
        $markers = [regex]::Matches($source, '(?m)^\s*const\s+AppVersion\s*=\s*"([^"\r\n]+)"')
        if ($markers.Count -ne 1) { Add-Problem "AppVersion source must contain exactly one const AppVersion marker; found $($markers.Count)." }
        else {
            $version = $markers[0].Groups[1].Value
            if ($version -notmatch '^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$') { Add-Problem "AppVersion is not a valid version marker: '$version'." }
        }
    } catch { Add-Problem ("Cannot read AppVersion source: {0}" -f $_.Exception.Message) }
}
if ($null -ne $binaryInfo -and $null -ne $version) {
    try {
        foreach ($field in @('ProductVersion', 'FileVersion')) {
            $value = [string]$binaryInfo.VersionInfo.$field
            if ($value.Trim() -cne $version) { Add-Problem "Root EXE $field '$value' does not match source AppVersion '$version'." }
        }
    } catch { Add-Problem ("Cannot read root EXE version resources: {0}" -f $_.Exception.Message) }
}

$currentPath = Get-SafeWorkspacePath 'CURRENT.json' 'CURRENT.json'
$releasePath = Get-SafeWorkspacePath 'release' 'release'
$current = $null
if ($null -ne $currentPath -and (Test-Path -LiteralPath $currentPath -PathType Leaf)) { $current = Read-JsonObject $currentPath 'CURRENT.json' }
elseif (-not $AllowUnpublished) { Add-Problem 'CURRENT.json is required in strict mode; use -AllowUnpublished only before publication.' }

$expectedZipName = $null
if ($null -ne $version -and $null -ne $rootHash) { $expectedZipName = 'MihomoLite-v' + $version + '-' + $rootHash.Substring(0, 12) + '-win-x64.zip' }
$artifactPath = $null
$manifestPath = $null
$releaseHash = $null
$manifest = $null
$hasPublishedRelease = $false
if ($null -ne $current) {
    if (-not (Has-Property $current 'schemaVersion') -or ($current.schemaVersion -isnot [int] -and $current.schemaVersion -isnot [long]) -or $current.schemaVersion -ne 1) { Add-Problem 'CURRENT.json.schemaVersion must be the integer 1.' }
    if ($current.version -isnot [string] -or $current.version -cne $version) { Add-Problem 'CURRENT.json.version does not match source AppVersion.' }
    if ($null -ne $expectedZipName -and $current.buildId -cne ($version + '-' + $rootHash.Substring(0, 12))) { Add-Problem 'CURRENT.json.buildId must be <version>-<first 12 lowercase binary hash characters>.' }
    if ($current.provenance -cnotin @('adopted-existing-binary', 'source-build')) { Add-Problem 'CURRENT.json.provenance must be adopted-existing-binary or source-build.' }
    if ($current.sourceVerified -isnot [bool]) { Add-Problem 'CURRENT.json.sourceVerified must be a boolean.' }
    elseif ($current.provenance -ceq 'adopted-existing-binary' -and $current.sourceVerified) { Add-Problem 'An adopted-existing-binary must have sourceVerified=false.' }
    elseif ($current.provenance -ceq 'source-build' -and -not $current.sourceVerified) { Add-Problem 'A source-build must have sourceVerified=true.' }
    Test-IsoTime $current.recordedAt 'CURRENT.json.recordedAt'
    Check-BinaryDescriptor $current.binary 'MihomoLite.exe' 'CURRENT.json.binary' $rootHash $rootSize
    if (-not (Has-Property $current 'release')) { Add-Problem 'CURRENT.json.release is required (an object, or null before publication).' }
    elseif ($null -eq $current.release) {
        if (-not $AllowUnpublished) { Add-Problem 'CURRENT.json.release is null: run tools/package.ps1 before a strict publication check.' }
    }
    elseif (-not (Is-Object $current.release)) { Add-Problem 'CURRENT.json.release must be an object, or null before publication.' }
    else {
        $hasPublishedRelease = $true
        $artifactPath = Get-SafeWorkspacePath $current.release.path 'CURRENT.json.release.path'
        $manifestPath = Get-SafeWorkspacePath $current.release.manifest 'CURRENT.json.release.manifest'
        if ($null -ne $expectedZipName -and $current.release.path -cne ('release/' + $expectedZipName)) { Add-Problem "CURRENT.json.release.path must be 'release/$expectedZipName'." }
        if ($current.release.manifest -cne 'release/current.json') { Add-Problem "CURRENT.json.release.manifest must be 'release/current.json'." }
        [void](Test-HashValue $current.release.sha256 'CURRENT.json.release.sha256')
    }
    if ($null -ne $manifestPath) { $manifest = Read-JsonObject $manifestPath 'release/current.json' }
}

if ($null -ne $releasePath -and (Test-Path -LiteralPath $releasePath -PathType Container)) {
    $allowedReleaseFiles = @('current.json', 'SHA256SUMS.txt', 'README.md')
    if ($hasPublishedRelease -and $null -ne $expectedZipName) { $allowedReleaseFiles += $expectedZipName }
    Check-Whitelist $releasePath @() $allowedReleaseFiles 'release'
    if (-not $hasPublishedRelease) {
        foreach ($entry in @(Get-ChildItem -LiteralPath $releasePath -Force)) {
            if ($entry.Name -ieq 'current.json') { Add-Problem 'release/current.json is present but is not referenced by CURRENT.json.' }
        }
    }
} elseif (-not $AllowUnpublished -or $hasPublishedRelease) { Add-Problem 'The release directory is required for the current publication.' }

foreach ($descriptor in @($current.release, $manifest)) {
    if (Has-Property $descriptor 'geoDataPolicy') {
        if (($descriptor.geoDataPolicy -isnot [int] -and $descriptor.geoDataPolicy -isnot [long]) -or $descriptor.geoDataPolicy -ne 1) {
            Add-Problem 'geoDataPolicy must be the integer 1 when present.'
        }
    }
}
if ((Has-Property $current.release 'geoDataPolicy') -ne (Has-Property $manifest 'geoDataPolicy')) {
    Add-Problem 'CURRENT.json.release and release/current.json must agree on geoDataPolicy.'
}
if ($VerifyPayload -and -not $hasPublishedRelease) { Add-Problem '-VerifyPayload requires an adopted published release, even with -AllowUnpublished.' }
if ($null -ne $artifactPath) {
    if (-not (Test-Path -LiteralPath $artifactPath -PathType Leaf)) { Add-Problem "Referenced release ZIP is missing: '$artifactPath'." }
    else {
        $releaseHash = Get-ActualHash $artifactPath 'release ZIP'
        if ($null -ne $current -and $null -ne $releaseHash -and $current.release.sha256 -cne $releaseHash) { Add-Problem 'CURRENT.json.release.sha256 does not match the actual ZIP.' }
        Check-Zip $artifactPath $rootHash $rootSize
    }
}
if ($null -ne $manifest) {
    if ($manifest.version -isnot [string] -or $manifest.version -cne $version) { Add-Problem 'release/current.json.version does not match source AppVersion.' }
    if ($null -ne $current -and $manifest.buildId -cne $current.buildId) { Add-Problem 'release/current.json.buildId does not match CURRENT.json.buildId.' }
    Check-BinaryDescriptor $manifest.binary 'MihomoLite/MihomoLite.exe' 'release/current.json.binary' $rootHash $rootSize
    Test-IsoTime $manifest.createdAt 'release/current.json.createdAt'
    if (-not (Is-Object $manifest.artifact)) { Add-Problem 'release/current.json.artifact must be an object.' }
    else {
        [void](Get-SafeSegments $manifest.artifact.path 'release/current.json.artifact.path')
        if ($manifest.artifact.path -isnot [string] -or $manifest.artifact.path -cne $expectedZipName) { Add-Problem "release/current.json.artifact.path must be the current ZIP filename '$expectedZipName'." }
        if (Test-HashValue $manifest.artifact.sha256 'release/current.json.artifact.sha256') {
            if ($null -ne $releaseHash -and $manifest.artifact.sha256 -cne $releaseHash) { Add-Problem 'release/current.json.artifact.sha256 does not match the actual ZIP.' }
        }
        if (-not (Is-Size $manifest.artifact.size)) { Add-Problem 'release/current.json.artifact.size must be a non-negative integer.' }
        elseif ($null -ne $artifactPath -and (Test-Path -LiteralPath $artifactPath -PathType Leaf)) {
            try { $size = (Get-Item -LiteralPath $artifactPath).Length; if ($manifest.artifact.size -ne $size) { Add-Problem "release/current.json.artifact.size is $($manifest.artifact.size); actual ZIP size is $size." } }
            catch { Add-Problem ("Cannot inspect release ZIP size: {0}" -f $_.Exception.Message) }
        }
    }
}

if ($script:Problems.Count -gt 0) {
    Write-Output ("Workspace constraints FAILED ({0} problem(s)):" -f $script:Problems.Count)
    foreach ($problem in $script:Problems) { Write-Output (" - " + $problem) }
    exit 1
}
if (-not $hasPublishedRelease) { Write-Output 'Workspace constraints OK (unpublished; no published release ZIP adopted).' }
else { Write-Output ("Workspace constraints OK: {0}, binary {1}, release {2}." -f $version, $rootHash.Substring(0, 12), $expectedZipName) }
exit 0
