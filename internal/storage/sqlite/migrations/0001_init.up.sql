-- Initial schema: mailboxes, messages, attachments, events.
-- SQLite dialect. Multi-tenant fields (users/teams/domains) added in 0002.

CREATE TABLE mailboxes (
    id           TEXT PRIMARY KEY,
    address      TEXT NOT NULL,
    match_type   TEXT NOT NULL DEFAULT 'exact' CHECK (match_type IN ('exact','wildcard','regex')),
    display_name TEXT NOT NULL DEFAULT '',
    mode         TEXT NOT NULL DEFAULT 'receive' CHECK (mode IN ('receive','send','both')),
    owner_type   TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','team')),
    owner_id     TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
);
CREATE INDEX idx_mailboxes_address ON mailboxes(address);
CREATE INDEX idx_mailboxes_owner   ON mailboxes(owner_type, owner_id);

CREATE TABLE messages (
    id          TEXT PRIMARY KEY,
    mailbox_id  TEXT NOT NULL,
    from_addr   TEXT NOT NULL,
    to_addrs    TEXT NOT NULL DEFAULT '[]',   -- JSON array
    cc_addrs    TEXT NOT NULL DEFAULT '[]',
    bcc_addrs   TEXT NOT NULL DEFAULT '[]',
    subject     TEXT NOT NULL DEFAULT '',
    message_id  TEXT NOT NULL DEFAULT '',
    in_reply_to TEXT NOT NULL DEFAULT '',
    refs        TEXT NOT NULL DEFAULT '[]',   -- JSON array of References
    raw_path    TEXT NOT NULL,
    size        INTEGER NOT NULL DEFAULT 0,
    received_at INTEGER NOT NULL,
    read_at     INTEGER
);
CREATE INDEX idx_messages_mailbox_received ON messages(mailbox_id, received_at DESC);
CREATE INDEX idx_messages_message_id       ON messages(message_id);

CREATE TABLE attachments (
    id         TEXT PRIMARY KEY,
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    filename   TEXT NOT NULL,
    mime_type  TEXT NOT NULL DEFAULT 'application/octet-stream',
    size       INTEGER NOT NULL DEFAULT 0,
    path       TEXT NOT NULL
);
CREATE INDEX idx_attachments_message ON attachments(message_id);

CREATE TABLE events (
    id         TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    type       TEXT NOT NULL,   -- received | relayed | opened | bounced | complained
    ts         INTEGER NOT NULL,
    payload    TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_events_message_ts ON events(message_id, ts DESC);

-- Full-text search over headers + subject + text body preview.
-- Body text is populated at parse time from the raw MIME.
CREATE VIRTUAL TABLE messages_fts USING fts5(
    subject,
    from_addr,
    to_addrs,
    body,
    content='',
    tokenize='porter unicode61 remove_diacritics 2'
);
