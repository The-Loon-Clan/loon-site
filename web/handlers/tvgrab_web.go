package handlers

import (
	"context"
	"sort"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The auto-grab: for a gap worth requesting, pick the single best torrent the
// trackers offer -- "request from the top tracker on the list, get the torrent
// file". It is the step between detecting a gap and handing an agent something
// concrete to fetch.
//
// WHERE THIS STOPS, AND WHY. The next step is dispatch: hand the chosen torrent
// to a fleet agent, which downloads it, re-uploads to Usenet, and returns an
// NZB with screenshots, metadata and subtitles, which the site then indexes.
// That agent runtime -- the /api/agent/* poll surface, the upload work queue,
// the download flow -- lives in the PRODUCTION host, not this reference demo
// (the agent plugin's own README is explicit that the runtime stays with the
// host and only its read surfaces are a plugin). So this demo demonstrates the
// pipeline up to a packed, ready-to-dispatch grab, and the dispatch itself is
// the host's agent Dispatcher feeding pluginapi.ContentPipeline with
// DeliveryTorrent -- both already named in the framework, neither wired here.
//
// LIVE, BUT BOUNDED. The search hits real public trackers, so the job grabs
// only the oldest few requestable gaps per pass -- the ones most worth
// chasing -- rather than searching a fortnight of backlog every six hours. The
// operator's Find button covers the rest on demand.

const (
	// tvGrabPerPass caps how many gaps are auto-searched per job pass. Small
	// on purpose: each is three real external searches, and the oldest gaps
	// are the ones a grab most wants to lead with.
	tvGrabPerPass = 5
	// tvGrabMinSeeders is the swarm a candidate needs before it is worth
	// dispatching. A torrent nobody seeds is a name an agent cannot fetch, so
	// it is not a grab -- better to report "no live copy" than queue a fetch
	// that will stall.
	tvGrabMinSeeders = 1
)

// tvGrab is one gap paired with the torrent chosen to fill it.
type tvGrab struct {
	Show string
	Code string
	Best pluginapi.TrackerCandidate
	Age  string
	// Found is false when the search returned nothing seedable; the gap is
	// still reported, because "we looked and there is no live copy" is a
	// different, useful answer from "we have not looked".
	Found bool
}

// runAutoGrab searches the oldest requestable gaps and records the best torrent
// for each. Called by the schedule job after the request trigger, and a no-op
// when no searcher is wired.
func (w *web) runAutoGrab(ctx context.Context) []tvGrab {
	if w.tv == nil || w.trackers == nil {
		return nil
	}
	cutoff := time.Now().Add(-tvRequestAge)
	gaps, err := w.tv.Gaps(ctx, time.Now().AddDate(0, 0, -tvBackfillDays), cutoff)
	if err != nil {
		return nil
	}
	// Oldest first is what Gaps already returns; take the carried ones.
	var requestable []pluginapi.TVGap
	for _, g := range gaps {
		if g.Indexed {
			requestable = append(requestable, g)
		}
	}
	if len(requestable) > tvGrabPerPass {
		requestable = requestable[:tvGrabPerPass]
	}

	now := time.Now()
	out := make([]tvGrab, 0, len(requestable))
	for _, g := range requestable {
		grab := tvGrab{
			Show: g.Episode.ShowTitle,
			Code: tvEpisodeCode(g.Episode),
			Age:  humanAge(now.Sub(g.Episode.AirsAt)),
		}
		if best, ok := w.bestCandidate(ctx, g); ok {
			grab.Best, grab.Found = best, true
		}
		out = append(out, grab)
	}
	return out
}

// bestCandidate runs one gap's search and returns the top seedable torrent.
//
// "Top tracker on the list" resolves to best swarm: the search already ranks
// candidates healthiest-first across every source, so the winner is the copy
// most likely to actually finish downloading, whichever tracker holds it.
func (w *web) bestCandidate(ctx context.Context, g pluginapi.TVGap) (pluginapi.TrackerCandidate, bool) {
	q := pluginapi.EpisodeSearch{
		ShowTitle: g.Episode.ShowTitle,
		Season:    g.Episode.Season,
		Episode:   g.Episode.Number,
		TVMazeID:  g.Episode.ShowExtID,
	}
	if g.Episode.ShowExtID != "" && w.tv.crossIDs != nil {
		if imdb, tvdb, err := w.tv.crossIDs(ctx, g.Episode.ShowExtID); err == nil {
			q.IMDbID, q.TVDBID = imdb, tvdb
		}
	}
	cands, err := w.trackers.SearchEpisode(ctx, q)
	if err != nil {
		return pluginapi.TrackerCandidate{}, false
	}
	// Already best-first, but assert it here so a change to the client's sort
	// cannot silently pick a worse copy: the grab takes the healthiest, and a
	// dead swarm is not a grab at all.
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Seeders > cands[j].Seeders })
	for _, c := range cands {
		if c.Seeders >= tvGrabMinSeeders {
			return c, true
		}
	}
	return pluginapi.TrackerCandidate{}, false
}
