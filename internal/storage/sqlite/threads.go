package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

// threadWindow is how far back the subject-fallback matcher looks. Beyond
// this, a message with the same subject starts a new thread — matches the
// "conversation view" behaviour of every mainstream mail client.
const threadWindow = 30 * 24 * time.Hour

// resolveThreadTx assigns a thread_id to a message and returns whether the
// thread is newly created (so upsertThreadStatsTx can seed created_at etc).
//
// Resolution order (RFC 5322 + Gmail conventions):
//  1. In-Reply-To → the message being replied to.
//  2. References → walk newest → oldest, use the first that exists.
//  3. Subject-only fallback within threadWindow.
//  4. New thread.
func resolveThreadTx(ctx context.Context, tx *sql.Tx, m *storage.Message) (string, bool, error) {
	// Look up existing message by Message-ID header — those rows already
	// carry a thread_id we should reuse.
	lookupByMessageID := func(hid string) (string, bool, error) {
		hid = strings.TrimSpace(hid)
		if hid == "" {
			return "", false, nil
		}
		var threadID string
		err := tx.QueryRowContext(ctx,
			`SELECT thread_id FROM messages WHERE message_id = ? LIMIT 1`, hid,
		).Scan(&threadID)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		if threadID == "" {
			return "", false, nil
		}
		return threadID, true, nil
	}

	// 1. In-Reply-To.
	if id, found, err := lookupByMessageID(m.InReplyTo); err != nil {
		return "", false, err
	} else if found {
		return id, false, nil
	}

	// 2. References (newest first — the last entry is the most-recent ancestor).
	for i := len(m.References) - 1; i >= 0; i-- {
		if id, found, err := lookupByMessageID(m.References[i]); err != nil {
			return "", false, err
		} else if found {
			return id, false, nil
		}
	}

	// 3. Subject fallback within window.
	subj := normaliseSubject(m.Subject)
	if subj != "" {
		windowStart := m.ReceivedAt.Add(-threadWindow).Unix()
		var threadID string
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM threads
			WHERE subject_normalized = ? AND last_at >= ?
			ORDER BY last_at DESC LIMIT 1`,
			subj, windowStart,
		).Scan(&threadID)
		if err == nil {
			return threadID, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
	}

	// 4. New thread.
	return newThreadID(), true, nil
}

// upsertThreadStatsTx creates or updates the threads row after the message
// has been inserted. Participants is a distinct union of every from/to/cc
// address seen in the thread so far.
func upsertThreadStatsTx(ctx context.Context, tx *sql.Tx, m *storage.Message, isNew bool) error {
	// Collect participants from the current message.
	incoming := make([]string, 0, len(m.ToAddrs)+len(m.CcAddrs)+1)
	incoming = append(incoming, m.FromAddr)
	incoming = append(incoming, m.ToAddrs...)
	incoming = append(incoming, m.CcAddrs...)

	if isNew {
		participants, _ := json.Marshal(dedupeLower(incoming))
		_, err := tx.ExecContext(ctx, `
			INSERT INTO threads (id, subject_normalized, last_at, message_count,
			                    participants, created_at)
			VALUES (?, ?, ?, 1, ?, ?)`,
			m.ThreadID, normaliseSubject(m.Subject),
			m.ReceivedAt.Unix(), string(participants), m.ReceivedAt.Unix(),
		)
		return err
	}

	// Existing thread: merge participants + bump stats.
	var existingParticipantsJSON string
	err := tx.QueryRowContext(ctx,
		`SELECT participants FROM threads WHERE id = ?`, m.ThreadID,
	).Scan(&existingParticipantsJSON)
	if err != nil {
		return err
	}
	var existing []string
	_ = json.Unmarshal([]byte(existingParticipantsJSON), &existing)
	merged := dedupeLower(append(existing, incoming...))
	participants, _ := json.Marshal(merged)

	_, err = tx.ExecContext(ctx, `
		UPDATE threads
		SET last_at = MAX(last_at, ?),
		    message_count = message_count + 1,
		    participants = ?
		WHERE id = ?`,
		m.ReceivedAt.Unix(), string(participants), m.ThreadID,
	)
	return err
}

// ── read side ──────────────────────────────────────────────────────

func (s *Store) ListThreads(ctx context.Context, mailboxID string, limit, offset int) ([]*storage.Thread, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}

	// When a mailbox filter is supplied, we only surface threads that have
	// at least one message in that mailbox — this is what makes the
	// per-mailbox inbox pane in the dashboard show only relevant threads.
	var (
		rows *sql.Rows
		err  error
	)
	if mailboxID == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, subject_normalized, last_at, message_count, participants, created_at
			FROM threads
			ORDER BY last_at DESC
			LIMIT ? OFFSET ?`, limit, offset)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT t.id, t.subject_normalized, t.last_at, t.message_count,
			       t.participants, t.created_at
			FROM threads t
			WHERE EXISTS (
			    SELECT 1 FROM messages m
			    WHERE m.thread_id = t.id AND m.mailbox_id = ?
			)
			ORDER BY t.last_at DESC
			LIMIT ? OFFSET ?`, mailboxID, limit, offset)
	}
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

func (s *Store) GetThread(ctx context.Context, id string) (*storage.Thread, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, subject_normalized, last_at, message_count, participants, created_at
		FROM threads WHERE id = ?`, id)
	return scanThread(row)
}

func (s *Store) ListThreadMessages(ctx context.Context, threadID string) ([]*storage.Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, thread_id, mailbox_id, from_addr, to_addrs, cc_addrs, bcc_addrs,
		       subject, message_id, in_reply_to, refs, raw_path, size,
		       received_at, read_at
		FROM messages
		WHERE thread_id = ?
		ORDER BY received_at ASC`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*storage.Message, 0, 8)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ── helpers ────────────────────────────────────────────────────────

func scanThread(sc scanner) (*storage.Thread, error) {
	var t storage.Thread
	var last, created int64
	var participants string
	err := sc.Scan(&t.ID, &t.SubjectNormalized, &last, &t.MessageCount,
		&participants, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.LastAt = time.Unix(last, 0).UTC()
	t.CreatedAt = time.Unix(created, 0).UTC()
	_ = json.Unmarshal([]byte(participants), &t.Participants)
	return &t, nil
}

// normaliseSubject strips "Re:", "Fwd:", "Fw:" prefixes (repeated) and
// collapses whitespace. Case-insensitive. Empty string maps to empty (never
// used for matching — the resolver skips subject fallback when empty).
func normaliseSubject(s string) string {
	s = strings.TrimSpace(s)
	changed := true
	for changed {
		changed = false
		for _, prefix := range []string{"re:", "fwd:", "fw:"} {
			if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
				s = strings.TrimSpace(s[len(prefix):])
				changed = true
			}
		}
	}
	// Collapse internal whitespace.
	var b strings.Builder
	prevSP := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			if !prevSP {
				b.WriteByte(' ')
				prevSP = true
			}
			continue
		}
		b.WriteRune(r)
		prevSP = false
	}
	return strings.ToLower(b.String())
}

func dedupeLower(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		low := strings.ToLower(strings.TrimSpace(s))
		if low == "" {
			continue
		}
		if _, ok := seen[low]; ok {
			continue
		}
		seen[low] = struct{}{}
		out = append(out, low)
	}
	return out
}

func newThreadID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "thr_" + hex.EncodeToString(b)
}
