package dsn

import (
	"strings"
	"testing"
)

// A realistic Postfix-style DSN (trimmed to the essentials).
const sampleDSN = `From: MAILER-DAEMON@relay.example
To: sender@example.com
Subject: Undelivered Mail Returned to Sender
Content-Type: multipart/report; report-type=delivery-status; boundary="X"

--X
Content-Type: text/plain; charset=us-ascii

This is the mail system.
The following recipient could not be delivered.

--X
Content-Type: message/delivery-status

Reporting-MTA: dns; relay.example
X-Postfix-Queue-ID: ABCDEF12345

Final-Recipient: rfc822; bounces@nowhere.test
Action: failed
Status: 5.1.1
Diagnostic-Code: smtp; 550 5.1.1 User unknown

Final-Recipient: rfc822; delayed@overloaded.test
Action: delayed
Status: 4.4.7
Diagnostic-Code: smtp; 451 4.4.7 Temporary failure

--X--
`

func TestParseFromRawExtractsPerRecipientOutcomes(t *testing.T) {
	// Content-Type header wants explicit boundary quoting in mime.ParseMediaType,
	// but the sample already meets that requirement. Normalise CRLF.
	raw := []byte(strings.ReplaceAll(sampleDSN, "\n", "\r\n"))

	reports, err := ParseFromRaw(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("want 2 reports, got %d: %+v", len(reports), reports)
	}

	hard := reports[0]
	if !hard.IsHardBounce() {
		t.Errorf("expected hard bounce, got %+v", hard)
	}
	if hard.Recipient != "bounces@nowhere.test" {
		t.Errorf("recipient: %q", hard.Recipient)
	}

	soft := reports[1]
	if !soft.IsSoftBounce() {
		t.Errorf("expected soft bounce, got %+v", soft)
	}
	if soft.Recipient != "delayed@overloaded.test" {
		t.Errorf("recipient: %q", soft.Recipient)
	}
}

func TestParseFromRawIgnoresNonDSN(t *testing.T) {
	raw := []byte("From: a@b.com\r\nTo: c@d.com\r\nSubject: hi\r\n\r\nbody\r\n")
	reports, err := ParseFromRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	if reports != nil {
		t.Fatalf("expected nil for non-DSN, got %+v", reports)
	}
}

func TestExtractRecipient(t *testing.T) {
	cases := []struct{ in, want string }{
		{"rfc822; user@example.com", "user@example.com"},
		{"user@example.com", "user@example.com"},
		{"rfc822;  <User@Example.COM>  ", "user@example.com"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := extractRecipient(tc.in); got != tc.want {
			t.Errorf("extractRecipient(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
