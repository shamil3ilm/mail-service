package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

type threadDTO struct {
	ID                string    `json:"id"`
	SubjectNormalized string    `json:"subject_normalized"`
	Subject           string    `json:"subject"` // subject of last message, human-readable
	LastAt            time.Time `json:"last_at"`
	MessageCount      int       `json:"message_count"`
	Participants      []string  `json:"participants"`
	CreatedAt         time.Time `json:"created_at"`
	// LastMessage is a compact preview of the most recent message so the
	// inbox row can show sender + snippet without a second round-trip.
	LastMessage *messageDTO `json:"last_message,omitempty"`
	Labels      []labelDTO  `json:"labels,omitempty"`
}

func toThreadDTO(t *storage.Thread, last *storage.Message, labels []*storage.Label) threadDTO {
	dto := threadDTO{
		ID:                t.ID,
		SubjectNormalized: t.SubjectNormalized,
		LastAt:            t.LastAt,
		MessageCount:      t.MessageCount,
		Participants:      t.Participants,
		CreatedAt:         t.CreatedAt,
	}
	if last != nil {
		dto.Subject = last.Subject
		m := toMessageDTO(last)
		dto.LastMessage = &m
	}
	dto.Labels = make([]labelDTO, 0, len(labels))
	for _, l := range labels {
		dto.Labels = append(dto.Labels, toLabelDTO(l))
	}
	return dto
}

// listThreads: GET /api/v1/threads?mailbox_id=&label_id=&limit=&offset=
func (s *Server) listThreads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mailboxID := q.Get("mailbox_id")
	labelID := q.Get("label_id")
	limit := parseIntDefault(q.Get("limit"), 50, 1, 500)
	offset := parseIntDefault(q.Get("offset"), 0, 0, 1_000_000)

	var threads []*storage.Thread
	var err error
	if labelID != "" {
		threads, err = s.Store.ListThreadsForLabel(r.Context(), labelID, limit, offset)
	} else {
		threads, err = s.Store.ListThreads(r.Context(), mailboxID, limit, offset)
	}
	if err != nil {
		s.Logger.Error("list threads", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}

	// For each thread fetch latest message + labels. The N+1 pattern is fine
	// at this list size; when we scale past ~200 threads/page we'll batch
	// via IN queries.
	out := make([]threadDTO, 0, len(threads))
	for _, t := range threads {
		msgs, _ := s.Store.ListThreadMessages(r.Context(), t.ID)
		var last *storage.Message
		if len(msgs) > 0 {
			last = msgs[len(msgs)-1]
		}
		labels, _ := s.Store.ListLabelsForThread(r.Context(), t.ID)
		out = append(out, toThreadDTO(t, last, labels))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"threads": out,
		"limit":   limit,
		"offset":  offset,
	})
}

// listThreadMessages: GET /api/v1/threads/:id/messages
func (s *Server) listThreadMessages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.Store.GetThread(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "thread not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	msgs, err := s.Store.ListThreadMessages(r.Context(), id)
	if err != nil {
		s.Logger.Error("list thread messages", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	// Inline attachment metadata per message so the UI can render chips
	// without an extra request per message.
	out := make([]messageDTO, 0, len(msgs))
	for _, m := range msgs {
		dto := toMessageDTO(m)
		if atts, err := s.Store.ListAttachmentsForMessage(r.Context(), m.ID); err == nil && len(atts) > 0 {
			list := make([]attachmentDTO, 0, len(atts))
			for _, a := range atts {
				list = append(list, toAttachmentDTO(a))
			}
			dto.Attachments = list
			dto.AttachmentCount = len(list)
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out})
}
