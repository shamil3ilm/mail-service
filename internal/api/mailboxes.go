package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

type mailboxDTO struct {
	ID          string    `json:"id"`
	Address     string    `json:"address"`
	MatchType   string    `json:"match_type"`
	DisplayName string    `json:"display_name"`
	Mode        string    `json:"mode"`
	OwnerType   string    `json:"owner_type"`
	OwnerID     string    `json:"owner_id"`
	CreatedAt   time.Time `json:"created_at"`
}

func toMailboxDTO(m *storage.Mailbox) mailboxDTO {
	return mailboxDTO{
		ID: m.ID, Address: m.Address, MatchType: m.MatchType,
		DisplayName: m.DisplayName, Mode: m.Mode,
		OwnerType: m.OwnerType, OwnerID: m.OwnerID,
		CreatedAt: m.CreatedAt,
	}
}

type createMailboxReq struct {
	Address     string `json:"address"`
	MatchType   string `json:"match_type"`   // exact|wildcard|regex (default exact)
	DisplayName string `json:"display_name"` // optional
	Mode        string `json:"mode"`         // receive|send|both (default receive)
}

func (s *Server) listMailboxes(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListMailboxes(r.Context())
	if err != nil {
		s.Logger.Error("listMailboxes", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]mailboxDTO, 0, len(list))
	for _, m := range list {
		out = append(out, toMailboxDTO(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"mailboxes": out})
}

func (s *Server) getMailbox(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	mb, err := s.Store.GetMailbox(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "mailbox not found")
		return
	}
	if err != nil {
		s.Logger.Error("getMailbox", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, toMailboxDTO(mb))
}

func (s *Server) createMailbox(w http.ResponseWriter, r *http.Request) {
	var req createMailboxReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Address = strings.ToLower(strings.TrimSpace(req.Address))
	if req.Address == "" {
		writeErr(w, http.StatusBadRequest, "address required")
		return
	}
	if req.MatchType == "" {
		req.MatchType = "exact"
	}
	if req.Mode == "" {
		req.Mode = "receive"
	}

	mb := &storage.Mailbox{
		ID:          newAPIID("mbx"),
		Address:     req.Address,
		MatchType:   req.MatchType,
		DisplayName: req.DisplayName,
		Mode:        req.Mode,
		OwnerType:   "user",
		OwnerID:     "system",
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.Store.UpsertMailbox(r.Context(), mb); err != nil {
		s.Logger.Error("createMailbox", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusCreated, toMailboxDTO(mb))
}

func newAPIID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
