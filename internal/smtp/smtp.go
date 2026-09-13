// Package smtp implements the inbound SMTP listener.
// Backed by github.com/emersion/go-smtp for wire protocol; our code owns
// session policy, size/rcpt limits, MIME parsing, storage handoff, and
// mailbox routing.
package smtp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	smtpsrv "github.com/emersion/go-smtp"
	"github.com/jhillyerd/enmime"

	"github.com/shamil3ilm/mail-service/internal/auth"
	"github.com/shamil3ilm/mail-service/internal/dsn"
	"github.com/shamil3ilm/mail-service/internal/events"
	"github.com/shamil3ilm/mail-service/internal/rawstore"
	"github.com/shamil3ilm/mail-service/internal/router"
	"github.com/shamil3ilm/mail-service/internal/storage"
)

// Options controls the listener.
type Options struct {
	Addr        string
	Domain      string // greeting hostname; local dev uses "localhost"
	MaxMessageB int    // hard cap on DATA size, bytes
	MaxRcpts    int    // per-transaction RCPT TO count
	ReadTimeout time.Duration
	WriteTimeout time.Duration

	// RequireAuth switches the listener into "submission" mode:
	//   - MAIL FROM is rejected until AUTH succeeds
	//   - AUTH PLAIN validates password against api_keys (password = msk_...)
	// Used for the :587 submission port so apps can authenticate as a user.
	// The dev-catch listener (:1025 / :2525) leaves this false.
	RequireAuth bool
}

// Deps is the runtime plumbing the SMTP server hands to each session.
type Deps struct {
	Store  storage.Store
	Raw    *rawstore.Store
	Router *router.Router
	Bus    events.Bus // may be nil in tests
	Logger *slog.Logger
}

// NewServer wires the emersion Backend + go-smtp server with our policy.
func NewServer(opts Options, deps Deps) *smtpsrv.Server {
	if opts.MaxMessageB == 0 {
		opts.MaxMessageB = 25 * 1024 * 1024 // 25 MB
	}
	if opts.MaxRcpts == 0 {
		opts.MaxRcpts = 50
	}
	if opts.ReadTimeout == 0 {
		opts.ReadTimeout = 60 * time.Second
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = 60 * time.Second
	}
	if opts.Domain == "" {
		opts.Domain = "localhost"
	}

	be := &backend{deps: deps, opts: opts}

	s := smtpsrv.NewServer(be)
	s.Addr = opts.Addr
	s.Domain = opts.Domain
	s.ReadTimeout = opts.ReadTimeout
	s.WriteTimeout = opts.WriteTimeout
	s.MaxMessageBytes = int64(opts.MaxMessageB)
	s.MaxRecipients = opts.MaxRcpts
	s.AllowInsecureAuth = true // dev only; TLS/AUTH enforced in cloud mode later

	return s
}

// backend is the go-smtp Backend implementation.
type backend struct {
	deps Deps
	opts Options
}

// NewSession is called per TCP connection.
func (b *backend) NewSession(c *smtpsrv.Conn) (smtpsrv.Session, error) {
	return &session{
		deps: b.deps,
		opts: b.opts,
		id:   newID("sess"),
		peer: c.Conn().RemoteAddr().String(),
	}, nil
}

// session holds transaction state for one SMTP conversation.
type session struct {
	deps Deps
	opts Options
	id   string
	peer string

	authed   bool             // true after successful AUTH PLAIN
	authUser *storage.User    // populated on AUTH; nil for the dev-catch path
	authKey  string           // api_key.id — for audit log; empty for cookie/dev

	from string
	to   []string
}

func (s *session) log() *slog.Logger {
	return s.deps.Logger.With(
		slog.String("component", "smtp"),
		slog.String("sess_id", s.id),
		slog.String("peer", s.peer),
	)
}

// AuthMechanisms tells go-smtp which SASL mechanisms to advertise in EHLO.
// Advertised only on the submission listener; the dev-catch listener leaves
// this returning nil so AUTH is not offered.
//
// Present on *session means the server sees this session as an AuthSession
// (interface satisfaction) and adds the AUTH capability to its EHLO response.
func (s *session) AuthMechanisms() []string {
	if !s.opts.RequireAuth {
		return nil
	}
	return []string{sasl.Plain}
}

