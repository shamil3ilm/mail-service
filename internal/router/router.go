// Package router resolves a recipient address to a Mailbox.
//
// Matching order (Day 2 — exact only; wildcard/regex land with domains phase):
//  1. Exact match on Mailbox.Address.
//  2. If not found and the address's domain matches one of AutoVerifyDomains,
//     auto-provision an exact mailbox and return it.
//  3. If neither, return the "unmatched" fallback so we never lose mail.
package router

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/shamil3ilm/mail-service/internal/storage"
)

// UnmatchedID is the virtual mailbox for RCPT TO with no match.
// A row with this ID is seeded lazily on first miss so foreign-key + list
// queries behave normally.
const UnmatchedID = "mbx_unmatched"

// Router matches incoming addresses to mailboxes.
type Router struct {
	Store             storage.Store
	AutoVerifyDomains []string // suffixes like ".test", ".local"
}

// Resolve returns the mailbox that should own the message for the given
// recipient. Never returns ErrNotFound — always falls back to UnmatchedID.
func (r *Router) Resolve(ctx context.Context, addr string) (*storage.Mailbox, error) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return r.unmatched(ctx)
	}

	if mb, err := r.Store.FindMailboxByAddress(ctx, addr); err == nil {
		return mb, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}

	if r.shouldAutoProvision(addr) {
		mb := &storage.Mailbox{
			ID:          newID("mbx"),
			Address:     addr,
			MatchType:   "exact",
			DisplayName: addr,
			Mode:        "receive",
			OwnerType:   "user",
			OwnerID:     "system",
			CreatedAt:   time.Now().UTC(),
		}
		if err := r.Store.UpsertMailbox(ctx, mb); err != nil {
			return nil, err
		}
		return mb, nil
	}

	return r.unmatched(ctx)
}

func (r *Router) shouldAutoProvision(addr string) bool {
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return false
	}
	domain := addr[at+1:]
	for _, suffix := range r.AutoVerifyDomains {
		suffix = strings.ToLower(strings.TrimSpace(suffix))
		if suffix == "" {
			continue
		}
		// Exact TLD-style match: ".test" matches "foo.test" but not "test-svc.com".
		if strings.HasSuffix("."+domain, suffix) || domain == strings.TrimPrefix(suffix, ".") {
			return true
		}
	}
	return false
}

func (r *Router) unmatched(ctx context.Context) (*storage.Mailbox, error) {
	mb, err := r.Store.GetMailbox(ctx, UnmatchedID)
	if err == nil {
		return mb, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}
	mb = &storage.Mailbox{
		ID:          UnmatchedID,
		Address:     "",
		MatchType:   "exact",
		DisplayName: "Unmatched",
		Mode:        "receive",
		OwnerType:   "user",
		OwnerID:     "system",
		CreatedAt:   time.Now().UTC(),
	}
	if err := r.Store.UpsertMailbox(ctx, mb); err != nil {
		return nil, err
	}
	return mb, nil
}

// newID returns a URL-safe random ID with the given prefix.
// Format: <prefix>_<24 hex chars>.
func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
