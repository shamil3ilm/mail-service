// Package dkim signs outbound RFC 5322 messages per RFC 6376 (DKIM).
//
// Uses only the standard library — no external DKIM implementation needed.
// Scope is deliberately small:
//   - RSA-SHA256 signatures only (v=1; a=rsa-sha256)
//   - relaxed/relaxed header + body canonicalization (widest compatibility)
//   - fixed header list (From, To, Cc, Subject, Date, Message-ID, MIME-Version)
//
// The relaxed algorithm is a good default: strict enough to catch tampering,
// lenient enough that intermediate MTAs' whitespace-normalisation doesn't
// break the signature.
package dkim

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// signedHeaders is the set of headers a valid mail-service signature always
// covers. From is required by RFC 6376 §3.5; the rest are hardening against
// From-alignment attacks and header-swap tampering.
var signedHeaders = []string{
	"From", "To", "Cc", "Subject", "Date", "Message-ID",
	"MIME-Version", "Content-Type", "Content-Transfer-Encoding",
	"Reply-To",
}

// Options controls a single Sign call.
type Options struct {
	Domain   string // d= tag ("example.com")
	Selector string // s= tag ("ms1")
	// PrivateKeyPEM is a PEM-encoded RSA private key in PKCS#1 form
	// (as produced by dnsx.GenerateDKIM).
	PrivateKeyPEM string
	// Now is injectable for tests. Zero → time.Now.
	Now time.Time
}

// Sign returns raw with a DKIM-Signature header prepended.
// The input must already use CRLF line endings.
func Sign(raw []byte, opts Options) ([]byte, error) {
	if opts.Domain == "" {
		return nil, errors.New("dkim: domain required")
	}
	if opts.Selector == "" {
		return nil, errors.New("dkim: selector required")
	}
	priv, err := parsePrivateKey(opts.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("dkim: parse key: %w", err)
	}

	headerBlock, body, err := splitHeadersBody(raw)
	if err != nil {
		return nil, err
	}

	// 1. Body canonicalisation (relaxed) → SHA-256 → base64.
	bh := sha256.Sum256(canonicaliseBodyRelaxed(body))
	bhB64 := base64.StdEncoding.EncodeToString(bh[:])

	// 2. Determine which of signedHeaders are actually present, in order.
	//    Only present headers go into h= — signing missing ones is an error.
	presentHeaders := collectPresentHeaders(headerBlock, signedHeaders)
	if !containsCI(presentHeaders, "From") {
		return nil, errors.New("dkim: From header required")
	}

	// 3. Build the partial DKIM-Signature header (b= empty for signing input).
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	partial := buildDKIMHeader(dkimTags{
		Version:    "1",
		Algorithm:  "rsa-sha256",
		Canon:      "relaxed/relaxed",
		Domain:     opts.Domain,
		Selector:   opts.Selector,
		Timestamp:  now.UTC().Unix(),
		Headers:    presentHeaders,
		BodyHashB64: bhB64,
		Signature:  "", // b= empty when hashing
	})

	// 4. Canonicalise headers being signed + the DKIM-Signature header itself,
	//    then compute the RSA-SHA256 signature over the concatenation.
	signedHeadersBytes := canonicaliseHeadersForSigning(headerBlock, presentHeaders)
	signedHeadersBytes = append(signedHeadersBytes, canonicaliseDKIMHeader(partial)...)

	digest := sha256.Sum256(signedHeadersBytes)
	sig, err := rsa.SignPKCS1v15(nil, priv, crypto.SHA256, digest[:])
	if err != nil {
		return nil, fmt.Errorf("dkim: sign: %w", err)
	}
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	// 5. Build the final DKIM-Signature header with the real b= value and
	//    prepend it to the message.
	final := buildDKIMHeader(dkimTags{
		Version:    "1",
		Algorithm:  "rsa-sha256",
		Canon:      "relaxed/relaxed",
		Domain:     opts.Domain,
		Selector:   opts.Selector,
		Timestamp:  now.UTC().Unix(),
		Headers:    presentHeaders,
		BodyHashB64: bhB64,
		Signature:  sigB64,
	})

	out := make([]byte, 0, len(final)+len(raw)+2)
	out = append(out, final...)
	out = append(out, '\r', '\n')
	out = append(out, raw...)
	return out, nil
}

func parsePrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("no PEM data")
	}
	// dnsx.GenerateDKIM produces PKCS#1 ("RSA PRIVATE KEY"). Handle PKCS#8
	// too for keys imported from other tools.
	if block.Type == "RSA PRIVATE KEY" {
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PKCS#8 key is %T, want *rsa.PrivateKey", key)
	}
	return rsaKey, nil
}

// splitHeadersBody splits at the first CRLF-CRLF. Header block does NOT
// include the terminating CRLF; body starts after the blank line.
func splitHeadersBody(raw []byte) (headers, body []byte, err error) {
	sep := []byte("\r\n\r\n")
	i := bytes.Index(raw, sep)
	if i < 0 {
		// Tolerate LF-only messages by normalising once, though composeMIME
		// always emits CRLF.
		if j := bytes.Index(raw, []byte("\n\n")); j >= 0 {
			return raw[:j], raw[j+2:], nil
		}
		return nil, nil, errors.New("dkim: missing header/body separator")
	}
	return raw[:i], raw[i+len(sep):], nil
}

// containsCI reports whether needle equals any element of hay (case-insensitive).
func containsCI(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}
