package dnsx

import (
	"context"
	"net"
	"testing"
)

// fakeResolver is a table-backed Resolver for hermetic tests — no real DNS.
type fakeResolver struct {
	txt map[string][]string
	mx  map[string][]*net.MX
}

func (f *fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, ok := f.txt[name]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f *fakeResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if v, ok := f.mx[name]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func TestVerifyAllPass(t *testing.T) {
	k, _ := GenerateDKIM("ms1")
	recs := RecordsFor("example.com", k, PolicyConfig{})

	fake := &fakeResolver{
		txt: map[string][]string{
			"example.com":                  {"v=spf1 a mx ip4:0.0.0.0/0 -all"},
			"ms1._domainkey.example.com":   {"v=DKIM1; k=rsa; p=" + k.PublicB64},
			"_dmarc.example.com":           {"v=DMARC1; p=quarantine; rua=mailto:dmarc@example.com; adkim=r; aspf=r"},
		},
		mx: map[string][]*net.MX{
			"example.com": {{Host: "mail.example.com.", Pref: 10}},
		},
	}
	verdicts := Verify(context.Background(), fake, recs)
	if len(verdicts) != 4 {
		t.Fatalf("verdicts: %d", len(verdicts))
	}
	for _, v := range verdicts {
		if !v.Verified {
			t.Fatalf("kind %q not verified: %+v", v.Kind, v)
		}
	}
}

func TestVerifyMissingRecordsSurface(t *testing.T) {
	k, _ := GenerateDKIM("ms1")
	recs := RecordsFor("example.com", k, PolicyConfig{})
	fake := &fakeResolver{}

	verdicts := Verify(context.Background(), fake, recs)
	for _, v := range verdicts {
		if v.Verified {
			t.Fatalf("expected miss for %q", v.Kind)
		}
		if v.Error != "not found" {
			t.Fatalf("kind %q: expected 'not found', got %q", v.Kind, v.Error)
		}
	}
}

func TestTXTMatchesTolerantOfWhitespace(t *testing.T) {
	if !txtMatches("v=SPF1  a  mx  -all", "v=spf1 a mx -all") {
		t.Fatal("case + whitespace tolerance broken")
	}
	if txtMatches("v=spf1 -all", "v=spf1 a -all") {
		t.Fatal("false positive")
	}
}
