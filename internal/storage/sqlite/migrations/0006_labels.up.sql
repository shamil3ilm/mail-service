-- Labels: Gmail-style tags applied per message, displayed at thread level.
-- Team-scoped so members can share a shared vocabulary.

CREATE TABLE labels (
    id          TEXT PRIMARY KEY,
    team_id     TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name        TEXT NOT NULL COLLATE NOCASE,
    color       TEXT NOT NULL DEFAULT '#6b7280',
    created_at  INTEGER NOT NULL,
    UNIQUE(team_id, name)
);
CREATE INDEX idx_labels_team ON labels(team_id);

-- Message ↔ label join. Deleting a label cascades. Deleting a message
-- doesn't cascade automatically since messages don't have FK constraints
-- on their id — the DeleteMessage path handles cleanup manually.
CREATE TABLE message_labels (
    message_id TEXT NOT NULL,
    label_id   TEXT NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
    PRIMARY KEY (message_id, label_id)
);
CREATE INDEX idx_message_labels_label ON message_labels(label_id);
