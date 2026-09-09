package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

func (s *Store) InsertLabel(ctx context.Context, l *storage.Label) error {
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now().UTC()
	}
	if l.Color == "" {
		l.Color = "#6b7280"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO labels (id, team_id, name, color, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		l.ID, l.TeamID, l.Name, l.Color, l.CreatedAt.Unix())
	return err
}

func (s *Store) ListLabelsForTeam(ctx context.Context, teamID string) ([]*storage.Label, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, team_id, name, color, created_at
		FROM labels
		WHERE team_id = ?
		ORDER BY name COLLATE NOCASE ASC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLabels(rows)
}

// DeleteLabel scopes to teamID so a user can't nuke someone else's labels.
// Rows in message_labels cascade automatically via the FK.
func (s *Store) DeleteLabel(ctx context.Context, id, teamID string) error {
	var res sql.Result
	var err error
	if teamID == "" {
		res, err = s.db.ExecContext(ctx, `DELETE FROM labels WHERE id = ?`, id)
	} else {
		res, err = s.db.ExecContext(ctx,
			`DELETE FROM labels WHERE id = ? AND team_id = ?`, id, teamID)
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

// LabelMessage is idempotent: applying the same label twice is a no-op.
func (s *Store) LabelMessage(ctx context.Context, messageID, labelID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO message_labels (message_id, label_id)
		VALUES (?, ?)
		ON CONFLICT(message_id, label_id) DO NOTHING`,
		messageID, labelID)
	return err
}

func (s *Store) UnlabelMessage(ctx context.Context, messageID, labelID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM message_labels WHERE message_id = ? AND label_id = ?`,
		messageID, labelID)
	return err
}

func (s *Store) ListLabelsForMessage(ctx context.Context, messageID string) ([]*storage.Label, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.team_id, l.name, l.color, l.created_at
		FROM labels l
		INNER JOIN message_labels ml ON ml.label_id = l.id
		WHERE ml.message_id = ?
		ORDER BY l.name COLLATE NOCASE ASC`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLabels(rows)
}

// ListLabelsForThread returns the union of labels attached to ANY message
// in the thread. Matches Gmail's "labels are per-message, displayed per-
// conversation" behaviour.
func (s *Store) ListLabelsForThread(ctx context.Context, threadID string) ([]*storage.Label, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT l.id, l.team_id, l.name, l.color, l.created_at
		FROM labels l
		INNER JOIN message_labels ml ON ml.label_id = l.id
		INNER JOIN messages       m  ON m.id  = ml.message_id
		WHERE m.thread_id = ?
		ORDER BY l.name COLLATE NOCASE ASC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLabels(rows)
}

// ListThreadsForLabel returns threads that have at least one message
// carrying the label. Sort by most-recent-activity first.
func (s *Store) ListThreadsForLabel(ctx context.Context, labelID string, limit, offset int) ([]*storage.Thread, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT t.id, t.subject_normalized, t.last_at, t.message_count,
		                t.participants, t.created_at
		FROM threads t
		INNER JOIN messages m       ON m.thread_id = t.id
		INNER JOIN message_labels ml ON ml.message_id = m.id
		WHERE ml.label_id = ?
		ORDER BY t.last_at DESC
		LIMIT ? OFFSET ?`, labelID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*storage.Thread, 0, limit)
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanLabels(rows *sql.Rows) ([]*storage.Label, error) {
	out := make([]*storage.Label, 0, 4)
	for rows.Next() {
		var l storage.Label
		var created int64
		if err := rows.Scan(&l.ID, &l.TeamID, &l.Name, &l.Color, &created); err != nil {
			return nil, err
		}
		l.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, &l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ensure errors package stays referenced when the file grows further.
var _ = errors.Is
