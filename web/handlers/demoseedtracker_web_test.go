package handlers

import (
	"testing"
)

// What is left here after the torrent builder moved into the tracker plugin:
// the seeder's own arithmetic.
//
// Everything about what a torrent IS — the key order, the piece count, the file
// lengths, the hash matching the bytes, surviving the download path — is tested
// in loon-plugins/tracker/torrentbuild_test.go now, against the one builder
// both this seeder and the mirror button call. Keeping a copy of those checks
// here would have tested the same function twice and drifted the first time
// either was edited.

// Every seeded row must land inside the one-hour window Store.Totals counts
// seeding and leeching over.
//
// This is here because it already happened. The first stagger was seventeen
// minutes, which put nine of one member's twelve rows outside the window: the
// table listed eleven seeding, the summary card above it said four, and both
// were reading the same rows. Nothing failed — the page rendered, the numbers
// were internally consistent per query, and only a screenshot showed the two
// disagreeing.
//
// 200 torrents, far past what the picker returns, because the arithmetic was
// fine at the count it was written for.
func TestDemoAccountingStaysInsideTheActivityWindow(t *testing.T) {
	const window = 60
	for m := range demoTrackerMembers {
		for i := range 200 {
			if ago := demoLastSeenAgo(i, m); ago >= window {
				t.Fatalf("member %d torrent %d last seen %d minutes ago, outside the %d-minute window",
					m, i, ago, window)
			}
		}
	}
}
