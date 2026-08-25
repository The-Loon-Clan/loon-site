package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// fakeFiler records what it was asked to file and can be told to dedup.
type fakeFiler struct {
	calls  []pluginapi.FiledRequest
	dedupe bool // when true, every file reports Created=false
	nextID int64
}

func (f *fakeFiler) FileAutomated(_ context.Context, req pluginapi.FiledRequest) (pluginapi.RequestFileResult, error) {
	f.calls = append(f.calls, req)
	f.nextID++
	return pluginapi.RequestFileResult{ID: f.nextID, Created: !f.dedupe}, nil
}

// gapSchedule builds a schedule holding a fixed gap list, old enough to be
// requestable, without going near the network.
func gapSchedule(gaps ...pluginapi.TVGap) *tvSchedule {
	s := &tvSchedule{seriesIndex: func() pluginapi.SeriesIndex { return stubIndex{} }}
	s.gaps, s.gapsOK = gaps, true
	// The trigger reads through Gaps(from,to), which filters on AirsAt, so the
	// window has to contain them; the gaps themselves are dated old below.
	return s
}

// stubIndex satisfies the seriesIndex accessor; the trigger never calls it.
type stubIndex struct{}

func (stubIndex) Series(context.Context, string, int, int) ([]pluginapi.SeriesRow, int, error) {
	return nil, 0, nil
}
func (stubIndex) SeriesByKey(context.Context, string) (string, bool, error) { return "", false, nil }
func (stubIndex) Seasons(context.Context, string) ([]pluginapi.SeriesSeason, error) {
	return nil, nil
}
func (stubIndex) Releases(context.Context, string, int, int, int) ([]pluginapi.Release, error) {
	return nil, nil
}

func (stubIndex) SeasonPresence(context.Context, string, int) (map[int]bool, bool, error) {
	return nil, false, nil
}

func oldGap(show string, season, ep int, indexed bool) pluginapi.TVGap {
	return pluginapi.TVGap{
		Episode: pluginapi.TVEpisode{
			ShowTitle: show, Season: season, Number: ep,
			AirsAt: time.Now().Add(-48 * time.Hour), // past tvRequestAge
		},
		SeriesKey: strings.ToLower(show),
		Indexed:   indexed,
	}
}

// webWithTV builds a *web around a schedule, with a cross-id resolver that
// answers nothing -- the trigger works from title and episode alone, and the
// ids only sharpen the request when a real catalog has them.
func webWithTV(s *tvSchedule) *web {
	if s.crossIDs == nil {
		s.crossIDs = func(context.Context, string) (string, string, error) { return "", "", nil }
	}
	w := &web{}
	w.tv = s
	return w
}

// A requestable gap is filed; the filer receives the episode's identity.
func TestATrackedGapPastADayIsFiled(t *testing.T) {
	w := webWithTV(gapSchedule(oldGap("The Ark", 3, 4, true)))
	f := &fakeFiler{}
	out := w.runGapRequests(context.Background(), f)

	if out.Requestable != 1 || out.Filed != 1 {
		t.Fatalf("want 1 requestable and 1 filed, got %+v", out)
	}
	if len(f.calls) != 1 {
		t.Fatalf("filer called %d times, want 1", len(f.calls))
	}
	c := f.calls[0]
	if c.Category != "tv" || c.Season != "3" || c.Episodes != "4" {
		t.Fatalf("request misbuilt: %+v", c)
	}
	if !strings.Contains(c.Title, "The Ark") || !strings.Contains(c.Title, "S03E04") {
		t.Fatalf("title should carry show and code: %q", c.Title)
	}
	if c.Reason == "" {
		t.Fatal("an automated request must say why it was filed")
	}
}

// A show we have never held a release of is NOT auto-requested.
func TestAnUnindexedGapIsNotFiled(t *testing.T) {
	w := webWithTV(gapSchedule(oldGap("Never Held", 1, 1, false)))
	f := &fakeFiler{}
	out := w.runGapRequests(context.Background(), f)
	if out.Requestable != 0 || len(f.calls) != 0 {
		t.Fatalf("an unindexed gap must not be filed; got %+v, %d calls", out, len(f.calls))
	}
}

