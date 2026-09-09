// Package dnspub publishes DNS records to an external authoritative DNS
// service so mail-service can automate domain provisioning end-to-end.
//
// The default publisher is Manual — a no-op that leaves records for the
// operator to copy-paste into their DNS provider. The PrivateDNS
// publisher pushes records into a privatedns instance
// (github.com/privatedns/native) via its REST API, closing the loop:
// creating a domain in mail-service publishes the DNS automatically.
//
// A publisher is optional. Nothing in the mail flow depends on it — the
// domain record + DKIM key are the source of truth. Failed publishes log
// a warning and leave the operator to fix DNS manually.
package dnspub

import (
	"context"

	"github.com/shamil3ilm/mail-service/internal/dnsx"
)

// Publisher applies a set of records to an authoritative DNS backend.
// Verify optionally checks whether records are present in the backend
// (which is faster + more reliable than a public DNS lookup for the
// integration path — DNS caches don't get in the way).
type Publisher interface {
	Name() string
	Publish(ctx context.Context, zone string, records []dnsx.Record) error
	// Verify may return an empty slice when the publisher can't inspect
	// records (e.g. Manual). Callers should combine this with a live DNS
	// lookup (dnsx.Verify) and use whichever succeeds.
	Verify(ctx context.Context, zone string, records []dnsx.Record) ([]dnsx.Verdict, error)
}

// Manual is the default no-op publisher. Records are shown in the
// dashboard for the operator to publish by hand.
type Manual struct{}

func (Manual) Name() string { return "manual" }
func (Manual) Publish(_ context.Context, _ string, _ []dnsx.Record) error {
	return nil
}
func (Manual) Verify(_ context.Context, _ string, _ []dnsx.Record) ([]dnsx.Verdict, error) {
	return nil, nil
}
