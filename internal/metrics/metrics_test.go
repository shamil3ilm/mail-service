package metrics

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestCounterInc(t *testing.T) {
	reg := New()
	c := reg.NewCounter("test_events_total", "help")
	c.Inc()
	c.Inc()
	c.Inc()

	var buf bytes.Buffer
	reg.Write(&buf)
	if !strings.Contains(buf.String(), "test_events_total 3\n") {
		t.Fatalf("expected value=3, got:\n%s", buf.String())
	}
}

func TestCounterWithLabels(t *testing.T) {
	reg := New()
	c := reg.NewCounter("http_requests_total", "help")
	c.Inc("status", "200")
	c.Inc("status", "200")
	c.Inc("status", "500")

	var buf bytes.Buffer
	reg.Write(&buf)
	out := buf.String()

	if !strings.Contains(out, `http_requests_total{status="200"} 2`) {
		t.Errorf("missing 200 series:\n%s", out)
	}
	if !strings.Contains(out, `http_requests_total{status="500"} 1`) {
		t.Errorf("missing 500 series:\n%s", out)
	}
}

func TestGaugeSet(t *testing.T) {
	reg := New()
	g := reg.NewGauge("queue_depth", "help")
	g.Set(42)
	g.Set(17) // overwrite

	var buf bytes.Buffer
	reg.Write(&buf)
	if !strings.Contains(buf.String(), "queue_depth 17\n") {
		t.Fatalf("expected last-write to win, got:\n%s", buf.String())
	}
}

func TestConcurrentSafe(t *testing.T) {
	reg := New()
	c := reg.NewCounter("concurrent_total", "")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Inc()
			}
		}()
	}
	wg.Wait()

	var buf bytes.Buffer
	reg.Write(&buf)
	if !strings.Contains(buf.String(), "concurrent_total 10000\n") {
		t.Fatalf("race lost updates:\n%s", buf.String())
	}
}

func TestPrometheusHeaders(t *testing.T) {
	reg := New()
	reg.NewCounter("mailservice_messages_total", "How many messages.")
	reg.NewGauge("mailservice_up", "Whether the mail service is up.")

	var buf bytes.Buffer
	reg.Write(&buf)
	out := buf.String()

	for _, want := range []string{
		"# HELP mailservice_messages_total How many messages.",
		"# TYPE mailservice_messages_total counter",
		"# HELP mailservice_up Whether the mail service is up.",
		"# TYPE mailservice_up gauge",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestLabelEscaping(t *testing.T) {
	reg := New()
	c := reg.NewCounter("weird_total", "")
	c.Inc("k", `has "quotes" and \ slashes`)

	var buf bytes.Buffer
	reg.Write(&buf)
	// Expect quotes + backslash escaped per Prometheus text format.
	if !strings.Contains(buf.String(), `k="has \"quotes\" and \\ slashes"`) {
		t.Fatalf("label not escaped:\n%s", buf.String())
	}
}
