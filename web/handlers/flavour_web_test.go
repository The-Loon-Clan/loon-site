package handlers

import "testing"

// The flavour is one value deciding two halves; this pins the mapping so a
// future fourth flavour cannot quietly leave one half undefined.
func TestFlavourHalves(t *testing.T) {
	defer flavourMode.Store(siteFlavour()) // leave the package state as found

	cases := []struct {
		flavour          string
		tracker, indexer bool
	}{
		{FlavourIndexer, false, true},
		{FlavourTorrent, true, false},
		{FlavourBoth, true, true},
	}
	for _, tc := range cases {
		flavourMode.Store(tc.flavour)
		if flavourTracker() != tc.tracker || flavourIndexer() != tc.indexer {
			t.Errorf("%s: tracker=%v indexer=%v, want %v/%v",
				tc.flavour, flavourTracker(), flavourIndexer(), tc.tracker, tc.indexer)
		}
	}
}

// An empty mirror — boot not run, or a cleared value — reads as indexer, the
// stance the demo has always shipped with, and NEVER as a tracker: announce
// endpoints must not appear because a load failed.
func TestFlavourDefaultsToIndexer(t *testing.T) {
	defer flavourMode.Store(siteFlavour())
	flavourMode.Store("")
	if siteFlavour() != FlavourIndexer || flavourTracker() {
		t.Fatalf("unset flavour reads as %q (tracker=%v), want indexer/false",
			siteFlavour(), flavourTracker())
	}
}

func TestValidFlavourIsAClosedSet(t *testing.T) {
	for _, ok := range []string{FlavourIndexer, FlavourTorrent, FlavourBoth} {
		if !validFlavour(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "Both", "tracker", "usenet", "all"} {
		if validFlavour(bad) {
			t.Errorf("%q accepted — an unknown mode is a form bug, not a state", bad)
		}
	}
}
