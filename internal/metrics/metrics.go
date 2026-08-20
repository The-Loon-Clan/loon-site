// Package metrics writes the Prometheus text exposition format, and holds the
// one distribution this site measures itself.
//
// WHY NOT client_golang. Almost everything here is a SNAPSHOT of state the
// process already holds — job status out of the scheduler's registry, plugin
// health out of a contract, row counts out of the database — and a pull-based
// exporter over existing state is a formatter, not a metrics system. The one
// thing that genuinely needs accumulating is HTTP request duration, and a
// fixed-bucket histogram is forty lines.
//
// That is a trade rather than a principle, so here is the other side of it: a
// hand-written exposition is a place to get label escaping and histogram
// cumulativeness subtly wrong, which is why both are pinned by tests below
// rather than reasoned about. And the swap is contained if it is ever worth
// making — plugins contribute through pluginapi.MetricSource, the endpoint
// formats, and nothing in between knows which library did it.
//
// THE FORMAT, since it is short enough to state:
//
//	# HELP name one line about it
//	# TYPE name counter
//	name{label="value"} 42
//
// A histogram is three families under one name: _bucket with a le label and
// CUMULATIVE counts, plus _sum and _count.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Kind is what a value means on the wire.
type Kind string

const (
	Counter Kind = "counter"
	Gauge   Kind = "gauge"
)

// Sample is one measurement ready to write.
type Sample struct {
	Name   string
	Help   string
	Kind   Kind
	Labels map[string]string
	Value  float64
}

// Write renders samples in exposition format.
//
// Samples are GROUPED BY NAME with one HELP and TYPE per group, because the
// format requires it: a repeated HELP line for the same metric makes a scrape
// invalid, and a scraper that rejects the payload reports the target as down —
// which is the same signal as the process being gone. Grouping is therefore not
// tidiness, it is the difference between working and a false alarm.
func Write(w io.Writer, samples []Sample) error {
	byName := map[string][]Sample{}
	var order []string
	for _, s := range samples {
		if s.Name == "" {
			continue
		}
		if _, seen := byName[s.Name]; !seen {
			order = append(order, s.Name)
		}
		byName[s.Name] = append(byName[s.Name], s)
	}
	sort.Strings(order)

	for _, name := range order {
		group := byName[name]
		if h := group[0].Help; h != "" {
			if _, err := fmt.Fprintf(w, "# HELP %s %s\n", name, escapeHelp(h)); err != nil {
				return err
			}
		}
		kind := group[0].Kind
		if kind == "" {
			kind = Gauge
		}
		if _, err := fmt.Fprintf(w, "# TYPE %s %s\n", name, kind); err != nil {
			return err
		}
		// Sorted so a scrape is byte-stable between calls — which makes a diff
		// of two scrapes readable, and makes these testable at all.
		sort.Slice(group, func(i, j int) bool {
			return labelString(group[i].Labels) < labelString(group[j].Labels)
		})
		for _, s := range group {
			if _, err := fmt.Fprintf(w, "%s%s %s\n", name, labelString(s.Labels), formatValue(s.Value)); err != nil {
				return err
			}
		}
	}
	return nil
}

// labelString renders {a="1",b="2"}, or "" for no labels.
func labelString(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escapeLabel(labels[k]))
		b.WriteString(`"`)
	}
	b.WriteByte('}')
	return b.String()
}

// escapeLabel escapes a label VALUE: backslash, quote and newline, in that
// order — the backslash first, or the escapes this adds get escaped again.
//
// It matters more than it looks. Label values here carry job names, plugin
// health summaries and route templates, all of which are written by people;
// one unescaped quote makes the whole scrape unparseable, and the target then
// reports as down rather than as "one bad label".
func escapeLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}

// escapeHelp escapes a HELP line: backslash and newline. A quote is legal here
// and is left alone.
func escapeHelp(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	return strings.ReplaceAll(v, "\n", `\n`)
}