// Auth returns a sasl.Server for the requested mechanism.
// For PLAIN, the callback validates the password as an mail-service API key
// (`msk_...`) and, on success, marks the session authenticated.
//
// Username is intentionally ignored — the API key alone identifies the
// caller. That's the same UX as Postmark / Mailgun / Resend, and it means
// a Laravel `MAIL_USERNAME` can be literally anything.
func (s *session) Auth(mech string) (sasl.Server, error) {
	if !s.opts.RequireAuth {
		return nil, smtpsrv.ErrAuthUnsupported
	}
	if mech != sasl.Plain {
		return nil, smtpsrv.ErrAuthUnsupported
	}
	return sasl.NewPlainServer(func(identity, username, password string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		if password == "" {
			return errors.New("535 5.7.8 authentication failed")
		}
		key, err := s.deps.Store.GetAPIKeyByHash(ctx, auth.HashAPIKey(password))
		if err != nil {
			s.log().Warn("auth failed (unknown key)", slog.String("user", username))
			return errors.New("535 5.7.8 authentication failed")
		}
		u, err := s.deps.Store.GetUserByID(ctx, key.UserID)
		if err != nil {
			s.log().Warn("auth failed (missing user)", slog.String("key_id", key.ID))
			return errors.New("535 5.7.8 authentication failed")
		}

		s.authed = true
		s.authUser = u
		s.authKey = key.ID

		// Fire-and-forget touch of last_used_at.
		go func(id string) {
			bg, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = s.deps.Store.TouchAPIKey(bg, id, time.Now().UTC())
		}(key.ID)

		s.log().Info("auth ok",
			slog.String("user", u.Email),
			slog.String("key_id", key.ID),
		)
		return nil
	}), nil
}

func (s *session) Mail(from string, _ *smtpsrv.MailOptions) error {
	if s.opts.RequireAuth && !s.authed {
		return errors.New("530 5.7.0 authentication required")
	}
	s.from = strings.TrimSpace(from)
	s.to = s.to[:0]
	s.log().Debug("mail from", slog.String("from", s.from))
	return nil
}

func (s *session) Rcpt(to string, _ *smtpsrv.RcptOptions) error {
	to = strings.TrimSpace(to)
	if len(s.to) >= s.opts.MaxRcpts {
		return errors.New("452 too many recipients")
	}
	s.to = append(s.to, to)
	s.log().Debug("rcpt to", slog.String("to", to))
	return nil
}

// Data is where we persist. Read raw bytes into memory (bounded by MaxMessageBytes),
// parse MIME, write raw to disk, resolve mailbox per recipient, insert row.
func (s *session) Data(r io.Reader) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Buffer the message once so we can (a) parse MIME headers and (b) reuse
	// bytes for filesystem storage without a second copy from the wire.
	var buf bytes.Buffer
	limited := io.LimitReader(r, int64(s.opts.MaxMessageB)+1)
	n, err := buf.ReadFrom(limited)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if n > int64(s.opts.MaxMessageB) {
		return errors.New("552 message too large")
	}

	envelope, err := enmime.ReadEnvelope(bytes.NewReader(buf.Bytes()))
	if err != nil {
		// Never fail-reject: mailbox might still want the raw bytes.
		// Log and continue with a minimal envelope.
		s.log().Warn("mime parse failed; storing raw only", slog.String("err", err.Error()))
		envelope = &enmime.Envelope{}
	}

	subject := envelope.GetHeader("Subject")
	messageID := strings.Trim(envelope.GetHeader("Message-ID"), "<>")
	inReplyTo := strings.Trim(envelope.GetHeader("In-Reply-To"), "<>")
	refs := parseRefs(envelope.GetHeader("References"))

	now := time.Now().UTC()

	// Persist per-recipient: each RCPT TO gets its own message row so per-mailbox
	// list queries are trivial. This is how Migadu/Fastmail model it too.
	inserted := 0
	for _, addr := range s.to {
		mb, err := s.deps.Router.Resolve(ctx, addr)
		if err != nil {
			s.log().Error("route", slog.String("addr", addr), slog.String("err", err.Error()))
			continue
		}

		msgID := newID("msg")
		rawPath, size, werr := s.deps.Raw.Write(msgID, now, bytes.NewReader(buf.Bytes()))
		if werr != nil {
			s.log().Error("rawstore write", slog.String("err", werr.Error()))
			continue
		}

		m := &storage.Message{
			ID:          msgID,
			MailboxID:   mb.ID,
			FromAddr:    s.from,
			ToAddrs:     []string{addr},
			Subject:     subject,
			MessageID:   messageID,
			InReplyTo:   inReplyTo,
			References:  refs,
			RawPath:     rawPath,
			Size:        size,
			ReceivedAt:  now,
			// BodyPreview includes attachment filenames so FTS matches
			// "budget.xlsx" queries even when the term isn't in body/subject.
			BodyPreview: bodyWithAttachmentNames(envelope),
		}
		if err := s.deps.Store.InsertMessage(ctx, m); err != nil {
			s.log().Error("insert", slog.String("err", err.Error()))
			// If DB insert fails, orphan the .eml — a retention sweep can catch it.
			continue
		}

		// Persist attachments. Best-effort: failures log but don't abort the
		// message (we've already committed the metadata + raw MIME).
		s.persistAttachments(ctx, msgID, envelope, now)

		inserted++
		s.log().Info("accepted",
			slog.String("msg_id", msgID),
			slog.String("mailbox", mb.ID),
			slog.String("from", s.from),
			slog.String("to", addr),
			slog.Int64("size", size),
			slog.String("subject", subject),
		)

		if s.deps.Bus != nil {
			s.deps.Bus.Publish(events.Event{
				Type:      events.MessageReceived,
				MessageID: msgID,
				MailboxID: mb.ID,
				Subject:   subject,
				FromAddr:  s.from,
				ToAddr:    addr,
				At:        now,
			})
		}

		// If this arrived on the "unmatched" mailbox and looks like a DSN,
		// parse it and update the suppression list. We only run this once
		// per message even if it hit multiple mailboxes.
		if inserted == 1 { // first successful insertion for this DATA
			s.maybeProcessDSN(ctx, buf.Bytes())
		}
	}

	if inserted == 0 && len(s.to) > 0 {
		return errors.New("451 temporary storage failure")
	}
	return nil
}

