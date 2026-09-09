// PrivateDNS publisher — talks to a running privatedns instance
// (github.com/privatedns/native) via its REST API.
//
// The privatedns API surface we depend on (from api/openapi.yaml):
//
//	POST /api/v1/auth/login              → { token, expires_at, user }
//	POST /api/v1/zones                   → create zone (idempotent w/ 409)
//	PUT  /api/v1/zones/{zone}/records    → upsert a record set (REPLACE)
//	GET  /api/v1/zones/{zone}/records    → list record sets
//
// Auth: two modes. If PublisherToken looks like a JWT (has two '.' separators)
// or an API key (contains "_"), send it as `Authorization: Bearer <token>`.
// Otherwise treat it as a password and log in with PublisherUser/PublisherToken
// to fetch a JWT. Password mode is convenient for dev; API-key/JWT is
// preferred for prod.

package dnspub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shamil3ilm/mail-service/internal/dnsx"
)

// PrivateDNS is a Publisher backed by a privatedns REST API.
type PrivateDNS struct {
	BaseURL    string // e.g. "http://127.0.0.1:8080"
	Token      string // bearer token OR password (see auth mode above)
	User       string // only used when Token is a password
	HTTPClient *http.Client

	// cached JWT after password login
	mu           sync.Mutex
	jwt          string
	jwtExpiresAt time.Time
}

// New returns a ready PrivateDNS publisher. httpClient may be nil.
func New(baseURL, user, token string, httpClient *http.Client) *PrivateDNS {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &PrivateDNS{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		User:       user,
		HTTPClient: httpClient,
	}
}

func (*PrivateDNS) Name() string { return "privatedns" }

// Publish creates the zone if missing then upserts every record.
// REPLACE semantics on the record endpoint means re-running is safe.
func (p *PrivateDNS) Publish(ctx context.Context, zone string, records []dnsx.Record) error {
	if err := p.ensureZone(ctx, zone); err != nil {
		return fmt.Errorf("ensure zone %s: %w", zone, err)
	}
	for _, r := range records {
		if err := p.putRecord(ctx, zone, r); err != nil {
			return fmt.Errorf("upsert %s %s %s: %w", r.Type, r.Name, r.Value, err)
		}
	}
	return nil
}

