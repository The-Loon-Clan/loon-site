package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// fakeSeries is a SeriesIndex over a literal set of releases.
//
// It answers Releases the way the real store does -- including the part that
// matters here, that a pack is episode 0 with Pack set -- because a stub that
// quietly returned packs under their real episode numbers would make the pack
// test pass against code that has the bug.
type fakeSeries struct {
	rels map[string][]pluginapi.Release // series key -> releases
	err  error
	// calls counts Releases lookups, so the per-season caching can be asserted
	// rather than assumed.
	calls int
}

func (f *fakeSeries) Series(context.Context, string, int, int) ([]pluginapi.SeriesRow, int, error) {
	return nil, 0, nil
}

func (f *fakeSeries) SeriesByKey(_ context.Context, key string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	_, ok := f.rels[key]
	return key, ok, nil
}

func (f *fakeSeries) Seasons(context.Context, string) ([]pluginapi.SeriesSeason, error) {
	return nil, nil
}

func (f *fakeSeries) SeasonPresence(_ context.Context, key string, season int) (map[int]bool, bool, error) {
	f.calls++
	if f.err != nil {
		return nil, false, f.err
	}
	eps := map[int]bool{}
	pack := false
	for _, r := range f.rels[key] {
		if r.Season != season {
			continue
		}
		if r.Pack {
			pack = true
			continue
		}
		eps[r.Episode] = true
	}
	return eps, pack, nil
}

