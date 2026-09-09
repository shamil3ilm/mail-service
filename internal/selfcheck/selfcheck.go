// Package selfcheck runs at startup in cloud mode to diagnose the most
// common self-hosted-MX misconfigurations BEFORE mail providers do:
//
//   - PTR record for the outbound IP matches the mail hostname
//     (forward-confirmed reverse DNS — required by every major provider).
//   - The outbound IP isn't on Spamhaus SBL/XBL/PBL or Barracuda.
//   - The mail hostname's DKIM public key is resolvable.
//
// Every check is best-effort. None of them fail startup — they log a
// verdict per check so the operator sees "you're on the PBL" in the
// systemd journal instead of hunting through spam folders a week later.
package selfcheck

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"
)

// Config is the runtime input for RunAll.
type Config struct {
	Hostname       string   // e.g. "mail.example.com" — used for PTR + DKIM checks
	OutboundIP     string   // public IPv4 the server sends from
	DKIMSelectors  []string // selectors to probe under _domainkey (usually just "ms1")
	Domains        []string // verified domains to probe DKIM records against
	DNSBLZones     []string // e.g. "zen.spamhaus.org", "b.barracudacentral.org"
	LookupTimeout  time.Duration
}

// Result is one verdict.
type Result struct {
	Name   string
	OK     bool
	Detail string // human-readable observation ("PTR = mail.example.com", "listed on zen.spamhaus.org")
	Err    error  // set when the check itself failed to complete
}

// RunAll runs every check with a bounded per-lookup timeout. Safe to call
// multiple times (e.g. on SIGHUP for a hot reprobe).
func RunAll(ctx context.Context, cfg Config, log *slog.Logger) []Result {
	if cfg.LookupTimeout == 0 {
		cfg.LookupTimeout = 5 * time.Second
	}
	log = log.With(slog.String("component", "selfcheck"))
	log.Info("running startup self-checks",
		slog.String("host", cfg.Hostname),
		slog.String("ip", cfg.OutboundIP),
	)

	var out []Result
	out = append(out, checkPTR(ctx, cfg))
	for _, zone := range cfg.DNSBLZones {
		out = append(out, checkDNSBL(ctx, cfg, zone))
	}
	for _, domain := range cfg.Domains {
		for _, selector := range cfg.DKIMSelectors {
			out = append(out, checkDKIM(ctx, cfg, domain, selector))
		}
	}
	for _, r := range out {
		attrs := []any{slog.String("check", r.Name), slog.Bool("ok", r.OK)}
		if r.Detail != "" {
			attrs = append(attrs, slog.String("detail", r.Detail))
		}
		if r.Err != nil {
			attrs = append(attrs, slog.String("err", r.Err.Error()))
		}
		if r.OK {
			log.Info("selfcheck", attrs...)
		} else {
			log.Warn("selfcheck", attrs...)
		}
	}
	return out
}

// checkPTR verifies forward-confirmed reverse DNS: the outbound IP must
// reverse to a hostname whose forward A/AAAA resolves back to the same IP,
// and the hostname should match cfg.Hostname (case-insensitive).
func checkPTR(ctx context.Context, cfg Config) Result {
	r := Result{Name: "ptr"}
	if cfg.OutboundIP == "" || cfg.Hostname == "" {
		r.Err = fmt.Errorf("hostname or outbound_ip not configured")
		return r
	}

	lctx, cancel := context.WithTimeout(ctx, cfg.LookupTimeout)
	defer cancel()

	names, err := net.DefaultResolver.LookupAddr(lctx, cfg.OutboundIP)
	if err != nil {
		r.Err = err
		return r
	}
	if len(names) == 0 {
		r.Detail = "no PTR record"
		return r
	}

	// Names come back with trailing dot; normalise.
	ptr := strings.TrimSuffix(strings.ToLower(names[0]), ".")
	wanted := strings.ToLower(cfg.Hostname)
	r.Detail = "PTR = " + ptr

	if ptr != wanted {
		return r
	}

	// Forward-confirm: the PTR host should resolve back to the same IP.
	addrs, err := net.DefaultResolver.LookupHost(lctx, ptr)
	if err != nil {
		r.Err = fmt.Errorf("forward lookup of %s: %w", ptr, err)
		return r
	}
	for _, a := range addrs {
		if a == cfg.OutboundIP {
			r.OK = true
			return r
		}
	}
	r.Detail += " (forward lookup does not include outbound IP)"
	return r
}

// checkDNSBL queries a single blocklist zone by encoding the IP octets in
// reverse under the zone. Any A record → listed.
func checkDNSBL(ctx context.Context, cfg Config, zone string) Result {
	r := Result{Name: "dnsbl/" + zone}
	if cfg.OutboundIP == "" {
		r.Err = fmt.Errorf("outbound_ip not configured")
		return r
	}
	parts := strings.Split(cfg.OutboundIP, ".")
	if len(parts) != 4 {
		r.Err = fmt.Errorf("outbound_ip %q is not IPv4", cfg.OutboundIP)
		return r
	}
	// Reverse the octets and append the zone: "1.2.3.4" → "4.3.2.1.<zone>"
	reversed := parts[3] + "." + parts[2] + "." + parts[1] + "." + parts[0]
	query := reversed + "." + zone

	lctx, cancel := context.WithTimeout(ctx, cfg.LookupTimeout)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupHost(lctx, query)
	if err != nil {
		// The most common error is "no such host" → not listed. That's
		// the passing outcome for a blocklist check.
		var dnsErr *net.DNSError
		if asErr(err, &dnsErr) && dnsErr.IsNotFound {
			r.OK = true
			r.Detail = "not listed"
			return r
		}
		r.Err = err
		return r
	}
	if len(addrs) > 0 {
		r.Detail = "listed at " + strings.Join(addrs, ", ")
		return r
	}
	r.OK = true
	r.Detail = "not listed"
	return r
}

// checkDKIM confirms the public DKIM key is resolvable at
// <selector>._domainkey.<domain>. We don't verify the key value here —
// that would need to know what we published locally. Presence-only is
// enough to catch "you forgot to publish the record" mistakes.
func checkDKIM(ctx context.Context, cfg Config, domain, selector string) Result {
	r := Result{Name: "dkim/" + selector + "." + domain}
	lctx, cancel := context.WithTimeout(ctx, cfg.LookupTimeout)
	defer cancel()

	name := selector + "._domainkey." + domain
	txts, err := net.DefaultResolver.LookupTXT(lctx, name)
	if err != nil {
		var dnsErr *net.DNSError
		if asErr(err, &dnsErr) && dnsErr.IsNotFound {
			r.Detail = "no TXT at " + name
			return r
		}
		r.Err = err
		return r
	}
	for _, txt := range txts {
		if strings.HasPrefix(txt, "v=DKIM1") {
			r.OK = true
			r.Detail = "DKIM key resolvable at " + name
			return r
		}
	}
	r.Detail = "no v=DKIM1 record at " + name
	return r
}

// asErr is a thin errors.As wrapper so callers don't need an errors import.
func asErr(err error, target any) bool {
	return errorsAs(err, target)
}
