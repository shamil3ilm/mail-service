package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

func (s *Store) InsertSMS(ctx context.Context, m *storage.SMSMessage) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.Segments == 0 {
		m.Segments = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sms_messages
			(id, direction, provider, provider_id, from_addr, to_addr,
			 body, status, error, segments, user_id, created_at,
			 sent_at, delivered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Direction, m.Provider, m.ProviderID,
		m.FromAddr, m.ToAddr,
		m.Body, m.Status, m.Error, m.Segments, m.UserID,
		m.CreatedAt.Unix(),
		nullableUnix(m.SentAt),
		nullableUnix(m.DeliveredAt),
	)
	return err
}

func (s *Store) GetSMS(ctx context.Context, id string) (*storage.SMSMessage, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, direction, provider, provider_id, from_addr, to_addr,
		       body, status, error, segments, user_id, created_at,
		       sent_at, delivered_at
		FROM sms_messages WHERE id = ?`, id)
	return scanSMS(row)
}

// ListSMS filters by direction ("outbound", "inbound", or empty for both)
// and returns most-recent first. limit/offset paginate.
func (s *Store) ListSMS(ctx context.Context, direction string, limit, offset int) ([]*storage.SMSMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, direction, provider, provider_id, from_addr, to_addr,
		       body, status, error, segments, user_id, created_at,
		       sent_at, delivered_at
		FROM sms_messages
		WHERE (? = '' OR direction = ?)
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?`, direction, direction, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*storage.SMSMessage, 0, limit)
	for rows.Next() {
		m, err := scanSMS(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateSMSStatus is used by providers after send/deliver receipts.
// Only non-nil timestamps are written — a nil sentAt won't clobber the
// existing value.
func (s *Store) UpdateSMSStatus(ctx context.Context, id, status, providerID, errMsg string, sentAt, deliveredAt *time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sms_messages
		SET status       = ?,
		    provider_id  = CASE WHEN ? = '' THEN provider_id ELSE ? END,
		    error        = ?,
		    sent_at      = COALESCE(?, sent_at),
		    delivered_at = COALESCE(?, delivered_at)
		WHERE id = ?`,
		status, providerID, providerID, errMsg,
		nullableUnix(sentAt), nullableUnix(deliveredAt),
		id)
	return err
}

type smsScanner interface {
	Scan(dest ...any) error
}

func scanSMS(sc smsScanner) (*storage.SMSMessage, error) {
	var m storage.SMSMessage
	var created int64
	var sentAt, deliveredAt sql.NullInt64
	err := sc.Scan(&m.ID, &m.Direction, &m.Provider, &m.ProviderID,
		&m.FromAddr, &m.ToAddr,
		&m.Body, &m.Status, &m.Error, &m.Segments, &m.UserID,
		&created, &sentAt, &deliveredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.CreatedAt = time.Unix(created, 0).UTC()
	if sentAt.Valid {
		t := time.Unix(sentAt.Int64, 0).UTC()
		m.SentAt = &t
	}
	if deliveredAt.Valid {
		t := time.Unix(deliveredAt.Int64, 0).UTC()
		m.DeliveredAt = &t
	}
	return &m, nil
}
