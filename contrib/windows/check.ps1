# Copyright 2026 Trevin Tindall (T-Crypt)
# SPDX-License-Identifier: Apache-2.0
# check.ps1: the operator's checklist for a Windows node on a real machine
# with an NVIDIA card. It prints what the drain-report issue form collects:
# the berth version, nvidia-smi, the card, and the VRAM each process holds
# in berth's reading next to the driver's (the same counter Task Manager
# reads), the event log, and, when you give it a CUDA app, one run of it
# under `berth run`. It writes all of it to a report file to attach to the
# issue. Plain Windows PowerShell 5.1, no external modules.
#
# Run:  powershell -ExecutionPolicy Bypass -File check.ps1
# With a CUDA app:  powershell -ExecutionPolicy Bypass -File check.ps1 -App "C:\cuda\app.exe --flag value"
#
# The counter names are the English ones; on a non-English Windows they are
# translated, and the driver's reading is then reported as unavailable.

param(
    [string]$Berth = "berth",
    [string]$App = "",
    [string]$Report = "berth-windows-check.txt"
)

$lines = [System.Collections.Generic.List[string]]::new()
function Note($text) {
    $lines.Add([string]$text)
    Write-Host $text
}

Note "berth windows check - $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')"
Note "machine: $env:COMPUTERNAME, user: $env:USERNAME, powershell: $PSVersionTable.PSVersion.ToString()"

# 1. The berth version and the node it talks to.
Note ""
Note "== berth version"
& $Berth version 2>&1 | ForEach-Object { Note $_ }

# 2. The node's services, when the command exists in this build.
Note ""
Note "== berth service status"
& $Berth service status 2>&1 | ForEach-Object {
    if ($null -ne $_ -and "$_" -ne "") { Note $_ }
}

# 3. The driver's own view of the card.
Note ""
Note "== nvidia-smi"
try {
    & nvidia-smi 2>&1 | ForEach-Object { Note $_ }
}
catch {
    Note "nvidia-smi: $($_.Exception.Message)"
}

# 4. The card and its processes: what berth measures next to what the
# driver measures. Under WDDM nvidia-smi cannot see the per-process memory,
# so the driver's column is the check of the node's reading.
Note ""
Note "== card and per-process VRAM"
$stat = $null
try {
    $raw = (& $Berth status --format json 2>$null) | Out-String
    $stat = $raw | ConvertFrom-Json
}
catch {
    Note "berth status: $($_.Exception.Message)"
}

$driverMiB = @{}
try {
    $c = Get-Counter -Counter '\GPU Process Memory(*)\Dedicated Usage' -SampleInterval 1 -MaxSamples 1
    foreach ($s in $c.CounterSamples) {
        if ($s.InstanceName -match 'pid_(\d+)') {
            $driverMiB[[int]$Matches[1]] = [math]::Round($s.CookedValue / 1MB)
        }
    }
}
catch {
    Note "driver per-process counters unavailable: $($_.Exception.Message)"
}

if ($null -eq $stat) {
    Note "berth status returned nothing; the rest of this section is skipped"
}
else {
    foreach ($g in $stat.gpus) {
        $mark = " "
        if ($g.scheduled) { $mark = "*" }
        Note ("card {0}{1} {2}: total {3} MiB, used {4} MiB" -f $g.index, $mark, $g.name, [math]::Round($g.total / 1MB), [math]::Round($g.used / 1MB))
    }
    Note ""
    Note "PID      NAME                     BERTH(MiB)  DRIVER(MiB)"
    $berthPids = @{}
    $rows = @()
    if ($null -ne $stat.holders) {
        foreach ($h in $stat.holders) {
            $berthPids[$h.pid] = $true
            $d = "-"
            if ($driverMiB.ContainsKey($h.pid)) { $d = "$($driverMiB[$h.pid])" }
            $rows += ,@($h.pid, "holder " + $h.name, $h.used / 1MB, $d)
        }
    }
    if ($null -ne $stat.squatters) {
        foreach ($s in $stat.squatters) {
            $berthPids[$s.pid] = $true
            $d = "-"
            if ($driverMiB.ContainsKey($s.pid)) { $d = "$($driverMiB[$s.pid])" }
            $rows += ,@($s.pid, "stowaway " + $s.name, $s.used / 1MB, $d)
        }
    }
    foreach ($r in $rows) {
        Note ("{0,-8}   {1,-24} {2,9}  {3,12}" -f $r[0], $r[1], [math]::Round($r[2]), $r[3])
    }
    if ($rows.Count -eq 0) {
        Note "(no process on the card that berth knows)"
    }
    if ($driverMiB.Count -gt 0) {
        $note = @()
        foreach ($p in $driverMiB.Keys) {
            if (-not $berthPids.ContainsKey($p) -and $driverMiB[$p] -gt 0) {
                $note += "pid $p ($([string]$driverMiB[$p]) MiB)"
            }
        }
        if ($note.Count -gt 0) {
            Note ("driver sees, berth does not: " + ($note -join ", "))
        }
    }
    if ($null -ne $stat.leases) {
        foreach ($l in $stat.leases) {
            Note ("lease {0} {1} {2}: {3} MiB on the card" -f $l.id, $l.engine, $l.state, [math]::Round($l.used / 1MB))
        }
    }
}

# 5. One CUDA app under a lease, when given: the run's own output and the
# card before and after it.
if ($App -ne "") {
    Note ""
    Note "== berth run --as hog -- $App"
    $appArgs = $App -split ' '
    & $Berth run --as hog -- @appArgs 2>&1 | ForEach-Object {
        if ($null -ne $_ -and "$_" -ne "") { Note $_ }
    }
    Note "berth run exit code: $LASTEXITCODE"
}

# 6. The event log around the run, the form's last field. Capture it before
# you restart anything.
Note ""
Note "== berth events --json (last 200 lines)"
$events = & $Berth events --json 2>$null
if ($null -ne $events) {
    $events | Select-Object -Last 200 | ForEach-Object {
        if ($null -ne $_ -and "$_" -ne "") { Note $_ }
    }
}
else {
    Note "berth events returned nothing"
}

[System.IO.File]::WriteAllLines($Report, $lines, (New-Object System.Text.UTF8Encoding($false)))
Write-Host ""
Write-Host "report written to $Report - attach it to the drain-report issue"
