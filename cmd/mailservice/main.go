// Command mailservice runs the mail service process.
// Boot order: config → logger → storage (with migrations) →
//             rawstore → router → HTTP + SMTP → wait.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/shamil3ilm/mail-service/internal/api"
	"github.com/shamil3ilm/mail-service/internal/auth"
	"github.com/shamil3ilm/mail-service/internal/config"
	"github.com/shamil3ilm/mail-service/internal/dnspub"
	"github.com/shamil3ilm/mail-service/internal/events"
	"github.com/shamil3ilm/mail-service/internal/logger"
	"github.com/shamil3ilm/mail-service/internal/metrics"
	"github.com/shamil3ilm/mail-service/internal/provider"
	"github.com/shamil3ilm/mail-service/internal/rawstore"
	"github.com/shamil3ilm/mail-service/internal/router"
	"github.com/shamil3ilm/mail-service/internal/selfcheck"
	mailsmtp "github.com/shamil3ilm/mail-service/internal/smtp"
	"github.com/shamil3ilm/mail-service/internal/retention"
	"github.com/shamil3ilm/mail-service/internal/smsprovider"
	"github.com/shamil3ilm/mail-service/internal/storage/sqlite"
	"github.com/shamil3ilm/mail-service/internal/warmup"
)

// warmupGate adapts *warmup.Scheduler to provider.WarmupGate so the
// provider package stays free of the warmup import.
type warmupGate struct{ s *warmup.Scheduler }

func (g warmupGate) Allow(host string) provider.WarmupDecision {
	d := g.s.Allow(host)
	return provider.WarmupDecision{
		Allowed:    d.Allowed,
		Provider:   string(d.Provider),
		SentToday:  d.SentToday,
		DailyLimit: d.DailyLimit,
	}
}

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(log)

	log.Info("starting mailservice",
		slog.String("version", version),
		slog.String("mode", string(cfg.Mode)),
		slog.String("db", cfg.DBPath),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer func() { _ = store.Close() }()
	log.Info("storage ready", slog.String("path", cfg.DBPath))

	raw, err := rawstore.New(cfg.RawStorePath)
	if err != nil {
		return fmt.Errorf("open rawstore: %w", err)
	}
	log.Info("rawstore ready", slog.String("root", raw.Root))

	rtr := &router.Router{Store: store, AutoVerifyDomains: cfg.AutoVerifyDomains}

	bus := events.NewMemory(log)
	defer bus.Close()

	// First-run: create an admin account so the operator can log in.
	// Prints password to stdout exactly once; ignored on subsequent starts.
	if _, _, err := auth.BootstrapAdmin(ctx, store, log); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}

	cloudMode := cfg.Mode == config.ModeCloud
	authMgr := auth.NewManager(store, cloudMode)

	// Outbound relay — chosen by MAIL_RELAY_PROVIDER.
	//   none|capture (default local) → loop back into local storage
	//   smtp                         → hand off to an upstream MTA
	// Other providers (SES, Resend) share the SMTP transport for now.
	relay := buildRelay(cfg, store, raw, rtr, bus, log)
	log.Info("relay configured", slog.String("provider", relay.Name()))

	// Cloud mode: run non-blocking startup self-checks in a goroutine so
	// slow DNS lookups don't hold up the listeners. Results are logged; no
	// action is taken automatically — the operator reads the journal.
	if cloudMode && cfg.CloudHostname != "" {
		go runSelfChecks(ctx, cfg, store, log)
	}

	// Retention runner — auto-deletes messages older than
	// MAIL_RETENTION_DAYS. Runs no-op if the horizon is 0 (unlimited).
	go retention.New(retention.Config{
		GlobalDays: cfg.RetentionDays,
		Interval:   cfg.RetentionInterval,
	}, store, raw, log).Run(ctx)

	// DNS publisher — auto-publish generated records into an authoritative
	// DNS backend when configured. Defaults to manual (no-op) so existing
	// deployments are unchanged.
	publisher := buildPublisher(cfg, log)
	log.Info("dns publisher configured",
		slog.String("publisher", publisher.Name()),
	)

	// SMS provider — capture by default, HTTP when a URL is configured.
	sms := buildSMSProvider(cfg, store, log)
	log.Info("sms provider configured", slog.String("provider", sms.Name()))

	// ── HTTP ─────────────────────────────────────────────────────────────
	srv := &api.Server{
		Store:             store,
		Raw:               raw,
		Bus:               bus,
		Auth:              authMgr,
		Relay:             relay,
		DNSPublisher:      publisher,
		SMS:               sms,
		Logger:            log,
		CloudMode:         cloudMode,
		AutoVerifyDomains: cfg.AutoVerifyDomains,
	}
	public := &http.Server{
		Addr:              net.JoinHostPort(cfg.ListenAddr, strconv.Itoa(cfg.HTTPPort)),
		Handler:           srv.Router(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	metricsReg := metrics.New()
	registerBaselineMetrics(metricsReg, store)

	admin := &http.Server{
		Addr:              net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.AdminPort)),
		Handler:           api.AdminRouter(metricsReg),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// ── SMTP inbound (dev-catch, no auth) ──────────────────────────────
	smtpAddr := net.JoinHostPort(cfg.ListenAddr, strconv.Itoa(cfg.SMTPPort))
	smtpSrv := mailsmtp.NewServer(mailsmtp.Options{
		Addr:   smtpAddr,
		Domain: hostnameOr("localhost"),
	}, mailsmtp.Deps{
		Store:  store,
		Raw:    raw,
		Router: rtr,
		Bus:    bus,
		Logger: log,
	})

	// ── SMTP submission (authenticated) ────────────────────────────────
	// Second listener on cfg.SubmissionPort (default 587). Requires AUTH
	// PLAIN with an API key as the password. Once authed, MAIL FROM is
	// accepted and the message flows through the same pipeline.
	submissionAddr := net.JoinHostPort(cfg.ListenAddr, strconv.Itoa(cfg.SubmissionPort))
	submissionSrv := mailsmtp.NewServer(mailsmtp.Options{
		Addr:        submissionAddr,
		Domain:      hostnameOr("localhost"),
		RequireAuth: true,
	}, mailsmtp.Deps{
		Store:  store,
		Raw:    raw,
		Router: rtr,
		Bus:    bus,
		Logger: log,
	})

	errCh := make(chan error, 4)

	go func() {
		log.Info("public http listening", slog.String("addr", public.Addr))
		if err := public.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("public http: %w", err)
		}
	}()
	go func() {
		log.Info("admin http listening", slog.String("addr", admin.Addr))
		if err := admin.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("admin http: %w", err)
		}
	}()
	go func() {
		log.Info("smtp inbound listening", slog.String("addr", smtpAddr))
		if err := smtpSrv.ListenAndServe(); err != nil && !isClosed(err) {
			errCh <- fmt.Errorf("smtp: %w", err)
		}
	}()
	go func() {
		log.Info("smtp submission listening (auth required)", slog.String("addr", submissionAddr))
		if err := submissionSrv.ListenAndServe(); err != nil && !isClosed(err) {
			errCh <- fmt.Errorf("smtp submission: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errCh:
		log.Error("server error", slog.String("err", err.Error()))
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := public.Shutdown(shutdownCtx); err != nil {
		log.Warn("public shutdown", slog.String("err", err.Error()))
	}
	if err := admin.Shutdown(shutdownCtx); err != nil {
		log.Warn("admin shutdown", slog.String("err", err.Error()))
	}
	if err := smtpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("smtp shutdown", slog.String("err", err.Error()))
	}
	if err := submissionSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("smtp submission shutdown", slog.String("err", err.Error()))
	}

	log.Info("stopped cleanly")
	return nil
}