func (f *fakeSeries) Releases(_ context.Context, key string, season, episode, _ int) ([]pluginapi.Release, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	var out []pluginapi.Release
	for _, r := range f.rels[key] {
		if season >= 0 && r.Season != season {
			continue
		}
		if episode >= 0 && r.Episode != episode {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// airedTV builds a schedule whose episodes are all old enough to judge.
func airedTV(idx pluginapi.SeriesIndex, eps ...pluginapi.TVEpisode) *tvSchedule {
	s := &tvSchedule{seriesIndex: func() pluginapi.SeriesIndex { return idx }}
	s.eps = eps
	return s
}

func ep(show string, season, num int, ago time.Duration) pluginapi.TVEpisode {
	return pluginapi.TVEpisode{
		ShowTitle: show, Season: season, Number: num,
		AirsAt: time.Now().Add(-ago),
	}
}

func gapKeys(gaps []pluginapi.TVGap) []string {
	out := make([]string, 0, len(gaps))
	for _, g := range gaps {
		out = append(out, g.Episode.ShowTitle)
	}
	return out
}

// A season pack covers the episodes inside it.
//
// THE TEST THIS FILE EXISTS FOR. The index stores a pack as episode 0, so a
// join that asks "is there a release with episode = 7" finds nothing and
// reports a gap for an episode sitting in a pack we already hold. On live data
// that is 1,100 packs' worth of false positives.
func TestASeasonPackIsNotAGap(t *testing.T) {
	idx := &fakeSeries{rels: map[string][]pluginapi.Release{
		"thewire": {{Season: 3, Episode: 0, Pack: true}},
	}}
	s := airedTV(idx, ep("The Wire", 3, 7, 48*time.Hour))
	s.recomputeGaps(context.Background())

	if got, err := s.Gaps(context.Background(), time.Now().Add(-72*time.Hour), time.Now()); err != nil {
		t.Fatalf("Gaps: %v", err)
	} else if len(got) != 0 {
		t.Fatalf("S03E07 is inside a season pack, but it was reported as a gap: %v", gapKeys(got))
	}

	// The bug this guards is invisible unless the pack is the ONLY thing
	// covering the episode, so prove the fixture would otherwise report it.
	idx.rels["thewire"] = nil
	s.recomputeGaps(context.Background())
	if got, _ := s.Gaps(context.Background(), time.Now().Add(-72*time.Hour), time.Now()); len(got) != 1 {
		t.Fatalf("without the pack the episode must be a gap, else the test above proves nothing; got %d", len(got))
	}
}

// An episode we actually hold is not a gap; one we do not, is.
func TestAMissingEpisodeIsAGapAndAHeldOneIsNot(t *testing.T) {
	idx := &fakeSeries{rels: map[string][]pluginapi.Release{
		"succession": {{Season: 2, Episode: 3}},
	}}
	s := airedTV(idx,
		ep("Succession", 2, 3, 24*time.Hour), // held
		ep("Succession", 2, 4, 24*time.Hour), // missing
	)
	s.recomputeGaps(context.Background())

	got, err := s.Gaps(context.Background(), time.Now().Add(-48*time.Hour), time.Now())
	if err != nil {
		t.Fatalf("Gaps: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly the missing episode, got %d: %v", len(got), gapKeys(got))
	}
	if got[0].Episode.Number != 4 {
		t.Fatalf("wrong episode reported: S%02dE%02d", got[0].Episode.Season, got[0].Episode.Number)
	}
	if got[0].SeriesKey != "succession" {
		t.Fatalf("SeriesKey = %q, want the fold the index is keyed by", got[0].SeriesKey)
	}
	if !got[0].Indexed {
		t.Fatal("the show has other releases, so Indexed must be true")
	}
	// One lookup for the season, not one per episode.
	if idx.calls != 1 {
		t.Fatalf("two episodes of one season took %d index lookups, want 1", idx.calls)
	}
}

// A show we have never indexed is a different problem from a missed episode.
func TestAShowWithNoReleasesAtAllIsMarkedUnindexed(t *testing.T) {
	idx := &fakeSeries{rels: map[string][]pluginapi.Release{}}
	s := airedTV(idx, ep("Some Show Nobody Posted", 1, 1, 24*time.Hour))
	s.recomputeGaps(context.Background())

	got, _ := s.Gaps(context.Background(), time.Now().Add(-48*time.Hour), time.Now())
	if len(got) != 1 {
		t.Fatalf("want one gap, got %d", len(got))
	}
	if got[0].Indexed {
		t.Fatal("Indexed must be false for a show with no releases -- the whole point of the flag " +
			"is separating 'we missed an episode' from 'we have never held this show'")
	}
}

// Too recent to judge.
func TestAnEpisodeInsideTheGracePeriodIsNotYetAGap(t *testing.T) {
	idx := &fakeSeries{rels: map[string][]pluginapi.Release{}}
	s := airedTV(idx,
		ep("Just Aired", 1, 1, tvGapGrace/2),       // inside the grace
		ep("Long Ago", 1, 1, tvGapGrace+time.Hour), // past it
		ep("Not Yet Aired", 1, 1, -6*time.Hour),    // airs in the future
	)
	s.recomputeGaps(context.Background())

	got, _ := s.Gaps(context.Background(), time.Now().Add(-72*time.Hour), time.Now().Add(72*time.Hour))
	if len(got) != 1 || got[0].Episode.ShowTitle != "Long Ago" {
		t.Fatalf("only the episode past the grace period is a gap; got %v", gapKeys(got))
	}
}

// A database that fell over is not evidence of a missing episode.
func TestAFailedLookupDoesNotInventGaps(t *testing.T) {
	idx := &fakeSeries{err: errors.New("connection refused")}
	s := airedTV(idx, ep("Anything", 1, 1, 24*time.Hour))
	s.recomputeGaps(context.Background())

	if got, _ := s.Gaps(context.Background(), time.Now().Add(-48*time.Hour), time.Now()); len(got) != 0 {
		t.Fatalf("an index error must not produce gaps -- these would be filed as requests for "+
			"episodes we already hold; got %v", gapKeys(got))
	}
}

// No index wired is "no opinion", not "nothing missing".
func TestNoSeriesIndexMeansNoAnswerRatherThanAnEmptyOne(t *testing.T) {
	s := &tvSchedule{seriesIndex: func() pluginapi.SeriesIndex { return nil }}
	s.eps = []pluginapi.TVEpisode{ep("Whatever", 1, 1, 24*time.Hour)}
	s.recomputeGaps(context.Background())

	s.mu.RLock()
	ok := s.gapsOK
	s.mu.RUnlock()
	if ok {
		t.Fatal("gapsOK must stay false without an index, or a host with no indexer " +
			"renders a confident 'nothing missing'")
	}
}

// Specials and unnumbered entries say nothing either way.
func TestUnnumberedAiringsAreNotJudged(t *testing.T) {
	idx := &fakeSeries{rels: map[string][]pluginapi.Release{}}
	s := airedTV(idx,
		ep("A Special", 0, 1, 24*time.Hour),
		ep("No Episode Number", 1, 0, 24*time.Hour),
	)
	s.recomputeGaps(context.Background())

	if got, _ := s.Gaps(context.Background(), time.Now().Add(-48*time.Hour), time.Now()); len(got) != 0 {
		t.Fatalf("season 0 and episode 0 are not judgeable airings; got %v", gapKeys(got))
	}
}

// The window filter is the caller's, and it is applied to the airing time.
func TestGapsHonoursTheRequestedWindow(t *testing.T) {
	idx := &fakeSeries{rels: map[string][]pluginapi.Release{}}
	s := airedTV(idx,
		ep("Old", 1, 1, 10*24*time.Hour),
		ep("Recent", 1, 1, 24*time.Hour),
	)
	s.recomputeGaps(context.Background())

	got, _ := s.Gaps(context.Background(), time.Now().Add(-48*time.Hour), time.Now())
	if len(got) != 1 || got[0].Episode.ShowTitle != "Recent" {
		t.Fatalf("want only the episode inside the window; got %v", gapKeys(got))
	}
}

// Oldest first: the list is read from the top, and the oldest gap is the worst.
func TestGapsAreOldestFirst(t *testing.T) {
	idx := &fakeSeries{rels: map[string][]pluginapi.Release{}}
	s := airedTV(idx,
		ep("Newer", 1, 1, 24*time.Hour),
		ep("Older", 1, 1, 96*time.Hour),
	)
	s.recomputeGaps(context.Background())

	got, _ := s.Gaps(context.Background(), time.Now().Add(-7*24*time.Hour), time.Now())
	if len(got) != 2 || got[0].Episode.ShowTitle != "Older" {
		t.Fatalf("want oldest first; got %v", gapKeys(got))
	}
}
