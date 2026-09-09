// Package dnsx handles DKIM key generation and the DNS records that a
// domain owner must publish to send authenticated mail through us.
//
// Named `dnsx` (not `dns`) so it never shadows the standard-library
// `net` package's dns lookups — matters at the import site.
package dnsx

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
)

// DKIMKeyBits is the RSA key size used for DKIM. 2048 is the current
// recommended default: strong enough for the medium term, small enough that
// the resulting TXT record fits comfortably (a single 253-byte string
// chunk with room to spare after base64).
const DKIMKeyBits = 2048

// DKIMKey is one generated DKIM keypair, ready to persist.
type DKIMKey struct {
	Selector   string // DNS label under _domainkey (e.g. "ms1")
	PrivatePEM string // PEM PKCS#1 — sign side, stored in DB
	PublicB64  string // base64 X.509 SubjectPublicKeyInfo — for the TXT record
}

// GenerateDKIM returns a fresh RSA-2048 DKIM keypair with the given selector.
// Selector convention: short, unique-per-rotation label ("ms1", "ms2026q1").
// Rotating the selector is how DKIM key rotation works — publish the new
// key under a new selector, keep the old one live during transition.
func GenerateDKIM(selector string) (*DKIMKey, error) {
	if selector == "" {
		selector = "ms1"
	}
	priv, err := rsa.GenerateKey(rand.Reader, DKIMKeyBits)
	if err != nil {
		return nil, fmt.Errorf("rsa gen: %w", err)
	}

	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})

	// DKIM uses the X.509 SubjectPublicKeyInfo encoding (PKIX), not the raw
	// PKCS#1 pubkey. This is what every DKIM verifier expects.
	pubBytes, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal pub: %w", err)
	}

	return &DKIMKey{
		Selector:   selector,
		PrivatePEM: string(privPEM),
		PublicB64:  base64.StdEncoding.EncodeToString(pubBytes),
	}, nil
}