// runSelfChecks assembles the selfcheck.Config from live data (list of
// verified domains queried from storage) and runs the panel of checks.
// Cloud-only; called in a goroutine so DNS timeouts don't stall startup.
func runSelfChecks(ctx context.Context, cfg *config.Config, store *sqlite.Store, log *slog.Logger) {
	// Give DNS some time. Not fatal if it takes longer — the goroutine
	// just runs until it completes.
	sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Every verified domain gets a DKIM-record probe. We look at each
	// team's domains — the personal team is the common single-tenant case.
	var domains []string
	teams, err := store.ListTeamsForUser(sctx, "usr_admin")
	if err == nil {
		for _, t := range teams {
			ds, _ := store.ListDomainsForTeam(sctx, t.ID)
			for _, d := range ds {
				if d.DKIMVerifiedAt != nil || d.AutoVerified {
					domains = append(domains, d.Name)
				}
			}
		}
	}

	selfcheck.RunAll(sctx, selfcheck.Config{
		Hostname:      cfg.CloudHostname,
		OutboundIP:    cfg.CloudOutboundIP,
		Domains:       domains,
		DKIMSelectors: []string{"ms1"},
		DNSBLZones:    cfg.CloudDNSBLZones,
	}, log)
}

// registerBaselineMetrics pre-registers every counter + gauge we care
// about so /metrics has a stable schema even before traffic. Storage
// gauges are refreshed on a slow tick so a scrape reflects reality
// without instrumenting every write path.
func registerBaselineMetrics(reg *metrics.Registry, store *sqlite.Store) {
	// Counters — subsystems Add() into these as they observe events.
	reg.NewCounter("mailservice_messages_received_total",
		"Messages accepted via SMTP inbound.")
	reg.NewCounter("mailservice_messages_sent_total",
		"Messages accepted by POST /api/v1/emails.")
	reg.NewCounter("mailservice_sms_sent_total",
		"SMS accepted by POST /api/v1/sms.")
	reg.NewCounter("mailservice_dkim_signed_total",
		"Outbound messages that received a DKIM signature.")
	reg.NewCounter("mailservice_http_requests_total",
		"HTTP requests by status code class.")

	// Gauges — background refresher.
	mbxGauge := reg.NewGauge("mailservice_mailboxes_total",
		"Current number of mailboxes.")
	msgGauge := reg.NewGauge("mailservice_messages_total",
		"Current number of stored messages.")

	go func() {
		ctx := context.Background()
		refresh := func() {
			if mbs, err := store.ListMailboxes(ctx); err == nil {
				mbxGauge.Set(float64(len(mbs)))
			}
			// Approximate: count-all via ListMessages("", huge limit) is
			// expensive; we use a bounded sample count of first page +
			// leave a proper COUNT(*) query for later.
			if msgs, err := store.ListMessages(ctx, "", 500, 0); err == nil {
				msgGauge.Set(float64(len(msgs)))
			}
		}
		refresh()
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			refresh()
		}
	}()
}

