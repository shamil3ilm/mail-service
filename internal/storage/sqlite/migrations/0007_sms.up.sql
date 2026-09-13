-- SMS: parallel primitive to mail. Each sms_messages row is one direction
-- (send or receive) of a text. Storage layout mirrors messages so the
-- dashboard can render both in the same shell.
--
-- to_addr is E.164 for real SMS ("+15551234567") or an arbitrary short-
-- code for internal-only tests. from_addr is the sender identity — for
-- SIM-based providers this is your SIM's number; for API providers it's
-- the sender ID you registered.
--
-- Status vocabulary matches Twilio's for provider parity:
--   queued | sending | sent | delivered | failed | received

CREATE TABLE sms_messages (
    id            TEXT PRIMARY KEY,
    direction     TEXT NOT NULL CHECK (direction IN ('outbound','inbound')),
    provider      TEXT NOT NULL DEFAULT '',   -- 'capture' | 'http' | 'twilio' | ...
    provider_id   TEXT NOT NULL DEFAULT '',   -- provider-side message id
    from_addr     TEXT NOT NULL,
    to_addr       TEXT NOT NULL,
    body          TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'queued',
    error         TEXT NOT NULL DEFAULT '',
    segments      INTEGER NOT NULL DEFAULT 1,
    user_id       TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    sent_at       INTEGER,
    delivered_at  INTEGER
);
CREATE INDEX idx_sms_created_at ON sms_messages(created_at DESC);
CREATE INDEX idx_sms_to_addr    ON sms_messages(to_addr);
CREATE INDEX idx_sms_direction  ON sms_messages(direction, created_at DESC);
