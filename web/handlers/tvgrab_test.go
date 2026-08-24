package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// fakeSearcher answers a fixed candidate set, or an error.
type fakeSearcher struct {
	cands []pluginapi.TrackerCandidate
	err   error
	asked int
}

func (f *fakeSearcher) SearchEpisode(context.Context, pluginapi.EpisodeSearch) ([]pluginapi.TrackerCandidate, error) {
	f.asked++
	return f.cands, f.err
}
func (f *fakeSearcher) Sources() []pluginapi.TrackerSource { return nil }

func webWithSearch(s *tvSchedule, searcher pluginapi.TrackerSearcher) *web {
	if s.crossIDs == nil {
		s.crossIDs = func(context.Context, string) (string, string, error) { return "", "", nil }
	}
	w := &web{}
	w.tv = s
	w.trackers = searcher
	return w
}

// The grab takes the healthiest swarm, whatever order the client returned.
func TestBestCandidateTakesTheHealthiestSwarm(t *testing.T) {
	f := &fakeSearcher{cands: []pluginapi.TrackerCandidate{
		{Title: "weak", Seeders: 2},
		{Title: "strong", Seeders: 90},
		{Title: "middle", Seeders: 40},
	}}
	w := webWithSearch(gapSchedule(), f)
	best, ok := w.bestCandidate(context.Background(), oldGap("Show", 1, 1, true))
	if !ok || best.Title != "strong" {
		t.Fatalf("want the 90-seeder copy, got %q ok=%v", best.Title, ok)
	}
}

// A dead swarm is not a grab: better to report "no live copy" than queue a
// fetch that will stall.
func TestBestCandidateRejectsADeadSwarm(t *testing.T) {
	f := &fakeSearcher{cands: []pluginapi.TrackerCandidate{
		{Title: "seedless", Seeders: 0},
	}}
	w := webWithSearch(gapSchedule(), f)
	if _, ok := w.bestCandidate(context.Background(), oldGap("Show", 1, 1, true)); ok {
		t.Fatal("a zero-seeder candidate must not be chosen as a grab")
	}
}

// A search failure yields no grab, not a panic.
func TestBestCandidateSurvivesASearchError(t *testing.T) {
	f := &fakeSearcher{err: errors.New("all sources down")}
	w := webWithSearch(gapSchedule(), f)
	if _, ok := w.bestCandidate(context.Background(), oldGap("Show", 1, 1, true)); ok {
		t.Fatal("a failed search cannot produce a grab")
	}
}

// The per-pass cap bounds how many gaps are auto-searched, since each is real
// external traffic.
func TestAutoGrabIsBoundedPerPass(t *testing.T) {
	var gaps []pluginapi.TVGap
	for i := 0; i < tvGrabPerPass+4; i++ {
		gaps = append(gaps, oldGap("Show", 1, i+1, true))
	}
	f := &fakeSearcher{cands: []pluginapi.TrackerCandidate{{Title: "x", Seeders: 5}}}
	w := webWithSearch(gapSchedule(gaps...), f)
	grabs := w.runAutoGrab(context.Background())
	if len(grabs) != tvGrabPerPass {
		t.Fatalf("want exactly %d grabs, got %d", tvGrabPerPass, len(grabs))
	}
	if f.asked != tvGrabPerPass {
		t.Fatalf("the cap must bound the searches too; asked %d", f.asked)
	}
}

// A gap with no live copy is still reported -- "we looked, nothing seedable"
// is a different answer from "we did not look".
func TestAGapWithNoCopyIsReportedNotDropped(t *testing.T) {
	f := &fakeSearcher{cands: nil}
	w := webWithSearch(gapSchedule(oldGap("Show", 3, 4, true)), f)
	grabs := w.runAutoGrab(context.Background())
	if len(grabs) != 1 {
		t.Fatalf("the gap must still be reported, got %d", len(grabs))
	}
	if grabs[0].Found {
		t.Fatal("Found must be false when nothing seedable came back")
	}
}

// No searcher wired: no grabs, no panic.
func TestAutoGrabNoOpsWithoutASearcher(t *testing.T) {
	w := &web{}
	w.tv = gapSchedule(oldGap("Show", 1, 1, true))
	if grabs := w.runAutoGrab(context.Background()); grabs != nil {
		t.Fatalf("no searcher means no grabs, got %d", len(grabs))
	}
}
