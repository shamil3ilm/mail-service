# Deploying mail-service to a cloud VPS

This directory has everything you need to run mail-service on a fresh
Linux VPS. Tested on Debian 12 / Ubuntu 22.04+ on Hetzner Cloud CX22,
Netcup VPS 200 G11, and OVH VPS Starter.

## Prerequisites

1. **A VPS that allows outbound port 25.** Skip AWS/GCP/Azure/DO/Linode
   without an approved exception. Hetzner, Netcup, OVH, Contabo work
   out of the box.
2. **PTR record control.** Set in your VPS provider's console — must
   resolve to `mail.<yourdomain>`.
3. **A domain you control DNS for.** ~$10/yr from Porkbun or Cloudflare
   Registrar. Or use a subdomain of one you already own.
4. **Cross-compiled Linux binary.** From Windows:
   ```powershell
   $env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
   go build -o bin/mailservice ./cmd/mailservice
   ```

## Install in one command

Copy the binary and this repo to your VPS, then:

```bash
sudo ./deploy/install.sh
```

The script:

- Creates the `mailservice` system user (no shell, no home)
- Places the binary at `/usr/local/bin/mailservice`
- Creates `/etc/mailservice/env` from a template
- Provisions `/var/lib/mailservice` for the SQLite DB + raw MIME
- Installs the sandboxed `systemd` unit (`NoNewPrivileges`,
  `ProtectSystem=strict`, `MemoryDenyWriteExecute`, filtered syscalls)
- Grants `CAP_NET_BIND_SERVICE` so the unprivileged user can bind
  ports 25/587/443 without full root
- Configures UFW (25/587/443/22)

Re-running is safe — it's idempotent.

## Configure

Edit `/etc/mailservice/env`:

```
MAIL_MODE=cloud
MAIL_CLOUD_HOSTNAME=mail.example.com
MAIL_CLOUD_OUTBOUND_IP=203.0.113.42     # your public IPv4
MAIL_RELAY_PROVIDER=smtp                 # or ses / resend / none
MAIL_RELAY_SMTP_HOST=email-smtp.eu-central-1.amazonaws.com
MAIL_RELAY_SMTP_USER=<your-smtp-creds>
MAIL_RELAY_SMTP_PASS=<your-smtp-creds>
```

## Publish DNS

The dashboard's **Settings → Domains → Add domain** flow generates
every record you need. Publish these at your DNS provider:

| Type  | Name                     | Value                                                              |
|-------|--------------------------|--------------------------------------------------------------------|
| A     | `mail.example.com`       | `203.0.113.42`                                                     |
| MX    | `example.com`            | `10 mail.example.com`                                              |
| TXT   | `example.com`            | `v=spf1 ip4:203.0.113.42 -all`                                     |
| TXT   | `ms1._domainkey.example.com` | `v=DKIM1; k=rsa; p=...` (from dashboard)                        |
| TXT   | `_dmarc.example.com`     | `v=DMARC1; p=quarantine; rua=mailto:dmarc@example.com; adkim=r; aspf=r` |
| PTR   | `203.0.113.42`           | `mail.example.com` (set in VPS provider console)                   |

Then in the dashboard click **Verify DNS**. Every row should turn green.

## Start

```bash
sudo systemctl enable --now mailservice
journalctl -u mailservice -f
```

Look for:

- `INF starting mailservice`
- `INF public http listening addr=0.0.0.0:443`
- `INF smtp inbound listening addr=0.0.0.0:25`
- `INF smtp submission listening (auth required) addr=0.0.0.0:587`
- **The bootstrap admin banner with a fresh random password** — save it.
- `INF selfcheck check=ptr ok=true` etc.

## Startup self-checks

In cloud mode with `MAIL_CLOUD_HOSTNAME` set, the service runs a panel
of DNS-based sanity checks at boot:

| Check | What it verifies |
|-------|------------------|
| `ptr` | Reverse DNS for `MAIL_CLOUD_OUTBOUND_IP` returns your hostname *and* forward-resolves back |
| `dnsbl/zen.spamhaus.org` | IP is not on Spamhaus's Zen composite blocklist |
| `dnsbl/b.barracudacentral.org` | Not on Barracuda |
| `dnsbl/bl.spamcop.net` | Not on SpamCop |
| `dkim/ms1.<domain>` | Public DKIM key resolves at `ms1._domainkey.<domain>` |

Any check that fails logs a WARN. None of them fail startup — you'll
see the verdict in `journalctl -u mailservice`. Failures usually mean:

- **`ptr` fails:** your VPS provider hasn't set the PTR you requested.
  Set it in their console and reboot.
- **`dnsbl/*` fails:** you drew an IP another tenant polluted. Request
  a new IP from your provider — usually free. Delisting is also possible
  but slower.
- **`dkim/*` fails:** the DNS record isn't published or hasn't propagated.
  Wait ~10min and re-run via `sudo systemctl reload mailservice`.

## HTTPS

Two options:

**Option A — service handles TLS directly.** Bind `MAIL_HTTP_PORT=443`
and place `fullchain.pem` + `privkey.pem` alongside the binary. TLS
configuration in the service itself is a Day 10 task; today the service
serves plain HTTP on the configured port.

**Option B — Caddy or nginx in front.** Bind
`MAIL_HTTP_PORT=8035 MAIL_LISTEN_ADDR=127.0.0.1` and let Caddy/nginx
terminate TLS and forward. Caddyfile example:

```
mail.example.com {
    reverse_proxy 127.0.0.1:8035
}
```

Caddy auto-provisions Let's Encrypt certs. `sudo apt install caddy`
and you're done.

## Backups

Nightly SQLite backup + raw-store rsync. Add to root's crontab:

```
0 2 * * * sudo -u mailservice sqlite3 /var/lib/mailservice/mail.db ".backup /var/lib/mailservice/backups/mail-$(date +%F).db"
0 3 * * * rsync -a /var/lib/mailservice/backups/ backup-user@backup.host:/backups/mailservice/
```

For encrypted off-site backups, pipe through `age`:

```
age -R ~/.ssh/authorized_keys < mail-2026-09-09.db | rclone rcat b2:mailservice/2026-09-09.db.age
```

## Upgrade

```bash
sudo systemctl stop mailservice
sudo install -m 755 bin/mailservice /usr/local/bin/mailservice
sudo systemctl start mailservice
```

Migrations apply automatically on startup — no manual step.
