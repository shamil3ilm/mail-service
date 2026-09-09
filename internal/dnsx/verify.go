package dnsx

import (
	"context"
	"net"
	"strings"
	"time"
)

// Verdict is the result of checking one Record against live DNS.
type Verdict struct {
	Kind     string   `json:"kind"`     // matches Record.Kind
	Verified bool     `json:"verified"` // true if any observed value matches
	Observed []string `json:"observed"` // what DNS actually returned (may be empty)
	Error    string   `json:"error,omitempty"`
}

// Resolver is the small subset of net.Resolver we depend on. Testable
// via fake, though the default net.DefaultResolver is what production uses.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
}

// Verify checks each Record against live DNS and returns per-record verdicts.
// The order of returned Verdicts matches the input.
//
// Rules per kind:
//   - TXT records: verified if ANY observed value equals the expected value
//     after normalising whitespace and case-insensitive comparison of the tag=value pairs.
//   - MX: verified if the observed MX list contains the expected target
//     (comparison strips the trailing dot registrars add).
func Verify(ctx context.Context, r Resolver, records []Record) []Verdict {
	if r == nil {
		r = net.DefaultResolver
	}
	out := make([]Verdict, len(records))
	for i, rec := range records {
		out[i] = verifyOne(ctx, r, rec)
	}
	return out
}

func verifyOne(ctx context.Context, r Resolver, rec Record) Verdict {
	// Every DNS lookup is bounded by our own timeout on top of ctx.
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	v := Verdict{Kind: rec.Kind}

	switch rec.Type {
	case "TXT":
		got, err := r.LookupTXT(lookupCtx, rec.Name)
		if err != nil {
			v.Error = simplifyDNSError(err)
			return v
		}
		v.Observed = got
		for _, obs := range got {
			if txtMatches(obs, rec.Value) {
				v.Verified = true
				break
			}
		}
	case "MX":
		got, err := r.LookupMX(lookupCtx, rec.Name)
		if err != nil {
			v.Error = simplifyDNSError(err)
			return v
		}
		want := strings.TrimSuffix(strings.ToLower(rec.Value), ".")
		for _, m := range got {
			host := strings.TrimSuffix(strings.ToLower(m.Host), ".")
			v.Observed = append(v.Observed, host)
			if host == want {
				v.Verified = true
			}
		}
	default:
		v.Error = "unknown record type " + rec.Type
	}
	return v
}

// txtMatches does a lenient compare that tolerates the whitespace differences
// resolvers introduce (quoted strings, chunk-joined TXT). We normalise both
// sides by collapsing whitespace and comparing case-insensitively.
func txtMatches(observed, expected string) bool {
	return normaliseTXT(observed) == normaliseTXT(expected)
}

func normaliseTXT(s string) string {
	s = strings.TrimSpace(s)
	// Collapse runs of whitespace to a single space. TXT chunk joining
	// sometimes leaves stray whitespace between concatenated parts.
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.ToLower(b.String())
}

// simplifyDNSError shortens verbose Go resolver errors into UI-friendly text.
func simplifyDNSError(err error) string {
	var dnsErr *net.DNSError
	if ok := errAs(err, &dnsErr); ok {
		if dnsErr.IsNotFound {
			return "not found"
		}
		if dnsErr.IsTimeout {
			return "timeout"
		}
		return dnsErr.Err
	}
	return err.Error()
}

// errAs is a stdlib-only errors.As, avoiding an import for a one-liner.
func errAs(err error, target any) bool {
	// Delegate to the stdlib. Inlined via wrapper for clarity.
	// (Kept out-of-file to avoid an errors import cycle in tests.)
	return errorsAs(err, target)
}