// A gap younger than the request age waits.
func TestAFreshGapIsNotYetRequested(t *testing.T) {
	fresh := oldGap("Just Aired", 1, 1, true)
	fresh.Episode.AirsAt = time.Now().Add(-2 * time.Hour) // inside tvRequestAge
	w := webWithTV(gapSchedule(fresh))
	f := &fakeFiler{}
	out := w.runGapRequests(context.Background(), f)
	if out.Requestable != 0 || len(f.calls) != 0 {
		t.Fatalf("a gap younger than the request age must wait; got %+v", out)
	}
}

// No filer wired: the pass still counts what it would file, and files nothing.
func TestNoFilerCountsButFilesNothing(t *testing.T) {
	w := webWithTV(gapSchedule(oldGap("The Ark", 3, 4, true), oldGap("Alone", 13, 10, true)))
	out := w.runGapRequests(context.Background(), nil)
	if out.FilerWired {
		t.Fatal("FilerWired must be false when nil is passed")
	}
	if out.Requestable != 2 {
		t.Fatalf("both gaps are requestable, got %d", out.Requestable)
	}
	if out.Filed != 0 || out.Deduped != 0 {
		t.Fatalf("nothing can be filed without a filer; got %+v", out)
	}
}

// Dedup: a board that already holds a request reports it, and the trigger
// counts it as deduped rather than filed.
func TestADedupedRequestIsCountedNotRefiled(t *testing.T) {
	w := webWithTV(gapSchedule(oldGap("The Ark", 3, 4, true)))
	f := &fakeFiler{dedupe: true}
	out := w.runGapRequests(context.Background(), f)
	if out.Filed != 0 || out.Deduped != 1 {
		t.Fatalf("a deduped file is not a new row; got %+v", out)
	}
}

// The per-pass cap counts a backlog without filing all of it at once.
func TestTheCapSpreadsABacklog(t *testing.T) {
	var gaps []pluginapi.TVGap
	for i := 0; i < tvRequestPerPass+5; i++ {
		gaps = append(gaps, oldGap("Show", 1, i+1, true))
	}
	w := webWithTV(gapSchedule(gaps...))
	f := &fakeFiler{}
	out := w.runGapRequests(context.Background(), f)
	if out.Requestable != tvRequestPerPass+5 {
		t.Fatalf("every gap is requestable; got %d", out.Requestable)
	}
	if out.Filed != tvRequestPerPass {
		t.Fatalf("the cap files exactly %d; got %d", tvRequestPerPass, out.Filed)
	}
	if len(f.calls) != tvRequestPerPass {
		t.Fatalf("the cap must stop the calls, not just the count; %d calls", len(f.calls))
	}
}

// The backlog drains: dedup hits do not consume the per-pass budget, so new
// gaps past the cap still get filed once the earlier ones are already open.
func TestBacklogDrainsPastDedupHits(t *testing.T) {
	// A filer that dedups the first N slugs (already open) and files the rest.
	already := map[string]bool{}
	var filed []string
	f := filerFunc(func(_ context.Context, req pluginapi.FiledRequest) (pluginapi.RequestFileResult, error) {
		if already[req.Title] {
			return pluginapi.RequestFileResult{Created: false}, nil // deduped
		}
		filed = append(filed, req.Title)
		return pluginapi.RequestFileResult{Created: true}, nil
	})
	// tvRequestPerPass+3 gaps; pretend the first tvRequestPerPass are already open.
	var gaps []pluginapi.TVGap
	for i := 0; i < tvRequestPerPass+3; i++ {
		g := oldGap("Show", 1, i+1, true)
		gaps = append(gaps, g)
	}
	w := webWithTV(gapSchedule(gaps...))
	for _, g := range gaps[:tvRequestPerPass] {
		already[g.Episode.ShowTitle+" "+tvEpisodeCode(g.Episode)] = true
	}
	out := w.runGapRequests(context.Background(), f)
	// The 3 new gaps must be filed despite the tvRequestPerPass dedups.
	if out.Filed != 3 {
		t.Fatalf("the 3 fresh gaps must file even behind %d dedups; Filed=%d", tvRequestPerPass, out.Filed)
	}
	if len(filed) != 3 {
		t.Fatalf("filer received %d new requests, want 3", len(filed))
	}
}

// filerFunc adapts a func to pluginapi.RequestFiler.
type filerFunc func(context.Context, pluginapi.FiledRequest) (pluginapi.RequestFileResult, error)

func (f filerFunc) FileAutomated(ctx context.Context, r pluginapi.FiledRequest) (pluginapi.RequestFileResult, error) {
	return f(ctx, r)
}
