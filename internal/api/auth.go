package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shamil3ilm/mail-service/internal/auth"
	"github.com/shamil3ilm/mail-service/internal/storage"
)

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type registerReq struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type userDTO struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	IsAdmin     bool      `json:"is_admin"`
	CreatedAt   time.Time `json:"created_at"`
}

func toUserDTO(u *storage.User) userDTO {
	return userDTO{
		ID: u.ID, Email: u.Email, DisplayName: u.DisplayName,
		IsAdmin: u.IsAdmin, CreatedAt: u.CreatedAt,
	}
}

// login accepts email+password, sets a session cookie on success.
// Response body is the user record; the cookie is the actual auth.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "email and password required")
		return
	}

	u, err := s.Store.GetUserByEmail(r.Context(), req.Email)
	if errors.Is(err, storage.ErrNotFound) {
		// Deliberately same error as bad password — don't leak which is wrong.
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		s.Logger.Error("login lookup", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	if err := auth.VerifyPassword(req.Password, u.PasswordHash); err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if _, err := s.Auth.Issue(r.Context(), w, r, u); err != nil {
		s.Logger.Error("login issue session", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	_ = s.Store.UpdateUserLastLogin(r.Context(), u.ID, time.Now().UTC())

	writeJSON(w, http.StatusOK, map[string]any{"user": toUserDTO(u)})
}

// logout clears the cookie and deletes the server-side session.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Auth.Revoke(r.Context(), w, r)
	w.WriteHeader(http.StatusNoContent)
}

// me returns the current user, or 401 if anonymous.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": toUserDTO(u)})
}

// register creates a new user. In local mode this is open (so devs can
// self-serve). In cloud mode it should be gated — for now we require an
// admin to be logged in. Public signup lands with the SaaS phase.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if s.CloudMode {
		caller := auth.UserFrom(r.Context())
		if caller == nil || !caller.IsAdmin {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
	}

	var req registerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "email and password required")
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 chars")
		return
	}

	if _, err := s.Store.GetUserByEmail(r.Context(), req.Email); err == nil {
		writeErr(w, http.StatusConflict, "email already registered")
		return
	} else if !errors.Is(err, storage.ErrNotFound) {
		s.Logger.Error("register lookup", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	u := &storage.User{
		ID:           newAPIID("usr"),
		Email:        req.Email,
		PasswordHash: hash,
		DisplayName:  req.DisplayName,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.Store.InsertUser(r.Context(), u); err != nil {
		s.Logger.Error("register insert", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": toUserDTO(u)})
}
