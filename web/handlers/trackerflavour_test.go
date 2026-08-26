package handlers

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every read of the tracker's tables must be gated on the site actually HAVING
// a tracker.
//
// The trap is that the tables OUTLIVE the feature. Switching a site from
// "both" to indexer-only skips the tracker plugins and 404s their routes, but
// tracker.user_stats and tracker.torrents are still sitting there full of
// perfectly good rows — so an ungated read returns real data and the page
// renders a live-looking tracker on a site that has none.
//
// Three surfaces learned this one at a time: /stats ("Found by running the
// link audit against a flavour nobody had audited"), the swarm badges on
// listings, and the release page. The fourth, a member's ratio and seeding
// counts on their profile, was missed and shipped — every profile advertised a
// ratio for a tracker whose every page answered 404.
//
// A structural test rather than a rendering one because the failure is a
// MISSING line, and nothing renders differently until somebody flips the
// flavour. This is the check that would have caught the fourth.
func TestTrackerReadsAreGatedOnTheFlavour(t *testing.T) {
	// The reads that touch the tracker's own schema. Adding one here is the
	// point: a new read is exactly when this is easy to forget.
	reads := []string{
		"ReadTrackerTotals(",
		"ReadTrackerSiteStats(",
		"ReadTrackerSwarm(",
		"SwarmCounts(",
	}
	funcStart := regexp.MustCompile(`(?m)^func .*\{`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(src)

		// Split into functions so "the guard is in this file somewhere" cannot
		// pass for "the guard is on this read".
		starts := funcStart.FindAllStringIndex(text, -1)
		for i, at := range starts {
			end := len(text)
			if i+1 < len(starts) {
				end = starts[i+1][0]
			}
			body := text[at[0]:end]
			for _, read := range reads {
				// The declaration itself is not a call site.
				if !strings.Contains(body, read) || strings.Contains(body, "func (st *Store) "+read) {
					continue
				}
				if !strings.Contains(body, "flavourTracker()") {
					name := strings.TrimSpace(strings.SplitN(body, "{", 2)[0])
					t.Errorf("%s: %s calls %s without checking flavourTracker()\n"+
						"    the tracker's tables outlive the tracker being switched off, so this "+
						"read returns real rows on an indexer-only site and the page shows a "+
						"tracker that is not there", file, name, strings.TrimSuffix(read, "("))
				}
			}
		}
	}
}
