// Package sqlite is the SQLite implementation of storage.Store.
// Uses modernc.org/sqlite (pure Go, no CGO) so cross-compilation to
// windows/darwin/linux "just works" in CI.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migrateSqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/shamil3ilm/mail-service/internal/storage"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the SQLite-backed storage.Store implementation.
type Store struct {
	db *sql.DB
}

// Open connects, applies PRAGMAs, runs migrations, and returns a ready Store.
// dbPath may be a filesystem path or ":memory:" for tests.
func Open(ctx context.Context, dbPath string) (*Store, error) {
	if dbPath != ":memory:" {
		if err := ensureDir(dbPath); err != nil {
			return nil, fmt.Errorf("ensure db dir: %w", err)
		}
	}

	// Query params tune SQLite for our access pattern.
	// _pragma is honored by modernc.org/sqlite on each connection.
	dsn := dbPath + "?" +
		"_pragma=journal_mode(WAL)&" +
		"_pragma=synchronous(NORMAL)&" +
		"_pragma=busy_timeout(5000)&" +
		"_pragma=foreign_keys(1)&" +
		"_pragma=temp_store(MEMORY)&" +
		"_pragma=cache_size(-64000)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Single writer per WAL best practice; readers are unlimited.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	s := &Store{db: db}

	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
}

func (s *Store) migrate() error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	drv, err := migrateSqlite.WithInstance(s.db, &migrateSqlite.Config{})
	if err != nil {
		return err
	}
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", drv)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Ready performs a lightweight SELECT to prove the schema is present.
// Called from /readyz — the endpoint fails until this succeeds.
func (s *Store) Ready(ctx context.Context) error {
	var n int
	return s.db.QueryRowContext(ctx, `SELECT count(*) FROM mailboxes`).Scan(&n)
}

// InsertMessage stores a row + updates the FTS index + resolves the thread.
// Raw MIME must already be written to disk at m.RawPath before calling.
//
// Thread resolution runs inside the same transaction so a message row can
// never exist without its thread row, and vice-versa.
func (s *Store) InsertMessage(ctx context.Context, m *storage.Message) error {
	toJSON := func(v []string) string { b, _ := json.Marshal(v); return string(b) }

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Assign m.ThreadID via the resolve helper. This may create a new
	// threads row (returned via isNew) or attach to an existing one.
	threadID, isNew, err := resolveThreadTx(ctx, tx, m)
	if err != nil {
		return err
	}
	m.ThreadID = threadID

	_, err = tx.ExecContext(ctx, `
		INSERT INTO messages
		    (id, thread_id, mailbox_id, from_addr, to_addrs, cc_addrs, bcc_addrs,
		     subject, message_id, in_reply_to, refs, raw_path, size, received_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.ThreadID, m.MailboxID, m.FromAddr,
		toJSON(m.ToAddrs), toJSON(m.CcAddrs), toJSON(m.BccAddrs),
		m.Subject, m.MessageID, m.InReplyTo, toJSON(m.References),
		m.RawPath, m.Size, m.ReceivedAt.Unix(),
	)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO messages_fts(rowid, subject, from_addr, to_addrs, body)
		VALUES ((SELECT rowid FROM messages WHERE id=?), ?, ?, ?, ?)`,
		m.ID, m.Subject, m.FromAddr, toJSON(m.ToAddrs), truncateForFTS(m.BodyPreview),
	)
	if err != nil {
		return err
	}

	if err := upsertThreadStatsTx(ctx, tx, m, isNew); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) GetMessage(ctx context.Context, id string) (*storage.Message, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, thread_id, mailbox_id, from_addr, to_addrs, cc_addrs, bcc_addrs,
		       subject, message_id, in_reply_to, refs, raw_path, size,
		       received_at, read_at
		FROM messages WHERE id=?`, id)
	return scanMessage(row)
}

func (s *Store) ListMessages(ctx context.Context, mailboxID string, limit, offset int) ([]*storage.Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, thread_id, mailbox_id, from_addr, to_addrs, cc_addrs, bcc_addrs,
		       subject, message_id, in_reply_to, refs, raw_path, size,
		       received_at, read_at
		FROM messages
		WHERE (?='' OR mailbox_id=?)
		ORDER BY received_at DESC
		LIMIT ? OFFSET ?`, mailboxID, mailboxID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*storage.Message, 0, limit)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) DeleteMessage(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return storage.ErrNotFound
	}
	// Best-effort FTS cleanup; row is orphaned but harmless.
	_, _ = s.db.ExecContext(ctx, `DELETE FROM messages_fts WHERE rowid NOT IN (SELECT rowid FROM messages)`)
	return nil
}

func (s *Store) UpsertMailbox(ctx context.Context, m *storage.Mailbox) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO mailboxes (id, address, match_type, display_name, mode, owner_type, owner_id, created_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
		    address=excluded.address,
		    match_type=excluded.match_type,
		    display_name=excluded.display_name,
		    mode=excluded.mode,
		    owner_type=excluded.owner_type,
		    owner_id=excluded.owner_id`,
		m.ID, m.Address, m.MatchType, m.DisplayName, m.Mode,
		m.OwnerType, m.OwnerID, m.CreatedAt.Unix())
	return err
}

func (s *Store) GetMailbox(ctx context.Context, id string) (*storage.Mailbox, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, address, match_type, display_name, mode, owner_type, owner_id, created_at
		FROM mailboxes WHERE id=?`, id)
	return scanMailbox(row)
}

func (s *Store) ListMailboxes(ctx context.Context) ([]*storage.Mailbox, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, address, match_type, display_name, mode, owner_type, owner_id, created_at
		FROM mailboxes
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*storage.Mailbox, 0, 16)
	for rows.Next() {
		m, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) FindMailboxByAddress(ctx context.Context, addr string) (*storage.Mailbox, error) {
	// Day-1: exact match only. Wildcard/regex routing lands in Phase 2.
	row := s.db.QueryRowContext(ctx, `
		SELECT id, address, match_type, display_name, mode, owner_type, owner_id, created_at
		FROM mailboxes WHERE address=? LIMIT 1`, addr)
	return scanMailbox(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanMessage(sc scanner) (*storage.Message, error) {
	var m storage.Message
	var toJ, ccJ, bccJ, refJ string
	var recv int64
	var read sql.NullInt64
	err := sc.Scan(&m.ID, &m.ThreadID, &m.MailboxID, &m.FromAddr, &toJ, &ccJ, &bccJ,
		&m.Subject, &m.MessageID, &m.InReplyTo, &refJ, &m.RawPath, &m.Size,
		&recv, &read)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(toJ), &m.ToAddrs)
	_ = json.Unmarshal([]byte(ccJ), &m.CcAddrs)
	_ = json.Unmarshal([]byte(bccJ), &m.BccAddrs)
	_ = json.Unmarshal([]byte(refJ), &m.References)
	m.ReceivedAt = time.Unix(recv, 0).UTC()
	if read.Valid {
		t := time.Unix(read.Int64, 0).UTC()
		m.ReadAt = &t
	}
	return &m, nil
}

func scanMailbox(sc scanner) (*storage.Mailbox, error) {
	var m storage.Mailbox
	var created int64
	err := sc.Scan(&m.ID, &m.Address, &m.MatchType, &m.DisplayName, &m.Mode,
		&m.OwnerType, &m.OwnerID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.CreatedAt = time.Unix(created, 0).UTC()
	return &m, nil
}

func ensureDir(dbPath string) error {
	dir := filepath.Dir(dbPath)
	if dir == "" || dir == "." {
		return nil
	}
	return mkdirAll(dir)
}
