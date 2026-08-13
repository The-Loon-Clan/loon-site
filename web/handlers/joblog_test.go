package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// capture builds a dedup sink writing JSON lines into buf, with a clock the
// test drives. A fake clock rather than sleeps: the heartbeat is measured in
// minutes and a test must not take minutes.
func capture(t *testing.T) (*jobLogDedup, *bytes.Buffer, func(time.Duration)) {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	d := newJobLogDedup(log)
	d.now = func() time.Time { return now }
	return d, &buf, func(adv time.Duration) { now = now.Add(adv) }
}

// lines returns one map per emitted record.
func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("bad log line %q: %v", raw, err)
		}
		out = append(out, m)
	}
	return out
}

// The reported case: one line repeated once a second while a builder pass runs.
//
// stamped(), NOT a bare string. loon's scheduler prepends "[15:04:05] " to
// every job line, so consecutive repeats are never byte-equal — and the first
// version of both this test and the code under it used bare strings, passed,
// and collapsed exactly nothing in production. The fixture has to be the shape
// the real sink receives.
func stamped(sec int, msg string) string {
	return time.Date(2026, 8, 6, 12, 0, sec, 0, time.UTC).Format("[15:04:05] ") + msg
}

func TestJobLogCollapsesTheBuilderSpam(t *testing.T) {
	d, buf, advance := capture(t)
	const spam = "build already running — skipping overlap"
	for i := 0; i < 156; i++ {
		d.Log("Usenet Builder", stamped(i, spam))
		advance(time.Second)
	}
	got := lines(t, buf)
	// 156 identical lines over 156s: the first, plus a heartbeat every 2min.
	if len(got) > 4 {
		t.Errorf("156 repeats produced %d log lines, want a handful: %v", len(got), got)
	}
	if len(got) == 0 {
		t.Fatal("nothing was logged at all — the line must not vanish")
	}
	// The full line, timestamp and all, is what gets logged — only the
	// COMPARISON ignores the clock.
	if first, _ := got[0]["line"].(string); !strings.HasSuffix(first, spam) || !strings.HasPrefix(first, "[") {
		t.Errorf("first line was %q, want the stamped message verbatim", first)
	}
	// Nothing is DROPPED: the repeats are accounted for.
	var reported float64
	for _, m := range got {
		if r, ok := m["repeated"].(float64); ok {
			reported += r
		}
	}
	if reported == 0 {
		t.Error("repeats were suppressed without ever being counted")
	}
}

// A changing line must never be collapsed — that would hide the actual work.
func TestJobLogNeverCollapsesDistinctLines(t *testing.T) {
	d, buf, advance := capture(t)
	for i, l := range []string{"crawl started", "crawl complete", "build complete"} {
		d.Log("Usenet Crawler", stamped(i, l))
		advance(time.Second)
	}
	got := lines(t, buf)
	if len(got) != 3 {
		t.Fatalf("3 distinct lines produced %d records: %v", len(got), got)
	}
	for i, want := range []string{"crawl started", "crawl complete", "build complete"} {
		if s, _ := got[i]["line"].(string); !strings.HasSuffix(s, want) {
			t.Errorf("line %d = %v, want it to end with %q", i, got[i]["line"], want)
		}
	}
}

// The same text from a DIFFERENT job is a different fact and must be printed.
func TestJobLogKeepsJobsApart(t *testing.T) {
	d, buf, _ := capture(t)
	d.Log("Usenet Builder", stamped(0, "already running"))
	d.Log("Usenet Backfill", stamped(0, "already running"))
	if got := lines(t, buf); len(got) != 2 {
		t.Errorf("same text from two jobs collapsed into %d line(s): %v", len(got), got)
	}
}

// When the line changes, the suppressed count is reported rather than lost.
func TestJobLogReportsTheCountBeforeMovingOn(t *testing.T) {
	d, buf, advance := capture(t)
	d.Log("J", stamped(0, "same"))
	for i := 1; i <= 5; i++ {
		d.Log("J", stamped(i, "same"))
		advance(time.Second)
	}
	d.Log("J", stamped(6, "different"))
	got := lines(t, buf)
	if len(got) != 3 {
		t.Fatalf("want first + summary + new line, got %d: %v", len(got), got)
	}
	if got[1]["repeated"] != float64(5) {
		t.Errorf("summary reported %v repeats, want 5", got[1]["repeated"])
	}
	if s, _ := got[2]["line"].(string); !strings.HasSuffix(s, "different") {
		t.Errorf("the new line was not printed: %v", got[2])
	}
}

// A job stuck on one line must still say so periodically: silence reads as
// "nothing is happening" when the truth is the opposite.
func TestJobLogHeartbeatsWhileStuck(t *testing.T) {
	d, buf, advance := capture(t)
	d.Log("J", stamped(0, "stuck"))
	for i := 1; i <= 20; i++ {
		advance(30 * time.Second)
		d.Log("J", stamped(i*30, "stuck"))
	}
	got := lines(t, buf)
	if len(got) < 3 {
		t.Errorf("10 minutes of repeats produced %d line(s) — no heartbeat: %v", len(got), got)
	}
	for _, m := range got[1:] {
		if _, ok := m["repeated"]; !ok {
			t.Errorf("heartbeat line carries no repeat count: %v", m)
		}
	}
}

// TestJobLogSurvivesInterleavedJobs is the case the LIVE log exposed and the
// original tests missed completely: they only ever fed one job at a time.
//
// Jobs run concurrently. The backfill logs a line roughly every second while
// the builder is repeating "already running", so with a single global
// last-line the builder's run was broken by every backfill line and restarted —
// ~300 lines became 61 rather than a handful. Per-job state is what fixes it,
// and this is the test that would have caught the difference.
func TestJobLogSurvivesInterleavedJobs(t *testing.T) {
	d, buf, advance := capture(t)
	const spam = "build already running — skipping overlap"
	for i := 0; i < 60; i++ {
		d.Log("Usenet Builder", stamped(i, spam))
		// A different job talking in between must not reset the builder's run.
		// The count VARIES, as the real line does — an identical message would
		// legitimately collapse and would not test interleaving at all.
		d.Log("Usenet Backfill", stamped(i, fmt.Sprintf("staged %d article(s)", 5000+i)))
		advance(time.Second)
	}
	got := lines(t, buf)

	var builder, backfill int
	for _, m := range got {
		switch m["name"] {
		case "Usenet Builder":
			builder++
		case "Usenet Backfill":
			backfill++
		}
	}
	// The builder repeats one line 60 times: first + at most a heartbeat.
	if builder > 3 {
		t.Errorf("interleaving broke the collapse — %d builder lines for 60 repeats", builder)
	}
	// The backfill's lines all differ (the count changes), so none collapse.
	if backfill != 60 {
		t.Errorf("backfill emitted %d lines, want all 60 — distinct lines must never collapse", backfill)
	}
}
