package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/shamil3ilm/mail-service/internal/auth"
	"github.com/shamil3ilm/mail-service/internal/dkim"
	"github.com/shamil3ilm/mail-service/internal/dnsx"
	"github.com/shamil3ilm/mail-service/internal/provider"
	"github.com/shamil3ilm/mail-service/internal/storage"
)

// sendEmailReq matches Resend's /emails schema for drop-in compatibility.
// Extra headers land in the "headers" object. Either text or html (or both)
// must be present. Attachments are base64-encoded in the JSON payload —
// keeps the API JSON-only. 25MB per attachment is enforced below.
type sendEmailReq struct {
	From        string             `json:"from"`
	To          []string           `json:"to"`
	Cc          []string           `json:"cc,omitempty"`
	Bcc         []string           `json:"bcc,omitempty"`
	Subject     string             `json:"subject"`
	Text        string             `json:"text,omitempty"`
	HTML        string             `json:"html,omitempty"`
	ReplyTo     string             `json:"reply_to,omitempty"`
	Headers     map[string]string  `json:"headers,omitempty"`
	Attachments []sendAttachmentIn `json:"attachments,omitempty"`
}

// sendAttachmentIn is one attachment on the way in — same shape as Resend.
type sendAttachmentIn struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	ContentB64  string `json:"content"` // base64-encoded bytes
}

// sendEmailResp mirrors Resend's shape too.
type sendEmailResp struct {
	ID       string   `json:"id"`
	Accepted []string `json:"accepted"`
	Rejected []string `json:"rejected,omitempty"`
}

// sendEmail: POST /api/v1/emails — Resend-compatible submission endpoint.
// The message is composed as a full RFC 5322 blob and handed to whichever
// Relay is configured (capture in local mode, SMTP in cloud).
func (s *Server) sendEmail(w http.ResponseWriter, r *http.Request) {
	if s.Relay == nil {
		writeErr(w, http.StatusServiceUnavailable, "no relay configured")
		return
	}

	var req sendEmailReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 25*1024*1024)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if err := validateSendReq(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Filter out suppressed recipients up front. If nothing is left, that's
	// a 422 — the message is well-formed but has no legal recipients.
	skipped := s.filterSuppressed(r.Context(), &req)
	if len(req.To)+len(req.Cc)+len(req.Bcc) == 0 {
		writeErr(w, http.StatusUnprocessableEntity,
			"all recipients are on the suppression list")
		return
	}

	raw, err := composeMIME(&req)
	if err != nil {
		s.Logger.Error("compose mime", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "compose failed")
		return
	}

	// DKIM signing: look up the from-domain, auto-provision if it matches
	// a local auto-verify suffix, otherwise reject (cloud) or warn (local).
	// Signed bytes replace raw; unsigned falls through only in local mode
	// for domains outside our auto-verify list.
	if signed, err := s.dkimSign(r.Context(), req.From, raw); err != nil {
		s.Logger.Warn("dkim decision",
			slog.String("from", req.From),
			slog.String("err", err.Error()),
		)
		if s.CloudMode {
			writeErr(w, http.StatusForbidden, "from-domain not verified: "+err.Error())
			return
		}
		// local mode: proceed unsigned so testing isn't blocked
	} else if signed != nil {
		raw = signed
	}

	// Decode attachments once so the capture provider can persist them
	// without re-parsing the MIME. composeMIME already validated the input.
	decoded, _ := decodeAttachments(req.Attachments)
	relayAtts := make([]provider.Attachment, 0, len(decoded))
	for _, a := range decoded {
		relayAtts = append(relayAtts, provider.Attachment{
			Filename:    a.filename,
			ContentType: a.contentType,
			Content:     a.content,
		})
	}

	relayReq := &provider.SendRequest{
		From:        req.From,
		To:          req.To,
		Cc:          req.Cc,
		Bcc:         req.Bcc,
		Subject:     req.Subject,
		Raw:         bytes.NewReader(raw),
		Size:        int64(len(raw)),
		BodyText:    firstNonEmptyText(req.Text, req.HTML),
		Attachments: relayAtts,
	}

	result, err := s.Relay.Send(r.Context(), relayReq)
	if err != nil && (result == nil || len(result.Accepted) == 0) {
		// Total failure — surface transient vs permanent so clients can
		// choose whether to retry.
		code := http.StatusBadGateway
		if errors.Is(err, provider.ErrPermanent) {
			code = http.StatusUnprocessableEntity
		}
		s.Logger.Warn("send failed",
			slog.String("provider", s.Relay.Name()),
			slog.String("err", err.Error()),
		)
		writeErr(w, code, err.Error())
		return
	}

	// Any addresses removed by the suppression filter show up in Rejected
	// so callers can see them.
	rejected := append([]string{}, result.Rejected...)
	rejected = append(rejected, skipped...)

	// Partial success (some accepted, some rejected) is a 200 with detail.
	writeJSON(w, http.StatusOK, sendEmailResp{
		ID:       result.ProviderMessageID,
		Accepted: result.Accepted,
		Rejected: rejected,
	})
}

