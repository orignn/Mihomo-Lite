#requires -Version 5.1
<#
Offline preflight and complete orchestration tests for tools/build-ci-release.ps1.
Every fixture is synthetic and isolated under .cache/tests; no network, no Go/Node build,
no real runtime data, no tag creation and no GitHub write.
#>
$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$run = Join-Path $repoRoot ('.cache/tests/ci-release/' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $run -Force | Out-Null
$script = Join-Path $repoRoot 'tools/build-ci-release.ps1'
if (-not (Test-Path -LiteralPath $script -PathType Leaf)) { throw 'Missing build-ci-release.ps1' }
$git = (Get-Command git -CommandType Application | Select-Object -First 1).Source
$passed = 0; $failed = 0; $results = @()
function Assert-Case([bool]$Condition, [string]$Name) {
    if ($Condition) { $script:passed++; Write-Host ('PASS ' + $Name) }
    else { $script:failed++; Write-Host ('FAIL ' + $Name) }
    $script:results += [ordered]@{ name = $Name; passed = [bool]$Condition }
}
function New-Fixture([string]$Name) {
    $dir = Join-Path $run $Name
    New-Item -ItemType Directory -Path (Join-Path $dir 'cmd/mihomolite'),(Join-Path $dir 'tools') -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $dir 'cmd/mihomolite/types.go'), 'package main' + [Environment]::NewLine + 'const AppVersion = "0.6.18"' + [Environment]::NewLine)
    [IO.File]::WriteAllText((Join-Path $dir 'go.mod'), 'module mihomolite' + [Environment]::NewLine + 'go 1.23' + [Environment]::NewLine)
    [IO.File]::WriteAllText((Join-Path $dir 'go.sum'), '')
    foreach ($tool in @('build-release.ps1','prepare-geodata.ps1','prepare-release-components.ps1','prepare-release-notices.ps1')) {
        [IO.File]::WriteAllText((Join-Path $dir ('tools/' + $tool)), '# synthetic offline fixture' + [Environment]::NewLine)
    }
    Copy-Item -LiteralPath $script -Destination (Join-Path $dir 'tools/build-ci-release.ps1')
    & $git -C $dir init -q
    & $git -C $dir config core.autocrlf false
    & $git -C $dir -c user.name=fixture -c user.email=fixture@example.invalid add -A
    & $git -C $dir -c user.name=fixture -c user.email=fixture@example.invalid commit -q -m fixture
    if ($LASTEXITCODE -ne 0) { throw 'Fixture commit failed.' }
    return $dir
}
function Invoke-CI([string]$Dir, [string[]]$Arguments, [string]$ScriptDir = '') {
    if (-not $ScriptDir) { $ScriptDir = $Dir }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = @(& powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $ScriptDir 'tools/build-ci-release.ps1') -WorkspaceRoot $Dir @Arguments 2>&1 | ForEach-Object { [string]$_ })
        $code = $LASTEXITCODE
    } finally { $ErrorActionPreference = $previous }
    return [pscustomobject]@{ exit = $code; text = ($output -join [Environment]::NewLine) }
}
function Assert-Failure([string]$Dir, [string[]]$Arguments, [string]$Pattern, [string]$Name, [string]$ScriptDir = '') {
    $result = Invoke-CI -Dir $Dir -Arguments $Arguments -ScriptDir $ScriptDir
    Assert-Case ($result.exit -ne 0 -and $result.text -match $Pattern) $Name
    if ($result.text -match '(?i)token|password|secret|[A-Z]:\\Users\\') { $script:failed++; Write-Host ('FAIL ' + $Name + ' leaked sensitive diagnostics') }
    return $result
}
try {
    $good = New-Fixture 'clean'
    $head = (& $git -C $good rev-parse HEAD).Trim()
    $result = Invoke-CI $good @('-PassThru')
    Assert-Case ($result.exit -eq 0 -and $result.text -match 'Verified clean release workspace') 'Clean dedicated checkout passes offline preflight'
    Assert-Case (-not (Test-Path -LiteralPath (Join-Path $good '.cache/package'))) 'Offline preflight does not create package output'
    Assert-Case (-not (Test-Path -LiteralPath (Join-Path $good 'runtime'))) 'Offline preflight does not prepare runtime components'
    $plan = Invoke-CI $good @('-Tag','v0.6.18')
    Assert-Case ($plan.exit -eq 0 -and $plan.text -match '"tag":\s*"v0\.6\.18"') 'Default tag derives from source AppVersion'
    Assert-Case ($plan.text -match ('"sourceCommit":\s*"' + $head + '"')) 'Plan records the exact checked-out commit'

    Assert-Failure $good @('-Tag','v9.9.9') 'must exactly match the source version' 'Mismatched tag is refused rather than relabelled'
    Assert-Failure $good @('-SourceCommit',('0' * 40)) 'does not match the selected revision' 'SourceCommit fixed to another revision is refused'
    Assert-Failure $good @('-SourceCommit','not-a-commit') 'full lowercase commit identity' 'Malformed SourceCommit is refused'

    $dirty = New-Fixture 'dirty'
    [IO.File]::AppendAllText((Join-Path $dirty 'cmd/mihomolite/types.go'), '// local edit' + [Environment]::NewLine)
    Assert-Failure $dirty @() 'Tracked source changes' 'Dirty tracked source is refused'

    $withData = New-Fixture 'with-data'
    New-Item -ItemType Directory -Path (Join-Path $withData 'data') -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $withData 'data/private.yaml'), 'synthetic')
    Assert-Failure $withData @() 'clean checkout, not an existing installation: data' 'Existing runtime data directory is refused'

    $withLauncher = New-Fixture 'with-launcher'
    [IO.File]::WriteAllText((Join-Path $withLauncher 'launcher.json'), '{}')
    Assert-Failure $withLauncher @() 'launcher\.json' 'Existing launcher settings are refused'

    $withRelease = New-Fixture 'with-release'
    New-Item -ItemType Directory -Path (Join-Path $withRelease 'release') -Force | Out-Null
    Assert-Failure $withRelease @() 'clean checkout, not an existing installation: release' 'Existing release directory is refused'

    $withNotices = New-Fixture 'with-notices'
    [IO.File]::WriteAllText((Join-Path $withNotices 'THIRD_PARTY_NOTICES.md'), 'generated elsewhere')
    Assert-Failure $withNotices @() 'THIRD_PARTY_NOTICES' 'Pre-generated notice files cannot be smuggled into the release workspace'

    $noGit = Join-Path $run 'no-git'
    Copy-Item -LiteralPath $good -Destination $noGit -Recurse
    Remove-Item -LiteralPath (Join-Path $noGit '.git') -Recurse -Force
    Assert-Failure $noGit @() 'must build its own tracked checkout' 'Workspace without its own Git repository is refused'

    $parentRepo = New-Fixture 'parent-repo'
    $child = Join-Path $parentRepo 'child'
    New-Item -ItemType Directory -Path $child -Force | Out-Null
    foreach ($relative in @('cmd','tools','go.mod','go.sum')) { Copy-Item -LiteralPath (Join-Path $good $relative) -Destination $child -Recurse }
    Assert-Failure -Dir $child -Arguments @() -Pattern 'must build its own tracked checkout' -Name 'Workspace below a parent repository is refused' -ScriptDir $child

    $missingTool = New-Fixture 'missing-tool'
    Remove-Item -LiteralPath (Join-Path $missingTool 'tools/prepare-release-notices.ps1') -Force
    & $git -C $missingTool add -A
    & $git -C $missingTool -c user.name=fixture -c user.email=fixture@example.invalid commit -q -m remove
    Assert-Failure $missingTool @() 'Required CI release file is missing' 'Missing release preparation entry point is refused'

    $missingVersion = New-Fixture 'missing-version'
    [IO.File]::WriteAllText((Join-Path $missingVersion 'cmd/mihomolite/types.go'), 'package main' + [Environment]::NewLine)
    & $git -C $missingVersion add -A
    & $git -C $missingVersion -c user.name=fixture -c user.email=fixture@example.invalid commit -q -m version
    Assert-Failure $missingVersion @() 'source version marker is invalid' 'Missing AppVersion marker is refused'

    $noRefreshResult = Invoke-CI $good @('-Tag','v0.6.18')
    Assert-Case ($noRefreshResult.text -notmatch '(?i)https?://|Invoke-WebRequest|Invoke-RestMethod') 'Offline preflight exposes no network call'
    $status = @(& $git -C $good status --porcelain)
    Assert-Case ($status.Count -eq 0) 'Preflight leaves the checkout untouched'
    # Real Refresh orchestration with synthetic transport/compiler results; no upstream code runs.
    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $tokens=$null; $errors=$null
    $fixtureAst=[Management.Automation.Language.Parser]::ParseFile((Join-Path $repoRoot 'tests/github-release.test.ps1'),[ref]$tokens,[ref]$errors)
    if($errors.Count){throw 'Publisher fixture parser errors'}
    foreach($name in @('Get-HashBytes','Get-HashFile','New-TempZip','New-Fixture')){
        $definition=$fixtureAst.Find({param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq $name},$true)
        if(-not $definition){throw 'Missing isolated artifact fixture helper'}
        $text=$definition.Extent.Text
        if($name -eq 'New-Fixture'){$text=$text.Replace('function New-Fixture(','function New-PublisherFixture(')}
        . ([scriptblock]::Create($text))
    }
    foreach($hostName in @('powershell.exe','pwsh.exe')) {
        $engine=Get-Command $hostName -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
        if(-not $engine){continue}
        $complete=New-Fixture ('complete-' + $hostName)
        [IO.File]::WriteAllText((Join-Path $complete '.gitignore'),'.cache/' + [Environment]::NewLine)
        [IO.File]::WriteAllText((Join-Path $complete 'go.sum'),'example.com/public v1.0.0 h1:' + ('A'*43) + '=' + [Environment]::NewLine)
        $source=New-PublisherFixture ('artifact-seed-' + $hostName) $null
        $seed=Join-Path $complete '.cache/integration/seed'
        New-Item -ItemType Directory -Path $seed -Force | Out-Null
        Copy-Item -Path (Join-Path $source.dir '*') -Destination $seed
        $package=[IO.File]::ReadAllText((Join-Path $seed 'current.json')) | ConvertFrom-Json
        foreach($archive in $package.sourceArchives){
            $name='Source-'+$archive.component+'-'+$archive.commit.Substring(0,12)+'.tar.gz'
            $relative='.cache/downloads/release-notices-integration/'+$archive.component+'-source-'+$archive.commit+'.tar.gz'
            $target=Join-Path $complete $relative
            New-Item -ItemType Directory -Path (Split-Path -Parent $target) -Force | Out-Null
            Copy-Item -LiteralPath (Join-Path $seed $name) -Destination $target
            $archive.path=$relative
        }
        [IO.File]::WriteAllText((Join-Path $seed 'current.json'),($package | ConvertTo-Json -Depth 20))
        [IO.File]::WriteAllText((Join-Path $complete '.cache/downloads/release-notices-integration/notices.json'),([ordered]@{status='verified-ready';redistributionReady=$true;sourceArchives=@($package.sourceArchives)} | ConvertTo-Json -Depth 20))
        [IO.File]::WriteAllText((Join-Path $complete 'tools/prepare-geodata.ps1'),'param([string]$WorkspaceRoot,[switch]$Refresh);[pscustomobject]@{root=".cache/downloads/release-geodata-integration"}')
        [IO.File]::WriteAllText((Join-Path $complete 'tools/prepare-release-components.ps1'),'param([string]$WorkspaceRoot,[string]$GeoDataRoot,[switch]$Refresh,[switch]$PassThru);[pscustomobject]@{manifest=".cache/downloads/release-components-integration/components.json"}')
        [IO.File]::WriteAllText((Join-Path $complete 'tools/prepare-release-notices.ps1'),'param([string]$WorkspaceRoot,[string]$ComponentManifest,[switch]$Refresh,[switch]$PassThru);[pscustomobject]@{manifest=".cache/downloads/release-notices-integration/notices.json"}')
        $buildStub=@'
param([switch]$Publish,[string]$GeoDataRoot,[string]$NoticeManifest,[string]$NodePath)
$root=Split-Path -Parent $PSScriptRoot
$seed=Join-Path $root '.cache/integration/seed'
$manifest=Get-Content -LiteralPath (Join-Path $seed 'current.json') -Raw | ConvertFrom-Json
$current=[ordered]@{version=$manifest.version;binary=$manifest.binary;tests=@{summary=@{go=@{topLevelPassed=2;subtestsPassed=3;topLevelSkipped=1;skipped=@('SyntheticNativeSkip')};node=@(@{passed=4})}}}
[IO.File]::WriteAllText((Join-Path $root 'CURRENT.json'),($current | ConvertTo-Json -Depth 12))
New-Item -ItemType Directory -Path (Join-Path $root 'release') -Force | Out-Null
foreach($name in @($manifest.artifact.path,'current.json','SHA256SUMS.txt')){Copy-Item -LiteralPath (Join-Path $seed $name) -Destination (Join-Path $root ('release/'+$name))}
[pscustomobject]@{status='passed';package=@{synthetic=$true}}
'@
        [IO.File]::WriteAllText((Join-Path $complete 'tools/build-release.ps1'),$buildStub)
        $nl=[char]13+[string][char]10
        [IO.File]::WriteAllText((Join-Path $complete '.cache/integration/fake-go.cmd'),('@echo off'+$nl+'echo %*>> "%~dp0go-calls.txt"'+$nl+'exit /b 0'+$nl))
        $driver=@'
param([string]$Root,[string]$OutputFile)
$ErrorActionPreference='Stop'
$env:GITHUB_OUTPUT=$OutputFile
$global:CITestFakeGo=Join-Path $Root '.cache/integration/fake-go.cmd'
function Get-Command {
    param([string]$Name,[string]$CommandType)
    if($Name -eq 'go'){return [pscustomobject]@{Source=$global:CITestFakeGo}}
    Microsoft.PowerShell.Core\Get-Command @PSBoundParameters
}
& (Join-Path $Root 'tools/build-ci-release.ps1') -WorkspaceRoot $Root -Tag v0.6.18 -Refresh -PassThru
'@
        $driverFile=Join-Path $complete '.cache/integration/driver.ps1'
        [IO.File]::WriteAllText($driverFile,$driver)
        & $git -C $complete add -A
        & $git -C $complete -c user.name=fixture -c user.email=fixture@example.invalid commit -q -m 'synthetic complete orchestration'
        if($LASTEXITCODE -ne 0){throw 'Complete fixture commit failed'}
        $outputFile=Join-Path $complete '.cache/integration/actions-output.txt'
        $oldPreference=$ErrorActionPreference;$ErrorActionPreference='Continue'
        try{$log=@(& $engine.Source -NoProfile -ExecutionPolicy Bypass -File $driverFile -Root $complete -OutputFile $outputFile 2>&1 | ForEach-Object{[string]$_});$code=$LASTEXITCODE}
        finally{$ErrorActionPreference=$oldPreference}
        [IO.File]::WriteAllText((Join-Path $complete '.cache/integration/driver.log'),($log -join [Environment]::NewLine))
        Assert-Case ($code -eq 0) ($hostName+' complete synthetic orchestration succeeds')
        if($code -ne 0){$log | Select-Object -Last 12 | Write-Host;continue}
        $outputLines=@([IO.File]::ReadAllLines($outputFile))
        Assert-Case ($outputLines.Count -eq 3 -and $outputLines[0] -ceq 'tag=v0.6.18' -and $outputLines[1] -cmatch '^source_commit=[0-9a-f]{40}$' -and $outputLines[2].StartsWith('artifact_dir=')) ($hostName+' Actions outputs are three independent canonical lines')
        $artifactDir=$outputLines[2].Substring('artifact_dir='.Length)
        $report=[IO.File]::ReadAllText((Join-Path $artifactDir 'release-report.json')) | ConvertFrom-Json
        Assert-Case ($report.assets -is [Array] -and $report.assets.Count -eq 8 -and @($report.assets | Where-Object{$_ -is [Array]}).Count -eq 0) ($hostName+' Release asset JSON is flat and complete')
        $notes=@([IO.File]::ReadAllLines((Join-Path $artifactDir 'RELEASE_NOTES.md')))
        Assert-Case ($notes[0] -ceq '# Mihomo Lite v0.6.18' -and $notes.Count -gt 10 -and @($notes | Where-Object{$_ -match '^Source commit: [0-9a-f]{40}$'}).Count -eq 1) ($hostName+' Release notes preserve headings and line breaks')
        Assert-Case (@($notes | Where-Object {$_ -ceq '- SyntheticNativeSkip'}).Count -eq 1) ($hostName+' Default regression skip names are recorded without inventing native acceptance')
        $calls=@([IO.File]::ReadAllLines((Join-Path $complete '.cache/integration/go-calls.txt')))
        Assert-Case ($calls.Count -eq 2 -and $calls[0] -ceq 'mod download example.com/public@v1.0.0' -and $calls[1] -ceq 'mod verify') ($hostName+' All pinned module downloads and cache verification are explicit')
        $oldPreference=$ErrorActionPreference;$ErrorActionPreference='Continue'
        try{$check=@(& $engine.Source -NoProfile -ExecutionPolicy Bypass -File (Join-Path $repoRoot 'tools/publish-github-release.ps1') -ArtifactDirectory $artifactDir -ExpectedCommit $report.sourceCommit -ExpectedTag $report.tag 2>&1 | ForEach-Object{[string]$_});$publishCode=$LASTEXITCODE}
        finally{$ErrorActionPreference=$oldPreference}
        Assert-Case ($publishCode -eq 0 -and ($check -join ' ') -match 'verified offline') ($hostName+' Real publisher accepts wrapper-generated synthetic assets offline')
        if($publishCode -ne 0){$check | Write-Host}
    }
} finally {
    $payload = [ordered]@{
        status = 'completed'
        passed = $passed
        failed = $failed
        cases = $results
        scope = 'offline preflight and complete real orchestration with synthetic download/compiler/package results; real asset copying/reports/Actions outputs/offline publisher; no network or GitHub writes'
        at = [datetime]::UtcNow.ToString('o')
    }
    [IO.File]::WriteAllText((Join-Path $run 'results.json'), ($payload | ConvertTo-Json -Depth 8), (New-Object Text.UTF8Encoding($false)))
    Write-Host ('RESULT ' + (Join-Path $run 'results.json'))
}
if ($failed -gt 0) { throw ($failed.ToString() + ' offline preflight assertions failed.') }
Write-Host ('Completed ' + $passed + ' isolated offline preflight assertions.')
