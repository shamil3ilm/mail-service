DROP INDEX IF EXISTS idx_mailboxes_retention;
-- SQLite pre-3.35 can't drop columns; leave the column and index removal only.