// filterSuppressed drops any recipient found on the suppression list and
// returns the list of dropped addresses (for the response payload). Errors
// from the DB "fail open" — we'd rather send than drop mail because the
// DB blipped.
func (s *Server) filterSuppressed(ctx context.Context, req *sendEmailReq) []string {
	dropped := make([]string, 0)
	filter := func(list []string) []string {
		out := list[:0]
		for _, a := range list {
			skip, err := s.Store.IsSuppressed(ctx, a)
			if err != nil {
				s.Logger.Warn("suppression lookup",
					slog.String("addr", a),
					slog.String("err", err.Error()),
				)
				out = append(out, a)
				continue
			}
			if skip {
				dropped = append(dropped, a)
				continue
			}
			out = append(out, a)
		}
		return out
	}
	req.To = filter(req.To)
	req.Cc = filter(req.Cc)
	req.Bcc = filter(req.Bcc)
	return dropped
}

// Attachment limits — matches what most providers accept.
const (
	maxAttachmentBytes = 25 * 1024 * 1024 // 25MB per attachment
	maxAttachmentCount = 20
)

func validateSendReq(req *sendEmailReq) error {
	if _, err := mail.ParseAddress(req.From); err != nil {
		return fmt.Errorf("invalid from: %w", err)
	}
	if len(req.To) == 0 && len(req.Cc) == 0 && len(req.Bcc) == 0 {
		return fmt.Errorf("at least one recipient required")
	}
	for _, list := range [][]string{req.To, req.Cc, req.Bcc} {
		for _, a := range list {
			if _, err := mail.ParseAddress(a); err != nil {
				return fmt.Errorf("invalid recipient %q: %w", a, err)
			}
		}
	}
	if req.Subject == "" {
		return fmt.Errorf("subject required")
	}
	if req.Text == "" && req.HTML == "" {
		return fmt.Errorf("text or html body required")
	}
	if len(req.Attachments) > maxAttachmentCount {
		return fmt.Errorf("too many attachments (max %d)", maxAttachmentCount)
	}
	for i, a := range req.Attachments {
		if a.Filename == "" {
			return fmt.Errorf("attachment %d: filename required", i)
		}
		if a.ContentB64 == "" {
			return fmt.Errorf("attachment %d: content required", i)
		}
		// Cheap size guard on the base64 length before decoding; base64 grows
		// bytes by ~4/3, so the encoded string is bounded by that ratio.
		if len(a.ContentB64) > int(float64(maxAttachmentBytes)*1.4) {
			return fmt.Errorf("attachment %d (%q): larger than %dMB",
				i, a.Filename, maxAttachmentBytes/1024/1024)
		}
	}
	return nil
}

// decodeAttachments turns the base64 payloads into raw bytes + fills in a
// default content-type from the filename extension. Returns an error on the
// first invalid attachment so the caller can 400 with a precise message.
type decodedAttachment struct {
	filename    string
	contentType string
	content     []byte
}

