// Package provider defines the outbound relay adapter interface.
// Concrete implementations: none (capture-only), smtp, ses, resend.
// Business logic depends on Relay, never on a specific provider.
package provider

import (
	"context"
	"errors"
	"io"
)

var (
	ErrNoProvider  = errors.New("no relay provider configured")
	ErrPermanent   = errors.New("permanent delivery failure")
	ErrTransient   = errors.New("transient delivery failure")
	ErrUnverified  = errors.New("sender domain not verified")
	ErrSuppressed  = errors.New("recipient is on suppression list")
)

// SendRequest is the normalized shape passed to any relay.
// Body is a reader so we don't hold large messages in memory.
type SendRequest struct {
	From    string
	To      []string
	Cc      []string
	Bcc     []string
	Subject string
	Raw     io.Reader // full RFC 5322 message, DKIM-signed by caller
	Size    int64

	// BodyText is the plaintext body pre-composition, used by the capture
	// provider to populate the FTS index without re-parsing MIME. Empty is
	// fine — the capture path will fall back to an unsearchable body.
	BodyText string

	// Attachments carries decoded attachment metadata + bytes so the
	// capture provider can persist them alongside the message row without
	// re-parsing the MIME it just built. Real SMTP relays ignore this —
	// the wire format (Raw) already contains everything they need.
	Attachments []Attachment
}

// Attachment on a SendRequest — decoded content ready for persistence.
type Attachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

// SendResult is what a relay returns on success.
type SendResult struct {
	ProviderMessageID string
	Accepted          []string
	Rejected          []string
}

// Relay is the adapter interface.
// Implementations must be safe for concurrent use.
type Relay interface {
	Name() string
	Send(ctx context.Context, req *SendRequest) (*SendResult, error)
}

// Noop is a placeholder relay used in capture mode.
type Noop struct{}

func (Noop) Name() string { return "noop" }

func (Noop) Send(_ context.Context, _ *SendRequest) (*SendResult, error) {
	return nil, ErrNoProvider
}
