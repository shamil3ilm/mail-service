package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

type suppressionDTO struct {
	Address   string    `json:"address"`
	Reason    string    `json:"reason"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

func toSuppressionDTO(s *storage.Suppression) suppressionDTO {
	return suppressionDTO{
		Address: s.Address, Reason: s.Reason,
		Source: s.Source, CreatedAt: s.CreatedAt,
	}
}

type createSuppressionReq struct {
	Address string `json:"address"`
	Reason  string `json:"reason"`
}

func (s *Server) listSuppressions(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListSuppressions(r.Context(), 200, 0)
	if err != nil {
		s.Logger.Error("list suppressions", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]suppressionDTO, 0, len(list))
	for _, x := range list {
		out = append(out, toSuppressionDTO(x))
	}
	writeJSON(w, http.StatusOK, map[string]any{"suppressions": out})
}

func (s *Server) createSuppression(w http.ResponseWriter, r *http.Request) {
	var req createSuppressionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Address = strings.ToLower(strings.TrimSpace(req.Address))
	if req.Address == "" {
		writeErr(w, http.StatusBadRequest, "address required")
		return
	}
	if req.Reason == "" {
		req.Reason = "manual"
	}
	sup := &storage.Suppression{
		Address:   req.Address,
		Reason:    req.Reason,
		Source:    "manual-ui",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.Store.UpsertSuppression(r.Context(), sup); err != nil {
		s.Logger.Error("upsert suppression", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusCreated, toSuppressionDTO(sup))
}

func (s *Server) removeSuppression(w http.ResponseWriter, r *http.Request) {
	addr := strings.ToLower(chi.URLParam(r, "address"))
	if err := s.Store.RemoveSuppression(r.Context(), addr); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		s.Logger.Error("remove suppression", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
