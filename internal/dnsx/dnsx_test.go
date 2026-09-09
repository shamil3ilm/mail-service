package dnsx

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

func TestGenerateDKIMRoundtrip(t *testing.T) {
	k, err := GenerateDKIM("ms1")
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if k.Selector != "ms1" {
		t.Fatalf("selector: %s", k.Selector)
	}

	// Private key parses back cleanly.
	block, _ := pem.Decode([]byte(k.PrivatePEM))
	if block == nil || block.Type != "RSA PRIVATE KEY" {
		t.Fatalf("private key not PEM PKCS#1")
	}
	priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse priv: %v", err)
	}
	if priv.N.BitLen() != DKIMKeyBits {
		t.Fatalf("key size: %d", priv.N.BitLen())
	}

	// Public key parses back cleanly and matches the private half.
	pubBytes, err := base64.StdEncoding.DecodeString(k.PublicB64)
	if err != nil {
		t.Fatalf("pub b64: %v", err)
	}
	pubAny, err := x509.ParsePKIXPublicKey(pubBytes)
	if err != nil {
		t.Fatalf("parse pub: %v", err)
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("public key is not RSA: %T", pubAny)
	}
	if pub.N.Cmp(priv.N) != 0 {
		t.Fatal("public and private moduli do not match")
	}
}

func TestGenerateDKIMDefaultsSelector(t *testing.T) {
	k, err := GenerateDKIM("")
	if err != nil {
		t.Fatal(err)
	}
	if k.Selector != "ms1" {
		t.Fatalf("default selector: %s", k.Selector)
	}
}

func TestRecordsForShape(t *testing.T) {
	k, _ := GenerateDKIM("ms1")
	recs := RecordsFor("example.com", k, PolicyConfig{})
	if len(recs) != 4 {
		t.Fatalf("expected 4 records, got %d", len(recs))
	}

	byKind := map[string]Record{}
	for _, r := range recs {
		byKind[r.Kind] = r
	}
	for _, want := range []string{"spf", "dkim", "dmarc", "mx"} {
		if _, ok := byKind[want]; !ok {
			t.Fatalf("missing record kind %q", want)
		}
	}
	if !strings.Contains(byKind["dkim"].Value, "v=DKIM1") {
		t.Fatalf("dkim value shape: %s", byKind["dkim"].Value)
	}
	if byKind["dkim"].Name != "ms1._domainkey.example.com" {
		t.Fatalf("dkim name: %s", byKind["dkim"].Name)
	}
	if byKind["mx"].Priority != 10 {
		t.Fatalf("mx priority: %d", byKind["mx"].Priority)
	}
}
