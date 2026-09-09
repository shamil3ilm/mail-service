-- SQLite doesn't support DROP COLUMN in older versions; live with a leftover
-- thread_id column if you roll back. Threads table can be dropped cleanly.
DROP INDEX IF EXISTS idx_messages_thread;
DROP TABLE IF EXISTS threads;
