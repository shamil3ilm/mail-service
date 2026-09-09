package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shamil3ilm/mail-service/internal/events"
	"github.com/shamil3ilm/mail-service/internal/storage"
)

type messageDTO struct {
	ID              string          `json:"id"`
	ThreadID        string          `json:"thread_id"`
	MailboxID       string          `json:"mailbox_id"`
	FromAddr        string          `json:"from_addr"`
	ToAddrs         []string        `json:"to_addrs"`
	CcAddrs         []string        `json:"cc_addrs,omitempty"`
	BccAddrs        []string        `json:"bcc_addrs,omitempty"`
	Subject         string          `json:"subject"`
	MessageID       string          `json:"message_id,omitempty"`
	InReplyTo       string          `json:"in_reply_to,omitempty"`
	References      []string        `json:"references,omitempty"`
	Size            int64           `json:"size"`
	ReceivedAt      time.Time       `json:"received_at"`
	Read            bool            `json:"read"`
	AttachmentCount int             `json:"attachment_count,omitempty"`
	Attachments     []attachmentDTO `json:"attachments,omitempty"`
}

func toMessageDTO(m *storage.Message) messageDTO {
	return messageDTO{
		ID:         m.ID,
		ThreadID:   m.ThreadID,
		MailboxID:  m.MailboxID,
		FromAddr:   m.FromAddr,
		ToAddrs:    m.ToAddrs,
		CcAddrs:    m.CcAddrs,
		BccAddrs:   m.BccAddrs,
		Subject:    m.Subject,
		MessageID:  m.MessageID,
		InReplyTo:  m.InReplyTo,
		References: m.References,
		Size:       m.Size,
		ReceivedAt: m.ReceivedAt,
		Read:       m.ReadAt != nil,
	}
}

// GET /api/v1/messages?mailbox_id=...&limit=50&offset=0
func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mailboxID := q.Get("mailbox_id")
	limit := parseIntDefault(q.Get("limit"), 50, 1, 500)
	offset := parseIntDefault(q.Get("offset"), 0, 0, 1_000_000)

	list, err := s.Store.ListMessages(r.Context(), mailboxID, limit, offset)
	if err != nil {
		s.Logger.Error("listMessages", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]messageDTO, 0, len(list))
	for _, m := range list {
		out = append(out, toMessageDTO(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages": out,
		"limit":    limit,
		"offset":   offset,
	})
}

// GET /api/v1/messages/:id
func (s *Server) getMessage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	m, err := s.Store.GetMessage(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "message not found")
		return
	}
	if err != nil {
		s.Logger.Error("getMessage", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, toMessageDTO(m))
}

// GET /api/v1/messages/:id/raw — stream the raw .eml.
func (s *Server) getMessageRaw(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	m, err := s.Store.GetMessage(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "message not found")
		return
	}
	if err != nil {
		s.Logger.Error("getMessageRaw metadata", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	f, err := s.Raw.Open(m.RawPath)
	if err != nil {
		s.Logger.Error("getMessageRaw open", "err", err, "path", m.RawPath)
		writeErr(w, http.StatusInternalServerError, "raw not available")
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Length", strconv.FormatInt(m.Size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+m.ID+`.eml"`)
	if _, err := io.Copy(w, f); err != nil {
		s.Logger.Warn("getMessageRaw stream", "err", err)
	}
}

// DELETE /api/v1/messages/:id
func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	m, err := s.Store.GetMessage(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "message not found")
		return
	}
	if err != nil {
		s.Logger.Error("deleteMessage lookup", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	// Fetch attachment paths BEFORE deleting the message row — the FK
	// cascade will drop the attachment rows once the parent goes.
	attachments, _ := s.Store.ListAttachmentsForMessage(r.Context(), id)

	if err := s.Store.DeleteMessage(r.Context(), id); err != nil {
		s.Logger.Error("deleteMessage db", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	// Best-effort cleanup — DB is source of truth. Orphaned files get
	// swept by the retention job later if any of these fail here.
	_ = s.Raw.Delete(m.RawPath)
	for _, a := range attachments {
		_ = s.Raw.Delete(a.Path)
	}

	if s.Bus != nil {
		s.Bus.Publish(events.Event{
			Type: events.MessageDeleted, MessageID: id, MailboxID: m.MailboxID,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseIntDefault(s string, def, min, max int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < min {
		return def
	}
	if n > max {
		return max
	}
	return n
}
