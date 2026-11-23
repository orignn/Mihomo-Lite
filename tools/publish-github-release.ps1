#requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$ArtifactDirectory,
    [Parameter(Mandatory=$true)][string]$ExpectedCommit,
    [string]$ExpectedTag = '',
    [switch]$Publish,
    [switch]$Draft,
    [switch]$PassThru
)
$ErrorActionPreference = 'Stop'
$repository = 'orignn/Mihomo-Lite'
function Read-GRJson([string]$Path, [long]$Limit=16777216) {
    $f = Get-Item -LiteralPath $Path -Force
    if ($f.PSIsContainer -or $f.Length -le 0 -or $f.Length -gt $Limit -or ($f.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Invalid release metadata file.' }
    $text = [IO.File]::ReadAllText($Path)
    if ((Get-Command ConvertFrom-Json).Parameters.ContainsKey('DateKind')) { return $text | ConvertFrom-Json -DateKind String }
    return $text | ConvertFrom-Json
}
function Test-GRSize($Value) { return ($Value -is [int] -or $Value -is [long]) -and $Value -gt 0 }
function Assert-GRName([string]$Name) {
    if ($Name -cnotmatch '\A[A-Za-z0-9][A-Za-z0-9._-]{0,200}\z' -or $Name -match '[. ]$|^(?i:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\.|$)') { throw 'Unsafe release asset basename.' }
}
function Assert-GRRoot([string]$Path) {
    if (-not [IO.Path]::IsPathRooted($Path)) { throw 'ArtifactDirectory must be an absolute isolated directory.' }
    $full = [IO.Path]::GetFullPath($Path).TrimEnd([char[]]@('/','\'))
    for ($cursor=$full; $cursor; $cursor=[IO.Path]::GetDirectoryName($cursor)) {
        $item=Get-Item -LiteralPath $cursor -Force
        if (-not $item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Release artifact path traverses an unsafe directory.' }
    }
    return $full
}
function Get-GRHash([string]$Path) { return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() }
function Assert-GRZIP([string]$Path, $Manifest) {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    if ($Manifest.releaseNoticePolicy -ne 1 -or $Manifest.geoDataPolicy -ne 1 -or $Manifest.sourceVerified -cne $true -or $Manifest.files -isnot [Array]) { throw 'Complete release requires source, licensing and double-DAT evidence.' }
    $expected=@{}; $notices=@{}; $seen=New-Object 'Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    foreach($record in $Manifest.files) {
        if ($record.path -isnot [string] -or $record.path -cnotmatch '\AMihomoLite/[A-Za-z0-9_.@ /+-]+\z' -or $record.path -match '(^|/)\.\.?(/|$)|[. ]$' -or $record.sha256 -cnotmatch '\A[0-9a-f]{64}\z' -or -not(Test-GRSize $record.size) -or -not $seen.Add($record.path)) { throw 'Invalid or duplicate package file identity.' }
        $expected[$record.path]=$record
    }
    $launcher=$expected['MihomoLite/MihomoLite.exe']
    if($null -eq $launcher -or $Manifest.binary.path -cne 'MihomoLite/MihomoLite.exe' -or $Manifest.binary.sha256 -cne $launcher.sha256 -or $Manifest.binary.size -ne $launcher.size){throw 'Package launcher identity disagrees with ZIP payload.'}
    foreach($record in @($Manifest.noticeFiles)) {
        $key='MihomoLite/'+$record.path
        if($record.path -cne 'THIRD_PARTY_NOTICES.md' -and $record.path -cnotmatch '\ALICENSES/[A-Za-z0-9._/-]+\.(?:txt|md|json)\z'){throw 'Invalid license payload path.'}
        if(-not $expected.ContainsKey($key) -or $expected[$key].sha256 -cne $record.sha256 -or $expected[$key].size -ne $record.size -or $notices.ContainsKey($key)){throw 'License inventory mismatch.'}
        $notices[$key]=$record
    }
    foreach($name in @('MihomoLite.exe','runtime/mihomo.exe','data/ui/index.html','data/geosite.dat','data/geoip.dat','data/geoip.metadb','README.md','example.yaml','docs/DEVELOPMENT.md','docs/RELEASE.md','docs/CHANGELOG.md','THIRD_PARTY_NOTICES.md','LICENSES/manifest.json')) {
        if(-not $expected.ContainsKey('MihomoLite/'+$name)){throw 'Required complete package file is absent.'}
    }
    if(-not $notices.ContainsKey('MihomoLite/THIRD_PARTY_NOTICES.md') -or -not $notices.ContainsKey('MihomoLite/LICENSES/manifest.json') -or @($notices.Keys|Where-Object{$_ -match '\.(txt|md)$' -and $_ -ne 'MihomoLite/THIRD_PARTY_NOTICES.md'}).Count -eq 0){throw 'Full license text/index is absent.'}
    $zip=[IO.Compression.ZipFile]::OpenRead($Path)
    try {
        $seen.Clear();$count=0;$index=$null
        foreach($entry in $zip.Entries){
            $name=$entry.FullName
            if($name -match '\\|[\x00-\x1f:]|(^|/)\.\.?(/|$)' -or -not $name.StartsWith('MihomoLite/',[StringComparison]::Ordinal) -or (($entry.ExternalAttributes -shr 16) -band 61440) -eq 40960){throw 'Unsafe ZIP path or symlink.'}
            if($name.EndsWith('/')){continue}
            if(-not $seen.Add($name) -or -not $expected.ContainsKey($name) -or $entry.Length -ne $expected[$name].size){throw 'ZIP inventory/size mismatch.'}
            $allowedFixed=@('MihomoLite/MihomoLite.exe','MihomoLite/runtime/mihomo.exe','MihomoLite/data/geoip.metadb','MihomoLite/data/geosite.dat','MihomoLite/data/geoip.dat','MihomoLite/example.yaml','MihomoLite/README.md','MihomoLite/docs/DEVELOPMENT.md','MihomoLite/docs/RELEASE.md','MihomoLite/docs/CHANGELOG.md','MihomoLite/docs/THIRD-PARTY.md')
            $isUI=$name.StartsWith('MihomoLite/data/ui/',[StringComparison]::Ordinal) -and $name -match '\.(?i:html|js|css|json|webmanifest|svg|png|jpg|jpeg|webp|gif|ico|woff|woff2|ttf|otf|txt|md|map|wasm)$'
            if($allowedFixed -cnotcontains $name -and -not $notices.ContainsKey($name) -and -not $isUI){throw 'ZIP payload is outside the public whitelist.'}
            if($name -match '(?i)(^|/)(?:profiles|logs|\.cache|cache|Crashpad|Local Storage|Session Storage|IndexedDB)(/|$)|/(?:launcher\.json|profiles\.json|custom-rules\.json|source\.ya?ml|runtime\.ya?ml)(?:$|\.)'){throw 'Private data is forbidden in a release ZIP.'}
            $stream=$entry.Open();$sha=[Security.Cryptography.SHA256]::Create()
            try{$digest=[BitConverter]::ToString($sha.ComputeHash($stream)).Replace('-','').ToLowerInvariant()}finally{$sha.Dispose();$stream.Dispose()}
            if($digest -cne $expected[$name].sha256){throw 'ZIP payload digest mismatch.'}
            if($name -ceq 'MihomoLite/LICENSES/manifest.json'){
                if($entry.Length -gt 4194304){throw 'License index exceeds size limit.'}
                $stream=$entry.Open();$reader=New-Object IO.StreamReader($stream,[Text.Encoding]::UTF8)
                try{$index=$reader.ReadToEnd()|ConvertFrom-Json}finally{$reader.Dispose();$stream.Dispose()}
            }
            $count++
        }
        if($count -ne $expected.Count -or $null -eq $index -or $index.schemaVersion -ne 1 -or $index.records -isnot [Array]){throw 'ZIP payload is incomplete or license index invalid.'}
        $indexed=@{}
        foreach($entry in $index.records){$key='MihomoLite/'+$entry.path;if($entry.path -ceq 'LICENSES/manifest.json' -or -not $key.StartsWith('MihomoLite/LICENSES/',[StringComparison]::Ordinal) -or $indexed.ContainsKey($key) -or -not $notices.ContainsKey($key) -or $entry.sha256 -cne $notices[$key].sha256 -or $entry.size -ne $notices[$key].size){throw 'License index records mismatch.'};$indexed[$key]=$true}
        foreach($key in $notices.Keys){if($key -cne 'MihomoLite/THIRD_PARTY_NOTICES.md' -and $key -cne 'MihomoLite/LICENSES/manifest.json' -and -not $indexed.ContainsKey($key)){throw 'License inventory entry absent from index.'}}
    }finally{$zip.Dispose()}
}
function Invoke-GRApi([string]$Method,[string]$URL,$Body=$null,[string]$File='',[switch]$AllowNotFound) {
    if(-not $Publish){throw 'Release network writes require explicit Publish.'}
    if($URL -cnotmatch '\Ahttps://(?:api\.github\.com/repos/orignn/Mihomo-Lite/(?:releases(?:/tags/v[0-9]+\.[0-9]+\.[0-9]+|/[0-9]+)?|git/(?:ref/tags/v[0-9]+\.[0-9]+\.[0-9]+|tags/[0-9a-f]{40}))|uploads\.github\.com/repos/orignn/Mihomo-Lite/releases/[0-9]+/assets\?name=[A-Za-z0-9._-]+)\z'){throw 'Untrusted release API location.'}
    $headers=@{Authorization='Bearer '+$env:GH_TOKEN;Accept='application/vnd.github+json';'User-Agent'='MihomoLite-Release-Publisher';'X-GitHub-Api-Version'='2022-11-28'}
    $request=@{Uri=$URL;Method=$Method;Headers=$headers;MaximumRedirection=0;TimeoutSec=300;ErrorAction='Stop';Verbose=$false;Debug=$false}
    if($File){$request.InFile=$File;$request.ContentType='application/octet-stream'}elseif($null -ne $Body){$request.Body=($Body|ConvertTo-Json -Depth 8 -Compress);$request.ContentType='application/json'}
    try{return Invoke-RestMethod @request}catch{if($AllowNotFound -and $_.Exception.Response -and [int]$_.Exception.Response.StatusCode -eq 404){return $null};throw 'GitHub release request failed; inspect the draft in GitHub before retrying (credentials/redirect details suppressed).'}
}

$dir=Assert-GRRoot $ArtifactDirectory
if($ExpectedCommit -cnotmatch '\A[0-9a-f]{40}\z'){throw 'ExpectedCommit must be a full lowercase commit.'}
$report=Read-GRJson (Join-Path $dir 'release-report.json')
if($report.schemaVersion -ne 1 -or $report.status -cne 'verified-ready' -or $report.repository -cne $repository -or $report.sourceCommit -cne $ExpectedCommit -or $report.tag -cnotmatch '\Av[0-9]+\.[0-9]+\.[0-9]+\z' -or $report.version -cne $report.tag.Substring(1) -or ($ExpectedTag -and $report.tag -cne $ExpectedTag)){throw 'Release report does not match the trusted repository/revision/tag.'}
$tag=[string]$report.tag
if($report.sourceVerified -cne $true -or $report.releaseNoticePolicy -ne 1 -or $report.geoDataPolicy -ne 1 -or $report.assets -isnot [Array] -or $report.assets.Count -lt 7){throw 'Incomplete release evidence.'}
$assets=@{};$names=New-Object 'Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
foreach($record in $report.assets){Assert-GRName $record.name;if(-not $names.Add($record.name) -or $record.sha256 -cnotmatch '\A[0-9a-f]{64}\z' -or -not(Test-GRSize $record.size)){throw 'Invalid release artifact records.'};$file=Join-Path $dir $record.name;$f=Get-Item -LiteralPath $file -Force;if($f.PSIsContainer -or ($f.Attributes -band [IO.FileAttributes]::ReparsePoint) -or $f.Length -ne $record.size -or (Get-GRHash $file) -cne $record.sha256){throw 'Release artifact hash/size mismatch.'};$assets[$record.name]=$record}
$zipName='MihomoLite-'+$tag+'-'+([string]$report.launcherSHA256).Substring(0,12)+'-win-x64.zip'
foreach($name in @($zipName,'SHA256SUMS.txt','current.json','RELEASE_NOTES.md','RELEASE_SHA256SUMS.txt')){if(-not $assets.ContainsKey($name)){throw 'Missing required release asset.'}}
foreach($name in $assets.Keys){if($name -cnotin @($zipName,'SHA256SUMS.txt','current.json','RELEASE_NOTES.md','RELEASE_SHA256SUMS.txt') -and $name -cnotmatch '\ASource-[a-z][a-z0-9-]{0,63}-[0-9a-f]{12}\.(?:tar\.gz|zip)\z'){throw 'Unapproved release asset name.'}}
$actual=@(Get-ChildItem -LiteralPath $dir -Force);if(@($actual|Where-Object{$_.PSIsContainer -or ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -or ($_.Name -cne 'release-report.json' -and -not $assets.ContainsKey($_.Name))}).Count -or $actual.Count -ne $assets.Count+1){throw 'Unexpected data in release artifact directory.'}
$manifest=Read-GRJson (Join-Path $dir 'current.json')
if($manifest.version -cne $report.version -or $manifest.binary.sha256 -cne $report.launcherSHA256 -or $manifest.artifact.path -cne $zipName -or $manifest.artifact.sha256 -cne $assets[$zipName].sha256 -or $manifest.artifact.size -ne $assets[$zipName].size){throw 'Package and release artifact identities differ.'}
$sources=New-Object 'Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
foreach($source in @($manifest.sourceArchives)){
    if($source.component -cnotmatch '\A[a-z][a-z0-9-]{0,63}\z' -or $source.commit -cnotmatch '\A[0-9a-f]{40}\z' -or $source.repository -cnotin @('MetaCubeX/mihomo','Zephyruso/zashboard','MetaCubeX/meta-rules-dat') -or $source.url -cne ('https://github.com/'+$source.repository+'/archive/'+$source.commit+'.tar.gz')){throw 'Invalid fixed upstream source identity.'}
    $suffix=if($source.path.EndsWith('.tar.gz',[StringComparison]::Ordinal)){'.tar.gz'}else{'.zip'};$name='Source-'+$source.component+'-'+$source.commit.Substring(0,12)+$suffix
    if(-not $sources.Add($name) -or -not $assets.ContainsKey($name) -or $source.sha256 -cne $assets[$name].sha256 -or $source.size -ne $assets[$name].size){throw 'Corresponding source archive missing or mismatched.'}
}
if($sources.Count -eq 0 -or @($assets.Keys|Where-Object{$_ -cmatch '^Source-' -and -not $sources.Contains($_)}).Count){throw 'Unexpected or absent source archives.'}
Assert-GRZIP (Join-Path $dir $zipName) $manifest
$payloadSums=@{};foreach($line in [IO.File]::ReadAllLines((Join-Path $dir 'SHA256SUMS.txt'))){if($line -cnotmatch '\A([0-9a-f]{64})  (.+)\z' -or $payloadSums.ContainsKey($Matches[2])){throw 'Invalid package checksum manifest.'};$payloadSums[$Matches[2]]=$Matches[1]}
if($payloadSums[$zipName] -cne $assets[$zipName].sha256 -or $payloadSums.Count -ne @($manifest.files).Count+1){throw 'Package checksum inventory mismatch.'};foreach($record in $manifest.files){if($payloadSums[$record.path] -cne $record.sha256){throw 'Package checksum mismatch.'}}
$releaseSums=@{};foreach($line in [IO.File]::ReadAllLines((Join-Path $dir 'RELEASE_SHA256SUMS.txt'))){if($line -cnotmatch '\A([0-9a-f]{64})  ([A-Za-z0-9._-]+)\z' -or $releaseSums.ContainsKey($Matches[2])){throw 'Invalid release checksum manifest.'};$releaseSums[$Matches[2]]=$Matches[1]}
if($releaseSums.Count -ne $assets.Count-1){throw 'Release checksum inventory mismatch.'};foreach($name in $assets.Keys){if($name -cne 'RELEASE_SHA256SUMS.txt' -and $releaseSums[$name] -cne $assets[$name].sha256){throw 'Release checksum mismatch.'}}
if(-not $Publish){Write-Host ('Release assets verified offline for '+$tag);if($PassThru){[pscustomobject]@{status='verified-ready';tag=$tag;assets=$assets.Count;published=$false}};return}
if($env:GITHUB_REPOSITORY -cne $repository -or [string]::IsNullOrWhiteSpace($env:GH_TOKEN)){throw 'Publishing requires the official GitHub repository job token.'}
$ref=Invoke-GRApi GET ('https://api.github.com/repos/'+$repository+'/git/ref/tags/'+$tag) -AllowNotFound
if($ref){$object=$ref.object;for($i=0;$object.type -ceq 'tag' -and $i -lt 5;$i++){$object=(Invoke-GRApi GET ('https://api.github.com/repos/'+$repository+'/git/tags/'+$object.sha)).object};if($object.type -cne 'commit' -or $object.sha -cne $ExpectedCommit){throw 'Existing tag identifies another commit; no tag will be replaced.'}}
$existing=Invoke-GRApi GET ('https://api.github.com/repos/'+$repository+'/releases/tags/'+$tag) -AllowNotFound
if($existing){throw 'A release with this tag already exists; do not overwrite or automatically retry uploads.'}
$release=Invoke-GRApi POST ('https://api.github.com/repos/'+$repository+'/releases') @{tag_name=$tag;target_commitish=$ExpectedCommit;name=('Mihomo Lite '+$tag);body=[IO.File]::ReadAllText((Join-Path $dir 'RELEASE_NOTES.md'));draft=$true;prerelease=$false}
if(-not(Test-GRSize $release.id) -or $release.tag_name -cne $tag -or $release.draft -cne $true){throw 'Unexpected release creation receipt; inspect GitHub before any retry.'}
$releaseID=[long]$release.id
foreach($name in @($assets.Keys|Sort-Object)){
    $uploaded=Invoke-GRApi POST ('https://uploads.github.com/repos/'+$repository+'/releases/'+$releaseID+'/assets?name='+$name) -File (Join-Path $dir $name)
    if($uploaded.name -cne $name -or $uploaded.state -cne 'uploaded' -or $uploaded.size -ne $assets[$name].size -or $uploaded.digest -cne ('sha256:'+$assets[$name].sha256)){throw 'Uploaded asset receipt/digest mismatch; draft retained for inspection.'}
}
$final=Invoke-GRApi GET ('https://api.github.com/repos/'+$repository+'/releases/'+$releaseID)
if(@($final.assets).Count -ne $assets.Count){throw 'Uploaded release inventory mismatch; draft retained.'}
foreach($uploaded in $final.assets){if(-not $assets.ContainsKey($uploaded.name) -or $uploaded.size -ne $assets[$uploaded.name].size -or $uploaded.digest -cne ('sha256:'+$assets[$uploaded.name].sha256)){throw 'Uploaded release inventory integrity mismatch.'}}
if(-not $Draft){$final=Invoke-GRApi PATCH ('https://api.github.com/repos/'+$repository+'/releases/'+$releaseID) @{draft=$false};if($final.draft -cne $false){throw 'Publish confirmation is incomplete; inspect GitHub release state.'}}
Write-Host ('GitHub release completed: '+$tag+'; draft='+[bool]$Draft)
if($PassThru){[pscustomobject]@{status='published';tag=$tag;releaseId=$releaseID;draft=[bool]$Draft;url=('https://github.com/'+$repository+'/releases/tag/'+$tag)}}
