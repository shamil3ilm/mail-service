package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

type attachmentDTO struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	MimeType string `json:"mime_type"`
	Size     int64  `json:"size"`
}

func toAttachmentDTO(a *storage.Attachment) attachmentDTO {
	return attachmentDTO{
		ID: a.ID, Filename: a.Filename,
		MimeType: a.MimeType, Size: a.Size,
	}
}

// listAttachments: GET /api/v1/messages/:id/attachments
// Metadata only — content is fetched via the download endpoint.
func (s *Server) listAttachments(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.Store.GetMessage(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "message not found")
			return
		}
		s.Logger.Error("get message", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	list, err := s.Store.ListAttachmentsForMessage(r.Context(), id)
	if err != nil {
		s.Logger.Error("list attachments", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]attachmentDTO, 0, len(list))
	for _, a := range list {
		out = append(out, toAttachmentDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"attachments": out})
}

// getAttachment: GET /api/v1/messages/:id/attachments/:aid
// Streams the file with the MIME type declared by the sender, and a
// Content-Disposition that sanitises the filename against header injection.
func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	msgID := chi.URLParam(r, "id")
	attID := chi.URLParam(r, "aid")

	a, err := s.Store.GetAttachment(r.Context(), attID)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "attachment not found")
		return
	}
	if err != nil {
		s.Logger.Error("get attachment", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	// Enforce parent-message match so an attacker can't guess an attachment
	// id and pull it via a message they don't otherwise have access to.
	// (Message access itself is already gated by auth middleware.)
	if a.MessageID != msgID {
		writeErr(w, http.StatusNotFound, "attachment not found")
		return
	}

	f, err := s.Raw.Open(a.Path)
	if err != nil {
		s.Logger.Error("open attachment", slog.String("err", err.Error()), slog.String("path", a.Path))
		writeErr(w, http.StatusInternalServerError, "attachment not available")
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", a.MimeType)
	w.Header().Set("Content-Length", strconv.FormatInt(a.Size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+headerSafeFilename(a.Filename)+`"`)
	if _, err := io.Copy(w, f); err != nil {
		s.Logger.Warn("stream attachment", slog.String("err", err.Error()))
	}
}

// headerSafeFilename strips characters that would break an HTTP header if
// echoed inside quotes. Belt-and-braces on top of the SMTP-side sanitiser.
func headerSafeFilename(name string) string {
	name = strings.ReplaceAll(name, `"`, "")
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	if name == "" {
		return "attachment"
	}
	return name
}
