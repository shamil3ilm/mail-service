package warmup

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

func newTest(t *testing.T, dayLimit map[Provider]int, started time.Time, nowFn func() time.Time) *Scheduler {
	t.Helper()
	s := New(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	s.cfg.Started = started
	s.cfg.PerWeek = []map[Provider]int{dayLimit}
	if nowFn != nil {
		s.now = nowFn
	}
	return s
}

func TestProviderFor(t *testing.T) {
	cases := []struct {
		host string
		want Provider
	}{
		{"user@gmail.com", ProviderGmail},        // via domainOf path
		{"gmail.com", ProviderGmail},
		{"googlemail.com", ProviderGmail},
		{"outlook.com", ProviderMS},
		{"hotmail.com", ProviderMS},
		{"office365.com", ProviderMS},
		{"yahoo.com", ProviderYahoo},
		{"icloud.com", ProviderApple},
		{"proton.me", ProviderProton},
		{"random.example.com", ProviderOther},
		{"", ProviderOther},
	}
	for _, tc := range cases {
		if got := providerFor(tc.host); got != tc.want {
			t.Errorf("providerFor(%q)=%v want %v", tc.host, got, tc.want)
		}
	}
}

func TestAllowRespectsDailyCap(t *testing.T) {
	fixed := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	s := newTest(t,
		map[Provider]int{ProviderGmail: 3},
		fixed,
		func() time.Time { return fixed },
	)
	for i := 0; i < 3; i++ {
		if d := s.Allow("gmail.com"); !d.Allowed {
			t.Fatalf("send %d denied: %+v", i+1, d)
		}
	}
	d := s.Allow("gmail.com")
	if d.Allowed {
		t.Fatalf("4th send should have been denied: %+v", d)
	}
	if d.SentToday != 3 || d.DailyLimit != 3 {
		t.Errorf("stats: sent=%d limit=%d", d.SentToday, d.DailyLimit)
	}
}

func TestAllowResetsAtDayRollover(t *testing.T) {
	day1 := time.Date(2026, 9, 15, 23, 59, 0, 0, time.UTC)
	day2 := day1.Add(2 * time.Minute) // rolls into 2026-09-16
	now := day1
	s := newTest(t,
		map[Provider]int{ProviderGmail: 1},
		day1,
		func() time.Time { return now },
	)
	if !s.Allow("gmail.com").Allowed {
		t.Fatal("first send should be allowed")
	}
	if s.Allow("gmail.com").Allowed {
		t.Fatal("second send same day should be denied")
	}
	now = day2
	if !s.Allow("gmail.com").Allowed {
		t.Fatal("first send next day should be allowed after reset")
	}
}

func TestUncappedProviderPassesThrough(t *testing.T) {
	fixed := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	// Gmail capped at 1, everything else uncapped.
	s := newTest(t,
		map[Provider]int{ProviderGmail: 1},
		fixed,
		func() time.Time { return fixed },
	)
	// Bulk hit to "other" — should all pass.
	for i := 0; i < 50; i++ {
		if d := s.Allow("random.example.com"); !d.Allowed {
			t.Fatalf("uncapped denied at %d: %+v", i, d)
		}
	}
}

func TestWeekIndexAdvancesRamp(t *testing.T) {
	started := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	s := New(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	s.cfg.Started = started
	s.cfg.PerWeek = []map[Provider]int{
		{ProviderGmail: 10},
		{ProviderGmail: 100},
	}
	// Day 3 → week 0
	s.now = func() time.Time { return started.Add(3 * 24 * time.Hour) }
	if s.limitFor(ProviderGmail) != 10 {
		t.Errorf("week 0 limit: %d", s.limitFor(ProviderGmail))
	}
	// Day 10 → week 1
	s.now = func() time.Time { return started.Add(10 * 24 * time.Hour) }
	if s.limitFor(ProviderGmail) != 100 {
		t.Errorf("week 1 limit: %d", s.limitFor(ProviderGmail))
	}
	// Day 30 → past ramp, uncapped
	s.now = func() time.Time { return started.Add(30 * 24 * time.Hour) }
	if s.limitFor(ProviderGmail) != 0 {
		t.Errorf("post-ramp limit: %d", s.limitFor(ProviderGmail))
	}
}

func TestStatusSnapshot(t *testing.T) {
	fixed := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	s := newTest(t,
		map[Provider]int{ProviderGmail: 5},
		fixed,
		func() time.Time { return fixed },
	)
	s.Allow("gmail.com")
	s.Allow("gmail.com")
	st := s.Status()
	if st.Counts["gmail"] != 2 || st.DailyLimit["gmail"] != 5 {
		t.Errorf("status: %+v", st)
	}
	if st.Date != "2026-09-15" {
		t.Errorf("status date: %s", st.Date)
	}
}
