package handlers

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The auto-request trigger: a gap that has stayed open long enough becomes a
// request the agent can act on.
//
// The fourth piece of the content pipeline, and the one that closes the loop:
// schedule knows it aired, gaps knows the index lacks it, trackers.search
// knows a copy exists -- and this files the request that gets it fetched.
//
// PROPOSED, and dormant until a request board publishes a filer. The board
// (loon-plugins/requests) is not wired into this demo host, and no host here
// registers pluginapi.RequestFilerName, so this trigger computes what it WOULD
// file and files nothing. That is deliberate: the decision is real, tested and
// visible on the gaps page now, so the day a board registers a filer the loop
// closes with no new host code. See pluginapi/requestfiler.go on why the seam
// exists rather than a hand-rolled INSERT.
//
// CHEAP BY DESIGN. It does not search trackers -- that is the operator's Find
// button and, later, the agent's job. It files a request carrying the precise
// catalog ids (imdb/tvdb/tmdb), which is the whole advantage of an automated
// request over a typed one: the show is identified exactly, so sourcing does
// not start from a title that may not match.

const (
	// tvRequestAge is how long a gap waits before it is worth a request.
	//
	// Longer than the display grace (three hours): that one only asks "is
	// this missing at all", while a request commits an agent's time, and a
	// day is enough for the site's own normal ingest to catch an episode
	// before anything is filed. A gap still open after a day is one the
	// ordinary flow did not fill.
	tvRequestAge = 24 * time.Hour

	// tvRequestPerPass caps how many requests one job pass files. A first run
	// against a fortnight of accumulated gaps should not file two hundred
	// rows at once; the cap spreads a backlog over passes, and a steady state
	// is a handful a day well under it.
	tvRequestPerPass = 20
)

// tvRequestOutcome is what one trigger pass did, for the job log and the page.
type tvRequestOutcome struct {
	// Requestable is how many gaps met the bar this pass.
	Requestable int
	// Filed and Deduped split what the filer did; both zero when no filer is
	// wired, which FilerWired reports so "nothing filed" and "nowhere to
	// file" stay distinct.
	Filed      int
	Deduped    int
	FilerWired bool
}

// runGapRequests is called by the schedule job after the gaps are recomputed.
//
// Takes the filer as an argument rather than reaching for it, so a test drives
// the whole path with a fake and the no-filer branch is exercised by passing
// nil.
func (w *web) runGapRequests(ctx context.Context, filer pluginapi.RequestFiler) tvRequestOutcome {
	var out tvRequestOutcome
	out.FilerWired = filer != nil
	if w.tv == nil {
		return out
	}

	// The window the request bar looks at: aired between the horizon's start
	// and the age cutoff. A gap younger than the cutoff is not yet requestable
	// and is left for a later pass.
	cutoff := time.Now().Add(-tvRequestAge)
	gaps, err := w.tv.Gaps(ctx, time.Now().AddDate(0, 0, -tvBackfillDays), cutoff)
	if err != nil {
		return out
	}

	for _, g := range gaps {
		// Only shows we actually carry. A gap for a show we have never held a
		// release of (Indexed false) is a catalogue artifact, not something to
		// send an agent after -- the same split the page draws.
		if !g.Indexed {
			continue
		}
		out.Requestable++
		// The cap counts NEWLY FILED rows only. Counting dedup hits too meant a
		// backlog never drained: once the board holds the first tvRequestPerPass
		// gaps, every later pass re-files those same gaps, each dedups, the
		// dedup count alone hits the cap, and the pass stops before reaching a
		// single un-filed gap. So gaps past the cap would never be filed. The
		// board dedups cheaply; what must stay bounded is new rows per pass.
		if out.Filed >= tvRequestPerPass {
			continue // counted, not filed: the cap spreads a backlog of NEW work
		}
		if filer == nil {
			continue // dormant: computed, visible, filed nowhere
		}
		req := w.filedRequestFor(ctx, g)
		res, err := filer.FileAutomated(ctx, req)
		if err != nil {
			w.log.Error("file gap request", "title", g.Episode.ShowTitle, "err", err)
			continue
		}
		if res.Created {
			out.Filed++
		} else {
			out.Deduped++
		}
	}
	return out
}

// filedRequestFor turns one gap into the request the board would store,
// resolving the show's external ids from the catalog so the request identifies
// it precisely rather than by a title.
func (w *web) filedRequestFor(ctx context.Context, g pluginapi.TVGap) pluginapi.FiledRequest {
	req := pluginapi.FiledRequest{
		Title:    g.Episode.ShowTitle + " " + tvEpisodeCode(g.Episode),
		Category: "tv",
		Season:   strconv.Itoa(g.Episode.Season),
		Episodes: strconv.Itoa(g.Episode.Number),
		Reason: fmt.Sprintf("aired %s, no release in the index",
			g.Episode.AirsAt.Format("2006-01-02")),
	}
	if g.Episode.ShowExtID != "" && w.tv.crossIDs != nil {
		if imdb, tvdb, err := w.tv.crossIDs(ctx, g.Episode.ShowExtID); err == nil {
			req.ImdbID, req.TvdbID = imdb, tvdb
		}
	}
	return req
}
