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
	"time"

	"github.com/shamil3ilm/mail-service/internal/events"
	"github.com/shamil3ilm/mail-service/internal/rawstore"
	"github.com/shamil3ilm/mail-service/internal/storage"
	"github.com/shamil3ilm/mail-service/internal/storage/sqlite"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := sqlite.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
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
	return &Server{Store: store, Raw: raw, Bus: bus, Logger: log}
}

func TestHealthz(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
}

func TestCreateAndListMailbox(t *testing.T) {
	s := newTestServer(t)

	body := bytes.NewBufferString(`{"address":"orders@example.test","display_name":"Orders"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mailboxes", body)
	req.Header.Set("Content-Type", "application/json")
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/mailboxes", nil)
	s.Router().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("list: %d", rec2.Code)
	}
	var resp struct {
		Mailboxes []mailboxDTO `json:"mailboxes"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	if len(resp.Mailboxes) != 1 || resp.Mailboxes[0].Address != "orders@example.test" {
		t.Fatalf("list body: %s", rec2.Body.String())
	}
}

func TestListMessagesEmpty(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/messages", nil)
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var resp struct {
		Messages []messageDTO `json:"messages"`
		Limit    int          `json:"limit"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Limit != 50 {
		t.Fatalf("default limit: %d", resp.Limit)
	}
	if len(resp.Messages) != 0 {
		t.Fatalf("expected empty")
	}
}

func TestSSEStreamEmitsHelloAndEvent(t *testing.T) {
	s := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start the request; the handler blocks until the context is cancelled.
	// We read the initial "hello" + one published event, then cancel.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() { defer close(done); s.Router().ServeHTTP(rec, req) }()

	// Publish after a tiny delay so we're subscribed first.
	time.AfterFunc(50*time.Millisecond, func() {
		s.Bus.Publish(events.Event{
			Type: events.MessageReceived, MessageID: "m1",
			Subject: "hi", FromAddr: "a@x.com", ToAddr: "b@y.com",
		})
	})

	// Give the handler time to write hello + event, then close.
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if !bytes.Contains([]byte(body), []byte("event: hello")) {
		t.Fatalf("missing hello event; body=%q", body)
	}
	if !bytes.Contains([]byte(body), []byte("event: message.received")) {
		t.Fatalf("missing message.received event; body=%q", body)
	}
	if !bytes.Contains([]byte(body), []byte("\"message_id\":\"m1\"")) {
		t.Fatalf("missing message_id payload; body=%q", body)
	}
}

func TestGetMessageAndRaw(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	// Seed a mailbox + message + raw file so we can exercise the read path.
	_ = s.Store.UpsertMailbox(ctx, &storage.Mailbox{
		ID: "mbx_1", Address: "hi@example.test", MatchType: "exact",
		Mode: "receive", OwnerType: "user", OwnerID: "u_1",
	})
	when := time.Now().UTC()
	rawPath, _, err := s.Raw.Write("msg_a", when, bytes.NewBufferString("From: a@example.com\r\n\r\nhi"))
	if err != nil {
		t.Fatalf("raw: %v", err)
	}
	if err := s.Store.InsertMessage(ctx, &storage.Message{
		ID: "msg_a", MailboxID: "mbx_1", FromAddr: "a@example.com",
		ToAddrs: []string{"hi@example.test"}, Subject: "hi",
		RawPath: rawPath, Size: 24, ReceivedAt: when,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/messages/msg_a", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	s.Router().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/messages/msg_a/raw", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("raw: %d", rec2.Code)
	}
	if got := rec2.Header().Get("Content-Type"); got != "message/rfc822" {
		t.Fatalf("content-type: %s", got)
	}
	if !bytes.Contains(rec2.Body.Bytes(), []byte("From: a@example.com")) {
		t.Fatalf("raw body missing headers")
	}
}
