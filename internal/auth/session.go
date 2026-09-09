package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

// SessionCookieName is the name of the cookie carrying the session ID.
// Prefixed with __Host- for defence-in-depth in HTTPS contexts (locks the
// cookie to the exact host, forbids Domain=, requires Path=/, Secure).
// We only apply that prefix in cloud mode; local uses a plain name so the
// cookie works over http://127.0.0.1.
const (
	SessionCookieLocal = "mail_session"
	SessionCookieCloud = "__Host-mail_session"

	// SessionTTL is how long a session lives before requiring re-login.
	SessionTTL = 30 * 24 * time.Hour
)

// contextKey is unexported so callers use With/User helpers.
type contextKey struct{}

// NewSessionID returns a 32-byte random hex string suitable for use as a
// cookie value. 256 bits of entropy — infeasible to guess.
func NewSessionID() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Manager owns session cookies + the DB rows behind them.
type Manager struct {
	Store      storage.Store
	CookieName string
	Secure     bool // true in cloud mode
}

// NewManager returns a Manager configured for the current deployment mode.
func NewManager(store storage.Store, cloudMode bool) *Manager {
	name := SessionCookieLocal
	if cloudMode {
		name = SessionCookieCloud
	}
	return &Manager{Store: store, CookieName: name, Secure: cloudMode}
}

// Issue creates a session row, sets the cookie, and returns the Session.
// Called by /auth/login after credentials validate.
func (m *Manager) Issue(ctx context.Context, w http.ResponseWriter, r *http.Request, user *storage.User) (*storage.Session, error) {
	now := time.Now().UTC()
	s := &storage.Session{
		ID:        NewSessionID(),
		UserID:    user.ID,
		CreatedAt: now,
		ExpiresAt: now.Add(SessionTTL),
		UserAgent: r.UserAgent(),
		IPAddr:    clientIP(r),
	}
	if err := m.Store.InsertSession(ctx, s); err != nil {
		return nil, err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     m.CookieName,
		Value:    s.ID,
		Path:     "/",
		Expires:  s.ExpiresAt,
		MaxAge:   int(SessionTTL / time.Second),
		HttpOnly: true,
		Secure:   m.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	return s, nil
}

// Revoke deletes the session and clears the cookie. Idempotent.
func (m *Manager) Revoke(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(m.CookieName); err == nil && c.Value != "" {
		_ = m.Store.DeleteSession(ctx, c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     m.CookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Load looks up the session referenced by the request's cookie.
// Returns (nil, nil) for missing/expired — that's not an error, just anon.
func (m *Manager) Load(ctx context.Context, r *http.Request) (*storage.User, error) {
	c, err := r.Cookie(m.CookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	s, err := m.Store.GetSession(ctx, c.Value)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(s.ExpiresAt) {
		_ = m.Store.DeleteSession(ctx, s.ID)
		return nil, nil
	}
	u, err := m.Store.GetUserByID(ctx, s.UserID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	return u, err
}

// Middleware loads the session (if any) and stashes the User in context.
// Never blocks the request — protected routes check RequireUser separately.
//
// Accepts either:
//   - a session cookie (browser dashboard traffic), OR
//   - an "Authorization: Bearer msk_..." header (app-to-service traffic).
// Both paths end up populating the same context key with a *storage.User.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := m.Load(r.Context(), r)
		if err == nil && u == nil {
			// No session cookie — try bearer.
			u, _ = m.LoadBearer(r.Context(), r)
		}
		if err == nil && u != nil {
			r = r.WithContext(context.WithValue(r.Context(), contextKey{}, u))
		}
		next.ServeHTTP(w, r)
	})
}

// LoadBearer authenticates by API key. Returns (nil, nil) if no bearer is
// present or the key is unknown/revoked — same "anon is not an error" rule
// as Load, so callers don't need to distinguish "no cookie" from "no bearer".
func (m *Manager) LoadBearer(ctx context.Context, r *http.Request) (*storage.User, error) {
	tok := ParseBearer(r.Header.Get("Authorization"))
	if tok == "" {
		return nil, nil
	}
	key, err := m.Store.GetAPIKeyByHash(ctx, HashAPIKey(tok))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Fire-and-forget touch. Skip if the DB errors — auth already succeeded.
	go func(id string) {
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = m.Store.TouchAPIKey(bg, id, time.Now().UTC())
	}(key.ID)

	u, err := m.Store.GetUserByID(ctx, key.UserID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	return u, err
}

// RequireUser is a middleware that 401s if no session is attached.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFrom(r.Context()) == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// UserFrom returns the authenticated user from context, or nil.
func UserFrom(ctx context.Context) *storage.User {
	u, _ := ctx.Value(contextKey{}).(*storage.User)
	return u
}

// clientIP returns the best-guess client IP. RemoteAddr is host:port; we
// only want host. In cloud mode a reverse proxy will set X-Forwarded-For,
// but we don't trust it by default until we have proxy whitelisting.
func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return addr[:i]
	}
	return addr
}
