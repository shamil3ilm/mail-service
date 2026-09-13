// Package smsprovider defines the outbound SMS relay adapter.
//
// Two implementations ship:
//   - Capture: default. Stores the SMS locally so the dashboard shows it,
//     nothing goes to a carrier. Ideal for dev, testing, LAN-only setups.
//   - HTTP: POSTs a JSON payload to a configured URL. Compatible with
//     open-source Android SMS Gateway (a phone with an unlimited-SMS SIM
//     acts as your carrier), Twilio's REST API, or any HTTP-to-SMS service
//     you want to build in front.
//
// Kept small and interface-driven so we can add native provider SDKs
// (Twilio, MessageBird, Vonage) later without touching business logic.
package smsprovider

import (
	"context"
	"errors"
)

var (
	ErrNoProvider = errors.New("no SMS provider configured")
	ErrPermanent  = errors.New("permanent SMS failure")
	ErrTransient  = errors.New("transient SMS failure")
)

// SendRequest is the normalised shape passed to any provider.
type SendRequest struct {
	From string // sender identity — SIM number, short code, or provider-assigned
	To   string // E.164 recipient (+15551234567) or short code
	Body string // UTF-8; providers segment > 160 chars automatically
}

// SendResult is what a provider returns on success.
type SendResult struct {
	ProviderMessageID string // provider-side id (Twilio SID, gateway UUID, etc.)
	Segments          int    // how many SMS segments the body used
}

// Relay is the adapter interface. Implementations must be safe for
// concurrent use — a single instance is shared across the process.
type Relay interface {
	Name() string
	Send(ctx context.Context, req *SendRequest) (*SendResult, error)
}

// Noop is a placeholder that always fails. Wired when no provider is
// configured, so misconfig surfaces at send time rather than silently
// eating messages.
type Noop struct{}

func (Noop) Name() string { return "noop" }
func (Noop) Send(_ context.Context, _ *SendRequest) (*SendResult, error) {
	return nil, ErrNoProvider
}

// segmentCount estimates SMS segment usage for a body. GSM-7 encoded
// bodies fit 160 chars per segment; UCS-2 (non-Latin) drops to 70 per
// segment. Multi-segment adds a header, dropping capacity to 153/67.
// Providers report the authoritative count post-send; this is only for
// pre-flight display.
func segmentCount(body string) int {
	if body == "" {
		return 0
	}
	runes := []rune(body)
	isUCS2 := false
	for _, r := range runes {
		if r > 127 {
			isUCS2 = true
			break
		}
	}
	single, multi := 160, 153
	if isUCS2 {
		single, multi = 70, 67
	}
	if len(runes) <= single {
		return 1
	}
	segs := (len(runes) + multi - 1) / multi
	if segs < 1 {
		segs = 1
	}
	return segs
}
