#requires -Version 5.1
[CmdletBinding()]
param([string]$ResultRoot = '')
$ErrorActionPreference = 'Stop'
$project = Split-Path -Parent $PSScriptRoot
$scriptPath = Join-Path $project 'tools/prepare-release-notices.ps1'
if (-not $ResultRoot) { $ResultRoot = Join-Path $project '.cache/tests/ci-release/20261123-2103/notices' }
$ResultRoot = [IO.Path]::GetFullPath($ResultRoot)
if (-not $ResultRoot.StartsWith([IO.Path]::GetFullPath((Join-Path $project '.cache/tests/')), [StringComparison]::OrdinalIgnoreCase)) { throw 'Tests must remain in isolated .cache/tests.' }
[void][IO.Directory]::CreateDirectory($ResultRoot)
$run = Join-Path $ResultRoot ('run-' + [guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($run)
$rows = New-Object 'Collections.Generic.List[object]'
$global:NoticeTest_mockCalls = New-Object 'Collections.Generic.List[string]'
$global:NoticeTest_offline = $false; $global:NoticeTest_noLicense = $false; $global:NoticeTest_movedCommit = $false
$global:NoticeTest_commit = 'a' * 40
function Put([string]$Root, [string]$Path, [string]$Text) {
    $p = Join-Path $Root $Path
    [void][IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($p))
    [IO.File]::WriteAllText($p, $Text, (New-Object Text.UTF8Encoding($false)))
}
function ReadText([string]$Root, [string]$Path) { return [IO.File]::ReadAllText((Join-Path $Root $Path)) }
function Assert([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
function ExpectReject([scriptblock]$Action, [string]$Pattern) {
    try { & $Action | Out-Null } catch { if ($_.Exception.Message -notmatch $Pattern) { throw ('Wrong rejection: ' + $_.Exception.Message) }; return }
    throw 'Expected rejection did not occur.'
}
function Case([string]$Name, [scriptblock]$Action) {
    try { & $Action; $rows.Add([pscustomobject]@{ name = $Name; status = 'PASS' }); Write-Output ('PASS ' + $Name) }
    catch { $rows.Add([pscustomobject]@{ name = $Name; status = 'FAIL'; error = $_.Exception.Message }); Write-Output ('FAIL ' + $Name + ': ' + $_.Exception.Message) }
}
function NewFixture([string]$Name) {
    $root = Join-Path $run $Name
    [void][IO.Directory]::CreateDirectory($root)
    $nl = [Environment]::NewLine
    Put $root 'go.mod' (('module mihomolite','go 1.23','require (',('github.com/dlclark/regexp2' + [char]9 + 'v1.12.0'),'gopkg.in/yaml.v3 v3.0.1',')') -join $nl)
    $sum = 'h1:' + ('A' * 43) + '='
    Put $root 'go.sum' (('github.com/dlclark/regexp2 v1.12.0 ' + $sum + $nl + 'gopkg.in/yaml.v3 v3.0.1 ' + $sum + $nl))
    foreach ($m in @('github.com/dlclark/regexp2@v1.12.0','gopkg.in/yaml.v3@v3.0.1')) {
        $base = '.cache/toolchain/gomod/' + $m
        Put $root ($base + '/go.mod') 'module fixture-public-module'
        Put $root ($base + '/LICENSE') 'SYNTHETIC TEST ONLY: Copyright Public Fixture Authors. MIT permission fixture.'
        Put $root ($base + '/NOTICE') 'SYNTHETIC TEST ONLY: retained NOTICE attribution.'
        Put $root ($base + '/nested/COPYING') 'SYNTHETIC TEST ONLY: second original license.'
        Put $root ($base + '/nested/LICENSE-MIT') 'SYNTHETIC TEST ONLY: alternate license.'
        Put $root ($base + '/ignore-private.txt') 'NOT A LICENSE: MUST NOT BE COLLECTED'
        $parts = $m.Split('@')
        Put $root ('.cache/toolchain/gomod/cache/download/' + $parts[0] + '/@v/' + $parts[1] + '.ziphash') $sum
    }
    $geoRoot = '.cache/downloads/release-geodata-fixture'
    Put $root ($geoRoot + '/provenance.json') ([ordered]@{schemaVersion=1;status='verified-ready';verified=$true;repository='MetaCubeX/meta-rules-dat';releaseId=3;releaseTag='20261123';geoDataRoot=$geoRoot} | ConvertTo-Json)
    foreach ($f in @('geosite.dat','geoip.dat')) { Put $root ($geoRoot + '/' + $f) ('SYNTHETIC ' + $f); Put $root ($geoRoot + '/' + $f + '.sha256sum') ('b' * 64) }
    $components = @()
    foreach ($v in @(@('core','MetaCubeX/mihomo',1,'v1.2.3'),@('panel','Zephyruso/zashboard',2,'v2.3.4'),@('metadb','MetaCubeX/meta-rules-dat',3,'20261123'))) {
        $components += [pscustomobject]@{name=$v[0];repository=$v[1];releaseId=$v[2];releaseTag=$v[3];sourceCommitSHA=$global:NoticeTest_commit;files=@()}
    }
    Put $root '.cache/downloads/release-components-fixture/components.json' ([ordered]@{schemaVersion=1;status='verified-ready';verified=$true;components=$components;geoDataRoot=$geoRoot;geoReleaseId=3} | ConvertTo-Json -Depth 8)
    Put $root 'data/sentinel.txt' 'SYNTHETIC PROTECTED DATA: DO NOT TOUCH'
    return $root
}
function Invoke-WebRequest {
    [CmdletBinding()]
    param([string]$Uri,[switch]$UseBasicParsing,[int]$MaximumRedirection,[int]$TimeoutSec,[hashtable]$Headers)
    if ($global:NoticeTest_offline) { throw 'TEST: network invoked during offline validation' }
    $global:NoticeTest_mockCalls.Add($Uri)
    if ($Uri -in @('https://www.maxmind.com/en/geolite2/eula','https://creativecommons.org/licenses/by-sa/4.0/legalcode.txt')) { return [pscustomobject]@{StatusCode=200;Content='SYNTHETIC TEST POLICY ONLY: MaxMind attribution and CC BY-SA 4.0 license text fixture.'} }
    if ($Uri -cnotmatch '^https://api\.github\.com/repos/') { throw 'Unexpected mock URL' }
    if ($Uri -match '/releases/([1-3])$') {
        $tags = @{1='v1.2.3';2='v2.3.4';3='20261123'}
        $body = @{id=[int]$Matches[1];tag_name=$tags[[int]$Matches[1]];draft=$false}
    } elseif ($Uri -match '/commits/') {
        $sha = $global:NoticeTest_commit
        if ($global:NoticeTest_movedCommit -and $Uri -match '/mihomo/commits/') { $sha = 'c' * 40 }
        $body = @{sha=$sha}
    } elseif ($Uri -match '/git/trees/') {
        $body = @{truncated=$false;tree=@(@{type='blob';path='nested/NOTICE.extra'})}
    } elseif ($Uri -match '/contents/(.+)\?ref=([a-f0-9]{40})$') {
        $file = $Matches[1]
        if ($global:NoticeTest_noLicense -and $Uri -match '/mihomo/contents/(LICENSE|COPYING)\?') {
            $e = New-Object System.Net.WebException 'SYNTHETIC missing license'
            # Production catches public 404; response mock property preserves the same shape.
            $e | Add-Member -NotePropertyName Response -NotePropertyValue ([pscustomobject]@{StatusCode=404}) -Force
            throw $e
        }
        $body = @{encoding='base64';type='file';path=$file;content=[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes('SYNTHETIC TEST ONLY: upstream original ' + $file + ' Copyright Fixture Authors.'))}
    } else { throw 'Unknown synthetic public URL' }
    return [pscustomobject]@{StatusCode=200;Content=($body | ConvertTo-Json -Depth 8)}
}
# Replace ONLY archive transport in a test copy via parsed AST, never run upstream code.
$t=$null; $e=$null
$ast=[Management.Automation.Language.Parser]::ParseFile($scriptPath,[ref]$t,[ref]$e)
if ($e.Count) { throw 'Production script parser errors.' }
$node=$ast.Find({param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Save-NoticeArchive'},$true)
$production=[IO.File]::ReadAllText($scriptPath)
$mockArchive=@'
function Save-NoticeArchive([string]$Repository,[string]$Commit,[string]$Component,[string]$Batch) {
    if (-not $Refresh) { throw 'TEST: archive network without Refresh' }
    $relative=$Batch+'/'+$Component+'-source-'+$Commit+'.tar.gz'
    Write-NoticeText $relative 'SYNTHETIC TEST ONLY: no upstream code was downloaded or executed.'
    $r=Get-NoticeRecord $relative
    return [pscustomobject]@{path=$r.path;sha256=$r.sha256;size=$r.size;component=$Component;repository=$Repository;commit=$Commit;url='https://github.com/'+$Repository+'/archive/'+$Commit+'.tar.gz'}
}
'@
$testScript = Join-Path $run 'mock-prepare-release-notices.ps1'
Put $run 'mock-prepare-release-notices.ps1' ($production.Substring(0,$node.Extent.StartOffset)+$mockArchive+$production.Substring($node.Extent.EndOffset))
$cm='.cache/downloads/release-components-fixture/components.json'
Case 'PS5.1 parser and mandatory workspace parameters' {
    Assert ($PSVersionTable.PSVersion -ge [version]'5.1') 'Expected supported PowerShell'
    Assert (@($ast.ParamBlock.Parameters | Where-Object { $_.Name.VariablePath.UserPath -eq 'WorkspaceRoot' }).Count -eq 1) 'Missing root parameter'
}
$global:NoticeTest_good = NewFixture 'good'
Case 'Refresh preserves every local license/notice and source archives' {
    $global:NoticeTest_prepared = & $testScript -WorkspaceRoot $global:NoticeTest_good -ComponentManifest $cm -Refresh
    Assert ($global:NoticeTest_prepared.status -eq 'verified-ready') 'Good public fixture should be ready'
    Assert (@($global:NoticeTest_prepared.records | Where-Object {$_.path -like 'LICENSES/go-*/*'}).Count -eq 8) 'Local license/notice inventory was not complete'
    Assert (@($global:NoticeTest_prepared.sourceArchives).Count -eq 2) 'Missing fixed source archive records'
    Assert (@($global:NoticeTest_prepared.records | Where-Object {$_.path -match '\.tar\.gz$'}).Count -eq 0) 'Source archive leaked into install ZIP records'
    $texts=@($global:NoticeTest_prepared.records | Where-Object {$_.path -like 'LICENSES/go-*/*'} | ForEach-Object {ReadText $global:NoticeTest_good $_.path}) -join ' '
    Assert ($texts -match 'retained NOTICE' -and $texts -match 'second original license' -and $texts -notmatch 'MUST NOT BE COLLECTED') 'License originals corrupted or private nonlicense collected'
}
Case 'Machine index has exact files and no self-hash cycle' {
    $index=(ReadText $global:NoticeTest_good 'LICENSES/manifest.json') | ConvertFrom-Json
    Assert (@($index.records | Where-Object {$_.path -eq 'LICENSES/manifest.json'}).Count -eq 0) 'Self hash cycle'
    Assert (@($global:NoticeTest_prepared.records | Where-Object {$_.path -eq 'LICENSES/manifest.json'}).Count -eq 1) 'Cache lacks machine index hash'
    Assert (@($index.records).Count -eq (@($global:NoticeTest_prepared.records).Count-2)) 'Index does not exactly cover LICENSES'
}
Case 'Actual production offline validation makes zero network calls' {
    $global:NoticeTest_offline=$true
    try { $r=& $scriptPath -WorkspaceRoot $global:NoticeTest_good -ComponentManifest $cm -NoticesManifest $global:NoticeTest_prepared.manifest; Assert ($r.status -eq 'verified-ready') 'Offline status changed' }
    finally { $global:NoticeTest_offline=$false }
}
Case 'Refresh refuses overwrite and preserves protected sentinel' {
    ExpectReject {& $testScript -WorkspaceRoot $global:NoticeTest_good -ComponentManifest $cm -Refresh} 'refuses existing'
    Assert ((ReadText $global:NoticeTest_good 'data/sentinel.txt') -eq 'SYNTHETIC PROTECTED DATA: DO NOT TOUCH') 'Protected synthetic runtime data changed'
}
Case 'No maintainer LICENSE does not invent or select MIT/GPL' {
    $n=ReadText $global:NoticeTest_good 'THIRD_PARTY_NOTICES.md'
    Assert ($n -match 'no project LICENSE' -and $n -match 'does not select MIT/GPL') 'Main project license was invented'
    Assert ($n -match 'thirty \(30\) days' -and $n -match 'MaxMind' -and $n -match 'Attribution-ShareAlike') 'Known GeoLite obligations missing'
}
Case 'Offline rejects tampered license and never repairs it' {
    $p=@($global:NoticeTest_prepared.records | Where-Object {$_.path -like 'LICENSES/go-*/*'})[0].path
    $old=ReadText $global:NoticeTest_good $p; Put $global:NoticeTest_good $p 'tampered'
    try { ExpectReject {& $scriptPath -WorkspaceRoot $global:NoticeTest_good -ComponentManifest $cm} 'hash/size mismatch' } finally { Put $global:NoticeTest_good $p $old }
}
Case 'Offline rejects unlisted LICENSES files' {
    Put $global:NoticeTest_good 'LICENSES/panel/unlisted.txt' 'SYNTHETIC unlisted'
    ExpectReject {& $scriptPath -WorkspaceRoot $global:NoticeTest_good -ComponentManifest $cm} 'Unlisted LICENSES'
}
Case 'Reject local/private module replacement before network or output' {
    $root=NewFixture 'replace'; Put $root 'go.mod' ((ReadText $root 'go.mod')+[Environment]::NewLine+'replace github.com/dlclark/regexp2 => C:/private/secret')
    $before=$global:NoticeTest_mockCalls.Count
    ExpectReject {& $testScript -WorkspaceRoot $root -ComponentManifest $cm -Refresh} 'replace/exclude'
    Assert ($before -eq $global:NoticeTest_mockCalls.Count -and -not [IO.Directory]::Exists((Join-Path $root 'LICENSES'))) 'Bad module input contacted network or wrote output'
}
Case 'Reject unsafe relative cache/manifest and daily workspace' {
    $root=NewFixture 'paths'
    ExpectReject {& $scriptPath -WorkspaceRoot '.' -ComponentManifest $cm} 'absolute local'
    ExpectReject {& $scriptPath -WorkspaceRoot $project -ComponentManifest $cm} 'isolated source-clone'
    ExpectReject {& $scriptPath -WorkspaceRoot $root -ComponentManifest '.cache/downloads/release-components-fixture/../components.json'} 'ComponentManifest'
    ExpectReject {& $scriptPath -WorkspaceRoot $root -ComponentManifest $cm -GoModCache 'C:/private/gomod'} 'GOMODCACHE'
}
Case 'Reject mismatching cache module lock before network' {
    $root=NewFixture 'ziphash'; Put $root '.cache/toolchain/gomod/cache/download/github.com/dlclark/regexp2/@v/v1.12.0.ziphash' ('h1:'+('B'*43)+'=')
    ExpectReject {& $testScript -WorkspaceRoot $root -ComponentManifest $cm -Refresh} 'ziphash differs'
}
Case 'Legacy Go module uses canonical cached .mod without changing its source' {
    $root=NewFixture 'legacy-mod'
    $source=Join-Path $root '.cache/toolchain/gomod/github.com/dlclark/regexp2@v1.12.0/go.mod'
    $archive=Join-Path $root '.cache/toolchain/gomod/github.com/dlclark/regexp2@v1.12.0/legacy-original.mod'
    if (-not [IO.File]::Exists($source) -or [IO.File]::Exists($archive)) { throw 'Unexpected isolated legacy fixture paths' }
    [IO.File]::Move($source,$archive)
    $cached='.cache/toolchain/gomod/cache/download/github.com/dlclark/regexp2/@v/v1.12.0.mod'
    Put $root $cached 'module github.com/dlclark/regexp2'
    $r=& $testScript -WorkspaceRoot $root -ComponentManifest $cm -Refresh
    Assert ($r.status -eq 'verified-ready') 'Legacy module should use canonical Go cache metadata'
    Assert (@($r.inputs | Where-Object {$_.path -ceq $cached}).Count -eq 1) 'Cached module metadata must be an immutable input'
    $global:NoticeTest_offline=$true
    try { & $scriptPath -WorkspaceRoot $root -ComponentManifest $cm -NoticesManifest $r.manifest | Out-Null }
    finally { $global:NoticeTest_offline=$false }
    Put $root $cached 'module tampered'
    ExpectReject { & $scriptPath -WorkspaceRoot $root -ComponentManifest $cm -NoticesManifest $r.manifest } 'hash/size mismatch|stale'
}
Case 'Reject mismatching Geo release provenance' {
    $root=NewFixture 'geobatch'; $p='.cache/downloads/release-geodata-fixture/provenance.json'; Put $root $p ((ReadText $root $p).Replace('"releaseId":  3','"releaseId":  4').Replace('"releaseId": 3','"releaseId": 4'))
    ExpectReject {& $testScript -WorkspaceRoot $root -ComponentManifest $cm -Refresh} 'Geo provenance disagrees'
}
Case 'Reject moving component source commit' {
    $root=NewFixture 'moving'; $global:NoticeTest_movedCommit=$true
    try {ExpectReject {& $testScript -WorkspaceRoot $root -ComponentManifest $cm -Refresh} 'tag commit changed'} finally {$global:NoticeTest_movedCommit=$false}
}
Case 'Missing actual official license is publication-blocked' {
    $root=NewFixture 'missing-license'; $global:NoticeTest_noLicense=$true
    try {$r=& $testScript -WorkspaceRoot $root -ComponentManifest $cm -Refresh; Assert ($r.status -eq 'redistribution-blocked' -and -not $r.redistributionReady) 'Missing license was allowed'} finally {$global:NoticeTest_noLicense=$false}
}
Case 'Offline refuses stale source locks' {
    $root=NewFixture 'stale'; $r=& $testScript -WorkspaceRoot $root -ComponentManifest $cm -Refresh
    Put $root 'go.mod' ((ReadText $root 'go.mod')+[Environment]::NewLine+'// source changed')
    ExpectReject {& $scriptPath -WorkspaceRoot $root -ComponentManifest $cm -NoticesManifest $r.manifest} 'hash/size mismatch|stale'
}
Case 'No token or signed/private URL appears in fixture manifest' {
    $text=ReadText $global:NoticeTest_good $global:NoticeTest_prepared.manifest
    Assert ($text -notmatch 'Authorization|Bearer |github_pat_|ghp_|token=|sig=|ignore-private') 'Sensitive transport/private path leaked'
}
$summary=[ordered]@{schemaVersion=1;scope='synthetic public upstream/module fixture only; no builds, no real runtime data, no live upstream tests';powershellVersion=$PSVersionTable.PSVersion.ToString();passed=@($rows | Where-Object {$_.status -eq 'PASS'}).Count;failed=@($rows | Where-Object {$_.status -eq 'FAIL'}).Count;tests=@($rows.ToArray());mockNetworkCalls=$global:NoticeTest_mockCalls.Count;resultDirectory=$run}
Put $run 'results.json' ($summary | ConvertTo-Json -Depth 10)
Write-Output ('RESULT '+(Join-Path $run 'results.json'))
if($summary.failed){exit 1}
