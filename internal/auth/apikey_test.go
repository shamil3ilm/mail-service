package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
	"github.com/shamil3ilm/mail-service/internal/storage/sqlite"
)

func TestGenerateAPIKeyFormat(t *testing.T) {
	full, prefix, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(full, APIKeyPrefix) {
		t.Fatalf("token missing prefix: %s", full)
	}
	if !strings.HasPrefix(full, prefix) {
		t.Fatalf("prefix mismatch: %s vs %s", prefix, full)
	}
	if len(prefix) != PrefixDisplayLen {
		t.Fatalf("prefix len: %d", len(prefix))
	}
	if hash != HashAPIKey(full) {
		t.Fatalf("hash mismatch")
	}
}

func TestGenerateAPIKeyDistinct(t *testing.T) {
	a, _, _, _ := GenerateAPIKey()
	b, _, _, _ := GenerateAPIKey()
	if a == b {
		t.Fatal("expected distinct tokens across calls")
	}
}

func TestParseBearer(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"Basic abc", ""},
		{"Bearer other_prefix_key", ""},
		{"Bearer msk_abcdef", "msk_abcdef"},
		{"Bearer   msk_abcdef  ", "msk_abcdef"},
	}
	for _, tc := range cases {
		if got := ParseBearer(tc.in); got != tc.want {
			t.Errorf("ParseBearer(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLoadBearerAuthenticates(t *testing.T) {
	store, err := sqlite.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Seed user + API key.
	u := &storage.User{ID: "u_1", Email: "x@x.test", PasswordHash: "n/a"}
	if err := store.InsertUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	full, prefix, hash, _ := GenerateAPIKey()
	k := &storage.APIKey{
		ID:     "akey_1",
		Prefix: prefix,
		Hash:   hash,
		UserID: u.ID,
		Name:   "test",
		Scopes: []string{"send"},
	}
	if err := store.InsertAPIKey(context.Background(), k); err != nil {
		t.Fatalf("insert key: %v", err)
	}

	m := NewManager(store, false)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+full)

	got, err := m.LoadBearer(req.Context(), req)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil || got.ID != u.ID {
		t.Fatalf("expected user %s, got %+v", u.ID, got)
	}

	// A revoked key should not authenticate.
	if err := store.RevokeAPIKey(context.Background(), k.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	got2, _ := m.LoadBearer(req.Context(), req)
	if got2 != nil {
		t.Fatalf("revoked key still authenticates: %+v", got2)
	}
}

func TestBearerLastUsedTouched(t *testing.T) {
	store, err := sqlite.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	u := &storage.User{ID: "u_1", Email: "x@x.test", PasswordHash: "n/a"}
	_ = store.InsertUser(context.Background(), u)

	full, prefix, hash, _ := GenerateAPIKey()
	k := &storage.APIKey{
		ID: "akey_2", Prefix: prefix, Hash: hash, UserID: u.ID, Name: "t",
	}
	_ = store.InsertAPIKey(context.Background(), k)

	m := NewManager(store, false)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+full)
	if _, err := m.LoadBearer(req.Context(), req); err != nil {
		t.Fatalf("load: %v", err)
	}

	// LastUsed is set in a goroutine — give it a moment.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		list, _ := store.ListAPIKeysForUser(context.Background(), u.ID)
		if len(list) == 1 && list[0].LastUsedAt != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("last_used_at was never populated")
}
