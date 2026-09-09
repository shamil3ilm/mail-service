package dkim

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/shamil3ilm/mail-service/internal/dnsx"
)

// TestSignInsertsHeader is a smoke test: signing a well-formed message
// produces a DKIM-Signature at the top with the expected tags.
func TestSignInsertsHeader(t *testing.T) {
	key, err := dnsx.GenerateDKIM("ms1")
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	msg := []byte(strings.Join([]string{
		"From: alice@example.com",
		"To: bob@example.net",
		"Subject: test",
		"Message-ID: <abc@example.com>",
		"Date: Tue, 09 Sep 2026 10:00:00 +0000",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"hello world",
		"",
	}, "\r\n"))

	signed, err := Sign(msg, Options{
		Domain:        "example.com",
		Selector:      "ms1",
		PrivateKeyPEM: key.PrivatePEM,
		Now:           time.Unix(1_700_000_000, 0),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !bytes.HasPrefix(signed, []byte("DKIM-Signature: ")) {
		t.Fatalf("signed msg missing header prefix; got:\n%s", string(signed)[:200])
	}
	for _, tag := range []string{"v=1", "a=rsa-sha256", "c=relaxed/relaxed",
		"d=example.com", "s=ms1", "h=From:To:Subject:Date:Message-ID",
		"bh=", "b="} {
		if !bytes.Contains(signed, []byte(tag)) {
			t.Errorf("missing tag %q", tag)
		}
	}
}

// TestSignVerifiesRoundtrip proves the signature we produce actually
// verifies against the corresponding public key using the same
// canonicalisation rules — catches drift between sign and verify halves.
func TestSignVerifiesRoundtrip(t *testing.T) {
	key, _ := dnsx.GenerateDKIM("ms1")

	msg := []byte(strings.Join([]string{
		"From: alice@example.com",
		"To: bob@example.net",
		"Subject: verify me",
		"Date: Tue, 09 Sep 2026 10:00:00 +0000",
		"Message-ID: <verify@example.com>",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"body line one",
		"body line two",
		"",
	}, "\r\n"))

	signed, err := Sign(msg, Options{
		Domain: "example.com", Selector: "ms1",
		PrivateKeyPEM: key.PrivatePEM,
		Now:           time.Unix(1_700_000_000, 0),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Extract the DKIM-Signature header and split off b= for verification.
	firstCRLF := bytes.Index(signed, []byte("\r\n\r\n"))
	if firstCRLF < 0 {
		t.Fatal("no header/body split")
	}
	// Find the DKIM-Signature header block (may be multi-line — we produce it as such).
	dkimEnd := bytes.Index(signed, []byte("\r\nFrom:"))
	if dkimEnd < 0 {
		t.Fatal("no next header found")
	}
	dkimHeader := string(signed[:dkimEnd])

	sigB64 := extractTag(dkimHeader, "b=")
	if sigB64 == "" {
		t.Fatal("no b= tag")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}

	// Reconstruct signing input: canonicalised signed headers + partial
	// DKIM-Signature (b= empty).
	// The original message (without our signature) is signed[dkimEnd+2:]
	origMsg := signed[dkimEnd+2:]
	origHeaders, origBody, err := splitHeadersBody(origMsg)
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	bh := sha256.Sum256(canonicaliseBodyRelaxed(origBody))
	bhB64 := base64.StdEncoding.EncodeToString(bh[:])
	if bhB64 != extractTag(dkimHeader, "bh=") {
		t.Fatalf("bh mismatch")
	}

	hList := strings.Split(extractTag(dkimHeader, "h="), ":")
	signedHdrs := canonicaliseHeadersForSigning(origHeaders, hList)

	// Zero out b= to reproduce the input the signer used.
	stripped := stripBTag(dkimHeader)
	signedHdrs = append(signedHdrs, canonicaliseDKIMHeader(stripped)...)

	digest := sha256.Sum256(signedHdrs)

	pubAny, err := parseTestPubKey(key.PublicB64)
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(pubAny, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// TestSignRejectsMissingFrom guards the RFC 6376 requirement.
func TestSignRejectsMissingFrom(t *testing.T) {
	key, _ := dnsx.GenerateDKIM("ms1")
	msg := []byte("Subject: no from\r\n\r\nbody")
	_, err := Sign(msg, Options{
		Domain: "example.com", Selector: "ms1",
		PrivateKeyPEM: key.PrivatePEM,
	})
	if err == nil {
		t.Fatal("expected error when From missing")
	}
}

func TestCanonicaliseBodyRelaxed(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"single line", "hi\r\n", "hi\r\n"},
		{"collapses spaces", "hi   there\r\n", "hi there\r\n"},
		{"strips trailing ws", "hi \t\r\n", "hi\r\n"},
		{"strips trailing blank lines", "a\r\n\r\n\r\n", "a\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(canonicaliseBodyRelaxed([]byte(tc.in)))
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

// ── helpers ─────────────────────────────────────────────────────────

func extractTag(header, tag string) string {
	// Naive scan: find "tag" preceded by "; " or start; take up to next ";" or line end.
	// Good enough for our own well-formed output.
	idx := strings.Index(header, tag)
	if idx < 0 {
		return ""
	}
	rest := header[idx+len(tag):]
	// Value ends at next ';' or CRLF or end.
	end := len(rest)
	for i, c := range rest {
		if c == ';' || c == '\r' || c == '\n' {
			end = i
			break
		}
	}
	return strings.TrimSpace(rest[:end])
}

func stripBTag(header string) string {
	// Replace whatever follows "b=" with empty, up to next ';' or end.
	idx := strings.Index(header, "b=")
	if idx < 0 {
		return header
	}
	rest := header[idx+2:]
	end := len(rest)
	for i, c := range rest {
		if c == ';' || c == '\r' || c == '\n' {
			end = i
			break
		}
	}
	return header[:idx+2] + header[idx+2+end:]
}

func parseTestPubKey(b64 string) (*rsa.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	return parsePubKeyPKIX(raw)
}
