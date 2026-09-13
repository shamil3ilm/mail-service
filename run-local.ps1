# run-local.ps1 — Path 1 wiring for mail-service + privatedns.
#
# Runs both binaries side-by-side on this Windows machine, wired together:
#   * privatedns serves an authoritative `.mail.local` zone
#   * mail-service auto-publishes SPF/DKIM/DMARC/MX records into it when a
#     domain is created via the dashboard
#   * All ports are non-privileged so no Administrator required
#
# Ports:
#   privatedns DNS   127.0.0.1:5353
#   privatedns API   127.0.0.1:8080
#   mail-service     127.0.0.1:8035 (dashboard), 2525 (SMTP), 587 (submission)
#
# Usage:
#   .\run-local.ps1
#
# Ctrl+C stops both binaries cleanly.

$ErrorActionPreference = 'Continue'

# ── config (edit these paths if your layout differs) ──────────────
$PrivateDnsDir   = 'C:\domain\native'
$PrivateDnsExe   = Join-Path $PrivateDnsDir 'privatedns.exe'
$PrivateDnsData  = Join-Path $PrivateDnsDir 'data'
$MailServiceDir  = 'C:\mail-service'

# Where to write a machine-readable summary once both services are up.
$SummaryFile = Join-Path $MailServiceDir 'run-local.status.json'

# ── sanity ────────────────────────────────────────────────────────
if (-not (Test-Path $PrivateDnsExe)) {
    Write-Host "[path1] privatedns.exe not found at $PrivateDnsExe" -ForegroundColor Red
    Write-Host "        build it: cd $PrivateDnsDir; go build -o privatedns.exe ." -ForegroundColor Yellow
    exit 1
}

# ── privatedns env (child inherits) ───────────────────────────────
$env:PRIVATEDNS_DATA_DIR       = $PrivateDnsData
$env:PRIVATEDNS_PRIVATE_TLD    = 'mail.local'
$env:PRIVATEDNS_DNS_ADDR       = '127.0.0.1:5353'
$env:PRIVATEDNS_API_ADDR       = '127.0.0.1:8080'
$env:PRIVATEDNS_LOG_LEVEL      = 'info'
# Path 1 never talks to the public internet — set upstreams to something
# unreachable so accidental external queries fail loud instead of leaking.
$env:PRIVATEDNS_UPSTREAMS      = '127.0.0.1:53'

# ── mail-service env ──────────────────────────────────────────────
$env:MAIL_MODE                 = 'local'
$env:MAIL_LISTEN_ADDR          = '127.0.0.1'
$env:MAIL_HTTP_PORT            = '8035'
$env:MAIL_ADMIN_PORT           = '8036'
$env:MAIL_SMTP_PORT            = '2525'
$env:MAIL_SUBMISSION_PORT      = '587'
$env:MAIL_LOG_FORMAT           = 'text'
$env:MAIL_AUTO_VERIFY_DOMAINS  = '.mail.local,.test,.local,.localhost'
$env:MAIL_DNS_PUBLISHER        = 'privatedns'
$env:MAIL_DNS_PUBLISHER_URL    = 'http://127.0.0.1:8080'
$env:MAIL_DNS_PUBLISHER_USER   = 'admin@local'
# MAIL_DNS_PUBLISHER_TOKEN — set below after we capture privatedns's
# bootstrap admin password from its first-run output.

# ── process tracking ──────────────────────────────────────────────
$script:pdns = $null
$script:mail = $null

function Stop-All {
    foreach ($p in @($script:mail, $script:pdns)) {
        if ($p -and -not $p.HasExited) {
            Write-Host "[path1] stopping PID $($p.Id)..." -ForegroundColor Yellow
            try { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue } catch {}
        }
    }
    # Nuke any orphaned child compilers from `go run`.
    Get-Process -Name mailservice -ErrorAction SilentlyContinue | Stop-Process -Force
}

trap { Stop-All; break }

# ── 1. start privatedns ───────────────────────────────────────────
Write-Host "[path1] starting privatedns (DNS :5353, API :8080)..." -ForegroundColor Green
$pdnsLog = Join-Path $MailServiceDir 'run-local.privatedns.log'
$script:pdns = Start-Process -FilePath $PrivateDnsExe `
    -WorkingDirectory $PrivateDnsDir `
    -PassThru -NoNewWindow `
    -RedirectStandardOutput $pdnsLog `
    -RedirectStandardError  "$pdnsLog.err"

# ── 2. wait for the API to be reachable ──────────────────────────
Write-Host "[path1] waiting for privatedns API..." -ForegroundColor Cyan
$ready = $false
for ($i = 0; $i -lt 60; $i++) {
    Start-Sleep -Milliseconds 300
    try {
        $null = Invoke-WebRequest -UseBasicParsing `
            -Uri 'http://127.0.0.1:8080/api/v1/auth/me' `
            -TimeoutSec 1 -ErrorAction Stop
        $ready = $true; break
    } catch {
        # 401 with no auth is what we want — proves the API is up.
        if ($_.Exception.Response.StatusCode.Value__ -eq 401) { $ready = $true; break }
    }
}
if (-not $ready) {
    Write-Host "[path1] privatedns API didn't come up. Check $pdnsLog" -ForegroundColor Red
    Stop-All; exit 1
}

