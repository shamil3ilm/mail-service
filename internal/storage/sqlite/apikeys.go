package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

func (s *Store) InsertAPIKey(ctx context.Context, k *storage.APIKey) error {
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now().UTC()
	}
	scopes, _ := json.Marshal(k.Scopes)
	var mailboxID any
	if k.MailboxID != "" {
		mailboxID = k.MailboxID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO api_keys
		    (id, prefix, hash, user_id, name, scopes, mailbox_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.Prefix, k.Hash, k.UserID, k.Name, string(scopes),
		mailboxID, k.CreatedAt.Unix(),
	)
	return err
}

func (s *Store) GetAPIKeyByHash(ctx context.Context, hash string) (*storage.APIKey, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, prefix, hash, user_id, name, scopes, mailbox_id,
		       created_at, last_used_at, revoked_at
		FROM api_keys
		WHERE hash = ? AND revoked_at IS NULL`, hash)
	return scanAPIKey(row)
}

func (s *Store) ListAPIKeysForUser(ctx context.Context, userID string) ([]*storage.APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, prefix, hash, user_id, name, scopes, mailbox_id,
		       created_at, last_used_at, revoked_at
		FROM api_keys
		WHERE user_id = ? AND revoked_at IS NULL
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*storage.APIKey, 0, 8)
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		// Never return the hash outside storage — API surface uses prefix only.
		k.Hash = ""
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeAPIKey marks a key revoked. Scoped by userID so a user can only
// revoke their own keys; use "" to skip the ownership check for admin ops.
func (s *Store) RevokeAPIKey(ctx context.Context, id, userID string) error {
	now := time.Now().UTC().Unix()
	var res sql.Result
	var err error
	if userID == "" {
		res, err = s.db.ExecContext(ctx,
			`UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
			now, id)
	} else {
		res, err = s.db.ExecContext(ctx,
			`UPDATE api_keys SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
			now, id, userID)
	}
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// TouchAPIKey updates last_used_at. Fire-and-forget from the auth path —
// we don't want a slow DB write to block every request, so callers should
// generally launch this in a goroutine with a short-lived context.
func (s *Store) TouchAPIKey(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at = ? WHERE id = ?`,
		at.Unix(), id)
	return err
}

func scanAPIKey(sc scanner) (*storage.APIKey, error) {
	var k storage.APIKey
	var scopesJSON string
	var mailboxID sql.NullString
	var created int64
	var lastUsed, revoked sql.NullInt64
	err := sc.Scan(&k.ID, &k.Prefix, &k.Hash, &k.UserID, &k.Name,
		&scopesJSON, &mailboxID, &created, &lastUsed, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(scopesJSON), &k.Scopes)
	if mailboxID.Valid {
		k.MailboxID = mailboxID.String
	}
	k.CreatedAt = time.Unix(created, 0).UTC()
	if lastUsed.Valid {
		t := time.Unix(lastUsed.Int64, 0).UTC()
		k.LastUsedAt = &t
	}
	if revoked.Valid {
		t := time.Unix(revoked.Int64, 0).UTC()
		k.RevokedAt = &t
	}
	return &k, nil
}
