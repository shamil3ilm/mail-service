// Package auth handles password hashing, sessions, and the HTTP middleware
// that turns a session cookie into an authenticated *storage.User in context.
//
// Password hashing uses PBKDF2-SHA256 from the standard library (Go 1.24+).
// PBKDF2 is not the modern-best algorithm (Argon2id is stronger against
// GPU attacks) but it's stdlib, well-audited, and the same primitive iOS
// Keychain / 1Password / LastPass have used for over a decade with 600k+
// iterations. Trade-off accepted so we ship zero external dependencies.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	// hashAlgID appears in the serialized hash so we can rotate later.
	hashAlgID = "pbkdf2-sha256"

	// pbkdf2Iters follows current OWASP guidance for PBKDF2-SHA256 (2023+).
	// Costs ~150ms on a modern laptop; login flow tolerates that cheerfully.
	pbkdf2Iters = 600_000

	pbkdf2SaltLen = 16
	pbkdf2KeyLen  = 32
)

var (
	ErrInvalidHash = errors.New("invalid password hash format")
	ErrMismatch    = errors.New("password does not match")
)

// HashPassword derives a PBKDF2-SHA256 key from password + fresh random salt
// and returns a self-describing string:
//
//	pbkdf2-sha256$<iter>$<salt-b64>$<key-b64>
//
// The format lets us change algorithms or bump iterations without a schema
// migration — Verify parses the fields per stored hash.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("password required")
	}
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iters, pbkdf2KeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%s$%s",
		hashAlgID,
		pbkdf2Iters,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword returns nil if password matches the encoded hash.
// Uses constant-time compare so timing doesn't leak partial correctness.
func VerifyPassword(password, encoded string) error {
	alg, iter, salt, want, err := parseHash(encoded)
	if err != nil {
		return err
	}
	if alg != hashAlgID {
		return fmt.Errorf("%w: unknown alg %q", ErrInvalidHash, alg)
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

func parseHash(encoded string) (alg string, iter int, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 {
		return "", 0, nil, nil, ErrInvalidHash
	}
	iter, err = strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return "", 0, nil, nil, ErrInvalidHash
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return "", 0, nil, nil, ErrInvalidHash
	}
	key, err = base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return "", 0, nil, nil, ErrInvalidHash
	}
	return parts[0], iter, salt, key, nil
}