func (s *session) Reset()        { s.from = ""; s.to = s.to[:0] }
func (s *session) Logout() error { return nil }

// persistAttachments writes each attachment (and each inline part) to the
// rawstore and inserts a metadata row. Runs once per accepted message row.
//
// enmime already parsed the MIME once — we reuse those parts rather than
// re-parsing. Both Attachments and Inlines are included: an inline image
// in an HTML body is still something a user may want to download.
func (s *session) persistAttachments(ctx context.Context, messageID string, env *enmime.Envelope, at time.Time) {
	if env == nil {
		return
	}
	parts := make([]*enmime.Part, 0, len(env.Attachments)+len(env.Inlines))
	parts = append(parts, env.Attachments...)
	parts = append(parts, env.Inlines...)

	for _, p := range parts {
		if p == nil || len(p.Content) == 0 {
			continue
		}
		attID := newID("att")
		path, size, err := s.deps.Raw.WriteAttachment(attID, at, bytes.NewReader(p.Content))
		if err != nil {
			s.log().Warn("attachment write",
				slog.String("filename", p.FileName),
				slog.String("err", err.Error()))
			continue
		}
		a := &storage.Attachment{
			ID:        attID,
			MessageID: messageID,
			Filename:  safeFilename(p.FileName),
			MimeType:  p.ContentType,
			Size:      size,
			Path:      path,
		}
		if a.MimeType == "" {
			a.MimeType = "application/octet-stream"
		}
		if err := s.deps.Store.InsertAttachment(ctx, a); err != nil {
			s.log().Warn("attachment insert",
				slog.String("filename", p.FileName),
				slog.String("err", err.Error()))
			continue
		}
		s.log().Debug("attachment stored",
			slog.String("filename", a.Filename),
			slog.Int64("size", size),
			slog.String("mime", a.MimeType))
	}
}

// bodyWithAttachmentNames concatenates the plaintext body with every
// attachment + inline filename, whitespace-separated. Feeding this into
// FTS lets the search index match on "budget.xlsx" even when neither
// the body nor subject mentions the file.
func bodyWithAttachmentNames(env *enmime.Envelope) string {
	if env == nil {
		return ""
	}
	parts := []string{env.Text}
	for _, p := range env.Attachments {
		if p != nil && p.FileName != "" {
			parts = append(parts, p.FileName)
		}
	}
	for _, p := range env.Inlines {
		if p != nil && p.FileName != "" {
			parts = append(parts, p.FileName)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// safeFilename strips control chars and path separators so a hostile MIME
// filename can't traverse directories or corrupt HTTP headers. Falls back
// to a placeholder if the sender omitted a name.
func safeFilename(name string) string {
	if name == "" {
		return "attachment"
	}
	// Reject control chars + path separators.
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 32 || c == 127 || c == '/' || c == '\\' {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return "attachment"
	}
	return string(out)
}

// maybeProcessDSN checks whether the message is an RFC 3464 delivery-status
// notification and, if it is, adds hard-bounced recipients to the
// suppression list. Silent no-op for anything that isn't a DSN.
func (s *session) maybeProcessDSN(ctx context.Context, raw []byte) {
	reports, err := dsn.ParseFromRaw(raw)
	if err != nil || len(reports) == 0 {
		return
	}
	for _, r := range reports {
		if !r.IsHardBounce() {
			continue
		}
		sup := &storage.Suppression{
			Address: r.Recipient,
			Reason:  "bounce_hard",
			Source:  "smtp-inbound-dsn",
		}
		if err := s.deps.Store.UpsertSuppression(ctx, sup); err != nil {
			s.log().Warn("suppress from DSN",
				slog.String("addr", r.Recipient),
				slog.String("err", err.Error()),
			)
			continue
		}
		s.log().Info("auto-suppressed from DSN",
			slog.String("addr", r.Recipient),
			slog.String("status", r.StatusCode),
			slog.String("diagnostic", r.Diagnostic),
		)
	}
}

// parseRefs splits a References header into individual Message-IDs.
func parseRefs(h string) []string {
	if h == "" {
		return nil
	}
	out := make([]string, 0, 4)
	for _, part := range strings.Fields(h) {
		p := strings.Trim(part, "<>")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
