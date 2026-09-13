// Package warmup implements a per-recipient-domain send-rate scheduler.
//
// New sending IPs must ramp their volume gradually or receiving mail
// providers flag them as spam and start rejecting. Industry consensus is:
//   week 1     50 / day / provider
//   week 2    500 / day / provider
//   week 3   5,000 / day / provider
//   week 4+  uncapped (reputation established)
//
// This scheduler tracks per-provider send counts, resets daily (UTC), and
// tells callers whether a send is allowed. It never blocks — callers decide
// what to do with a denial (queue for tomorrow, spill to a fallback relay,
// or hard-reject; policy is deliberately not encoded here).
package warmup

import (
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Provider groups recipient domains that share reputation policy. Gmail
// treats gmail.com and googlemail.com identically; Microsoft treats
// outlook.com/hotmail.com/live.com together. Keeping these together
// prevents accidentally exceeding a per-provider quota by splitting sends
// across the aliases they own.
type Provider string

const (
	ProviderGmail   Provider = "gmail"
	ProviderMS      Provider = "microsoft"
	ProviderYahoo   Provider = "yahoo"
	ProviderAOL     Provider = "aol"
	ProviderApple   Provider = "apple"
	ProviderProton  Provider = "proton"
	ProviderFastmail Provider = "fastmail"
	ProviderOther   Provider = "other"
)

// providerFor maps a recipient hostname to a canonical Provider. Anything
// unknown falls into "other" (which gets its own quota so a big blast to
// generic corporate MXes still gets rate-limited).
func providerFor(recipientHost string) Provider {
	h := strings.ToLower(strings.TrimSpace(recipientHost))
	switch {
	case strings.HasSuffix(h, "gmail.com"), strings.HasSuffix(h, "googlemail.com"), strings.HasSuffix(h, "google.com"):
		return ProviderGmail
	case strings.HasSuffix(h, "outlook.com"), strings.HasSuffix(h, "hotmail.com"),
		strings.HasSuffix(h, "live.com"), strings.HasSuffix(h, "msn.com"),
		strings.HasSuffix(h, "office365.com"), strings.HasSuffix(h, "outlook.office365.com"):
		return ProviderMS
	case strings.HasSuffix(h, "yahoo.com"), strings.HasSuffix(h, "ymail.com"), strings.HasSuffix(h, "rocketmail.com"):
		return ProviderYahoo
	case strings.HasSuffix(h, "aol.com"):
		return ProviderAOL
	case strings.HasSuffix(h, "icloud.com"), strings.HasSuffix(h, "me.com"), strings.HasSuffix(h, "mac.com"):
		return ProviderApple
	case strings.HasSuffix(h, "proton.me"), strings.HasSuffix(h, "protonmail.com"), strings.HasSuffix(h, "pm.me"):
		return ProviderProton
	case strings.HasSuffix(h, "fastmail.com"), strings.HasSuffix(h, "fastmail.fm"):
		return ProviderFastmail
	default:
		return ProviderOther
	}
}

// Config sets per-provider daily caps. Zero means uncapped. Set to
// aggressive values on day 1, relax over the warmup period.
type Config struct {
	// Started is when the sending IP first went live. Used to compute the
	// current warmup week automatically.
	Started time.Time
	// PerWeek[week][provider] = daily cap. Week 0 = first week.
	// A missing entry defaults to the same-week Uncapped value.
	// If PerWeek is nil, the DefaultRamp is applied.
	PerWeek []map[Provider]int
}

// DefaultRamp is the industry-consensus ramp. Applied when Config.PerWeek
// is empty. Beyond week 3 everything is uncapped.
func DefaultRamp() []map[Provider]int {
	return []map[Provider]int{
		// Week 0 (days 1-7)
		{ProviderGmail: 50, ProviderMS: 50, ProviderYahoo: 50, ProviderOther: 100},
		// Week 1 (days 8-14)
		{ProviderGmail: 500, ProviderMS: 500, ProviderYahoo: 500, ProviderOther: 1000},
		// Week 2 (days 15-21)
		{ProviderGmail: 5000, ProviderMS: 5000, ProviderYahoo: 5000, ProviderOther: 10000},
	}
}

// Scheduler holds per-provider counters. Zero value is usable but capless —
// use New for the default ramp.
type Scheduler struct {
	cfg    Config
	now    func() time.Time
	logger *slog.Logger

	mu     sync.Mutex
	dayUTC string // "2006-01-02" — resets counts when this rolls over
	counts map[Provider]int
}

// New returns a Scheduler with the default ramp, started as of now.
// Callers can override cfg.Started later if needed (e.g. loaded from DB).
func New(logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		cfg: Config{
			Started: time.Now().UTC(),
			PerWeek: DefaultRamp(),
		},
		now:    func() time.Time { return time.Now().UTC() },
		logger: logger.With(slog.String("component", "warmup")),
		counts: make(map[Provider]int),
	}
}

