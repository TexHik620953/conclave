// Package obs provides lightweight metrics (Prometheus text exposition) and
// tracing helpers.
package obs

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Labels are metric labels.
type Labels map[string]string

// Registry holds metric series.
type Registry struct {
	mu       sync.Mutex
	counters map[string]*series
	gauges   map[string]*series
	hists    map[string]*histogram
}

type series struct {
	name   string
	help   string
	labels Labels
	value  float64
}

type histogram struct {
	name    string
	help    string
	labels  Labels
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
}

// New creates an empty registry.
func New() *Registry {
	return &Registry{
		counters: map[string]*series{},
		gauges:   map[string]*series{},
		hists:    map[string]*histogram{},
	}
}

var defaultRegistry = New()

// Default returns the process-wide registry.
func Default() *Registry { return defaultRegistry }

func key(name string, labels Labels) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString(name)
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%s=%q", k, labels[k])
	}
	sb.WriteByte('}')
	return sb.String()
}

// Counter returns a monotonic counter series.
func (r *Registry) Counter(name, help string, labels Labels) *series {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(name, labels)
	s, ok := r.counters[k]
	if !ok {
		s = &series{name: name, help: help, labels: labels}
		r.counters[k] = s
	}
	return s
}

// Inc increments the counter by 1.
func (s *series) Inc() { s.Add(1) }

// Add adds delta to the counter.
func (s *series) Add(delta float64) {
	defaultRegistry.mu.Lock()
	s.value += delta
	defaultRegistry.mu.Unlock()
}

// Gauge returns a gauge series.
func (r *Registry) Gauge(name, help string, labels Labels) *series {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(name, labels)
	s, ok := r.gauges[k]
	if !ok {
		s = &series{name: name, help: help, labels: labels}
		r.gauges[k] = s
	}
	return s
}

// Set sets the gauge value.
func (s *series) Set(v float64) {
	defaultRegistry.mu.Lock()
	s.value = v
	defaultRegistry.mu.Unlock()
}

// Observe records a value into a histogram.
func (r *Registry) Observe(name, help string, labels Labels, v float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(name, labels)
	h, ok := r.hists[k]
	if !ok {
		buckets := []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
		h = &histogram{name: name, help: help, labels: labels, buckets: buckets, counts: make([]uint64, len(buckets))}
		r.hists[k] = h
	}
	h.sum += v
	h.count++
	for i, b := range h.buckets {
		if v <= b {
			h.counts[i]++
		}
	}
}

// Timer measures elapsed time and observes it on Stop.
type Timer struct {
	reg    *Registry
	name   string
	help   string
	labels Labels
	start  time.Time
}

// StartTimer begins timing an operation.
func (r *Registry) StartTimer(name, help string, labels Labels) *Timer {
	return &Timer{reg: r, name: name, help: help, labels: labels, start: time.Now()}
}

// Stop records the elapsed seconds.
func (t *Timer) Stop() {
	t.reg.Observe(t.name, t.help, t.labels, time.Since(t.start).Seconds())
}

// WritePrometheus writes all series in Prometheus text exposition format.
func (r *Registry) WritePrometheus(w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// HELP/TYPE must appear once per metric name, before all of its series.
	writeGrouped(w, r.counters, "counter")
	writeGrouped(w, r.gauges, "gauge")

	histNames := map[string]bool{}
	for _, k := range sortedKeysH(r.hists) {
		h := r.hists[k]
		if !histNames[h.name] {
			writeHeader(w, h.name, h.help, "histogram")
			histNames[h.name] = true
		}
		for i, b := range h.buckets {
			fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", h.name, trimFloat(b), h.counts[i])
		}
		fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n", h.name, h.count)
		fmt.Fprintf(w, "%s_sum %g\n", h.name, h.sum)
		fmt.Fprintf(w, "%s_count %d\n", h.name, h.count)
	}
}

func writeGrouped(w io.Writer, m map[string]*series, typ string) {
	seen := map[string]bool{}
	for _, k := range sortedKeys(m) {
		s := m[k]
		if !seen[s.name] {
			writeHeader(w, s.name, s.help, typ)
			seen[s.name] = true
		}
		fmt.Fprintf(w, "%s %g\n", k, s.value)
	}
}

func writeHeader(w io.Writer, name, help, typ string) {
	if help != "" {
		fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	}
	fmt.Fprintf(w, "# TYPE %s %s\n", name, typ)
}

func sortedKeys(m map[string]*series) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysH(m map[string]*histogram) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func trimFloat(f float64) string { return fmt.Sprintf("%g", f) }
