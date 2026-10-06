# mail-service

A self-hostable, single-binary mail platform that combines Mailpit-style local
capture with Resend-style transactional sending. One binary, one dashboard, one
API — from local dev on Laragon to your own MX in production.

**Repo:** https://github.com/shamil3ilm/mail-service

---

## Status

Runs end to end as one binary. Started by `cmd/mailservice` today:

- SMTP capture on the inbound port, plus a submission port
- HTTP API and an embedded web dashboard (Mailpit-style tabs) with full-text
  search over messages and attachment names (SQLite FTS5)
- Outbound relay through a swappable provider (`capture` or `smtp`), with a
  per-host warm-up scheduler
- Accounts with a bootstrapped admin
- Retention (auto-delete after `MAIL_RETENTION_DAYS`), `/metrics` on the
  admin port, and startup self-checks
- SMS capture, or an HTTP SMS provider when one is configured
- Optional DNS record publishing to privatedns
- Backup and restore scripts in `scripts/`

Built but not yet wired in: the DKIM signer in `internal/dkim` (RSA-SHA256,
RFC 6376, tested) is not yet applied to outbound mail.

## Prerequisites

- Go 1.24+ — https://go.dev/dl/ (crypto/pbkdf2 for password hashing)
- (optional) Docker Desktop or docker + docker-compose

Install Go on Windows and verify:

```powershell
winget install --id GoLang.Go -e   # or download from go.dev
go version
```

## Quick start (local, on your machine)

```powershell
git clone https://github.com/shamil3ilm/mail-service.git mail-service
cd mail-service
copy .env.example .env
go mod tidy
go run ./cmd/mailservice
```

Then in another shell:

```powershell
curl http://127.0.0.1:8025/healthz
curl http://127.0.0.1:8025/readyz
curl http://127.0.0.1:8026/debug/pprof/  # admin, loopback only
```

## Quick start (Docker)

```bash
docker compose -f deploy/docker-compose.yml up --build
curl http://127.0.0.1:8025/healthz
```

## Configuration

All config is via environment variables. See `.env.example` for the full list
and defaults. Highlights:

| Var | Default | Purpose |
|---|---|---|
| `MAIL_MODE` | `local` | `local` (loopback, capture) or `cloud` (public MX, relay) |
| `MAIL_LISTEN_ADDR` | `127.0.0.1` | Bind for public router — change to `0.0.0.0` for LAN/cloud |
| `MAIL_HTTP_PORT` | `8025` | Dashboard + API |
| `MAIL_ADMIN_PORT` | `8026` | pprof + metrics — always loopback |
| `MAIL_SMTP_PORT` | `1025` | Inbound dev catch |
| `MAIL_DB_PATH` | `./data/mail.db` | SQLite file (WAL enabled) |
| `MAIL_RELAY_PROVIDER` | `none` | `none` \| `smtp` \| `ses` \| `resend` |

## Layout

```
mail-service/
├── cmd/mailservice/          main.go — boot, HTTP, signal handling
├── internal/
│   ├── config/               env-driven config loader + validation
│   ├── logger/               slog JSON/text with context helpers
│   ├── storage/              Store interface (adapter boundary)
│   │   └── sqlite/           modernc.org/sqlite + WAL + FTS5 + migrations
│   ├── provider/             Relay interface (SES/SMTP/Resend swappable)
│   └── api/                  chi router, /healthz, /readyz, admin pprof
├── deploy/                   docker-compose.yml
├── .github/workflows/ci.yml  build+test+cross-compile matrix
├── Dockerfile
├── Makefile
├── .env.example
└── README.md
```

Migrations live in `internal/storage/sqlite/migrations/` and are embedded into
the binary via `//go:embed`. They apply automatically on startup.

## Design principles

- **Single binary, no runtime deps.** Pure-Go SQLite (`modernc.org/sqlite`),
  static linking, cross-compiled to Windows/macOS/Linux in CI.
- **Adapter boundaries from Day 1.** `storage.Store` and `provider.Relay` are
  the seams that let us swap SQLite ↔ D1 ↔ Postgres and self-hosted ↔ SES ↔
  Resend without touching business logic.
- **12-factor config.** Env vars only, no config files, validated at startup.
- **Separate admin surface.** pprof and metrics never share a port with the
  public API.
- **Test with `:memory:` SQLite.** Full suite runs in seconds, no Docker.

## Tests

```bash
go test -race ./...
```

Coverage:

```bash
make cover     # or: go test -race -covermode=atomic -coverprofile=coverage.out ./...
```

## Roadmap (see also design docs — coming)

- **Week 1** ✅ Foundation: config, logger, storage, health, CI, Docker
- **Week 2** SMTP inbound (LMTP or native), MIME parse, WebSocket stream
- **Week 3** Users/teams/domains schema, auth, verification wizard, mailbox CRUD
- **Week 4** Vue 3 dashboard (inbox, detail, search, live, mobile-first)
- **Week 5** Outbound engine + `POST /emails` API + DKIM + provider adapters
- **Week 6** Reputation monitor, warmup scheduler, bounce/complaint processing
- **Week 7** Threading + Gmail-like UI + rspamd + RFC 2142/8058 compliance
- **Week 8** Backup runner, deploy runbook, ops docs

## License

TBD.
