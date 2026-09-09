package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/shamil3ilm/mail-service/internal/auth"
	"github.com/shamil3ilm/mail-service/internal/events"
	"github.com/shamil3ilm/mail-service/internal/rawstore"
	"github.com/shamil3ilm/mail-service/internal/storage/sqlite"
)

// newAuthenticatedServer returns a Server with an active auth.Manager and a
// seeded admin user. Test cases can either hit /auth/login to get a cookie
// or attach one directly.
func newAuthenticatedServer(t *testing.T) (*Server, string /*email*/, string /*password*/) {
	t.Helper()
	store, err := sqlite.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	raw, err := rawstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("rawstore: %v", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	bus := events.NewMemory(log)
	t.Cleanup(func() {
		_ = store.Close()
		bus.Close()
	})

	email, password, err := auth.BootstrapAdmin(context.Background(), store, log)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	return &Server{
		Store:  store,
		Raw:    raw,
		Bus:    bus,
		Auth:   auth.NewManager(store, false),
		Logger: log,
	}, email, password
}

func TestLoginLogoutMe(t *testing.T) {
	s, email, password := newAuthenticatedServer(t)
	router := s.Router()

	// 1. /auth/me without cookie → 401
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("me pre-login: got %d want 401", rec.Code)
	}

	// 2. /auth/login with correct creds → 200 + cookie
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: got %d want 200 (%s)", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set a cookie")
	}
	cookie := cookies[0]

	// 3. /auth/me WITH cookie → 200 + user body
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("me post-login: got %d", rec.Code)
	}
	var meResp struct {
		User userDTO `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &meResp); err != nil {
		t.Fatalf("me body: %v", err)
	}
	if meResp.User.Email != email {
		t.Fatalf("me user: %+v", meResp.User)
	}

	// 4. /auth/logout invalidates the session server-side
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: got %d", rec.Code)
	}

	// 5. /auth/me with the now-revoked cookie → 401
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("me post-logout: got %d", rec.Code)
	}
}

func TestLoginBadCredentials(t *testing.T) {
	s, email, _ := newAuthenticatedServer(t)

	cases := []struct{ name, email, password string }{
		{"wrong password", email, "definitely-not-the-password"},
		{"unknown email", "nobody@nowhere.example", "irrelevant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"email": tc.email, "password": tc.password})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			s.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("got %d want 401", rec.Code)
			}
		})
	}
}

func TestProtectedRouteRequiresAuth(t *testing.T) {
	s, _, _ := newAuthenticatedServer(t)

	// No cookie — protected route should 401.
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/mailboxes", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", rec.Code)
	}
}