func decodeAttachments(in []sendAttachmentIn) ([]decodedAttachment, error) {
	out := make([]decodedAttachment, 0, len(in))
	for i, a := range in {
		raw, err := base64.StdEncoding.DecodeString(a.ContentB64)
		if err != nil {
			return nil, fmt.Errorf("attachment %d (%q): invalid base64: %w",
				i, a.Filename, err)
		}
		if len(raw) > maxAttachmentBytes {
			return nil, fmt.Errorf("attachment %d (%q): %d bytes exceeds %dMB",
				i, a.Filename, len(raw), maxAttachmentBytes/1024/1024)
		}
		ct := a.ContentType
		if ct == "" {
			ct = mime.TypeByExtension(filepathExt(a.Filename))
			if ct == "" {
				ct = "application/octet-stream"
			}
		}
		out = append(out, decodedAttachment{
			filename:    a.Filename,
			contentType: ct,
			content:     raw,
		})
	}
	return out, nil
}

func filepathExt(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return ""
	}
	return name[i:]
}

// composeMIME builds an RFC 5322 message from the JSON request.
//
// Shape depends on what's present:
//
//	text only, no attach          → single text/plain
//	html only, no attach          → single text/html
//	text + html, no attach        → multipart/alternative
//	any body + attachments        → multipart/mixed
//	                                  ├── (body block above)
//	                                  └── attachment parts, base64 encoded
//
// Line endings are CRLF per RFC 5322 §2.3.
func composeMIME(req *sendEmailReq) ([]byte, error) {
	decoded, err := decodeAttachments(req.Attachments)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	hdr := func(k, v string) {
		if v == "" {
			return
		}
		fmt.Fprintf(&buf, "%s: %s\r\n", k, v)
	}

	// ── Envelope headers (same for every shape) ───────────────────
	hdr("From", req.From)
	hdr("To", strings.Join(req.To, ", "))
	if len(req.Cc) > 0 {
		hdr("Cc", strings.Join(req.Cc, ", "))
	}
	hdr("Reply-To", req.ReplyTo)
	hdr("Subject", mime.QEncoding.Encode("utf-8", req.Subject))
	hdr("MIME-Version", "1.0")
	hdr("Date", time.Now().UTC().Format(time.RFC1123Z))
	hdr("Message-ID", "<"+newMessageID()+"@mail-service.local>")

	for k, v := range req.Headers {
		if reservedHeader(k) {
			continue
		}
		hdr(k, v)
	}

	// ── Body block ────────────────────────────────────────────────
	// Build the "body block" (the whole content between the envelope
	// headers and, if attachments exist, the mixed boundary). This is
	// either raw body bytes or a multipart/alternative sub-part.
	bodyCT, bodyBytes := renderBodyBlock(req)

	if len(decoded) == 0 {
		// No attachments — body block goes straight into the message.
		hdr("Content-Type", bodyCT)
		if !strings.HasPrefix(bodyCT, "multipart/") {
			hdr("Content-Transfer-Encoding", "8bit")
		}
		buf.WriteString("\r\n")
		buf.Write(bodyBytes)
		return buf.Bytes(), nil
	}

	// Attachments present — wrap in multipart/mixed.
	mixedBoundary := newBoundary()
	hdr("Content-Type", `multipart/mixed; boundary="`+mixedBoundary+`"`)
	buf.WriteString("\r\n")

	// First part: the body block. If bodyCT is itself multipart, the raw
	// bytes already include its own boundary — we just put them under a
	// Content-Type header addressed at the mixed part.
	fmt.Fprintf(&buf, "--%s\r\n", mixedBoundary)
	fmt.Fprintf(&buf, "Content-Type: %s\r\n", bodyCT)
	if !strings.HasPrefix(bodyCT, "multipart/") {
		buf.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	}
	buf.WriteString("\r\n")
	buf.Write(bodyBytes)
	if !bytes.HasSuffix(bodyBytes, []byte("\r\n")) {
		buf.WriteString("\r\n")
	}

	// Subsequent parts: attachments, base64-encoded with CRLF line breaks
	// every 76 chars (RFC 2045). Filename in Content-Disposition uses
	// RFC 5987 encoding for non-ASCII names.
	for _, a := range decoded {
		fmt.Fprintf(&buf, "--%s\r\n", mixedBoundary)
		fmt.Fprintf(&buf, "Content-Type: %s\r\n", a.contentType)
		fmt.Fprintf(&buf, "Content-Transfer-Encoding: base64\r\n")
		fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=%q\r\n\r\n",
			a.filename)
		writeBase64Wrapped(&buf, a.content)
		buf.WriteString("\r\n")
	}
	fmt.Fprintf(&buf, "--%s--\r\n", mixedBoundary)

	return buf.Bytes(), nil
}