// SetStarted overrides the ramp origin (useful when the sending IP has
// been live for a while but the process just started).
func (s *Scheduler) SetStarted(t time.Time) {
	s.mu.Lock()
	s.cfg.Started = t.UTC()
	s.mu.Unlock()
}

// Decision is the result of an Allow call.
type Decision struct {
	Allowed    bool
	Provider   Provider
	SentToday  int // how many we've already sent today to this provider
	DailyLimit int // 0 means uncapped
	WeekIndex  int // 0-based
}

// Allow reports whether sending one message to recipientHost is within
// today's quota. Increments the counter on Allowed=true.
func (s *Scheduler) Allow(recipientHost string) Decision {
	provider := providerFor(recipientHost)
	limit := s.limitFor(provider)

	s.mu.Lock()
	defer s.mu.Unlock()

	today := s.now().Format("2006-01-02")
	if s.dayUTC != today {
		s.dayUTC = today
		s.counts = make(map[Provider]int)
	}

	sent := s.counts[provider]
	weekIdx := s.weekIndex()

	if limit > 0 && sent >= limit {
		s.logger.Warn("warmup denied",
			slog.String("provider", string(provider)),
			slog.Int("sent_today", sent),
			slog.Int("daily_limit", limit),
			slog.Int("week_index", weekIdx),
		)
		return Decision{
			Allowed:    false,
			Provider:   provider,
			SentToday:  sent,
			DailyLimit: limit,
			WeekIndex:  weekIdx,
		}
	}

	s.counts[provider] = sent + 1
	return Decision{
		Allowed:    true,
		Provider:   provider,
		SentToday:  sent + 1,
		DailyLimit: limit,
		WeekIndex:  weekIdx,
	}
}

// Status returns per-provider counters for the dashboard. Snapshot only —
// safe to call concurrently with Allow.
type Status struct {
	Date       string           `json:"date"`
	WeekIndex  int              `json:"week_index"`
	Counts     map[string]int   `json:"counts"`
	DailyLimit map[string]int   `json:"daily_limit"`
}

func (s *Scheduler) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	weekIdx := s.weekIndex()
	counts := make(map[string]int, len(s.counts))
	limits := make(map[string]int)
	for p, c := range s.counts {
		counts[string(p)] = c
	}
	if weekIdx < len(s.cfg.PerWeek) {
		for p, l := range s.cfg.PerWeek[weekIdx] {
			limits[string(p)] = l
		}
	}
	return Status{
		Date:       s.now().Format("2006-01-02"),
		WeekIndex:  weekIdx,
		Counts:     counts,
		DailyLimit: limits,
	}
}

// weekIndex returns the current warmup week (0-based). Caller holds s.mu.
func (s *Scheduler) weekIndex() int {
	if s.cfg.Started.IsZero() {
		return 0
	}
	elapsed := s.now().Sub(s.cfg.Started)
	if elapsed < 0 {
		return 0
	}
	return int(elapsed / (7 * 24 * time.Hour))
}

// limitFor returns the daily cap for provider at the current week, or 0
// (uncapped) if we're past the ramp.
func (s *Scheduler) limitFor(provider Provider) int {
	weekIdx := s.weekIndex()
	if weekIdx >= len(s.cfg.PerWeek) {
		return 0
	}
	return s.cfg.PerWeek[weekIdx][provider]
}
