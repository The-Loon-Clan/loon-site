package site

import "testing"

// The match sweep walks DOWN the index and wraps at the bottom.
//
// The wrap is not a nicety. "Releases with no cover" alone looks like a
// sufficient filter and is not: a release that cannot be matched never gets a
// cover, so it qualifies again next run. Without a cursor the sweep jams
// against the first batch of unmatchable titles and never reaches the rest of
// the index — which is the shape of the bug this replaced, where only the
// newest 200 were ever considered and 0.63% of releases had cover art.
func TestCandidateCursorSweepsAndWraps(t *testing.T) {
	// A full page: there is more below, so continue from the lowest id seen.
	if got := nextCandidateCursor(1000, 1000, 54321); got != 54321 {
		t.Errorf("full page advanced to %d, want 54321", got)
	}
	// A short page: the bottom. Wrap.
	if got := nextCandidateCursor(999, 1000, 12); got != 0 {
		t.Errorf("short page went to %d, want 0 (wrap)", got)
	}
	// An empty page is the bottom too — and must not park the cursor at 0's
	// neighbour and re-read nothing forever.
	if got := nextCandidateCursor(0, 1000, 0); got != 0 {
		t.Errorf("empty page went to %d, want 0 (wrap)", got)
	}
}