// renderBodyBlock returns (Content-Type header, raw bytes) for the body
// portion of a message — text/plain, text/html, or multipart/alternative
// when both are present. Never includes the envelope headers.
func renderBodyBlock(req *sendEmailReq) (string, []byte) {
	switch {
	case req.Text != "" && req.HTML != "":
		boundary := newBoundary()
		var b bytes.Buffer
		writePart(&b, boundary, "text/plain; charset=utf-8", req.Text)
		writePart(&b, boundary, "text/html; charset=utf-8", req.HTML)
		fmt.Fprintf(&b, "--%s--\r\n", boundary)
		return `multipart/alternative; boundary="` + boundary + `"`, b.Bytes()
	case req.HTML != "":
		return "text/html; charset=utf-8", []byte(req.HTML)
	default:
		return "text/plain; charset=utf-8", []byte(req.Text)
	}
}

// writeBase64Wrapped emits base64 bytes broken into 76-column lines per
// RFC 2045 §6.8. Some mail clients reject unwrapped base64.
func writeBase64Wrapped(w *bytes.Buffer, data []byte) {
	encoded := base64.StdEncoding.EncodeToString(data)
	const width = 76
	for i := 0; i < len(encoded); i += width {
		j := i + width
		if j > len(encoded) {
			j = len(encoded)
		}
		w.WriteString(encoded[i:j])
		w.WriteString("\r\n")
	}
}

func writePart(buf *bytes.Buffer, boundary, contentType, body string) {
	fmt.Fprintf(buf, "--%s\r\n", boundary)
	fmt.Fprintf(buf, "Content-Type: %s\r\n", contentType)
	fmt.Fprintf(buf, "Content-Transfer-Encoding: 8bit\r\n\r\n")
	buf.WriteString(body)
	if !strings.HasSuffix(body, "\r\n") {
		buf.WriteString("\r\n")
	}
}

func newMessageID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newBoundary() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "mailsvc_" + hex.EncodeToString(b)
}

// dkimSign locates (or auto-provisions) the from-domain and signs the raw
// message with its private key. Returns:
//   - (signedBytes, nil) when signed successfully
//   - (nil, err)        when the domain is unknown/unverified — caller decides
//     whether that's fatal (cloud mode) or acceptable (local mode).
func (s *Server) dkimSign(ctx context.Context, fromAddr string, raw []byte) ([]byte, error) {
	domain := domainOf(fromAddr)
	if domain == "" {
		return nil, errors.New("from address missing domain")
	}

	// Try to find an existing verified domain for the caller.
	d, err := s.findOrProvisionDomain(ctx, domain)
	if err != nil {
		return nil, err
	}
	if d.DKIMPrivateKey == "" {
		return nil, errors.New("domain has no DKIM key")
	}

	// Cloud mode: require DKIM verified at DNS level before we sign.
	// Local mode: sign anyway so devs can see the header land.
	if s.CloudMode && d.DKIMVerifiedAt == nil {
		return nil, errors.New("DKIM not yet verified at DNS")
	}

	return dkim.Sign(raw, dkim.Options{
		Domain:        d.Name,
		Selector:      d.DKIMSelector,
		PrivateKeyPEM: d.DKIMPrivateKey,
	})
}

