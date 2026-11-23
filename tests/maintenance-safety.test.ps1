#requires -Version 5.1
[CmdletBinding()]
param([string]$WorkspaceRoot = '')
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($WorkspaceRoot)) { $WorkspaceRoot = Split-Path -Parent $PSScriptRoot }
$root = (Resolve-Path -LiteralPath $WorkspaceRoot).Path
$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
    $script:passed++
    Write-Output ('PASS: ' + $Message)
}
function Assert-Throws([scriptblock]$Action, [string]$Pattern) {
    $caught = $null
    try { & $Action | Out-Null } catch { $caught = $_.Exception.Message }
    Assert-True ($null -ne $caught -and $caught -match $Pattern) ('Expected guard: ' + $Pattern)
}
$binaryPath = Join-Path $root 'MihomoLite.exe'
$before = (Get-FileHash -LiteralPath $binaryPath -Algorithm SHA256).Hash
$setupPath = Join-Path $root 'setup.ps1'
$parserInputs = @(
    'build.ps1','tools\package.ps1','tools\check-workspace.ps1','tools\organize-workspace.ps1',
    'tools\build-release.ps1','tools\build-metrics.ps1','tools\release-geodata.ps1','tools\prepare-geodata.ps1',
    'tests\build-pipeline.test.ps1'
)
if (Test-Path -LiteralPath $setupPath -PathType Leaf) { $parserInputs += 'setup.ps1' }
foreach ($relative in $parserInputs) {
    $tokens=$null; $errors=$null
    $null = [Management.Automation.Language.Parser]::ParseFile((Join-Path $root $relative),[ref]$tokens,[ref]$errors)
    Assert-True ($errors.Count -eq 0) ('Parser accepts ' + $relative)
}
$oldProbe = [Environment]::GetEnvironmentVariable('MIHOMOLITE_TEST_SYSTEM_PROXY','Process')
$oldNodeOptions = [Environment]::GetEnvironmentVariable('NODE_OPTIONS','Process')
try {
    # Never execute a native build/test: the forbidden flag combination exits first.
    [Environment]::SetEnvironmentVariable('MIHOMOLITE_TEST_SYSTEM_PROXY','do-not-run','Process')
    [Environment]::SetEnvironmentVariable('NODE_OPTIONS','do-not-run','Process')
    Assert-Throws { & (Join-Path $root 'build.ps1') -SkipTests -Promote } 'Promote.*forbidden.*SkipTests'
    Assert-True ([Environment]::GetEnvironmentVariable('MIHOMOLITE_TEST_SYSTEM_PROXY','Process') -eq 'do-not-run') 'Build failure restores inherited test environment'
    Assert-True ([Environment]::GetEnvironmentVariable('NODE_OPTIONS','Process') -eq 'do-not-run') 'Build failure restores inherited Node environment'
} finally {
    [Environment]::SetEnvironmentVariable('MIHOMOLITE_TEST_SYSTEM_PROXY',$oldProbe,'Process')
    [Environment]::SetEnvironmentVariable('NODE_OPTIONS',$oldNodeOptions,'Process')
}
# Local setup is intentionally absent in public source exports. Keep its safety regression when available.
if (Test-Path -LiteralPath $setupPath -PathType Leaf) {
    # Deterministically mock a running current launcher; no downloads/update can begin.
    $global:MaintenanceMockExecutable = $binaryPath
    function Get-CimInstance { param($ClassName) [pscustomobject]@{ExecutablePath=$global:MaintenanceMockExecutable;ProcessId=-1;Name='MihomoLite.exe'} }
    function Invoke-RestMethod { throw 'Unexpected network operation in safety test' }
    function Invoke-WebRequest { throw 'Unexpected network operation in safety test' }
    try {
        $errorMessage = $null
        try { & $setupPath | Out-Null } catch { $errorMessage = $_.Exception.Message }
        Assert-True ($null -ne $errorMessage -and $errorMessage -match '\u4e0d\u4f1a\u5f3a\u6740') 'Component update rejects a mocked active launcher before downloads'
    } finally {
        Remove-Item -LiteralPath Function:Get-CimInstance,Function:Invoke-RestMethod,Function:Invoke-WebRequest
        Remove-Variable -Name MaintenanceMockExecutable -Scope Global
    }
} else {
    Write-Output 'SKIP: Local setup.ps1 parser/update guard (excluded from public source export)'
}
# Execute only the atomic metadata helper definitions, never the build/package bodies.
$script:Workspace = $root
$fixture = Join-Path $root ('.cache\tests\maintenance-safety\' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $fixture -Force | Out-Null
foreach ($definition in @(
    @{file='build.ps1'; names=@('Get-WorkspacePath','Write-JsonAtomic'); writer='Write-JsonAtomic'; output='build-metadata.json'},
    @{file='tools\package.ps1'; names=@('Get-PackagePath','Write-PackageJson'); writer='Write-PackageJson'; output='package-metadata.json'}
)) {
    $tokens=$null; $errors=$null
    $ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $root $definition.file),[ref]$tokens,[ref]$errors)
    $names = $definition.names
    $functions = @($ast.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -in $names},$true))
    if ($functions.Count -ne $names.Count) { throw 'Cannot isolate metadata helper functions.' }
    foreach ($function in $functions) { Invoke-Expression $function.Extent.Text }
    $output = Join-Path $fixture $definition.output
    & $definition.writer $output ([ordered]@{status='initial'})
    & $definition.writer $output ([ordered]@{status='updated'})
    $metadata = [IO.File]::ReadAllText($output) | ConvertFrom-Json
    Assert-True ($metadata.status -eq 'updated') ('Atomic metadata replacement works: ' + $definition.writer)
}
$after = (Get-FileHash -LiteralPath $binaryPath -Algorithm SHA256).Hash
Assert-True ($before -eq $after) 'Safety checks do not replace the current launcher'
Write-Output ('Maintenance safety assertions passed: ' + $passed)
