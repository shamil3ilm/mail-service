package dkim

import (
	"crypto/rsa"
	"crypto/x509"
	"fmt"
)

// parsePubKeyPKIX parses a PKIX-encoded RSA public key (the same format
// GenerateDKIM emits, and what DKIM verifiers expect in the DNS TXT record).
// Used by tests; kept here rather than in sign.go so production paths don't
// pull in the verification-side surface.
func parsePubKeyPKIX(der []byte) (*rsa.PublicKey, error) {
	any, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := any.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("PKIX key is %T, want *rsa.PublicKey", any)
	}
	return rsaKey, nil
}