// findOrProvisionDomain returns the Domain for `name`. If the caller has a
// team and `name` matches an auto-verify suffix (`.test` etc.), it's
// provisioned with a fresh DKIM key on the fly. Otherwise returns whatever
// the DB says — or ErrNotFound.
func (s *Server) findOrProvisionDomain(ctx context.Context, name string) (*storage.Domain, error) {
	name = strings.ToLower(strings.TrimSpace(name))

	// Search all teams the current user belongs to. For MVP that's exactly
	// one team; the loop future-proofs multi-team members.
	u := auth.UserFrom(ctx)
	if u == nil {
		return nil, errors.New("no authenticated user")
	}
	teams, err := s.Store.ListTeamsForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	for _, t := range teams {
		list, err := s.Store.ListDomainsForTeam(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		for _, d := range list {
			if strings.EqualFold(d.Name, name) {
				// list responses scrub the private key — re-fetch by id for signing.
				full, err := s.Store.GetDomain(ctx, d.ID)
				if err != nil {
					return nil, err
				}
				return full, nil
			}
		}
	}

	// Not found — auto-provision if the name matches an auto-verify suffix.
	if !s.matchesAutoVerify(name) || len(teams) == 0 {
		return nil, storage.ErrNotFound
	}

	key, err := dnsx.GenerateDKIM("ms1")
	if err != nil {
		return nil, fmt.Errorf("dkim gen: %w", err)
	}
	now := time.Now().UTC()
	d := &storage.Domain{
		ID:             newAPIID("dom"),
		TeamID:         teams[0].ID,
		Name:           name,
		DKIMSelector:   key.Selector,
		DKIMPublicKey:  key.PublicB64,
		DKIMPrivateKey: key.PrivatePEM,
		AutoVerified:   true,
		// Auto-verified: mark all four as verified now so local dev "just works".
		SPFVerifiedAt:   &now,
		DKIMVerifiedAt:  &now,
		DMARCVerifiedAt: &now,
		MXVerifiedAt:    &now,
		CreatedAt:       now,
	}
	if err := s.Store.InsertDomain(ctx, d); err != nil {
		return nil, err
	}
	// InsertDomain writes verification cols only via UpdateDomainVerification,
	// so persist them now.
	if err := s.Store.UpdateDomainVerification(ctx, d); err != nil {
		return nil, err
	}
	s.Logger.Info("auto-provisioned domain",
		slog.String("name", d.Name),
		slog.String("team_id", d.TeamID),
	)
	return d, nil
}

func (s *Server) matchesAutoVerify(name string) bool {
	name = strings.ToLower(name)
	for _, suf := range s.AutoVerifyDomains {
		suf = strings.ToLower(strings.TrimSpace(suf))
		if suf == "" {
			continue
		}
		if strings.HasSuffix("."+name, suf) || name == strings.TrimPrefix(suf, ".") {
			return true
		}
	}
	return false
}

func domainOf(addr string) string {
	i := strings.LastIndexByte(addr, '@')
	if i < 0 || i == len(addr)-1 {
		return ""
	}
	return strings.ToLower(addr[i+1:])
}

// firstNonEmptyText returns the plaintext body if present, else the HTML.
// Even a raw HTML string is better than nothing for search — humans rarely
// need to search for tag names, and the tokeniser strips markup effectively.
func firstNonEmptyText(text, html string) string {
	if text != "" {
		return text
	}
	return html
}

// reservedHeader lists headers callers must not override — we own them.
func reservedHeader(name string) bool {
	switch strings.ToLower(name) {
	case "from", "to", "cc", "bcc", "subject", "date",
		"message-id", "mime-version", "content-type",
		"content-transfer-encoding":
		return true
	}
	return false
}
