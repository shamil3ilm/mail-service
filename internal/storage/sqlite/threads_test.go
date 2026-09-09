package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

func seedMailbox(t *testing.T, s *Store, addr string) *storage.Mailbox {
	t.Helper()
	mb := &storage.Mailbox{
		ID: "mbx_" + addr, Address: addr, MatchType: "exact",
		Mode: "receive", OwnerType: "user", OwnerID: "u_1",
	}
	if err := s.UpsertMailbox(context.Background(), mb); err != nil {
		t.Fatal(err)
	}
	return mb
}

func insertMsg(t *testing.T, s *Store, m *storage.Message) {
	t.Helper()
	if err := s.InsertMessage(context.Background(), m); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestThreadingByInReplyTo(t *testing.T) {
	s := newTestStore(t)
	mb := seedMailbox(t, s, "hi@example.test")

	root := &storage.Message{
		ID: "m1", MailboxID: mb.ID, FromAddr: "a@x.com",
		ToAddrs: []string{"hi@example.test"}, Subject: "hello",
		MessageID: "root@x.com", ReceivedAt: time.Now().UTC(),
	}
	insertMsg(t, s, root)

	reply := &storage.Message{
		ID: "m2", MailboxID: mb.ID, FromAddr: "b@x.com",
		ToAddrs: []string{"hi@example.test"}, Subject: "Re: hello",
		MessageID: "reply@x.com", InReplyTo: "root@x.com",
		ReceivedAt: time.Now().UTC(),
	}
	insertMsg(t, s, reply)

	if root.ThreadID == "" || reply.ThreadID == "" {
		t.Fatal("thread ids not assigned")
	}
	if root.ThreadID != reply.ThreadID {
		t.Fatalf("expected same thread, got %s vs %s", root.ThreadID, reply.ThreadID)
	}

	th, err := s.GetThread(context.Background(), root.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if th.MessageCount != 2 {
		t.Fatalf("count: %d", th.MessageCount)
	}
	if th.SubjectNormalized != "hello" {
		t.Fatalf("subject normalize: %q", th.SubjectNormalized)
	}
}

func TestThreadingBySubjectFallback(t *testing.T) {
	s := newTestStore(t)
	mb := seedMailbox(t, s, "hi@example.test")

	a := &storage.Message{
		ID: "m1", MailboxID: mb.ID, FromAddr: "a@x.com",
		ToAddrs: []string{"hi@example.test"}, Subject: "order confirmation",
		MessageID: "a@x.com", ReceivedAt: time.Now().UTC(),
	}
	b := &storage.Message{
		ID: "m2", MailboxID: mb.ID, FromAddr: "b@x.com",
		ToAddrs: []string{"hi@example.test"}, Subject: "Re: Order Confirmation",
		MessageID: "b@x.com", // no In-Reply-To — subject fallback path
		ReceivedAt: time.Now().UTC().Add(time.Hour),
	}
	insertMsg(t, s, a)
	insertMsg(t, s, b)
	if a.ThreadID != b.ThreadID {
		t.Fatalf("subject fallback failed: %s vs %s", a.ThreadID, b.ThreadID)
	}
}

func TestThreadingDistinctForUnrelated(t *testing.T) {
	s := newTestStore(t)
	mb := seedMailbox(t, s, "hi@example.test")

	a := &storage.Message{
		ID: "m1", MailboxID: mb.ID, FromAddr: "a@x.com",
		ToAddrs: []string{"hi@example.test"}, Subject: "shipping notice",
		MessageID: "a@x.com", ReceivedAt: time.Now().UTC(),
	}
	b := &storage.Message{
		ID: "m2", MailboxID: mb.ID, FromAddr: "b@x.com",
		ToAddrs: []string{"hi@example.test"}, Subject: "unrelated",
		MessageID: "b@x.com", ReceivedAt: time.Now().UTC(),
	}
	insertMsg(t, s, a)
	insertMsg(t, s, b)
	if a.ThreadID == b.ThreadID {
		t.Fatal("expected distinct threads")
	}
}

func TestListThreadsFiltersByMailbox(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m1 := seedMailbox(t, s, "orders@example.test")
	m2 := seedMailbox(t, s, "support@example.test")

	insertMsg(t, s, &storage.Message{
		ID: "a", MailboxID: m1.ID, FromAddr: "x@x.com",
		ToAddrs: []string{"orders@example.test"}, Subject: "one",
		MessageID: "a@x.com", ReceivedAt: time.Now().UTC(),
	})
	insertMsg(t, s, &storage.Message{
		ID: "b", MailboxID: m2.ID, FromAddr: "y@x.com",
		ToAddrs: []string{"support@example.test"}, Subject: "two",
		MessageID: "b@x.com", ReceivedAt: time.Now().UTC(),
	})

	all, _ := s.ListThreads(ctx, "", 50, 0)
	if len(all) != 2 {
		t.Fatalf("all threads: %d", len(all))
	}
	only1, _ := s.ListThreads(ctx, m1.ID, 50, 0)
	if len(only1) != 1 {
		t.Fatalf("mailbox 1 threads: %d", len(only1))
	}
}

func TestNormaliseSubjectStripsPrefixes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Re: hello", "hello"},
		{"RE: FW: Re: hello there", "hello there"},
		{"Fwd:   Multiple   Spaces", "multiple spaces"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normaliseSubject(tc.in); got != tc.want {
			t.Errorf("normaliseSubject(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
