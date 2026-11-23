#requires -Version 5.1
<#
Offline preflight tests for tools/build-ci-release.ps1.
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
} finally {
    $payload = [ordered]@{
        status = 'completed'
        passed = $passed
        failed = $failed
        cases = $results
        scope = 'offline isolated preflight only; Refresh, downloads, build, packaging and GitHub are not exercised'
        at = [datetime]::UtcNow.ToString('o')
    }
    [IO.File]::WriteAllText((Join-Path $run 'results.json'), ($payload | ConvertTo-Json -Depth 8), (New-Object Text.UTF8Encoding($false)))
    Write-Host ('RESULT ' + (Join-Path $run 'results.json'))
}
if ($failed -gt 0) { throw ($failed.ToString() + ' offline preflight assertions failed.') }
Write-Host ('Completed ' + $passed + ' isolated offline preflight assertions.')
