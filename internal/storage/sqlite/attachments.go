package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

func (s *Store) InsertAttachment(ctx context.Context, a *storage.Attachment) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO attachments (id, message_id, filename, mime_type, size, path)
		VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, a.MessageID, a.Filename, a.MimeType, a.Size, a.Path,
	)
	return err
}

func (s *Store) ListAttachmentsForMessage(ctx context.Context, messageID string) ([]*storage.Attachment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, message_id, filename, mime_type, size, path
		FROM attachments
		WHERE message_id = ?
		ORDER BY filename ASC`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*storage.Attachment, 0, 4)
	for rows.Next() {
		var a storage.Attachment
		if err := rows.Scan(&a.ID, &a.MessageID, &a.Filename, &a.MimeType, &a.Size, &a.Path); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

func (s *Store) GetAttachment(ctx context.Context, id string) (*storage.Attachment, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, message_id, filename, mime_type, size, path
		FROM attachments WHERE id = ?`, id)
	var a storage.Attachment
	err := row.Scan(&a.ID, &a.MessageID, &a.Filename, &a.MimeType, &a.Size, &a.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}
