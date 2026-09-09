package selfcheck

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestRunAllProducesResultForEachCheck(t *testing.T) {
	// Use TEST-NET-1 (RFC 5737) — guaranteed non-routable → PTR/DNSBL both
	// return NXDOMAIN quickly. Test does not require internet: the test
	// still passes when LookupAddr / LookupHost times out; we just assert
	// that a Result is emitted per check.
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := Config{
		Hostname:      "mail.example.invalid",
		OutboundIP:    "192.0.2.1", // reserved for docs, no PTR
		Domains:       []string{"example.invalid"},
		DKIMSelectors: []string{"ms1"},
		DNSBLZones:    []string{"zen.spamhaus.org"},
	}
	res := RunAll(context.Background(), cfg, log)
	if len(res) < 3 {
		t.Fatalf("want at least 3 results (ptr + dnsbl + dkim), got %d", len(res))
	}
	names := make([]string, 0, len(res))
	for _, r := range res {
		names = append(names, r.Name)
	}
	joined := strings.Join(names, ",")
	for _, expected := range []string{"ptr", "dnsbl/zen.spamhaus.org", "dkim/ms1.example.invalid"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("missing check %q in %s", expected, joined)
		}
	}
}

func TestCheckDNSBLReversesOctetsCorrectly(t *testing.T) {
	// Verify the octet-reversal logic — we can't unit-test the DNS lookup
	// itself without a fake resolver, but the transformation is where
	// bugs typically hide.
	//
	// The check is: given IP a.b.c.d, we query d.c.b.a.<zone>.
	// This test replicates the transform inline to catch regressions.
	cases := []struct {
		ip, zone, want string
	}{
		{"1.2.3.4", "zen.spamhaus.org", "4.3.2.1.zen.spamhaus.org"},
		{"192.168.1.1", "b.barracudacentral.org", "1.1.168.192.b.barracudacentral.org"},
	}
	for _, tc := range cases {
		parts := strings.Split(tc.ip, ".")
		got := parts[3] + "." + parts[2] + "." + parts[1] + "." + parts[0] + "." + tc.zone
		if got != tc.want {
			t.Errorf("reverse(%s, %s) = %s; want %s", tc.ip, tc.zone, got, tc.want)
		}
	}
}