// buildSMSProvider picks the SMS backend based on config. Unknown values
// default to Capture so misconfig doesn't fail-close a subsystem the
// operator may not care about.
func buildSMSProvider(cfg *config.Config, store *sqlite.Store, log *slog.Logger) smsprovider.Relay {
	switch cfg.SMSProvider {
	case "http":
		if cfg.SMSProviderURL == "" {
			log.Warn("sms provider: 'http' selected but URL missing — falling back to capture")
			return &smsprovider.Capture{Store: store}
		}
		return &smsprovider.HTTP{
			URL:         cfg.SMSProviderURL,
			BearerToken: cfg.SMSAuthBearer,
			Store:       store,
		}
	default:
		return &smsprovider.Capture{Store: store}
	}
}

// buildPublisher picks the DNS publisher based on config.
// Empty/unknown values default to Manual so misconfig doesn't fail-close
// on a subsystem the operator may not care about.
func buildPublisher(cfg *config.Config, log *slog.Logger) dnspub.Publisher {
	switch cfg.DNSPublisher {
	case "privatedns":
		if cfg.DNSPublisherURL == "" || cfg.DNSPublisherToken == "" {
			log.Warn("dns publisher: 'privatedns' selected but URL or token missing — falling back to manual")
			return dnspub.Manual{}
		}
		return dnspub.New(cfg.DNSPublisherURL, cfg.DNSPublisherUser, cfg.DNSPublisherToken, nil)
	default:
		return dnspub.Manual{}
	}
}

// buildRelay picks the outbound provider based on config.
// Kept close to main.go rather than in package provider so all wiring lives
// in one place and provider stays free of config imports.
func buildRelay(
	cfg *config.Config,
	store *sqlite.Store,
	raw *rawstore.Store,
	rtr *router.Router,
	bus *events.Memory,
	log *slog.Logger,
) provider.Relay {
	switch cfg.RelayProvider {
	case config.RelaySMTP, config.RelaySES, config.RelayResend:
		// SES + Resend both accept SMTP submissions; native SDKs land later.
		return &provider.SMTPRelay{
			Cfg: provider.SMTPConfig{
				Host: cfg.RelaySMTP.Host,
				Port: cfg.RelaySMTP.Port,
				User: cfg.RelaySMTP.User,
				Pass: cfg.RelaySMTP.Pass,
			},
			// Warmup gate — outbound sends are capped per recipient
			// provider so a fresh sending IP ramps volume gracefully.
			Warmup: warmupGate{s: warmup.New(log)},
		}
	default:
		// none / anything unknown → capture into local storage.
		log.Info("using capture relay (mail loops into local storage)")
		return &provider.Capture{
			Store:  store,
			Raw:    raw,
			Router: rtr,
			Bus:    bus,
		}
	}
}

func hostnameOr(fallback string) string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return fallback
}

// isClosed reports whether err is the expected "listener closed" error from
// go-smtp's Serve after Shutdown/Close — we don't want to treat that as fatal.
func isClosed(err error) bool {
	if err == nil {
		return true
	}
	return errors.Is(err, net.ErrClosed) || err.Error() == "smtp: server closed"
}
