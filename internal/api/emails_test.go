package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/shamil3ilm/mail-service/internal/auth"
	"github.com/shamil3ilm/mail-service/internal/events"
	"github.com/shamil3ilm/mail-service/internal/provider"
	"github.com/shamil3ilm/mail-service/internal/rawstore"
	"github.com/shamil3ilm/mail-service/internal/router"
	"github.com/shamil3ilm/mail-service/internal/storage/sqlite"
)

// newSendServer boots a Server wired up with the capture relay so send tests
// stay hermetic — no network, no upstream SMTP required.
func newSendServer(t *testing.T) (*Server, string, string, *http.Cookie) {
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
	rtr := &router.Router{Store: store, AutoVerifyDomains: []string{".test"}}
	t.Cleanup(func() {
		_ = store.Close()
		bus.Close()
	})

	email, password, err := auth.BootstrapAdmin(context.Background(), store, log)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	s := &Server{
		Store: store, Raw: raw, Bus: bus,
		Auth:              auth.NewManager(store, false),
		Relay:             &provider.Capture{Store: store, Raw: raw, Router: rtr, Bus: bus},
		Logger:            log,
		AutoVerifyDomains: []string{".test"},
	}

	// Log in and grab the cookie for authenticated requests.
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap login: %d %s", rec.Code, rec.Body.String())
	}
	return s, email, password, rec.Result().Cookies()[0]
}

func TestSendEmailEndToEnd(t *testing.T) {
	s, _, _, cookie := newSendServer(t)

	send := sendEmailReq{
		From:    "alice@example.com",
		To:      []string{"hello@myapp.test"},
		Subject: "day 5 send api works",
		Text:    "sent from the API and looped back into local storage",
	}
	body, _ := json.Marshal(send)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/emails", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}

	var resp sendEmailResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse resp: %v", err)
	}
	if len(resp.Accepted) != 1 || resp.Accepted[0] != "hello@myapp.test" {
		t.Fatalf("accepted: %+v", resp.Accepted)
	}

	// The capture provider should have looped the message into a mailbox
	// that /messages can now return.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/messages", nil)
	req2.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	s.Router().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("list: %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "day 5 send api works") {
		t.Fatalf("message not stored: %s", rec2.Body.String())
	}
}

func TestSendEmailValidation(t *testing.T) {
	s, _, _, cookie := newSendServer(t)

	cases := []struct {
		name string
		req  sendEmailReq
	}{
		{"missing from", sendEmailReq{To: []string{"x@y.test"}, Subject: "s", Text: "b"}},
		{"invalid from", sendEmailReq{From: "not-an-email", To: []string{"x@y.test"}, Subject: "s", Text: "b"}},
		{"no recipients", sendEmailReq{From: "a@b.com", Subject: "s", Text: "b"}},
		{"no subject", sendEmailReq{From: "a@b.com", To: []string{"x@y.test"}, Text: "b"}},
		{"no body", sendEmailReq{From: "a@b.com", To: []string{"x@y.test"}, Subject: "s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.req)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/emails", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			s.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSendEmailRequiresAuth(t *testing.T) {
	s, _, _, _ := newSendServer(t)
	body, _ := json.Marshal(sendEmailReq{
		From: "a@b.com", To: []string{"x@y.test"}, Subject: "s", Text: "b",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/emails", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestSendEmailIsDKIMSigned(t *testing.T) {
	s, _, _, cookie := newSendServer(t)

	body, _ := json.Marshal(sendEmailReq{
		From:    "alice@auto.test", // matches AutoVerifyDomains → auto-provisioned
		To:      []string{"bob@auto.test"},
		Subject: "signed test",
		Text:    "hello dkim",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/emails", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}

	// The capture provider stored the message with its raw MIME on disk.
	// Fetch and confirm the DKIM-Signature header is present.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/messages", nil)
	req2.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	s.Router().ServeHTTP(rec2, req2)
	var list struct {
		Messages []messageDTO `json:"messages"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &list)
	if len(list.Messages) == 0 {
		t.Fatal("no message stored")
	}
	msgID := list.Messages[0].ID

	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/messages/"+msgID+"/raw", nil)
	req3.AddCookie(cookie)
	rec3 := httptest.NewRecorder()
	s.Router().ServeHTTP(rec3, req3)
	if !bytes.Contains(rec3.Body.Bytes(), []byte("DKIM-Signature:")) {
		t.Fatalf("no DKIM-Signature in stored raw:\n%s", rec3.Body.String())
	}
	if !bytes.Contains(rec3.Body.Bytes(), []byte("d=auto.test")) {
		t.Fatalf("DKIM d= wrong: %s", rec3.Body.String())
	}
}

func TestComposeMIMEMultipart(t *testing.T) {
	body, err := composeMIME(&sendEmailReq{
		From: "a@b.com", To: []string{"x@y.test"},
		Subject: "hello",
		Text:    "plain body",
		HTML:    "<p>rich body</p>",
	})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	str := string(body)
	if !strings.Contains(str, "multipart/alternative") {
		t.Errorf("missing multipart header")
	}
	if !strings.Contains(str, "plain body") || !strings.Contains(str, "<p>rich body</p>") {
		t.Errorf("missing bodies")
	}
	if !strings.Contains(str, "Message-ID: <") {
		t.Errorf("missing Message-ID")
	}
}
