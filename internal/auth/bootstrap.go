package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

// BootstrapAdmin creates a default admin account on first startup (when the
// users table is empty). The generated password is printed exactly once —
// there is no way to recover it later, only to reset via the DB.
//
// This is the standard self-hosted pattern: Grafana, Portainer, Uptime Kuma,
// and Sonarr all do the same. It avoids shipping a well-known default
// password (which is the #1 cause of self-hosted-app compromise).
//
// Returns (created, password) when it provisioned, ("", "") if users already
// exist. Password is never persisted; only the hash is.
func BootstrapAdmin(ctx context.Context, store storage.Store, log *slog.Logger) (string, string, error) {
	n, err := store.CountUsers(ctx)
	if err != nil {
		return "", "", fmt.Errorf("count users: %w", err)
	}
	if n > 0 {
		return "", "", nil
	}

	password := generateBootstrapPassword()
	hash, err := HashPassword(password)
	if err != nil {
		return "", "", err
	}

	u := &storage.User{
		ID:           "usr_admin",
		Email:        "admin@local",
		PasswordHash: hash,
		DisplayName:  "Administrator",
		IsAdmin:      true,
		CreatedAt:    time.Now().UTC(),
	}
	if err := store.InsertUser(ctx, u); err != nil {
		return "", "", fmt.Errorf("insert admin: %w", err)
	}

	// Personal team so the admin can own domains + team-scoped mailboxes
	// from the moment they log in. Named "Personal" and marked owner-role.
	team := &storage.Team{
		ID:        "team_personal",
		Name:      "Personal",
		CreatedAt: time.Now().UTC(),
	}
	if err := store.InsertTeam(ctx, team); err != nil {
		return "", "", fmt.Errorf("insert personal team: %w", err)
	}
	if err := store.AddTeamMember(ctx, team.ID, u.ID, "owner"); err != nil {
		return "", "", fmt.Errorf("add admin to team: %w", err)
	}

	log.Warn("bootstrap admin account created — password printed once below")
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	fmt.Println("║  FIRST-RUN ADMIN ACCOUNT — SAVE THIS PASSWORD NOW           ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  email:    %-50s║\n", u.Email)
	fmt.Printf("║  password: %-50s║\n", password)
	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()

	return u.Email, password, nil
}

// generateBootstrapPassword returns a 20-character crockford-base32 password.
// 20 chars × 5 bits = 100 bits of entropy — far beyond what any brute-force
// or leak-list attack can reach in a reasonable timeframe.
func generateBootstrapPassword() string {
	b := make([]byte, 15) // 15 bytes → 24 base32 chars, we trim to 20
	_, _ = rand.Read(b)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	return strings.ToLower(enc[:20])
}
