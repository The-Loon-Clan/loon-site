package metrics

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func render(t *testing.T, samples []Sample) string {
	t.Helper()
	var b bytes.Buffer
	if err := Write(&b, samples); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestOneHelpAndTypePerName is the rule that makes a scrape valid at all. A
// repeated HELP for one metric is a parse error, and a scraper that rejects the
// payload reports the target as DOWN — the same signal as the process being
// gone, from a formatting slip.
func TestOneHelpAndTypePerName(t *testing.T) {
	out := render(t, []Sample{
		{Name: "job_runs_total", Help: "runs", Kind: Counter, Labels: map[string]string{"job": "backup"}, Value: 3},
		{Name: "job_runs_total", Help: "runs", Kind: Counter, Labels: map[string]string{"job": "sitemap"}, Value: 7},
	})
	if n := strings.Count(out, "# HELP job_runs_total"); n != 1 {
		t.Errorf("%d HELP lines, want 1\n%s", n, out)
	}
	if n := strings.Count(out, "# TYPE job_runs_total"); n != 1 {
		t.Errorf("%d TYPE lines, want 1\n%s", n, out)
	}
	if n := strings.Count(out, "job_runs_total{"); n != 2 {
		t.Errorf("%d series, want 2\n%s", n, out)
	}
}

// TestLabelValuesAreEscaped. Label values here carry job names, plugin health
// summaries and route templates — all written by people. One unescaped quote
// makes the whole scrape unparseable.
func TestLabelValuesAreEscaped(t *testing.T) {
	out := render(t, []Sample{{
		Name: "plugin_health", Kind: Gauge, Value: 1,
		Labels: map[string]string{"why": `he said "no" \ then left` + "\nand stayed gone"},
	}})
	want := `plugin_health{why="he said \"no\" \\ then left\nand stayed gone"} 1`
	if !strings.Contains(out, want) {
		t.Errorf("got:\n%s\nwant a line containing:\n%s", out, want)
	}
	// The backslash must be escaped BEFORE the quote, or the backslash this
	// adds in front of a quote gets escaped a second time.
	if strings.Contains(out, `\\"`) {
		t.Errorf("double-escaped a quote:\n%s", out)
	}
}

func TestValuesRenderWithoutNoise(t *testing.T) {
	cases := map[float64]string{
		42:           "42",
		0:            "0",
		0.5:          "0.5",
		1755000000:   "1.755e+09",
		math.NaN():   "NaN",
		math.Inf(1):  "+Inf",
		math.Inf(-1): "-Inf",
	}
	for v, want := range cases {
		if got := formatValue(v); got != want {
			t.Errorf("formatValue(%v) = %q, want %q", v, got, want)
		}
	}
}

func TestNoLabelsRendersBare(t *testing.T) {
	out := render(t, []Sample{{Name: "members_total", Kind: Gauge, Value: 12}})
	if !strings.Contains(out, "\nmembers_total 12\n") {
		t.Errorf("want a bare series line, got:\n%s", out)
	}
}

func TestOutputIsStable(t *testing.T) {
	s := []Sample{
		{Name: "b_total", Kind: Counter, Labels: map[string]string{"z": "1", "a": "2"}, Value: 1},
		{Name: "a_total", Kind: Counter, Value: 2},
	}
	first := render(t, s)
	for i := 0; i < 20; i++ {
		if got := render(t, s); got != first {
			t.Fatalf("scrape %d differed — map iteration leaked into the output:\n%s\n%s", i, first, got)
		}
	}
	if strings.Index(first, "a_total") > strings.Index(first, "b_total") {
		t.Error("metric names are not sorted")
	}
	if !strings.Contains(first, `b_total{a="2",z="1"}`) {
		t.Errorf("labels are not sorted:\n%s", first)
	}
}

// ── histogram ───────────────────────────────────────────────────────

func hist(t *testing.T, h *Histogram) string {
	t.Helper()
	var b bytes.Buffer
	if err := h.Render(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestBucketsAreCumulative is the rule a hand-written exporter gets wrong. Each
// _bucket must hold every observation at or below its bound, so the counts only
// ever go up across the series.
func TestBucketsAreCumulative(t *testing.T) {
	h := NewHistogram("req_seconds", "how long", []float64{0.1, 0.5, 1})
	for _, v := range []float64{0.05, 0.05, 0.3, 0.8, 7} {
		h.Observe(nil, v)
	}
	out := hist(t, h)
	for _, want := range []string{
		`req_seconds_bucket{le="0.1"} 2`,
		`req_seconds_bucket{le="0.5"} 3`,
		`req_seconds_bucket{le="1"} 4`,
		`req_seconds_bucket{le="+Inf"} 5`,
		`req_seconds_sum 8.2`,
		`req_seconds_count 5`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestInfEqualsCount — a +Inf bucket that disagrees with _count makes a
// quantile query return nonsense rather than an error.
func TestInfEqualsCount(t *testing.T) {
	h := NewHistogram("x_seconds", "", []float64{0.1})
	for i := 0; i < 9; i++ {
		h.Observe(nil, float64(i))
	}
	out := hist(t, h)
	if !strings.Contains(out, `x_seconds_bucket{le="+Inf"} 9`) || !strings.Contains(out, "x_seconds_count 9") {
		t.Errorf("+Inf and _count disagree:\n%s", out)
	}
}

func TestHistogramSeparatesLabelSets(t *testing.T) {
	h := NewHistogram("req_seconds", "", []float64{1})
	h.Observe(map[string]string{"route": "/a"}, 0.5)
	h.Observe(map[string]string{"route": "/b"}, 0.5)
	h.Observe(map[string]string{"route": "/b"}, 0.5)
	out := hist(t, h)
	if !strings.Contains(out, `req_seconds_count{route="/a"} 1`) {
		t.Errorf("route /a wrong:\n%s", out)
	}
	if !strings.Contains(out, `req_seconds_count{route="/b"} 2`) {
		t.Errorf("route /b wrong:\n%s", out)
	}
	if n := strings.Count(out, "# TYPE req_seconds histogram"); n != 1 {
		t.Errorf("%d TYPE lines for two label sets, want 1", n)
	}
}

// TestUnsortedBucketsAreSorted — bounds given out of order would otherwise
// produce cumulative counts that go DOWN, which is invalid.
func TestUnsortedBucketsAreSorted(t *testing.T) {
	h := NewHistogram("x", "", []float64{1, 0.1, 0.5})
	h.Observe(nil, 0.05)
	out := hist(t, h)
	i1 := strings.Index(out, `le="0.1"`)
	i5 := strings.Index(out, `le="0.5"`)
	i10 := strings.Index(out, `le="1"`)
	if i1 > i5 || i5 > i10 {
		t.Errorf("buckets are out of order:\n%s", out)
	}
}

// TestEmptyHistogramWritesNothing. A metric with no observations should not
// emit a TYPE line with no series under it.
func TestEmptyHistogramWritesNothing(t *testing.T) {
	if out := hist(t, NewHistogram("x", "", DefaultBuckets)); out != "" {
		t.Errorf("want nothing, got:\n%s", out)
	}
}

func TestConcurrentObserveDoesNotRace(t *testing.T) {
	h := NewHistogram("x", "", DefaultBuckets)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 200; j++ {
				h.Observe(map[string]string{"route": "/x"}, 0.02)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if !strings.Contains(hist(t, h), `x_count{route="/x"} 1600`) {
		t.Errorf("lost observations:\n%s", hist(t, h))
	}
}
