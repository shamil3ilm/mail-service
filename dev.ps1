# dev.ps1 — auto-restart mailservice when .env or *.go changes
#
# Usage:
#   .\dev.ps1
#
# Watches .env plus every .go file under ./cmd and ./internal. On any change,
# gracefully stops the running mailservice, rebuilds, and restarts. Ctrl+C
# to exit — cleans up the child process on the way out.

$ErrorActionPreference = 'Continue'

# ── config ───────────────────────────────────────────────────────────
$rootDir      = $PSScriptRoot
$goEntryPoint = './cmd/mailservice'
# Rebuild-throttle window: coalesce multiple file changes fired in quick
# succession (editors save temp files then rename → 2-3 events per save).
$debounceMs   = 500

# ── state ────────────────────────────────────────────────────────────
$script:proc         = $null
$script:lastRestart  = [datetime]::MinValue
$script:needsRestart = $false

# ── helpers ──────────────────────────────────────────────────────────
function Stop-App {
    if ($script:proc -and -not $script:proc.HasExited) {
        Write-Host "[dev] stopping mailservice (PID $($script:proc.Id))..." -ForegroundColor Yellow
        try {
            # Stop the whole tree — `go run` spawns a child compile+exec.
            Stop-Process -Id $script:proc.Id -Force -ErrorAction SilentlyContinue
            # Also nuke any leftover mailservice.exe from a previous crash.
            Get-Process -Name mailservice -ErrorAction SilentlyContinue | Stop-Process -Force
        } catch {}
        $script:proc.WaitForExit(3000) | Out-Null
    }
    $script:proc = $null
}

function Start-App {
    Write-Host "[dev] starting mailservice..." -ForegroundColor Green
    $script:proc = Start-Process -FilePath 'go' `
        -ArgumentList @('run', $goEntryPoint) `
        -WorkingDirectory $rootDir `
        -NoNewWindow -PassThru
    $script:lastRestart = Get-Date
}

function Trigger-Restart {
    $script:needsRestart = $true
}

# ── watch every relevant file ────────────────────────────────────────
$watchers = @()

function New-Watcher($path, $filter, $recurse) {
    $w = New-Object System.IO.FileSystemWatcher $path, $filter
    $w.IncludeSubdirectories = $recurse
    $w.NotifyFilter = [System.IO.NotifyFilters]::LastWrite -bor `
                      [System.IO.NotifyFilters]::FileName  -bor `
                      [System.IO.NotifyFilters]::Size
    $w.EnableRaisingEvents = $true
    foreach ($event in 'Changed','Created','Renamed') {
        Register-ObjectEvent $w $event -Action {
            $global:pendingRestart = $true
        } | Out-Null
    }
    return $w
}

$watchers += New-Watcher $rootDir '.env' $false
if (Test-Path (Join-Path $rootDir 'cmd')) {
    $watchers += New-Watcher (Join-Path $rootDir 'cmd') '*.go' $true
}
if (Test-Path (Join-Path $rootDir 'internal')) {
    $watchers += New-Watcher (Join-Path $rootDir 'internal') '*.go' $true
}
if (Test-Path (Join-Path $rootDir 'web/dist')) {
    $watchers += New-Watcher (Join-Path $rootDir 'web/dist') '*' $true
}

Write-Host "[dev] watching .env + Go sources + web/dist. Ctrl+C to exit." -ForegroundColor Cyan
Start-App

try {
    while ($true) {
        Start-Sleep -Milliseconds 100
        if ($global:pendingRestart) {
            # Debounce: wait for the storm to end before restarting.
            Start-Sleep -Milliseconds $debounceMs
            $global:pendingRestart = $false
            Write-Host "[dev] change detected — restarting" -ForegroundColor Cyan
            Stop-App
            Start-Sleep -Milliseconds 200
            Start-App
        }
    }
}
finally {
    Write-Host "[dev] shutting down..." -ForegroundColor Yellow
    foreach ($w in $watchers) {
        try { $w.EnableRaisingEvents = $false; $w.Dispose() } catch {}
    }
    Get-EventSubscriber | Unregister-Event -ErrorAction SilentlyContinue
    Stop-App
}
