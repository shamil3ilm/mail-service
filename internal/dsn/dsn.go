// Package dsn parses Delivery Status Notifications (RFC 3464).
//
// A DSN is a multipart/report message that MTAs send back when delivery
// fails. The interesting bits live in the message/delivery-status part:
//
//	Final-Recipient: rfc822; user@example.com
//	Action: failed
//	Status: 5.1.1
//	Diagnostic-Code: smtp; 550 5.1.1 User unknown
//
// We extract per-recipient outcomes so callers can decide whether to add
// them to a suppression list. Hard bounces (5.x.x) are suppressible;
// soft bounces (4.x.x) usually are not.
package dsn

import (
	"bufio"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
)

// Report is the extracted per-recipient outcome from a DSN.
type Report struct {
	Recipient  string // parsed from Final-Recipient
	Action     string // "failed", "delayed", "delivered", "relayed", "expanded"
	StatusCode string // e.g. "5.1.1"
	Diagnostic string // free-form MTA message
}

// IsHardBounce is true for permanent failures (5.x.x).
// Callers should suppress the recipient in that case.
func (r Report) IsHardBounce() bool {
	return r.Action == "failed" && strings.HasPrefix(r.StatusCode, "5.")
}

// IsSoftBounce is true for temporary failures — retry later, don't suppress.
// Two shapes count as soft per RFC 3464:
//   - Action: delayed (MTA queued for retry regardless of status code)
//   - Action: failed with a 4.x.x status (permanent-attempt, temp category)
func (r Report) IsSoftBounce() bool {
	if r.Action == "delayed" {
		return true
	}
	return r.Action == "failed" && strings.HasPrefix(r.StatusCode, "4.")
}

// ParseFromRaw looks for a message/delivery-status part in `raw` (the full
// MIME bytes of a bounce message) and returns per-recipient reports.
// Returns nil if raw isn't a DSN — this is not an error condition; most
// mail is not a DSN.
func ParseFromRaw(raw []byte) ([]Report, error) {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return nil, nil // not a valid message — silently skip
	}
	ct := msg.Header.Get("Content-Type")
	if ct == "" {
		return nil, nil
	}
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return nil, nil
	}
	if !strings.HasPrefix(mediaType, "multipart/") || params["boundary"] == "" {
		return nil, nil
	}

	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil, nil
		}
		if err != nil {
			return nil, nil
		}
		partCT := part.Header.Get("Content-Type")
		if !strings.HasPrefix(partCT, "message/delivery-status") {
			continue
		}
		return parseDeliveryStatus(part), nil
	}
}

// parseDeliveryStatus parses the message/delivery-status body, which is a
// sequence of RFC-822-like header groups separated by blank lines. The
// first group is per-message; each subsequent group is per-recipient.
func parseDeliveryStatus(r io.Reader) []Report {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var groups []map[string]string
	current := map[string]string{}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(current) > 0 {
				groups = append(groups, current)
				current = map[string]string{}
			}
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:colon]))
		value := strings.TrimSpace(line[colon+1:])
		current[key] = value
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	if len(groups) < 2 {
		return nil
	}

	reports := make([]Report, 0, len(groups)-1)
	for _, g := range groups[1:] {
		r := Report{
			Action:     strings.ToLower(g["action"]),
			StatusCode: g["status"],
			Diagnostic: g["diagnostic-code"],
			Recipient:  extractRecipient(g["final-recipient"]),
		}
		if r.Recipient == "" {
			r.Recipient = extractRecipient(g["original-recipient"])
		}
		if r.Recipient == "" {
			continue
		}
		reports = append(reports, r)
	}
	return reports
}

// extractRecipient handles the RFC 3464 form "rfc822; user@example.com" and
// the bare "user@example.com" variant some MTAs emit.
func extractRecipient(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if i := strings.IndexByte(raw, ';'); i >= 0 {
		raw = strings.TrimSpace(raw[i+1:])
	}
	// Strip angle brackets if present.
	raw = strings.Trim(raw, "<>")
	return strings.ToLower(raw)
}
