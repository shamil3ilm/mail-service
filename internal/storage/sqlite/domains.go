package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

// ── Teams ──────────────────────────────────────────────────────────

func (s *Store) InsertTeam(ctx context.Context, t *storage.Team) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO teams (id, name, created_at) VALUES (?, ?, ?)`,
		t.ID, t.Name, t.CreatedAt.Unix())
	return err
}

func (s *Store) AddTeamMember(ctx context.Context, teamID, userID, role string) error {
	if role == "" {
		role = "owner"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO team_members (team_id, user_id, role)
		VALUES (?, ?, ?)
		ON CONFLICT(team_id, user_id) DO UPDATE SET role = excluded.role`,
		teamID, userID, role)
	return err
}

func (s *Store) ListTeamsForUser(ctx context.Context, userID string) ([]*storage.Team, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.name, t.created_at
		FROM teams t
		INNER JOIN team_members tm ON tm.team_id = t.id
		WHERE tm.user_id = ?
		ORDER BY t.created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*storage.Team, 0, 4)
	for rows.Next() {
		var t storage.Team
		var created int64
		if err := rows.Scan(&t.ID, &t.Name, &created); err != nil {
			return nil, err
		}
		t.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, &t)
	}
	return out, rows.Err()
}

// ── Domains ────────────────────────────────────────────────────────

func (s *Store) InsertDomain(ctx context.Context, d *storage.Domain) error {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO domains
		    (id, team_id, name, dkim_selector, dkim_public_key, dkim_private_key,
		     auto_verified, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.TeamID, d.Name, d.DKIMSelector,
		d.DKIMPublicKey, d.DKIMPrivateKey,
		boolToInt(d.AutoVerified), d.CreatedAt.Unix(),
	)
	return err
}

func (s *Store) GetDomain(ctx context.Context, id string) (*storage.Domain, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, team_id, name, dkim_selector, dkim_public_key, dkim_private_key,
		       spf_verified_at, dkim_verified_at, dmarc_verified_at, mx_verified_at,
		       auto_verified, created_at
		FROM domains WHERE id = ?`, id)
	return scanDomain(row)
}

func (s *Store) ListDomainsForTeam(ctx context.Context, teamID string) ([]*storage.Domain, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, team_id, name, dkim_selector, dkim_public_key, dkim_private_key,
		       spf_verified_at, dkim_verified_at, dmarc_verified_at, mx_verified_at,
		       auto_verified, created_at
		FROM domains
		WHERE team_id = ?
		ORDER BY created_at DESC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*storage.Domain, 0, 4)
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		// Scrub private key from list responses; callers who need it fetch by id.
		d.DKIMPrivateKey = ""
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDomain removes a domain, scoped to teamID so a user can't nuke
// someone else's. Pass teamID="" only from admin/system paths.
func (s *Store) DeleteDomain(ctx context.Context, id, teamID string) error {
	var res sql.Result
	var err error
	if teamID == "" {
		res, err = s.db.ExecContext(ctx, `DELETE FROM domains WHERE id = ?`, id)
	} else {
		res, err = s.db.ExecContext(ctx,
			`DELETE FROM domains WHERE id = ? AND team_id = ?`, id, teamID)
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

// UpdateDomainVerification writes the four verification timestamps.
// A nil field means "not yet verified"; nil stays nil.
func (s *Store) UpdateDomainVerification(ctx context.Context, d *storage.Domain) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE domains SET
		    spf_verified_at   = ?,
		    dkim_verified_at  = ?,
		    dmarc_verified_at = ?,
		    mx_verified_at    = ?
		WHERE id = ?`,
		nullableUnix(d.SPFVerifiedAt),
		nullableUnix(d.DKIMVerifiedAt),
		nullableUnix(d.DMARCVerifiedAt),
		nullableUnix(d.MXVerifiedAt),
		d.ID)
	return err
}

func scanDomain(sc scanner) (*storage.Domain, error) {
	var d storage.Domain
	var spf, dkim, dmarc, mx sql.NullInt64
	var autoVerified int
	var created int64
	err := sc.Scan(&d.ID, &d.TeamID, &d.Name, &d.DKIMSelector,
		&d.DKIMPublicKey, &d.DKIMPrivateKey,
		&spf, &dkim, &dmarc, &mx, &autoVerified, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.SPFVerifiedAt = unixToPtr(spf)
	d.DKIMVerifiedAt = unixToPtr(dkim)
	d.DMARCVerifiedAt = unixToPtr(dmarc)
	d.MXVerifiedAt = unixToPtr(mx)
	d.AutoVerified = autoVerified == 1
	d.CreatedAt = time.Unix(created, 0).UTC()
	return &d, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableUnix(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

func unixToPtr(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.Unix(n.Int64, 0).UTC()
	return &t
}
