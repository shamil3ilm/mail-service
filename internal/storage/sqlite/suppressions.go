package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

// UpsertSuppression is idempotent — sending twice with the same address
// just refreshes reason/source. Case-insensitive because email addresses
// are (localpart is technically case-sensitive per RFC but treated as
// insensitive by every real mail system).
func (s *Store) UpsertSuppression(ctx context.Context, sup *storage.Suppression) error {
	if sup.CreatedAt.IsZero() {
		sup.CreatedAt = time.Now().UTC()
	}
	addr := strings.ToLower(strings.TrimSpace(sup.Address))
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO suppressions (address, reason, source, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(address) DO UPDATE SET
		    reason = excluded.reason,
		    source = excluded.source`,
		addr, sup.Reason, sup.Source, sup.CreatedAt.Unix())
	return err
}

// IsSuppressed reports whether address is on the list. Hot-path — no
// error is returned for a "not found" state, only for actual DB errors.
func (s *Store) IsSuppressed(ctx context.Context, address string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM suppressions WHERE address = ? COLLATE NOCASE LIMIT 1`,
		strings.TrimSpace(address)).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *Store) ListSuppressions(ctx context.Context, limit, offset int) ([]*storage.Suppression, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT address, reason, source, created_at
		FROM suppressions
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*storage.Suppression, 0, 16)
	for rows.Next() {
		var sup storage.Suppression
		var created int64
		if err := rows.Scan(&sup.Address, &sup.Reason, &sup.Source, &created); err != nil {
			return nil, err
		}
		sup.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, &sup)
	}
	return out, rows.Err()
}

func (s *Store) RemoveSuppression(ctx context.Context, address string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM suppressions WHERE address = ? COLLATE NOCASE`, address)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return storage.ErrNotFound
	}
	return nil
}
