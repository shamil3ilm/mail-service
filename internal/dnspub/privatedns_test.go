package dnspub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shamil3ilm/mail-service/internal/dnsx"
)

// fakePrivateDNS is an httptest.Server that speaks just enough of the
// privatedns REST API to exercise our client end-to-end.
type fakePrivateDNS struct {
	*httptest.Server

	loginCalls   int
	zoneCreates  []string
	records      []struct{ Zone, Name, Type, Value string }
	returnRecords map[string][]map[string]any // by zone
}

func newFake(t *testing.T, expectedPassword string) *fakePrivateDNS {
	t.Helper()
	f := &fakePrivateDNS{
		returnRecords: make(map[string][]map[string]any),
	}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		f.loginCalls++
		var body struct{ Email, Password string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Password != expectedPassword {
			http.Error(w, "bad creds", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "fake.jwt.token",
			"expires_at": "2099-01-01T00:00:00Z",
		})
	})

	mux.HandleFunc("/api/v1/zones", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		var body struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		// idempotent — second create for same name returns 409, first is 201
		for _, z := range f.zoneCreates {
			if z == body.Name {
				http.Error(w, "conflict", http.StatusConflict)
				return
			}
		}
		f.zoneCreates = append(f.zoneCreates, body.Name)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"name": body.Name})
	})

	// PUT/GET /api/v1/zones/{zone}/records
	mux.HandleFunc("/api/v1/zones/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/zones/"), "/")
		if len(parts) < 2 || parts[1] != "records" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		zone := parts[0]

		switch r.Method {
		case http.MethodPut:
			var body struct{ Name, Type, Value string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.records = append(f.records, struct{ Zone, Name, Type, Value string }{
				zone, body.Name, body.Type, body.Value,
			})
			f.returnRecords[zone] = append(f.returnRecords[zone], map[string]any{
				"name":    body.Name,
				"type":    body.Type,
				"content": []string{body.Value},
			})
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(f.returnRecords[zone])
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func sampleRecords(zone string) []dnsx.Record {
	return []dnsx.Record{
		{Kind: "spf", Type: "TXT", Name: zone, Value: "v=spf1 -all"},
		{Kind: "dkim", Type: "TXT", Name: "ms1._domainkey." + zone, Value: "v=DKIM1; p=abc"},
		{Kind: "dmarc", Type: "TXT", Name: "_dmarc." + zone, Value: "v=DMARC1; p=quarantine"},
		{Kind: "mx", Type: "MX", Name: zone, Value: "mail." + zone, Priority: 10},
	}
}

func TestPublishCreatesZoneAndRecords(t *testing.T) {
	fake := newFake(t, "correct-password")

	pub := New(fake.URL, "admin@local", "correct-password", nil)
	if err := pub.Publish(context.Background(), "example.com", sampleRecords("example.com")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if fake.loginCalls != 1 {
		t.Errorf("expected 1 login, got %d", fake.loginCalls)
	}
	if len(fake.zoneCreates) != 1 || fake.zoneCreates[0] != "example.com" {
		t.Errorf("zone creates: %v", fake.zoneCreates)
	}
	if len(fake.records) != 4 {
		t.Errorf("expected 4 records, got %d", len(fake.records))
	}
	// MX record should be sent with priority prefix.
	for _, r := range fake.records {
		if r.Type == "MX" && r.Value != "10 mail.example.com" {
			t.Errorf("mx value: %s", r.Value)
		}
	}
}

func TestPublishJWTPassthrough(t *testing.T) {
	fake := newFake(t, "not-used")
	// Token has two dots → looks like a JWT, so client should skip login.
	pub := New(fake.URL, "", "aaa.bbb.ccc", nil)
	if err := pub.Publish(context.Background(), "example.com", sampleRecords("example.com")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if fake.loginCalls != 0 {
		t.Errorf("expected no login with pre-provided JWT, got %d", fake.loginCalls)
	}
}

func TestPublishExistingZoneIsIdempotent(t *testing.T) {
	fake := newFake(t, "pw")
	pub := New(fake.URL, "admin@local", "pw", nil)

	// First publish: zone gets created.
	if err := pub.Publish(context.Background(), "example.com", sampleRecords("example.com")); err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	// Second publish: zone POST returns 409, we swallow it.
	if err := pub.Publish(context.Background(), "example.com", sampleRecords("example.com")); err != nil {
		t.Fatalf("publish 2 (idempotent): %v", err)
	}
	// Zone created exactly once; records upserted twice.
	if len(fake.zoneCreates) != 1 {
		t.Errorf("zone creates: %v", fake.zoneCreates)
	}
	if len(fake.records) != 8 {
		t.Errorf("record upserts: %d", len(fake.records))
	}
}

func TestVerifyReadsBackFromPublisher(t *testing.T) {
	fake := newFake(t, "pw")
	pub := New(fake.URL, "admin@local", "pw", nil)
	recs := sampleRecords("example.com")
	_ = pub.Publish(context.Background(), "example.com", recs)

	verdicts, err := pub.Verify(context.Background(), "example.com", recs)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(verdicts) != len(recs) {
		t.Fatalf("verdicts count: %d", len(verdicts))
	}
	for _, v := range verdicts {
		if !v.Verified {
			t.Errorf("kind %q not verified — observed=%v", v.Kind, v.Observed)
		}
	}
}

func TestPublishBadCredsSurfaceError(t *testing.T) {
	fake := newFake(t, "correct-password")
	pub := New(fake.URL, "admin@local", "wrong-password", nil)
	err := pub.Publish(context.Background(), "example.com", sampleRecords("example.com"))
	if err == nil {
		t.Fatal("expected error with bad password")
	}
}

func TestRelativeName(t *testing.T) {
	cases := []struct{ fqdn, zone, want string }{
		{"example.com", "example.com", "@"},
		{"www.example.com", "example.com", "www"},
		{"ms1._domainkey.example.com", "example.com", "ms1._domainkey"},
		{"unrelated.other.tld", "example.com", "unrelated.other.tld"},
		{"EXAMPLE.COM", "example.com", "@"},
	}
	for _, tc := range cases {
		if got := relativeName(tc.fqdn, tc.zone); got != tc.want {
			t.Errorf("relativeName(%q,%q) = %q; want %q", tc.fqdn, tc.zone, got, tc.want)
		}
	}
}

// helpers referenced in the fake's error path
var _ = io.Discard
