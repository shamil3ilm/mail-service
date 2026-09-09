package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) InsertUser(ctx context.Context, u *storage.User) error {
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	isAdmin := 0
	if u.IsAdmin {
		isAdmin = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, display_name, is_admin, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.PasswordHash, u.DisplayName, isAdmin, u.CreatedAt.Unix(),
	)
	return err
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*storage.User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, email, password_hash, display_name, is_admin, created_at, last_login_at
		FROM users WHERE email = ? COLLATE NOCASE`, email)
	return scanUser(row)
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*storage.User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, email, password_hash, display_name, is_admin, created_at, last_login_at
		FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *Store) UpdateUserLastLogin(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_login_at = ? WHERE id = ?`, at.Unix(), id)
	return err
}

func (s *Store) InsertSession(ctx context.Context, ss *storage.Session) error {
	if ss.CreatedAt.IsZero() {
		ss.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, created_at, expires_at, user_agent, ip_addr)
		VALUES (?, ?, ?, ?, ?, ?)`,
		ss.ID, ss.UserID, ss.CreatedAt.Unix(), ss.ExpiresAt.Unix(),
		ss.UserAgent, ss.IPAddr,
	)
	return err
}

func (s *Store) GetSession(ctx context.Context, id string) (*storage.Session, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, created_at, expires_at, user_agent, ip_addr
		FROM sessions WHERE id = ?`, id)
	var ss storage.Session
	var created, expires int64
	err := row.Scan(&ss.ID, &ss.UserID, &created, &expires, &ss.UserAgent, &ss.IPAddr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ss.CreatedAt = time.Unix(created, 0).UTC()
	ss.ExpiresAt = time.Unix(expires, 0).UTC()
	return &ss, nil
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanUser(sc scanner) (*storage.User, error) {
	var u storage.User
	var isAdmin int
	var created int64
	var last sql.NullInt64
	err := sc.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName,
		&isAdmin, &created, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.IsAdmin = isAdmin == 1
	u.CreatedAt = time.Unix(created, 0).UTC()
	if last.Valid {
		t := time.Unix(last.Int64, 0).UTC()
		u.LastLoginAt = &t
	}
	return &u, nil
}
