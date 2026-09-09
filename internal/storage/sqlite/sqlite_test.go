package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

// newTestStore returns an in-memory Store with migrations applied.
// Sub-millisecond setup — no Docker, no temp files.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrationsApply(t *testing.T) {
	s := newTestStore(t)
	if err := s.Ready(context.Background()); err != nil {
		t.Fatalf("ready after migrations: %v", err)
	}
}

func TestMailboxUpsertAndFind(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mb := &storage.Mailbox{
		ID:          "mbx_1",
		Address:     "hello@example.test",
		MatchType:   "exact",
		DisplayName: "Hello",
		Mode:        "receive",
		OwnerType:   "user",
		OwnerID:     "u_1",
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.UpsertMailbox(ctx, mb); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.FindMailboxByAddress(ctx, "hello@example.test")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.ID != mb.ID || got.Address != mb.Address {
		t.Fatalf("mismatch: %+v", got)
	}
}

func TestMessageInsertAndList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Seed a mailbox so we can attach the message to it.
	_ = s.UpsertMailbox(ctx, &storage.Mailbox{
		ID: "mbx_1", Address: "hi@example.test", MatchType: "exact",
		Mode: "receive", OwnerType: "user", OwnerID: "u_1",
	})

	m := &storage.Message{
		ID:         "msg_1",
		MailboxID:  "mbx_1",
		FromAddr:   "sender@example.com",
		ToAddrs:    []string{"hi@example.test"},
		Subject:    "hello",
		RawPath:    "/tmp/msg_1.eml",
		Size:       123,
		ReceivedAt: time.Now().UTC(),
	}
	if err := s.InsertMessage(ctx, m); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := s.GetMessage(ctx, "msg_1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Subject != "hello" || len(got.ToAddrs) != 1 || got.ToAddrs[0] != "hi@example.test" {
		t.Fatalf("mismatch: %+v", got)
	}

	list, err := s.ListMessages(ctx, "mbx_1", 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 message, got %d", len(list))
	}
}

func TestDeleteMessageMissing(t *testing.T) {
	s := newTestStore(t)
	err := s.DeleteMessage(context.Background(), "nope")
	if err != storage.ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
