// Package config loads runtime configuration from environment variables.
// 12-factor: no config files, no surprises across environments.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Mode string

const (
	ModeLocal Mode = "local"
	ModeCloud Mode = "cloud"
)

type MailboxMode string

const (
	MailboxCapture MailboxMode = "capture"
	MailboxRelay   MailboxMode = "relay"
)

type RelayProvider string

const (
	RelayNone   RelayProvider = "none"
	RelaySMTP   RelayProvider = "smtp"
	RelaySES    RelayProvider = "ses"
	RelayResend RelayProvider = "resend"
)

type Config struct {
	Mode      Mode
	LogLevel  string
	LogFormat string

	ListenAddr     string
	HTTPPort       int
	AdminPort      int
	SMTPPort       int
	SubmissionPort int

	DBPath       string
	RawStorePath string

	AutoVerifyDomains  []string
	DefaultMailboxMode MailboxMode

	RelayProvider RelayProvider
	RelaySMTP     SMTPRelay

	ShutdownTimeout time.Duration

	// Cloud-only: startup self-check inputs. Ignored in local mode.
	CloudHostname   string
	CloudOutboundIP string
	CloudDNSBLZones []string

	// DNS publisher: automates publishing generated SPF/DKIM/DMARC/MX
	// records into an authoritative DNS backend (currently: privatedns).
	// Empty defaults leave the workflow manual — records shown in the
	// dashboard for the operator to copy-paste into their DNS provider.
	DNSPublisher      string // "manual" | "privatedns"
	DNSPublisherURL   string // e.g. http://127.0.0.1:8080
	DNSPublisherUser  string // login email (when Token is a password)
	DNSPublisherToken string // JWT | API key | password
}

type SMTPRelay struct {
	Host string
	Port int
	User string
	Pass string
}

// Load reads env vars (and .env if present in the working dir) into a Config.
// Real process env always wins over the .env file — same as any 12-factor app.
func Load() (*Config, error) {
	// Silently loads defaults from .env if it exists; missing file is fine.
	_ = LoadDotEnv(".env")

	c := &Config{
		Mode:               Mode(getenv("MAIL_MODE", "local")),
		LogLevel:           getenv("MAIL_LOG_LEVEL", "debug"),
		LogFormat:          getenv("MAIL_LOG_FORMAT", "text"),
		ListenAddr:         getenv("MAIL_LISTEN_ADDR", "127.0.0.1"),
		// Default HTTP port is 8035 (not 8025) because Mailpit binds :8025
		// and both tools commonly coexist on developer laptops.
		HTTPPort:  getenvInt("MAIL_HTTP_PORT", 8035),
		AdminPort: getenvInt("MAIL_ADMIN_PORT", 8036),
		// SMTP: 2525 for the same reason (Mailpit takes 1025).
		SMTPPort: getenvInt("MAIL_SMTP_PORT", 2525),
		SubmissionPort:     getenvInt("MAIL_SUBMISSION_PORT", 587),
		DBPath:             getenv("MAIL_DB_PATH", "./data/mail.db"),
		RawStorePath:       getenv("MAIL_RAW_STORE_PATH", "./data/raw"),
		AutoVerifyDomains:  splitCSV(getenv("MAIL_AUTO_VERIFY_DOMAINS", ".test,.local,.localhost")),
		DefaultMailboxMode: MailboxMode(getenv("MAIL_DEFAULT_MAILBOX_MODE", "capture")),
		RelayProvider:      RelayProvider(getenv("MAIL_RELAY_PROVIDER", "none")),
		RelaySMTP: SMTPRelay{
			Host: getenv("MAIL_RELAY_SMTP_HOST", ""),
			Port: getenvInt("MAIL_RELAY_SMTP_PORT", 587),
			User: getenv("MAIL_RELAY_SMTP_USER", ""),
			Pass: getenv("MAIL_RELAY_SMTP_PASS", ""),
		},
		ShutdownTimeout: getenvDuration("MAIL_SHUTDOWN_TIMEOUT", 15*time.Second),

		CloudHostname:   getenv("MAIL_CLOUD_HOSTNAME", ""),
		CloudOutboundIP: getenv("MAIL_CLOUD_OUTBOUND_IP", ""),
		CloudDNSBLZones: splitCSV(getenv("MAIL_CLOUD_DNSBL_ZONES",
			"zen.spamhaus.org,b.barracudacentral.org,bl.spamcop.net")),

		DNSPublisher:      getenv("MAIL_DNS_PUBLISHER", "manual"),
		DNSPublisherURL:   getenv("MAIL_DNS_PUBLISHER_URL", ""),
		DNSPublisherUser:  getenv("MAIL_DNS_PUBLISHER_USER", "admin@local"),
		DNSPublisherToken: getenv("MAIL_DNS_PUBLISHER_TOKEN", ""),
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	switch c.Mode {
	case ModeLocal, ModeCloud:
	default:
		return fmt.Errorf("invalid MAIL_MODE %q (want local|cloud)", c.Mode)
	}
	switch c.DefaultMailboxMode {
	case MailboxCapture, MailboxRelay:
	default:
		return fmt.Errorf("invalid MAIL_DEFAULT_MAILBOX_MODE %q", c.DefaultMailboxMode)
	}
	switch c.RelayProvider {
	case RelayNone, RelaySMTP, RelaySES, RelayResend:
	default:
		return fmt.Errorf("invalid MAIL_RELAY_PROVIDER %q", c.RelayProvider)
	}
	if c.RelayProvider == RelaySMTP && c.RelaySMTP.Host == "" {
		return fmt.Errorf("MAIL_RELAY_SMTP_HOST required when MAIL_RELAY_PROVIDER=smtp")
	}
	return nil
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return fallback
	}
	return n
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
