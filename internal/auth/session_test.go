package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
	"github.com/shamil3ilm/mail-service/internal/storage/sqlite"
)

func newStore(t *testing.T) storage.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedUser(t *testing.T, s storage.Store) *storage.User {
	t.Helper()
	u := &storage.User{
		ID:           "u_1",
		Email:        "admin@local",
		PasswordHash: "unused-in-this-test",
		IsAdmin:      true,
	}
	if err := s.InsertUser(context.Background(), u); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return u
}

func TestIssueLoadRevoke(t *testing.T) {
	s := newStore(t)
	u := seedUser(t, s)
	m := NewManager(s, false)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", nil)

	sess, err := m.Issue(req.Context(), rec, req, u)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Value != sess.ID {
		t.Fatalf("cookie not set")
	}

	// Load the session via a fresh request carrying the cookie.
	req2 := httptest.NewRequest(http.MethodGet, "/x", nil)
	req2.AddCookie(cookies[0])
	loaded, err := m.Load(req2.Context(), req2)
	if err != nil || loaded == nil || loaded.ID != u.ID {
		t.Fatalf("load: %v %+v", err, loaded)
	}

	// Revoke and confirm subsequent load returns nil.
	rec2 := httptest.NewRecorder()
	m.Revoke(req2.Context(), rec2, req2)

	req3 := httptest.NewRequest(http.MethodGet, "/x", nil)
	req3.AddCookie(cookies[0])
	loaded2, _ := m.Load(req3.Context(), req3)
	if loaded2 != nil {
		t.Fatalf("expected nil after revoke, got %+v", loaded2)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	s := newStore(t)
	u := seedUser(t, s)
	m := NewManager(s, false)

	// Insert an already-expired session directly.
	past := time.Now().Add(-time.Hour).UTC()
	sess := &storage.Session{
		ID: NewSessionID(), UserID: u.ID,
		CreatedAt: past.Add(-time.Hour), ExpiresAt: past,
	}
	if err := s.InsertSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: m.CookieName, Value: sess.ID})
	got, err := m.Load(req.Context(), req)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != nil {
		t.Fatalf("expected expired session to yield nil")
	}
}

func TestRequireUserBlocksAnonymous(t *testing.T) {
	handler := RequireUser(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}
