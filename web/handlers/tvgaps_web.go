package handlers

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// What aired, and never arrived.
//
// Step two of the content pipeline. The calendar knows an episode of a show we
// carry aired on Tuesday; this asks the index whether anything matching it is
// actually here. What comes out is a list of episodes to go and find -- the
// input the auto-request needs, and, until that exists, a straight answer to
// the question an indexer operator asks constantly: what did we miss?
//
// COMPUTED IN THE SCHEDULE JOB, not on render. It is a query per show-season
// against the index, and a month view would run dozens of them per viewer for
// an answer that changes when a release lands, not when somebody looks. The
// six-hourly pass that fetches the schedule recomputes this immediately after,
// against the window it just fetched.

const (
	// tvGapGrace is how long after broadcast an episode has to be missing
	// before it counts as missing.
	//
	// WITHOUT IT THE LIST IS ALL NOISE. An episode is absent from every index
	// on earth the minute it airs; the first release typically shows up inside
	// a couple of hours. With no grace the newest entries -- the ones that draw
	// the eye -- would always be the ones nothing is wrong with, and the real
	// gaps would sit underneath them. Three hours is past the usual arrival and
	// still early enough that acting on it is worth something.
	tvGapGrace = 3 * time.Hour

	// tvGapSeasonLimit caps the per-season release query. A season with more
	// releases than this is comprehensively covered by any measure, and the
	// only thing read off the rows is which episode numbers are present.
	tvGapSeasonLimit = 500
)

var _ pluginapi.TVGapFinder = (*tvSchedule)(nil)

// Gaps returns the episodes that aired in [from, to) with nothing to match.
func (s *tvSchedule) Gaps(_ context.Context, from, to time.Time) ([]pluginapi.TVGap, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.gaps) == 0 {
		return nil, nil
	}
	out := make([]pluginapi.TVGap, 0, len(s.gaps))
	for _, g := range s.gaps {
		if g.Episode.AirsAt.Before(from) || !g.Episode.AirsAt.Before(to) {
			continue
		}
		out = append(out, g)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// seasonHave is what one (show, season) lookup yields: which episodes are
// present, and whether a pack covers the lot.
type seasonHave struct {
	eps  map[int]bool
	pack bool
	// any is whether the show has any release at all, which is answered by the
	// same rows and is a different question from whether this episode is here.
	any bool
}

// has reports whether this season holds the given episode.
func (h seasonHave) has(ep int) bool { return h.pack || h.eps[ep] }

// recomputeGaps rebuilds the gap list from the window currently loaded.
//
// Called by the schedule job with the lock NOT held; takes it only to read the
// window and again to publish the result.
func (s *tvSchedule) recomputeGaps(ctx context.Context) {
	idx := s.seriesIndex()
	if idx == nil {
		// No index wired means no opinion, which is not the same as no gaps.
		// Publishing an empty list here would let a host with no indexer show a
		// confident "nothing missing".
		s.mu.Lock()
		s.gaps, s.gapsOK = nil, false
		s.mu.Unlock()
		return
	}

	s.mu.RLock()
	eps := make([]pluginapi.TVEpisode, len(s.eps))
	copy(eps, s.eps)
	s.mu.RUnlock()

	cutoff := time.Now().Add(-tvGapGrace)
	// One lookup per show-season, not per episode: a season of eight episodes
	// asks the index once. Also the ONLY way to see a pack -- the index stores
	// a season pack as episode 0, so asking for episode 7 specifically would
	// report a gap for every episode inside every pack we hold.
	seen := map[string]seasonHave{}
	var gaps []pluginapi.TVGap

	for _, e := range eps {
		if !e.AirsAt.Before(cutoff) {
			continue // too recent to judge
		}
		if e.Season <= 0 || e.Number <= 0 {
			// Season 0 is specials and episode 0 is not an episode. Neither
			// numbers reliably enough upstream to say anything about a match,
			// and guessing produces a gap nobody can act on.
			continue
		}
		key := pluginapi.SeriesKey(e.ShowTitle)
		if key == "" {
			continue
		}
		cacheKey := key + "\x00" + strconv.Itoa(e.Season)
		have, ok := seen[cacheKey]
		if !ok {
			have = s.seasonHave(ctx, idx, key, e.Season)
			seen[cacheKey] = have
		}
		if have.has(e.Number) {
			continue
		}
		gaps = append(gaps, pluginapi.TVGap{
			Episode: e, SeriesKey: key, Indexed: have.any,
		})
	}
	// Oldest first: a gap that has been open a week is a worse miss than
	// last night's, and the list is read from the top.
	sort.Slice(gaps, func(i, j int) bool {
		return gaps[i].Episode.AirsAt.Before(gaps[j].Episode.AirsAt)
	})

	s.mu.Lock()
	s.gaps, s.gapsOK = gaps, true
	s.mu.Unlock()
}

// seasonHave asks the index what it holds of one season of one show.
func (s *tvSchedule) seasonHave(ctx context.Context, idx pluginapi.SeriesIndex, key string, season int) seasonHave {
	h := seasonHave{eps: map[int]bool{}}
	// episode -1 is "every episode of this season", which is what returns the
	// packs as well as the singles.
	rels, err := idx.Releases(ctx, key, season, -1, tvGapSeasonLimit)
	if err != nil {
		// Treat a failed lookup as "we have it". Reporting a gap because the
		// database was briefly unhappy would file requests for episodes that
		// are sitting in the index.
		h.pack = true
		return h
	}
	for _, r := range rels {
		h.any = true
		if r.Pack {
			h.pack = true
			continue
		}
		h.eps[r.Episode] = true
	}
	if !h.any {
		// Nothing in this season says nothing about the show. Ask once whether
		// the index knows it at all, so a gap can say which of the two
		// problems it is.
		if _, ok, err := idx.SeriesByKey(ctx, key); err == nil {
			h.any = ok
		}
	}
	return h
}

// tvEpisodeID identifies one airing across the schedule and the gap list.
//
// Show title rather than ShowExtID: the gap list is keyed by what the schedule
// reported, and both sides come from the same rows, so this only has to be
// stable within one pass -- not to survive an upstream id change.
func tvEpisodeID(e pluginapi.TVEpisode) string {
	return e.ShowTitle + "\x00" + strconv.Itoa(e.Season) + "\x00" + strconv.Itoa(e.Number)
}

// gapSet is the calendar's lookup: which airings in a window are missing.
func (s *tvSchedule) gapSet(ctx context.Context, from, to time.Time) map[string]bool {
	gaps, err := s.Gaps(ctx, from, to)
	if err != nil || len(gaps) == 0 {
		return nil
	}
	out := make(map[string]bool, len(gaps))
	for _, g := range gaps {
		out[tvEpisodeID(g.Episode)] = true
	}
	return out
}