# ── 3. capture the first-run admin password from the log ──────────
# privatedns prints "Admin password: <pw>" once on bootstrap. On subsequent
# starts the account persists but the password isn't reprinted — user
# should keep it saved. For repeat runs, set PRIVATEDNS_ADMIN_PW manually.
$adminPw = $env:PRIVATEDNS_ADMIN_PW
if (-not $adminPw) {
    if (Test-Path $pdnsLog) {
        $content = Get-Content $pdnsLog -Raw
        # Bootstrap output shape (bootstrap.go:47-49):
        #   privatedns bootstrap admin credentials
        #     email: admin@local
        #     password: <value>
        $m = [regex]::Match($content, '(?im)^\s*password:\s*(\S+)')
        if ($m.Success) { $adminPw = $m.Groups[1].Value }
    }
}
if (-not $adminPw) {
    Write-Host "[path1] could not find admin password in $pdnsLog." -ForegroundColor Yellow
    Write-Host "        Not first run? Set \`$env:PRIVATEDNS_ADMIN_PW=<pw> and re-run." -ForegroundColor Yellow
    Write-Host "        Or nuke $PrivateDnsData and re-run to get a fresh password." -ForegroundColor Yellow
    Stop-All; exit 1
}

# ── 4. log in, mint an API key for the mail-service bridge ────────
Write-Host "[path1] minting bridge API key from privatedns..." -ForegroundColor Cyan
$loginBody = @{ email = 'admin@local'; password = $adminPw } | ConvertTo-Json
try {
    $login = Invoke-RestMethod -Method Post `
        -Uri 'http://127.0.0.1:8080/api/v1/auth/login' `
        -ContentType 'application/json' `
        -Body $loginBody
} catch {
    Write-Host "[path1] login failed: $($_.Exception.Message)" -ForegroundColor Red
    Stop-All; exit 1
}

$keyResp = Invoke-RestMethod -Method Post `
    -Uri 'http://127.0.0.1:8080/api/v1/keys' `
    -Headers @{ Authorization = "Bearer $($login.token)" } `
    -ContentType 'application/json' `
    -Body (@{ name = 'mail-service-bridge' } | ConvertTo-Json)

$env:MAIL_DNS_PUBLISHER_TOKEN = $keyResp.token
Write-Host "[path1] bridge key minted (prefix $($keyResp.prefix))" -ForegroundColor Green

# ── 5. start mail-service in the background ───────────────────────
Write-Host "[path1] starting mail-service (dashboard :8035, SMTP :2525)..." -ForegroundColor Green
$mailLog = Join-Path $MailServiceDir 'run-local.mailservice.log'
$script:mail = Start-Process -FilePath 'go' `
    -ArgumentList @('run', './cmd/mailservice') `
    -WorkingDirectory $MailServiceDir `
    -PassThru -NoNewWindow `
    -RedirectStandardOutput $mailLog `
    -RedirectStandardError  "$mailLog.err"

# ── 6. print the summary ──────────────────────────────────────────
Start-Sleep -Seconds 2
$summary = [ordered]@{
    dashboard        = 'http://127.0.0.1:8035'
    admin_dashboard  = 'http://127.0.0.1:8080'
    smtp_dev         = '127.0.0.1:2525'
    smtp_submission  = '127.0.0.1:587'
    dns_authoritative = '127.0.0.1:5353'
    private_tld      = 'mail.local'
    privatedns_admin_password = $adminPw
    bridge_key_prefix = $keyResp.prefix
}
$summary | ConvertTo-Json | Set-Content $SummaryFile

Write-Host ""
Write-Host "============================================================" -ForegroundColor Green
Write-Host " Path 1 up. Both services running." -ForegroundColor Green
Write-Host "============================================================" -ForegroundColor Green
Write-Host " mail dashboard        http://127.0.0.1:8035"
Write-Host " privatedns dashboard  http://127.0.0.1:8080"
Write-Host " mail SMTP (dev)       127.0.0.1:2525    (Laravel: MAIL_HOST/MAIL_PORT)"
Write-Host " mail SMTP submission  127.0.0.1:587     (with API key auth)"
Write-Host " authoritative DNS     127.0.0.1:5353    (dig @127.0.0.1 -p 5353 ...)"
Write-Host " private TLD           .mail.local"
Write-Host ""
Write-Host " privatedns admin      admin@local / $adminPw"
Write-Host " bridge key prefix     $($keyResp.prefix) (full key stashed in env for this shell only)"
Write-Host ""
Write-Host " Try: create a domain 'app.mail.local' in the mail dashboard."
Write-Host " Records should auto-publish to privatedns. Verify with:"
Write-Host "   dig @127.0.0.1 -p 5353 mx app.mail.local +short"
Write-Host "============================================================" -ForegroundColor Green
Write-Host " Ctrl+C stops both. Logs: $pdnsLog, $mailLog"
Write-Host ""

# ── 7. block until Ctrl+C or a child dies ─────────────────────────
try {
    while ($true) {
        if ($script:pdns.HasExited) { Write-Host "[path1] privatedns exited (code $($script:pdns.ExitCode))" -ForegroundColor Red; break }
        if ($script:mail.HasExited) { Write-Host "[path1] mail-service exited (code $($script:mail.ExitCode))" -ForegroundColor Red; break }
        Start-Sleep -Seconds 1
    }
}
finally {
    Stop-All
    Write-Host "[path1] done." -ForegroundColor Yellow
}
