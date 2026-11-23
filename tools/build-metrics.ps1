#requires -Version 5.1
# Shared, side-effect-free timing and regression summary helpers.
function New-BuildMetrics {
    [ordered]@{schemaVersion=1;startedAt=[datetime]::UtcNow.ToString('o');completedAt=$null;durationMs=0;status='running';stages=@()}
}
function Start-BuildStage {
    param($Metrics, [string]$Name)
    $record = [ordered]@{name=$Name;startedAt=[datetime]::UtcNow.ToString('o');completedAt=$null;durationMs=0;status='running'}
    $Metrics.stages += $record
    Write-Host ('Starting: ' + $Name)
    [pscustomobject]@{record=$record;clock=[Diagnostics.Stopwatch]::StartNew()}
}
function Stop-BuildStage {
    param($Stage, [ValidateSet('passed','failed')][string]$Status = 'passed')
    if ($null -eq $Stage -or $Stage.record.status -ne 'running') { return }
    $Stage.clock.Stop()
    $Stage.record.completedAt = [datetime]::UtcNow.ToString('o')
    $Stage.record.durationMs = [math]::Round($Stage.clock.Elapsed.TotalMilliseconds, 3)
    $Stage.record.status = $Status
    Write-Host ('Finished: {0} ({1:N2}s, {2})' -f $Stage.record.name, ($Stage.record.durationMs / 1000), $Status)
}
function Complete-BuildMetrics {
    param($Metrics, $Clock, [ValidateSet('passed','failed')][string]$Status)
    $Clock.Stop()
    $Metrics.completedAt = [datetime]::UtcNow.ToString('o')
    $Metrics.durationMs = [math]::Round($Clock.Elapsed.TotalMilliseconds, 3)
    $Metrics.status = $Status
}
function Get-GoRegressionSummary {
    param([string]$Path)
    $summary = [ordered]@{topLevelPassed=0;subtestsPassed=0;topLevelSkipped=0;failures=0;packagePassed=$false;skipped=@();elapsedSeconds=0;cached=$false}
    foreach ($line in [IO.File]::ReadLines($Path)) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        try { $event = $line | ConvertFrom-Json } catch { throw 'Go regression log is not valid JSON; summary is unavailable.' }
        if ($event.Test) {
            if ($event.Action -eq 'pass') {
                if ($event.Test.Contains('/')) { $summary.subtestsPassed++ } else { $summary.topLevelPassed++ }
            } elseif ($event.Action -eq 'skip' -and -not $event.Test.Contains('/')) {
                $summary.topLevelSkipped++; $summary.skipped += [string]$event.Test
            } elseif ($event.Action -eq 'fail') { $summary.failures++ }
        } elseif ($event.Action -eq 'pass') {
            if (-not ($event.Elapsed -is [int] -or $event.Elapsed -is [long] -or $event.Elapsed -is [double] -or $event.Elapsed -is [decimal]) -or [double]$event.Elapsed -lt 0 -or [double]::IsNaN([double]$event.Elapsed) -or [double]::IsInfinity([double]$event.Elapsed) -or [string]::IsNullOrWhiteSpace([string]$event.Package)) { throw 'Go package pass event has invalid or missing elapsed time/package.' }
            $summary.packagePassed = $true; $summary.elapsedSeconds = $event.Elapsed
        } elseif ($event.Action -eq 'fail') { $summary.packagePassed = $false; $summary.failures++ }
        if ($event.Action -eq 'output' -and -not $event.Test -and $event.Output -match '^\s*ok\s+.+\s+\(cached\)\s*$') { $summary.cached = $true }
    }
    if (-not $summary.packagePassed -or $summary.failures -gt 0) { throw 'Go regression did not report a passing package.' }
    $summary
}
function Get-NodeRegressionSummary {
    param([string]$Path)
    $summary = [ordered]@{passed=0;failed=0;skipped=0;tests=0;cancelled=0;todo=0;durationMs=0}
    $seen = @{}
    foreach ($line in [IO.File]::ReadLines($Path)) {
        if ($line -match '^# (tests|pass|fail|skipped|cancelled|todo|duration_ms) ([0-9]+(?:[.][0-9]+)?)\s*$') {
            $key = @{tests='tests';pass='passed';fail='failed';skipped='skipped';cancelled='cancelled';todo='todo';duration_ms='durationMs'}[$matches[1]]
            if ($seen.ContainsKey($key)) { throw 'Node regression summary has duplicate totals.' }
            $value = [double]::Parse($matches[2], [Globalization.CultureInfo]::InvariantCulture)
            if ($key -ne 'durationMs' -and $value -ne [math]::Floor($value)) { throw 'Node regression counts must be whole numbers.' }
            $summary[$key] = $value
            $seen[$key] = $true
        }
    }
    foreach ($key in $summary.Keys) { if (-not $seen.ContainsKey($key)) { throw 'Node regression summary is incomplete.' } }
    if ($summary.failed -ne 0 -or $summary.cancelled -ne 0 -or $summary.tests -ne ($summary.passed + $summary.failed + $summary.skipped + $summary.cancelled + $summary.todo)) { throw 'Node regression did not report consistent passing totals.' }
    $summary
}
