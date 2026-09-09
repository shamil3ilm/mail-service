-- Multi-tenant: users, teams, sessions, and domains.
-- Existing mailboxes.owner_type/owner_id already exist from 0001; we just
-- add optional joins to teams + a real domains table for verification data.

CREATE TABLE users (
    id             TEXT PRIMARY KEY,
    email          TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash  TEXT NOT NULL,
    display_name   TEXT NOT NULL DEFAULT '',
    is_admin       INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL,
    last_login_at  INTEGER
);
CREATE INDEX idx_users_email ON users(email);

CREATE TABLE teams (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  INTEGER NOT NULL
);

CREATE TABLE team_members (
    team_id  TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role     TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member','readonly')),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX idx_team_members_user ON team_members(user_id);

-- Sessions are DB-backed rather than JWT so we can revoke instantly.
CREATE TABLE sessions (
    id          TEXT PRIMARY KEY,   -- opaque 32-byte random; the value in the cookie
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    user_agent  TEXT NOT NULL DEFAULT '',
    ip_addr     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_sessions_user    ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

-- Domains a team owns. Verification timestamps let us gate outbound send
-- until SPF+DKIM+DMARC are all published correctly.
CREATE TABLE domains (
    id                  TEXT PRIMARY KEY,
    team_id             TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name                TEXT NOT NULL COLLATE NOCASE,
    dkim_selector       TEXT NOT NULL DEFAULT 'ms1',
    dkim_public_key     TEXT NOT NULL DEFAULT '',
    dkim_private_key    TEXT NOT NULL DEFAULT '',
    spf_verified_at     INTEGER,
    dkim_verified_at    INTEGER,
    dmarc_verified_at   INTEGER,
    mx_verified_at      INTEGER,
    auto_verified       INTEGER NOT NULL DEFAULT 0,   -- true for .test/.local auto-provision
    created_at          INTEGER NOT NULL,
    UNIQUE(name)
);
CREATE INDEX idx_domains_team ON domains(team_id);
