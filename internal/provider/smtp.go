// SMTP relay: hand the message to an upstream MTA (SES, Postmark,
// Mailgun, self-hosted Postfix — all speak SMTP).
//
// Uses net/smtp from the standard library. Not fancy — no connection pooling,
// no HELO-name customisation, no MX fallback — but every popular provider
// works because we're speaking their canonical wire protocol.

package provider

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTPConfig points at an upstream MTA.
type SMTPConfig struct {
	Host string // e.g. "email-smtp.us-east-1.amazonaws.com"
	Port int    // typically 587 (STARTTLS) or 465 (implicit TLS)
	User string // may be empty for open relays inside a private network
	Pass string
	// TLS behavior:
	//   - Port 465     → implicit TLS (dial with tls.Dial)
	//   - Port 587/25  → STARTTLS after EHLO if the server offers it
	//   - InsecureTLS  → skip cert verification (dev / self-signed only)
	InsecureTLS bool
	// DialTimeout bounds the initial TCP handshake.
	DialTimeout time.Duration
}

// SMTPRelay is a Relay backed by an upstream SMTP server.
type SMTPRelay struct {
	Cfg SMTPConfig
	// Warmup is optional. When set, recipients that exceed the per-provider
	// daily cap are moved to Rejected (not Accepted) with a warning logged.
	// Nil means unlimited.
	Warmup WarmupGate
}

// WarmupGate is the small subset of *warmup.Scheduler we depend on. Kept
// as an interface so provider stays free of the warmup package import.
type WarmupGate interface {
	Allow(recipientHost string) WarmupDecision
}

// WarmupDecision mirrors warmup.Decision so callers don't need to import
// warmup just to type-assert.
type WarmupDecision struct {
	Allowed    bool
	Provider   string
	SentToday  int
	DailyLimit int
}

// Name returns the provider identifier for logs and metrics.
func (r *SMTPRelay) Name() string { return "smtp" }

// Send opens one SMTP transaction and delivers to all recipients.
// Provider errors are mapped to ErrPermanent / ErrTransient by SMTP class
// (4xx transient, 5xx permanent); anything else is wrapped and returned.
func (r *SMTPRelay) Send(ctx context.Context, req *SendRequest) (*SendResult, error) {
	if r.Cfg.Host == "" {
		return nil, ErrNoProvider
	}
	if req == nil || req.From == "" {
		return nil, fmt.Errorf("invalid request")
	}
	recips := allRecipients(req)
	if len(recips) == 0 {
		return nil, fmt.Errorf("no recipients")
	}

	dialTimeout := r.Cfg.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = 15 * time.Second
	}
	addr := net.JoinHostPort(r.Cfg.Host, itoa(r.Cfg.Port))

	// Honour the caller's deadline if one is set — otherwise cap ourselves.
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d < dialTimeout {
			dialTimeout = d
		}
	}

	client, err := dialSMTP(addr, r.Cfg.Host, dialTimeout, r.Cfg.Port == 465, r.Cfg.InsecureTLS)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Hello(clientHostname()); err != nil {
		return nil, wrapErr(err)
	}

	// STARTTLS for anything other than the implicit-TLS 465.
	if r.Cfg.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			tlsCfg := &tls.Config{
				ServerName:         r.Cfg.Host,
				InsecureSkipVerify: r.Cfg.InsecureTLS, // #nosec G402 — dev opt-in
				MinVersion:         tls.VersionTLS12,
			}
			if err := client.StartTLS(tlsCfg); err != nil {
				return nil, wrapErr(err)
			}
		}
	}

	if r.Cfg.User != "" {
		if ok, _ := client.Extension("AUTH"); ok {
			auth := smtp.PlainAuth("", r.Cfg.User, r.Cfg.Pass, r.Cfg.Host)
			if err := client.Auth(auth); err != nil {
				return nil, wrapErr(err)
			}
		}
	}

	if err := client.Mail(req.From); err != nil {
		return nil, wrapErr(err)
	}

	result := &SendResult{}
	for _, addr := range recips {
		// Warmup gate — recipients we'd blast past today's cap for their
		// provider get rejected before we open RCPT TO. Preserves IP
		// reputation on a fresh sending IP.
		if r.Warmup != nil {
			if d := r.Warmup.Allow(hostOf(addr)); !d.Allowed {
				result.Rejected = append(result.Rejected, addr)
				continue
			}
		}
		if err := client.Rcpt(addr); err != nil {
			// Per-recipient rejection is normal (bad address, over quota) —
			// keep going for the rest instead of failing the whole batch.
			result.Rejected = append(result.Rejected, addr)
			continue
		}
		result.Accepted = append(result.Accepted, addr)
	}

	if len(result.Accepted) == 0 {
		return result, fmt.Errorf("%w: all recipients rejected", ErrPermanent)
	}

	w, err := client.Data()
	if err != nil {
		return result, wrapErr(err)
	}
	if _, err := io.Copy(w, req.Raw); err != nil {
		_ = w.Close()
		return result, wrapErr(err)
	}
	if err := w.Close(); err != nil {
		return result, wrapErr(err)
	}
	if err := client.Quit(); err != nil {
		// Quit failing after successful DATA is rarely fatal for delivery,
		// but log-worthy — surface it as a transient error so callers can
		// decide.
		return result, fmt.Errorf("%w: quit: %v", ErrTransient, err)
	}

	// Providers that assign their own message-id would return it here in a
	// response we'd have to parse — net/smtp doesn't expose the last reply.
	// Left empty; callers use our own generated id in event payloads.
	return result, nil
}

func dialSMTP(addr, host string, timeout time.Duration, implicitTLS, insecure bool) (*smtp.Client, error) {
	dialer := &net.Dialer{Timeout: timeout}
	if implicitTLS {
		tlsCfg := &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: insecure, // #nosec G402
			MinVersion:         tls.VersionTLS12,
		}
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
		if err != nil {
			return nil, err
		}
		return smtp.NewClient(conn, host)
	}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return smtp.NewClient(conn, host)
}

// hostOf returns the host portion of an email address (case-insensitive
// callers on the receiving side apply their own normalisation). Empty
// input or a malformed address returns "".
func hostOf(addr string) string {
	i := strings.LastIndexByte(addr, '@')
	if i < 0 || i == len(addr)-1 {
		return ""
	}
	return addr[i+1:]
}

func allRecipients(req *SendRequest) []string {
	all := make([]string, 0, len(req.To)+len(req.Cc)+len(req.Bcc))
	all = append(all, req.To...)
	all = append(all, req.Cc...)
	all = append(all, req.Bcc...)
	return all
}

func clientHostname() string {
	// Presented in the EHLO greeting. Most providers only care that it's a
	// valid hostname; we use the OS hostname when available, else localhost.
	h, err := osHostname()
	if err != nil || h == "" {
		return "localhost"
	}
	return h
}

// wrapErr maps SMTP errors to permanent/transient categories.
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	// net/smtp returns *textproto.Error with .Code as the numeric status.
	var te interface{ Msg() string }
	_ = te
	// We look for the printable form since asserting the concrete type
	// would require importing net/textproto everywhere.
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "5"):
		return fmt.Errorf("%w: %v", ErrPermanent, err)
	case strings.HasPrefix(msg, "4"):
		return fmt.Errorf("%w: %v", ErrTransient, err)
	}
	// Non-SMTP errors (dial timeout, TLS handshake) — treat as transient.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", ErrTransient, err)
	}
	return err
}

// itoa is a tiny replacement for strconv.Itoa so smtp.go stays dep-free of strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
