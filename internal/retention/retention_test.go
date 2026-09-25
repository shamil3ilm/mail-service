package retention

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/shamil3ilm/mail-service/internal/rawstore"
	"github.com/shamil3ilm/mail-service/internal/storage"
	"github.com/shamil3ilm/mail-service/internal/storage/sqlite"
)

func TestSweepOnceDeletesOldMessages(t *testing.T) {
	ctx := context.Background()

	store, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	raw, err := rawstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("rawstore: %v", err)
	}

	// Seed one mailbox with two messages: one 100 days old (should die),
	// one 1 day old (should live).
	mb := &storage.Mailbox{
		ID: "mbx_1", Address: "inbox@example.test",
		MatchType: "exact", Mode: "receive",
		OwnerType: "user", OwnerID: "u_1",
	}
	if err := store.UpsertMailbox(ctx, mb); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	old := &storage.Message{
		ID: "msg_old", MailboxID: mb.ID, FromAddr: "a@x.com",
		ToAddrs: []string{"inbox@example.test"}, Subject: "old",
		MessageID: "old@x", RawPath: t.TempDir() + "/old.eml", Size: 10,
		ReceivedAt: now.Add(-100 * 24 * time.Hour),
	}
	fresh := &storage.Message{
		ID: "msg_fresh", MailboxID: mb.ID, FromAddr: "b@x.com",
		ToAddrs: []string{"inbox@example.test"}, Subject: "fresh",
		MessageID: "fresh@x", RawPath: t.TempDir() + "/fresh.eml", Size: 10,
		ReceivedAt: now.Add(-1 * 24 * time.Hour),
	}
	if err := store.InsertMessage(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertMessage(ctx, fresh); err != nil {
		t.Fatal(err)
	}

	runner := New(Config{GlobalDays: 30}, store, raw,
		slog.New(slog.NewTextHandler(os.Stderr, nil)))
	res := runner.SweepOnce(ctx)

	if res.Deleted != 1 {
		t.Errorf("deleted: got %d want 1", res.Deleted)
	}
	if res.Kept != 1 {
		t.Errorf("kept: got %d want 1 (mailbox counted, not per-msg)", res.Kept)
	}

	// Confirm exactly the fresh one survives.
	list, err := store.ListMessages(ctx, mb.ID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "msg_fresh" {
		t.Fatalf("after sweep: %+v", list)
	}
}

func TestSweepOnceNoOpWhenDisabled(t *testing.T) {
	store, _ := sqlite.Open(context.Background(), ":memory:")
	t.Cleanup(func() { _ = store.Close() })
	raw, _ := rawstore.New(t.TempDir())

	// GlobalDays=0 disables retention entirely.
	runner := New(Config{GlobalDays: 0}, store, raw,
		slog.New(slog.NewTextHandler(os.Stderr, nil)))
	// Run() should exit immediately when disabled; SweepOnce still runs
	// but produces zero work.
	res := runner.SweepOnce(context.Background())
	if res.Deleted != 0 {
		t.Errorf("expected no deletes when disabled")
	}
}
