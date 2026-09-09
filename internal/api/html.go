package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jhillyerd/enmime"

	"github.com/shamil3ilm/mail-service/internal/htmlsan"
	"github.com/shamil3ilm/mail-service/internal/storage"
)

type messageContentResp struct {
	Text    string `json:"text"`
	HTML    string `json:"html,omitempty"`
	HasHTML bool   `json:"has_html"`
}

// getMessageContent: GET /api/v1/messages/:id/content
// Re-parses raw MIME on demand to pull text/plain + sanitised text/html.
// Reparsing per-request keeps storage lean — we never persist parsed
// bodies, only the raw .eml file, and the parse is fast enough (<5ms
// for typical mail) that caching would be premature optimisation.
func (s *Server) getMessageContent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	m, err := s.Store.GetMessage(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "message not found")
		return
	}
	if err != nil {
		s.Logger.Error("get message", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}

	f, err := s.Raw.Open(m.RawPath)
	if err != nil {
		s.Logger.Error("open raw", slog.String("err", err.Error()), slog.String("path", m.RawPath))
		writeErr(w, http.StatusInternalServerError, "raw not available")
		return
	}
	defer f.Close()

	env, err := enmime.ReadEnvelope(f)
	if err != nil {
		// Broken MIME still deserves a best-effort answer — return whatever
		// text we have and let the client show something.
		s.Logger.Warn("parse mime", slog.String("err", err.Error()))
		writeJSON(w, http.StatusOK, messageContentResp{Text: readAll(f)})
		return
	}

	resp := messageContentResp{
		Text: env.Text,
	}
	if raw := env.HTML; raw != "" {
		cleaned := htmlsan.Sanitize(raw, htmlsan.Options{
			// Default matches Gmail's "images off" — tracking pixels don't
			// fire on view. Users can flip to "show images" client-side later.
			BlockRemoteImages: true,
		})
		if cleaned != "" {
			resp.HTML = cleaned
			resp.HasHTML = true
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// readAll is a tiny helper used on the broken-MIME fallback path.
func readAll(r io.Reader) string {
	b, _ := io.ReadAll(r)
	return string(b)
}
