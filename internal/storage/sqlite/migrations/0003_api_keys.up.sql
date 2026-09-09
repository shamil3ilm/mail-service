-- API keys: long-lived bearer credentials for app-to-service calls and
-- SMTP submission (username can be anything, password is the msk_ token).
--
-- Only the sha256 hash of the full secret is stored. The prefix is kept in
-- plaintext so users can identify a key ("which one is this?") without
-- exposing the secret. Prefix is the first 12 chars of the full token
-- (e.g. "msk_a1b2c3d4"); the remaining 40 hex chars are the secret.

CREATE TABLE api_keys (
    id           TEXT PRIMARY KEY,
    prefix       TEXT NOT NULL,           -- displayed in UI, e.g. "msk_a1b2c3d4"
    hash         TEXT NOT NULL,           -- sha256(full_token) hex — unique
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL DEFAULT '',
    scopes       TEXT NOT NULL DEFAULT '[]', -- JSON array: ["send","read"]
    mailbox_id   TEXT,                    -- optional: scope key to one mailbox
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    revoked_at   INTEGER
);
CREATE UNIQUE INDEX idx_api_keys_hash ON api_keys(hash);
CREATE INDEX        idx_api_keys_user ON api_keys(user_id);
