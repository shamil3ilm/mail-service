package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// APIKeyPrefix is the visible marker so keys are unambiguous in logs and
// codebases: "mail-service key" not "generic 40-char blob".
const APIKeyPrefix = "msk_"

// PrefixDisplayLen controls how much of the full token is stored in cleartext
// for UI identification. 12 chars = prefix ("msk_") + 8 hex = 4 random bytes,
// so an attacker seeing the displayed prefix learns nothing useful.
const PrefixDisplayLen = 12

var ErrInvalidAPIKey = errors.New("invalid api key")

// GenerateAPIKey returns:
//
//	fullToken  — the plaintext returned to the caller ONCE
//	prefix     — the identifier shown in UI ("msk_a1b2c3d4")
//	hash       — the sha256 hex, stored in DB and used for lookup
//
// Total token length: len("msk_") + 40 hex chars = 44 chars, 160 bits of entropy.
func GenerateAPIKey() (fullToken, prefix, hash string, err error) {
	b := make([]byte, 20)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", err
	}
	fullToken = APIKeyPrefix + hex.EncodeToString(b)
	prefix = fullToken[:PrefixDisplayLen]
	hash = HashAPIKey(fullToken)
	return fullToken, prefix, hash, nil
}

// HashAPIKey returns hex sha256 of the full token. Used both when creating
// (to persist) and when authenticating (to look up).
func HashAPIKey(fullToken string) string {
	sum := sha256.Sum256([]byte(fullToken))
	return hex.EncodeToString(sum[:])
}

// ParseBearer extracts "msk_..." from an "Authorization: Bearer <token>"
// header value. Returns "" if the header isn't a bearer or the token
// doesn't have our prefix.
func ParseBearer(headerValue string) string {
	const bearer = "Bearer "
	if !strings.HasPrefix(headerValue, bearer) {
		return ""
	}
	tok := strings.TrimSpace(headerValue[len(bearer):])
	if !strings.HasPrefix(tok, APIKeyPrefix) {
		return ""
	}
	return tok
}
