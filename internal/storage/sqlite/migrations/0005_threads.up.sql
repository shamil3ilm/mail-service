-- Threads: Gmail-style conversation grouping.
--
-- Thread membership is derived from RFC 5322 references (Message-ID +
-- In-Reply-To + References headers) with a fallback to normalised-subject
-- match within a 30-day window. Every message belongs to exactly one thread,
-- so a solo message is a thread of size 1 — that keeps the API uniform.

CREATE TABLE threads (
    id                 TEXT PRIMARY KEY,
    subject_normalized TEXT NOT NULL DEFAULT '',
    last_at            INTEGER NOT NULL,
    message_count      INTEGER NOT NULL DEFAULT 0,
    participants       TEXT NOT NULL DEFAULT '[]',   -- JSON array of email addrs
    created_at         INTEGER NOT NULL
);
CREATE INDEX idx_threads_last_at ON threads(last_at DESC);
CREATE INDEX idx_threads_subject ON threads(subject_normalized);

-- Existing rows get "" — resolveThread will start populating on new inserts,
-- and a background rethread job (future) can walk history if desired.
ALTER TABLE messages ADD COLUMN thread_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_messages_thread ON messages(thread_id, received_at DESC);