// formatValue renders a float the way the format wants it.
//
// 'g' with -1 precision, so a whole number is "42" rather than "42.000000" and
// a fraction keeps exactly the digits it needs. The special values have named
// spellings that a scraper requires and Go does not produce.
func formatValue(f float64) string {
	switch {
	case f != f:
		return "NaN"
	case f > 1e308*1.7:
		return "+Inf"
	case f < -1e308*1.7:
		return "-Inf"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// ── the one distribution this site keeps ────────────────────────────

// DefaultBuckets are the upper bounds, in seconds, for request duration.
//
// Chosen around what this site actually does rather than from a template: a
// cached page is single-digit milliseconds, a search over the index is tens,
// and anything past a second is a page somebody has noticed. The top bucket
// exists to separate "slow" from "gave up".
var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Histogram counts observations into fixed buckets, by label set.
//
// Fixed buckets, and no way to add one at runtime: bucket boundaries are part
// of a metric's identity, and a series whose boundaries changed halfway through
// is one no query can average honestly.
type Histogram struct {
	name    string
	help    string
	buckets []float64

	mu     sync.Mutex
	series map[string]*histSeries
}

type histSeries struct {
	labels map[string]string
	counts []uint64 // one per bucket, NON-cumulative; Write accumulates
	sum    float64
	count  uint64
}

// NewHistogram builds one. Bucket bounds are sorted defensively: an unsorted
// list produces cumulative counts that go down, which is invalid and which no
// scraper reports usefully.
func NewHistogram(name, help string, buckets []float64) *Histogram {
	b := append([]float64(nil), buckets...)
	sort.Float64s(b)
	return &Histogram{name: name, help: help, buckets: b, series: map[string]*histSeries{}}
}

// Observe records one value against a label set.
func (h *Histogram) Observe(labels map[string]string, v float64) {
	key := labelString(labels)
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.series[key]
	if s == nil {
		cp := make(map[string]string, len(labels))
		for k, val := range labels {
			cp[k] = val
		}
		s = &histSeries{labels: cp, counts: make([]uint64, len(h.buckets))}
		h.series[key] = s
	}
	s.sum += v
	s.count++
	for i, ub := range h.buckets {
		if v <= ub {
			s.counts[i]++
			break // NON-cumulative here; Write does the accumulating
		}
	}
}

// Render writes the histogram.
//
// NOT called WriteTo, which govet caught: that name has a meaning — io.WriterTo
// returns (int64, error) — and a method with the name but not the signature is
// one somebody will eventually try to pass where the interface is wanted.
//
// The buckets are CUMULATIVE on the wire — each _bucket holds every observation
// at or below its bound — which is the rule a hand-written exporter gets wrong,
// and the reason Observe stores raw counts and this does the summing in one
// place. +Inf is required and must equal _count, or a quantile query over the
// series silently returns nonsense rather than an error.
func (h *Histogram) Render(w io.Writer) error {
	h.mu.Lock()
	keys := make([]string, 0, len(h.series))
	for k := range h.series {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	snapshot := make([]*histSeries, 0, len(keys))
	for _, k := range keys {
		s := h.series[k]
		cp := &histSeries{labels: s.labels, counts: append([]uint64(nil), s.counts...), sum: s.sum, count: s.count}
		snapshot = append(snapshot, cp)
	}
	h.mu.Unlock()

	if len(snapshot) == 0 {
		return nil
	}
	if h.help != "" {
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n", h.name, escapeHelp(h.help)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "# TYPE %s histogram\n", h.name); err != nil {
		return err
	}
	for _, s := range snapshot {
		var running uint64
		for i, ub := range h.buckets {
			running += s.counts[i]
			l := withLabel(s.labels, "le", formatValue(ub))
			if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelString(l), running); err != nil {
				return err
			}
		}
		inf := withLabel(s.labels, "le", "+Inf")
		if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, labelString(inf), s.count); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s_sum%s %s\n", h.name, labelString(s.labels), formatValue(s.sum)); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s_count%s %d\n", h.name, labelString(s.labels), s.count); err != nil {
			return err
		}
	}
	return nil
}

func withLabel(base map[string]string, k, v string) map[string]string {
	out := make(map[string]string, len(base)+1)
	for key, val := range base {
		out[key] = val
	}
	out[k] = v
	return out
}
