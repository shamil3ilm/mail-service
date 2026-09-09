-- Suppression list: recipients we must not attempt to send to.
-- Populated automatically by hard-bounce / complaint processing, and
-- manually via the dashboard.
--
-- Reason values are free text but conventionally one of:
--   bounce_hard | bounce_soft | complaint | manual | unsubscribe

CREATE TABLE suppressions (
    address     TEXT PRIMARY KEY COLLATE NOCASE,
    reason      TEXT NOT NULL DEFAULT 'manual',
    source      TEXT NOT NULL DEFAULT '',   -- e.g. Message-ID that triggered it
    created_at  INTEGER NOT NULL
);
CREATE INDEX idx_suppressions_reason ON suppressions(reason);
