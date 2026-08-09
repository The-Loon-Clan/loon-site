package main

import (
	"testing"

	"github.com/the-loon-clan/loon-plugins/tracker"
)

// The host renders a ratio in three places and the plugin computes one for its
// own pages. Two definitions of the same number is how a member ends up seeing
// "∞" in the top bar and "0.00" on their profile, so this pins them together:
// if tracker.Totals.Ratio ever changes, this fails rather than the site quietly
// disagreeing with itself.
//
// The two odd cases are the ones worth pinning. Both-zero is 0 rather than NaN
// so an inactive member sorts to the BOTTOM of a ratio-ordered table, and
// upload-only is the upload figure rather than +Inf for the same reason.
func TestTrackerRatioMatchesPlugin(t *testing.T) {
	for _, c := range []struct{ up, down int64 }{
		{0, 0},              // never announced anything
		{5 << 30, 0},        // upload-only — must not be +Inf
		{0, 5 << 30},        // leech-only
		{10 << 30, 5 << 30}, // 2.00
		{1 << 30, 3 << 30},  // 0.33
		{7, 3},              // small numbers, no rounding luck
		{1 << 40, 1 << 40},  // exactly 1.00
	} {
		host := trackerTotals{Uploaded: c.up, Downloaded: c.down}.Ratio()
		plugin := tracker.Totals{Uploaded: c.up, Downloaded: c.down}.Ratio()
		if host != plugin {
			t.Errorf("up=%d down=%d: host ratio %v, plugin ratio %v", c.up, c.down, host, plugin)
		}
	}
}

// What a member actually reads. A number is not always the honest answer: a
// member who has uploaded and never downloaded has no ratio, and rendering
// their byte count as one ("12884901888.00") reads as a bug rather than as
// "you owe nothing".
func TestTrackerRatioLabel(t *testing.T) {
	for _, c := range []struct {
		up, down int64
		want     string
	}{
		{0, 0, "—"},
		{5 << 30, 0, "∞"},
		{10 << 30, 5 << 30, "2.00"},
		{0, 5 << 30, "0.00"},
		{1 << 30, 3 << 30, "0.33"},
	} {
		if got := (trackerTotals{Uploaded: c.up, Downloaded: c.down}).RatioLabel(); got != c.want {
			t.Errorf("up=%d down=%d: label %q, want %q", c.up, c.down, got, c.want)
		}
	}
}

// The whole surface is gated on the tracker being switched on. With it off the
// reads must not merely render nothing — they must not RUN, because the tables
// they name live in a schema a host without the plugin has never created, and a
// failing query on every page render is a slow way to serve a working site.
//
// nil DB stands in for that here: if the gate were removed, these would panic
// rather than return false, so the test fails loudly rather than passing for
// the wrong reason.
func TestTrackerReadsAreInertWhenDisabled(t *testing.T) {
	t.Setenv("LOON_DEMO_TRACKER", "")
	if trackerEnabled() {
		t.Fatal("tracker reads as enabled with the flag cleared")
	}
	if _, ok := readTrackerTotals(t.Context(), nil, 1); ok {
		t.Error("readTrackerTotals reported data with the tracker off")
	}
	if _, ok := readTrackerSwarm(t.Context(), nil, 1); ok {
		t.Error("readTrackerSwarm reported data with the tracker off")
	}
	// And with it ON but no pool — the state during boot, and after a database
	// blip. Still no data, still no panic.
	t.Setenv("LOON_DEMO_TRACKER", "1")
	if !trackerEnabled() {
		t.Fatal("tracker reads as disabled with the flag set")
	}
	if _, ok := readTrackerTotals(t.Context(), nil, 1); ok {
		t.Error("readTrackerTotals reported data with no pool")
	}
	if _, ok := readTrackerSwarm(t.Context(), nil, 1); ok {
		t.Error("readTrackerSwarm reported data with no pool")
	}
}
