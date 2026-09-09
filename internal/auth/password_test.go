package auth

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundtrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(h, hashAlgID+"$") {
		t.Fatalf("format: %s", h)
	}
	if err := VerifyPassword("correct horse battery staple", h); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyWrongPassword(t *testing.T) {
	h, _ := HashPassword("hunter2")
	if err := VerifyPassword("wrong", h); err != ErrMismatch {
		t.Fatalf("want ErrMismatch, got %v", err)
	}
}

func TestHashesDifferPerCall(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("expected distinct salts to produce distinct hashes")
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	if err := VerifyPassword("x", "not-a-hash"); err != ErrInvalidHash {
		t.Fatalf("want ErrInvalidHash, got %v", err)
	}
}

func TestEmptyPasswordRejected(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password should error")
	}
}
