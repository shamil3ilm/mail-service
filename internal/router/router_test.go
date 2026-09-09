package router

import (
	"context"
	"testing"

	"github.com/shamil3ilm/mail-service/internal/storage"
	"github.com/shamil3ilm/mail-service/internal/storage/sqlite"
)

func newRouter(t *testing.T, autoVerify ...string) (*Router, storage.Store) {
	t.Helper()
	s, err := sqlite.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &Router{Store: s, AutoVerifyDomains: autoVerify}, s
}

func TestResolveExactMatch(t *testing.T) {
	r, store := newRouter(t)
	ctx := context.Background()

	_ = store.UpsertMailbox(ctx, &storage.Mailbox{
		ID: "mbx_a", Address: "hi@example.com",
		MatchType: "exact", Mode: "receive", OwnerType: "user", OwnerID: "u_1",
	})

	mb, err := r.Resolve(ctx, "HI@example.com")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if mb.ID != "mbx_a" {
		t.Fatalf("wrong mailbox: %s", mb.ID)
	}
}

func TestResolveAutoProvisionsForVerifiedSuffix(t *testing.T) {
	r, _ := newRouter(t, ".test", ".local")
	ctx := context.Background()

	mb, err := r.Resolve(ctx, "orders@myapp.test")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if mb.Address != "orders@myapp.test" {
		t.Fatalf("address: %s", mb.Address)
	}

	// Second call reuses the same mailbox (idempotent).
	again, _ := r.Resolve(ctx, "orders@myapp.test")
	if again.ID != mb.ID {
		t.Fatalf("expected reuse, got %s vs %s", mb.ID, again.ID)
	}
}

func TestResolveFallsBackToUnmatched(t *testing.T) {
	r, _ := newRouter(t) // no auto-verify
	ctx := context.Background()

	mb, err := r.Resolve(ctx, "someone@production.example")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if mb.ID != UnmatchedID {
		t.Fatalf("expected unmatched, got %s", mb.ID)
	}
}

func TestResolveDoesNotAutoProvisionForNonVerified(t *testing.T) {
	r, _ := newRouter(t, ".test")
	ctx := context.Background()

	mb, err := r.Resolve(ctx, "user@gmail.com")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if mb.ID != UnmatchedID {
		t.Fatalf("expected unmatched, got %s", mb.ID)
	}
}
