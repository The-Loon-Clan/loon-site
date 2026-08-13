package storage

import (
	"github.com/jmoiron/sqlx"
	"github.com/the-loon-clan/loon-site/internal/config"

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
		host := TrackerTotals{Uploaded: c.up, Downloaded: c.down}.Ratio()
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
		if got := (TrackerTotals{Uploaded: c.up, Downloaded: c.down}).RatioLabel(); got != c.want {
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
	// An UNUSABLE handle, deliberately: sqlx.NewDb(nil, ...) is a *sqlx.DB
	// whose inner *sql.DB is nil, so any query through it panics. That is the
	// proof. If the tracker gate were removed from these reads, they would
	// reach the database and blow up rather than quietly returning false, so
	// this test fails loudly instead of passing for the wrong reason.
	//
	// It used to pass nil for the pool and rely on a `db == nil` guard inside
	// each read. Those guards are gone — storage.New refuses a nil handle, so
	// the state they defended against cannot reach a method any more — and the
	// guard's absence is exactly what this needs to keep proving.
	st := &Store{db: sqlx.NewDb(nil, "postgres")}

	t.Setenv("LOON_TRACKER", "")
	if config.TrackerEnabled() {
		t.Fatal("tracker reads as enabled with the flag cleared")
	}
	if _, ok := st.ReadTrackerTotals(t.Context(), 1); ok {
		t.Error("ReadTrackerTotals reported data with the tracker off")
	}
	if _, ok := st.ReadTrackerSwarm(t.Context(), 1); ok {
		t.Error("ReadTrackerSwarm reported data with the tracker off")
	}
}

// New is where a missing database handle is now caught — once, at boot, rather
// than at 44 call sites that could only report it as an empty page.
func TestNewRefusesANilHandle(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("storage.New accepted a nil handle; every method assumes it is not")
		}
	}()
	New(nil)
}
