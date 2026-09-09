package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shamil3ilm/mail-service/internal/auth"
	"github.com/shamil3ilm/mail-service/internal/storage"
)

type labelDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	CreatedAt time.Time `json:"created_at"`
}

func toLabelDTO(l *storage.Label) labelDTO {
	return labelDTO{
		ID: l.ID, Name: l.Name, Color: l.Color, CreatedAt: l.CreatedAt,
	}
}

type createLabelReq struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// listLabels: GET /api/v1/labels
func (s *Server) listLabels(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	teamID, err := s.currentTeamID(r.Context(), u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	list, err := s.Store.ListLabelsForTeam(r.Context(), teamID)
	if err != nil {
		s.Logger.Error("list labels", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]labelDTO, 0, len(list))
	for _, l := range list {
		out = append(out, toLabelDTO(l))
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": out})
}

func (s *Server) createLabel(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	teamID, err := s.currentTeamID(r.Context(), u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req createLabelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}

	l := &storage.Label{
		ID:        newAPIID("lbl"),
		TeamID:    teamID,
		Name:      name,
		Color:     firstNonEmpty(req.Color, defaultLabelColor(name)),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.Store.InsertLabel(r.Context(), l); err != nil {
		if isUniqueViolation(err) {
			writeErr(w, http.StatusConflict, "label with that name already exists")
			return
		}
		s.Logger.Error("insert label", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusCreated, toLabelDTO(l))
}

func (s *Server) deleteLabel(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	teamID, err := s.currentTeamID(r.Context(), u)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.Store.DeleteLabel(r.Context(), id, teamID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "label not found")
			return
		}
		s.Logger.Error("delete label", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// applyLabelToThread: POST /api/v1/threads/:id/labels/:label_id
// Applies the label to every message in the thread. Idempotent.
func (s *Server) applyLabelToThread(w http.ResponseWriter, r *http.Request) {
	threadID := chi.URLParam(r, "id")
	labelID := chi.URLParam(r, "label_id")
	msgs, err := s.Store.ListThreadMessages(r.Context(), threadID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	for _, m := range msgs {
		if err := s.Store.LabelMessage(r.Context(), m.ID, labelID); err != nil {
			s.Logger.Error("label message", slog.String("err", err.Error()))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeLabelFromThread(w http.ResponseWriter, r *http.Request) {
	threadID := chi.URLParam(r, "id")
	labelID := chi.URLParam(r, "label_id")
	msgs, err := s.Store.ListThreadMessages(r.Context(), threadID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	for _, m := range msgs {
		if err := s.Store.UnlabelMessage(r.Context(), m.ID, labelID); err != nil {
			s.Logger.Error("unlabel message", slog.String("err", err.Error()))
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// listThreadLabels: GET /api/v1/threads/:id/labels — returns labels attached to
// any message in the thread.
func (s *Server) listThreadLabels(w http.ResponseWriter, r *http.Request) {
	threadID := chi.URLParam(r, "id")
	list, err := s.Store.ListLabelsForThread(r.Context(), threadID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]labelDTO, 0, len(list))
	for _, l := range list {
		out = append(out, toLabelDTO(l))
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": out})
}

// isUniqueViolation detects the SQLite unique-constraint error message so
// createLabel can map it to a clean 409 for the client.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// defaultLabelColor picks a stable palette colour from the label name so
// two "priority" labels always look the same, without a colour picker UI.
func defaultLabelColor(name string) string {
	palette := []string{
		"#2563eb", "#0891b2", "#059669", "#65a30d",
		"#d97706", "#dc2626", "#db2777", "#7c3aed",
	}
	var sum int
	for _, c := range strings.ToLower(name) {
		sum += int(c)
	}
	return palette[sum%len(palette)]
}
