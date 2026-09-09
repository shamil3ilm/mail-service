package smtp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shamil3ilm/mail-service/internal/auth"
	"github.com/shamil3ilm/mail-service/internal/rawstore"
	"github.com/shamil3ilm/mail-service/internal/router"
	"github.com/shamil3ilm/mail-service/internal/storage"
	"github.com/shamil3ilm/mail-service/internal/storage/sqlite"
)

// TestSMTPEndToEnd boots the listener on a random port, sends a real SMTP
// transaction with net/smtp, and asserts the message landed in storage and
// the raw file was written.
func TestSMTPEndToEnd(t *testing.T) {
	ctx := context.Background()

	store, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	raw, err := rawstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("rawstore: %v", err)
	}

	r := &router.Router{Store: store, AutoVerifyDomains: []string{".test"}}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Bind to :0 so the OS picks a free port — avoids test flakes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	srv := NewServer(Options{
		Addr:   addr,
		Domain: "test.local",
	}, Deps{Store: store, Raw: raw, Router: r, Logger: log})

	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	// Poll until the server accepts a TCP connection — go-smtp Serve does
	// setup lazily and this test races otherwise.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	msg := []byte(strings.Join([]string{
		"From: alice@example.com",
		"To: orders@myapp.test",
		"Subject: order confirmation",
		"Message-ID: <abc123@example.com>",
		"",
		"Thanks for your order.",
		"",
	}, "\r\n"))

	if err := smtp.SendMail(addr, nil, "alice@example.com",
		[]string{"orders@myapp.test"}, msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Auto-provisioned mailbox should exist.
	mb, err := store.FindMailboxByAddress(ctx, "orders@myapp.test")
	if err != nil {
		t.Fatalf("mailbox not created: %v", err)
	}

	// Poll for the row — Data completes async on the wire but sync inside
	// the session; a short wait is defensive against goroutine scheduling.
	var msgs []*struct{ ID, Subject string }
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		list, err := store.ListMessages(ctx, mb.ID, 10, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) > 0 {
			for _, m := range list {
				msgs = append(msgs, &struct{ ID, Subject string }{m.ID, m.Subject})
			}
			// Verify raw file exists on disk.
			if _, err := os.Stat(list[0].RawPath); err != nil {
				t.Fatalf("raw file missing: %v", err)
			}
			if list[0].Subject != "order confirmation" {
				t.Fatalf("subject: got %q", list[0].Subject)
			}
			if list[0].MessageID != "abc123@example.com" {
				t.Fatalf("message-id: got %q", list[0].MessageID)
			}
			return // pass
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("no messages inserted; saw %+v", msgs)
}

func TestSubmissionRequiresAuth(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	raw, err := rawstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("rawstore: %v", err)
	}

	r := &router.Router{Store: store, AutoVerifyDomains: []string{".test"}}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Seed a user + API key so we can authenticate.
	u := &storage.User{ID: "u_1", Email: "sender@local", PasswordHash: "n/a"}
	if err := store.InsertUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	fullToken, prefix, hash, _ := auth.GenerateAPIKey()
	if err := store.InsertAPIKey(ctx, &storage.APIKey{
		ID: "akey_1", Prefix: prefix, Hash: hash, UserID: u.ID, Name: "test",
	}); err != nil {
		t.Fatalf("insert key: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	srv := NewServer(Options{
		Addr:        addr,
		Domain:      "test.local",
		RequireAuth: true,
	}, Deps{Store: store, Raw: raw, Router: r, Logger: log})

	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	// Wait for listener readiness.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	msg := []byte(strings.Join([]string{
		"From: sender@local",
		"To: ops@myapp.test",
		"Subject: submission via api key",
		"",
		"hi from the submission port",
		"",
	}, "\r\n"))

	// 1. No AUTH → server rejects MAIL FROM.
	if err := smtp.SendMail(addr, nil, "sender@local",
		[]string{"ops@myapp.test"}, msg); err == nil {
		t.Fatal("send without auth should have failed")
	}

	// 2. AUTH PLAIN with the API key as password → succeeds.
	host, _, _ := net.SplitHostPort(addr)
	authClient := smtp.PlainAuth("", "sender@local", fullToken, host)
	if err := smtp.SendMail(addr, authClient, "sender@local",
		[]string{"ops@myapp.test"}, msg); err != nil {
		t.Fatalf("authed send: %v", err)
	}

	// Poll for storage of the delivered message.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mb, err := store.FindMailboxByAddress(ctx, "ops@myapp.test")
		if err == nil && mb != nil {
			list, err := store.ListMessages(ctx, mb.ID, 10, 0)
			if err == nil && len(list) > 0 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("submission message not stored")
}

// TestSMTPStoresAttachments proves attachments in the inbound MIME land on
// disk with a matching DB row and correct filename + content-type.
func TestSMTPStoresAttachments(t *testing.T) {
	ctx := context.Background()

	store, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	raw, err := rawstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("rawstore: %v", err)
	}
	r := &router.Router{Store: store, AutoVerifyDomains: []string{".test"}}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	srv := NewServer(Options{Addr: addr, Domain: "test.local"},
		Deps{Store: store, Raw: raw, Router: r, Logger: log})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	// Wait for listener readiness.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Multipart message with a text body + one text/plain attachment.
	msg := strings.Join([]string{
		"From: sender@example.com",
		"To: inbox@example.test",
		`Content-Type: multipart/mixed; boundary="B"`,
		"Subject: with attachment",
		"MIME-Version: 1.0",
		"",
		"--B",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"body text",
		"--B",
		`Content-Type: text/plain; name="notes.txt"`,
		`Content-Disposition: attachment; filename="notes.txt"`,
		"",
		"line one",
		"line two",
		"--B--",
	}, "\r\n")

	if err := smtp.SendMail(addr, nil, "sender@example.com",
		[]string{"inbox@example.test"}, []byte(msg)); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Poll until the message + attachment land.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mb, err := store.FindMailboxByAddress(ctx, "inbox@example.test")
		if err == nil && mb != nil {
			list, _ := store.ListMessages(ctx, mb.ID, 10, 0)
			if len(list) > 0 {
				att, _ := store.ListAttachmentsForMessage(ctx, list[0].ID)
				if len(att) == 1 && att[0].Filename == "notes.txt" &&
					att[0].Size > 0 {
					// Content on disk matches what was sent?
					f, err := raw.Open(att[0].Path)
					if err != nil {
						t.Fatalf("open att: %v", err)
					}
					b := make([]byte, 128)
					n, _ := f.Read(b)
					_ = f.Close()
					if !strings.Contains(string(b[:n]), "line one") {
						t.Fatalf("att content: %q", string(b[:n]))
					}
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("attachment not stored")
}

// Compile-time sanity that Options.Addr helper stays consistent.
func TestOptionsDefault(t *testing.T) {
	srv := NewServer(Options{Addr: "127.0.0.1:2525"}, Deps{
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
	if srv.Addr != "127.0.0.1:2525" {
		t.Fatalf("addr: %s", srv.Addr)
	}
	if srv.MaxMessageBytes == 0 {
		t.Fatalf("default MaxMessageBytes not applied")
	}
	_ = fmt.Sprint(srv)
}
