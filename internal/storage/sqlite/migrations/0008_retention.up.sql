-- Per-mailbox retention override. NULL means "use the global default from
-- MAIL_RETENTION_DAYS"; 0 means "keep forever regardless of global default".
-- Positive N means "delete messages older than N days from this mailbox".

ALTER TABLE mailboxes ADD COLUMN retention_days INTEGER;
CREATE INDEX idx_mailboxes_retention ON mailboxes(retention_days);
