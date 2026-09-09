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

type apiKeyDTO struct {
	ID         string     `json:"id"`
	Prefix     string     `json:"prefix"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	MailboxID  string     `json:"mailbox_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

func toAPIKeyDTO(k *storage.APIKey) apiKeyDTO {
	return apiKeyDTO{
		ID: k.ID, Prefix: k.Prefix, Name: k.Name, Scopes: k.Scopes,
		MailboxID: k.MailboxID, CreatedAt: k.CreatedAt,
		LastUsedAt: k.LastUsedAt,
	}
}

type createAPIKeyReq struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`     // optional; default ["send","read"]
	MailboxID string   `json:"mailbox_id"` // optional
}

type createAPIKeyResp struct {
	Key   apiKeyDTO `json:"key"`
	Token string    `json:"token"` // returned ONCE; never persisted
}

// createAPIKey issues a new key. The full token is returned in the response
// body a single time. Storing only the sha256 hash means the user has one
// chance to copy it — same UX as GitHub / AWS / Stripe personal tokens.
func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req createAPIKeyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	if len(req.Scopes) == 0 {
		req.Scopes = []string{"send", "read"}
	}

	full, prefix, hash, err := auth.GenerateAPIKey()
	if err != nil {
		s.Logger.Error("apikey gen", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}

	k := &storage.APIKey{
		ID:        newAPIID("akey"),
		Prefix:    prefix,
		Hash:      hash,
		UserID:    u.ID,
		Name:      name,
		Scopes:    req.Scopes,
		MailboxID: req.MailboxID,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.Store.InsertAPIKey(r.Context(), k); err != nil {
		s.Logger.Error("apikey insert", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusCreated, createAPIKeyResp{
		Key:   toAPIKeyDTO(k),
		Token: full,
	})
}

// listAPIKeys returns keys for the current user (prefix + metadata only —
// the hash is scrubbed by the storage layer).
func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	list, err := s.Store.ListAPIKeysForUser(r.Context(), u.ID)
	if err != nil {
		s.Logger.Error("apikey list", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]apiKeyDTO, 0, len(list))
	for _, k := range list {
		out = append(out, toAPIKeyDTO(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// revokeAPIKey soft-deletes a key. Ownership is enforced at the storage
// layer via the userID filter.
func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.Store.RevokeAPIKey(r.Context(), id, u.ID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		s.Logger.Error("apikey revoke", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
