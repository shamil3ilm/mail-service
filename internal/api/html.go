package api

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jhillyerd/enmime"

	"github.com/shamil3ilm/mail-service/internal/htmlsan"
	"github.com/shamil3ilm/mail-service/internal/storage"
)

// messageContentResp mirrors what Mailpit's detail view offers: sanitised
// HTML for rich rendering, the raw HTML source for inspection, the text
// alternative, a parsed header block, and the full raw .eml.
type messageContentResp struct {
	Text       string           `json:"text"`
	HTML       string           `json:"html,omitempty"`
	HTMLSource string           `json:"html_source,omitempty"` // untouched HTML from MIME
	HasHTML    bool             `json:"has_html"`
	Headers    []messageHeader  `json:"headers"`
	Raw        string           `json:"raw"`
	Size       int64            `json:"size"`
}

type messageHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// getMessageContent: GET /api/v1/messages/:id/content
// Returns every view the dashboard tabs offer (HTML, HTML Source, Text,
// Headers, Raw) plus the byte size. Re-parses raw MIME per call — parse
// is <5ms for typical mail, no caching needed.
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

	raw, err := io.ReadAll(f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "raw read")
		return
	}
	resp := messageContentResp{
		Raw:  string(raw),
		Size: int64(len(raw)),
	}

	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		// Broken MIME still deserves a best-effort answer — return the raw
		// so the operator can eyeball what went wrong.
		s.Logger.Warn("parse mime", slog.String("err", err.Error()))
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.Text = env.Text
	if source := env.HTML; source != "" {
		resp.HTMLSource = source
		cleaned := htmlsan.Sanitize(source, htmlsan.Options{
			BlockRemoteImages: true,
		})
		if cleaned != "" {
			resp.HTML = cleaned
			resp.HasHTML = true
		}
	}
	resp.Headers = extractHeaders(env, raw)

	writeJSON(w, http.StatusOK, resp)
}

// extractHeaders returns the ordered list of RFC 5322 headers as
// (name, value) pairs. Falls back to a linear scan of the raw bytes if
// the envelope doesn't expose them directly, so order is preserved.
func extractHeaders(env *enmime.Envelope, raw []byte) []messageHeader {
	// Header block is the raw prefix up to the first blank line.
	i := bytes.Index(raw, []byte("\r\n\r\n"))
	if i < 0 {
		i = bytes.Index(raw, []byte("\n\n"))
	}
	if i < 0 {
		return nil
	}
	block := string(raw[:i])
	// Unfold: RFC 5322 §2.2.3 — a line starting with WSP continues the
	// previous header. Join those before splitting on ':'.
	unfolded := make([]string, 0, 32)
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && len(unfolded) > 0 {
			unfolded[len(unfolded)-1] += " " + strings.TrimSpace(line)
			continue
		}
		unfolded = append(unfolded, line)
	}
	out := make([]messageHeader, 0, len(unfolded))
	for _, line := range unfolded {
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		out = append(out, messageHeader{
			Name:  strings.TrimSpace(line[:colon]),
			Value: strings.TrimSpace(line[colon+1:]),
		})
	}
	return out
}
