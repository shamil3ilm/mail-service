// Package metrics exposes runtime counters + gauges in Prometheus text
// format on the admin port. Stdlib-only — no client_golang dependency —
// so the metrics endpoint doesn't pull in the reflection-heavy Prometheus
// SDK. The tradeoff is that histograms are approximated as gauges
// (last-observed) rather than proper buckets; adequate for a personal-
// scale mail service, upgrade later if you need Grafana histograms.
//
// Naming follows the Prometheus best-practices doc:
//   * counters end in _total
//   * gauges name what they measure (bytes, count)
//   * labels use snake_case
package metrics

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
)

// Registry holds the collectors. One is created at startup and shared
// across every subsystem via dependency injection — no package-globals.
type Registry struct {
	mu       sync.RWMutex
	counters map[string]*Counter
	gauges   map[string]*Gauge
}

func New() *Registry {
	return &Registry{
		counters: make(map[string]*Counter),
		gauges:   make(map[string]*Gauge),
	}
}

// Counter is a monotonically-increasing float64 value with optional labels.
type Counter struct {
	name   string
	help   string
	values sync.Map // labelKey → *counterValue
}

type counterValue struct {
	value uint64 // atomic
	label string // rendered label pairs
}

// Gauge is a value that can go up or down.
type Gauge struct {
	name   string
	help   string
	values sync.Map // labelKey → *gaugeValue
}

type gaugeValue struct {
	// Stored as bits of a float64 in an atomic uint64 so reads/writes are
	// lock-free without giving up float semantics.
	bits  uint64
	label string
}

// NewCounter registers (or fetches) a counter. Same name returns the same
// instance so multiple call sites can push into the same series safely.
func (r *Registry) NewCounter(name, help string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[name]; ok {
		return c
	}
	c := &Counter{name: name, help: help}
	r.counters[name] = c
	return c
}

// NewGauge registers a gauge by name.
func (r *Registry) NewGauge(name, help string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gauges[name]; ok {
		return g
	}
	g := &Gauge{name: name, help: help}
	r.gauges[name] = g
	return g
}

// Inc records +1 with the given labels (key/value pairs).
func (c *Counter) Inc(labels ...string) { c.Add(1, labels...) }

// Add records +delta.
func (c *Counter) Add(delta uint64, labels ...string) {
	key := labelKey(labels)
	v, _ := c.values.LoadOrStore(key, &counterValue{label: renderLabels(labels)})
	atomic.AddUint64(&v.(*counterValue).value, delta)
}

// Set stores an absolute value with the given labels (typically a snapshot
// like "current DB size in bytes"). Setting a gauge is the pull-model
// analogue of pushing a metric.
func (g *Gauge) Set(value float64, labels ...string) {
	key := labelKey(labels)
	v, _ := g.values.LoadOrStore(key, &gaugeValue{label: renderLabels(labels)})
	atomic.StoreUint64(&v.(*gaugeValue).bits, floatToBits(value))
}

// Handler returns an http.HandlerFunc suitable for `/metrics`. Emits
// stable-ordered text-format output (Prometheus scrapes both text and
// OpenMetrics; we choose text for zero deps).
func (r *Registry) Write(w io.Writer) {
	r.mu.RLock()
	// Snapshot names for deterministic output ordering.
	cnames := make([]string, 0, len(r.counters))
	for n := range r.counters {
		cnames = append(cnames, n)
	}
	gnames := make([]string, 0, len(r.gauges))
	for n := range r.gauges {
		gnames = append(gnames, n)
	}
	r.mu.RUnlock()

	sort.Strings(cnames)
	sort.Strings(gnames)

	for _, name := range cnames {
		c := r.counters[name]
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name)
		writeSeries(w, c.name, &c.values, func(v any) string {
			return fmt.Sprintf("%d", atomic.LoadUint64(&v.(*counterValue).value))
		}, func(v any) string { return v.(*counterValue).label })
	}
	for _, name := range gnames {
		g := r.gauges[name]
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", g.name, g.help, g.name)
		writeSeries(w, g.name, &g.values, func(v any) string {
			return fmt.Sprintf("%g", bitsToFloat(atomic.LoadUint64(&v.(*gaugeValue).bits)))
		}, func(v any) string { return v.(*gaugeValue).label })
	}
}

func writeSeries(w io.Writer, name string, values *sync.Map,
	valueFn func(any) string, labelFn func(any) string) {
	// Collect + sort for stable output — Prometheus doesn't require it but
	// it makes diffs across scrapes much easier to reason about.
	type row struct{ label, value string }
	rows := make([]row, 0, 4)
	values.Range(func(_, v any) bool {
		rows = append(rows, row{label: labelFn(v), value: valueFn(v)})
		return true
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].label < rows[j].label })
	for _, r := range rows {
		if r.label == "" {
			fmt.Fprintf(w, "%s %s\n", name, r.value)
		} else {
			fmt.Fprintf(w, "%s%s %s\n", name, r.label, r.value)
		}
	}
}

// labelKey is a stable string used as the sync.Map key. Not exposed.
func labelKey(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	return renderLabels(labels)
}

// renderLabels turns [k1, v1, k2, v2, ...] into the Prometheus format
// {k1="v1",k2="v2"}. Odd-length label slices drop the tail.
func renderLabels(labels []string) string {
	if len(labels) < 2 {
		return ""
	}
	// Pair them up, sort by key, join.
	type kv struct{ k, v string }
	pairs := make([]kv, 0, len(labels)/2)
	for i := 0; i+1 < len(labels); i += 2 {
		pairs = append(pairs, kv{k: labels[i], v: labels[i+1]})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })
	out := "{"
	for i, p := range pairs {
		if i > 0 {
			out += ","
		}
		// Simple escaping — quote, backslash, newline.
		v := p.v
		v = escapeLabel(v)
		out += p.k + `="` + v + `"`
	}
	out += "}"
	return out
}

func escapeLabel(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\', '"':
			b = append(b, '\\', c)
		case '\n':
			b = append(b, '\\', 'n')
		default:
			b = append(b, c)
		}
	}
	return string(b)
}

// floatToBits / bitsToFloat live in a separate file so the atomic gauge
// values can be lock-free on 32-bit platforms without adding a mutex.
func floatToBits(v float64) uint64 { return floatToBitsImpl(v) }
func bitsToFloat(b uint64) float64 { return bitsToFloatImpl(b) }