// Verify reads the record sets from privatedns and compares to expected.
// Returns per-record verdicts using the same shape as dnsx.Verify so
// callers can merge public-DNS + publisher-side checks trivially.
//
// Records go in with FQDN names but privatedns stores + returns them
// relative to the zone (e.g. "www" not "www.example.com", "@" for apex).
// We collapse both sides to the relative form before matching.
func (p *PrivateDNS) Verify(ctx context.Context, zone string, records []dnsx.Record) ([]dnsx.Verdict, error) {
	got, err := p.listRecords(ctx, zone)
	if err != nil {
		return nil, err
	}
	out := make([]dnsx.Verdict, 0, len(records))
	for _, want := range records {
		v := dnsx.Verdict{Kind: want.Kind}
		wantName := relativeName(want.Name, zone)
		wantValue := want.Value
		if want.Type == "MX" {
			wantValue = fmt.Sprintf("%d %s", want.Priority, want.Value)
		}
		obs := findRecord(got, wantName, want.Type)
		v.Observed = obs
		for _, o := range obs {
			if valueMatches(o, wantValue, want.Type) {
				v.Verified = true
				break
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// ── HTTP plumbing ─────────────────────────────────────────────────

// ensureZone POSTs the zone; a 409 (already exists) is treated as success.
func (p *PrivateDNS) ensureZone(ctx context.Context, zone string) error {
	body, _ := json.Marshal(map[string]any{
		"name": zone,
	})
	resp, err := p.do(ctx, http.MethodPost, "/api/v1/zones", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK, http.StatusConflict:
		return nil
	default:
		return unexpectedStatus(resp)
	}
}

// putRecord upserts one record set. Record.Type is the DNS type ("MX", "TXT"),
// Record.Name is FQDN — we strip the zone suffix to get the relative name
// privatedns expects (its API uses relative names like "www" not "www.example.com").
func (p *PrivateDNS) putRecord(ctx context.Context, zone string, r dnsx.Record) error {
	name := relativeName(r.Name, zone)
	body := map[string]any{
		"name": name,
		"type": r.Type,
	}
	if r.Type == "MX" {
		body["value"] = fmt.Sprintf("%d %s", r.Priority, r.Value)
	} else {
		body["value"] = r.Value
	}
	buf, _ := json.Marshal(body)
	resp, err := p.do(ctx, http.MethodPut, "/api/v1/zones/"+zone+"/records", buf)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return unexpectedStatus(resp)
	}
	return nil
}

// listRecords fetches every record set in the zone. Returned map keys are
// "<name>|<type>" and values are the record's observed value(s).
type observedRecord struct {
	Name, Type string
	Values     []string
}

func (p *PrivateDNS) listRecords(ctx context.Context, zone string) ([]observedRecord, error) {
	resp, err := p.do(ctx, http.MethodGet, "/api/v1/zones/"+zone+"/records", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, unexpectedStatus(resp)
	}
	// privatedns returns [{name,type,ttl,content:[...]}] — content is the
	// authoritative field; some responses may also carry a legacy `value`.
	var raw []struct {
		Name    string   `json:"name"`
		Type    string   `json:"type"`
		Content []string `json:"content"`
		Value   string   `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode records: %w", err)
	}
	out := make([]observedRecord, 0, len(raw))
	for _, r := range raw {
		values := r.Content
		if len(values) == 0 && r.Value != "" {
			values = []string{r.Value}
		}
		out = append(out, observedRecord{Name: r.Name, Type: r.Type, Values: values})
	}
	return out, nil
}

// do issues an authenticated JSON request. If auth is a password, this
// transparently logs in and caches the JWT.
func (p *PrivateDNS) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	token, err := p.bearerToken(ctx)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return p.HTTPClient.Do(req)
}

// bearerToken returns a valid JWT — either a pre-provided one (JWT or API
// key that looks like a bearer credential) or a fresh login using the
// configured user + password.
func (p *PrivateDNS) bearerToken(ctx context.Context) (string, error) {
	if p.Token == "" {
		return "", errors.New("privatedns publisher: token required")
	}
	// If Token is a JWT (two dots) or looks like an API key (prefix + underscore),
	// pass it through unchanged. Otherwise treat as password and login.
	if isBearerCredential(p.Token) {
		return p.Token, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.jwt != "" && time.Now().Before(p.jwtExpiresAt.Add(-1*time.Minute)) {
		return p.jwt, nil
	}

	body, _ := json.Marshal(map[string]string{
		"email":    firstNonEmpty(p.User, "admin@local"),
		"password": p.Token,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", unexpectedStatus(resp)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode login: %w", err)
	}
	p.jwt = out.Token
	p.jwtExpiresAt = out.ExpiresAt
	return p.jwt, nil
}

// ── helpers ───────────────────────────────────────────────────────

func isBearerCredential(s string) bool {
	// Two dots → JWT; underscore-prefixed → API key (privatedns uses pdns_).
	// Everything else → treat as password (triggers login flow).
	return strings.Count(s, ".") == 2 || strings.Contains(s, "_")
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// relativeName strips the zone suffix from a FQDN so we send "www" instead
// of "www.example.com" (privatedns's API is relative-name shaped). If the
// FQDN equals the zone (apex record), returns "@" per DNS convention.
func relativeName(fqdn, zone string) string {
	fqdn = strings.TrimSuffix(strings.ToLower(fqdn), ".")
	zone = strings.TrimSuffix(strings.ToLower(zone), ".")
	if fqdn == zone {
		return "@"
	}
	if strings.HasSuffix(fqdn, "."+zone) {
		return fqdn[:len(fqdn)-len(zone)-1]
	}
	return fqdn // already relative or an unrelated name; caller decides
}

// findRecord looks up a record set by relative name + type.
// Both sides use privatedns's relative-name shape ("@" for apex, "www" not
// "www.example.com") so the compare is a straight case-insensitive equality.
func findRecord(all []observedRecord, relName, typ string) []string {
	for _, r := range all {
		if !strings.EqualFold(r.Type, typ) {
			continue
		}
		if strings.EqualFold(r.Name, relName) {
			return r.Values
		}
	}
	return nil
}

// valueMatches compares an observed record value to what we expect.
// TXT values are compared with whitespace collapsed to tolerate the
// quoting/chunking that resolvers apply. MX values need to match the
// "priority host" shape used by privatedns.
func valueMatches(observed, expected, typ string) bool {
	switch strings.ToUpper(typ) {
	case "TXT":
		return normaliseWS(observed) == normaliseWS(expected)
	default:
		return strings.EqualFold(strings.TrimSpace(observed), strings.TrimSpace(expected))
	}
}

func normaliseWS(s string) string {
	var b strings.Builder
	prev := false
	for _, r := range strings.TrimSpace(s) {
		if r == ' ' || r == '\t' || r == '\n' {
			if !prev {
				b.WriteByte(' ')
				prev = true
			}
			continue
		}
		b.WriteRune(r)
		prev = false
	}
	return strings.ToLower(b.String())
}

func unexpectedStatus(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("privatedns %s: %s", resp.Status, strings.TrimSpace(string(b)))
}
