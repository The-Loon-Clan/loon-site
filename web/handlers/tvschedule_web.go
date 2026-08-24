package handlers

import (
	"context"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"

	"github.com/the-loon-clan/loon-site/internal/config"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon-plugins/scraper/sources/tvmaze"
)

// What airs, and when, for the shows this site carries.
//
// The first half of the content pipeline: a calendar that knows an episode of
// something we index lands on Tuesday is one step from filing the request for
// it. The rest of that flow does not exist yet; this part is useful on its own
// and is the piece nothing else was doing.
//
// A JOB AND A CACHE, not a fetch on render. The upstream answers one day per
// call and asks for no more than twenty calls every ten seconds from an
// address, with no key to raise it. A month view is thirty-one days, which is
// nineteen seconds of polite waiting -- per viewer, per render. So a
// background loop fills a table in memory and the calendar reads that.
//
// IN MEMORY, not a table. The whole horizon is a few thousand rows, every one
// of them derived from a public feed and cheap to refetch, and none of it is
// worth a migration or a backup. A restart costs one pass; nothing is lost
// that was not already somebody else's copy.

const (
	// tvHorizonDays is how far ahead the schedule is kept. Two weeks is what a
	// month view can show of the future without the loop taking minutes, and
	// it is well past the point where a release actually appears.
	tvHorizonDays = 14
	// tvBackfillDays keeps recent days too. A calendar opened on the 3rd still
	// shows the 1st, and "did last week's episode land here" is the question an
	// indexer's calendar is actually for.
	tvBackfillDays = 7

	tvIntervalMin = 6 * 60 // six hours; a schedule changes slowly
	tvBootDelay   = 90 * time.Second
)

// tvSchedule holds the window and answers questions about it.
type tvSchedule struct {
	src     *tvmaze.Source
	country string
	// carried reports the show ids this site has catalogue entries for. Nil
	// means "keep everything", which is not what any real host wants -- see
	// filter().
	carried func(ctx context.Context) (map[string]bool, error)

	// seriesIndex reaches the index the gap join asks. A func rather than the
	// value because the index is wired on the Core and a host may have none --
	// see tvgaps_web.go.
	seriesIndex func() pluginapi.SeriesIndex
	// requestFiler is the proposed auto-request seam, nil until a request
	// board publishes one -- which is every host today. See tvgapsrequest_web.go.
	requestFiler pluginapi.RequestFiler
	// crossIDs resolves a show's imdb/tvdb ids from its tvmaze id, so an
	// automated request identifies the show precisely. A func for the same
	// reason carried is one: it reaches host storage a test does not have.
	crossIDs func(ctx context.Context, tvmazeID string) (imdb, tvdb string, err error)

	mu      sync.RWMutex
	eps     []pluginapi.TVEpisode // sorted by AirsAt
	filled  time.Time
	lastErr error
	// gaps is what aired without arriving, oldest first, recomputed after each
	// refill. gapsOK distinguishes "nothing is missing" from "nobody asked" --
	// an empty list means the same thing in Go and very different things to a
	// reader.
	gaps   []pluginapi.TVGap
	gapsOK bool
	// lastReq is what the most recent trigger pass decided, shown on the gaps
	// page so the dormant seam is visible: "3 requestable, no filer wired".
	lastReq tvRequestOutcome
}

var _ pluginapi.TVScheduleProvider = (*tvSchedule)(nil)

// Upcoming returns the episodes airing in [from, to).
//
// Empty and no error when the loop has not run yet. That is an ordinary state
// on a process ninety seconds old, and a calendar must draw without television
// rather than fail with it.
func (s *tvSchedule) Upcoming(_ context.Context, from, to time.Time) ([]pluginapi.TVEpisode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.eps) == 0 {
		return nil, nil
	}
	// Sorted, so the window is a pair of binary searches rather than a scan.
	lo := sort.Search(len(s.eps), func(i int) bool { return !s.eps[i].AirsAt.Before(from) })
	hi := sort.Search(len(s.eps), func(i int) bool { return !s.eps[i].AirsAt.Before(to) })
	if lo >= hi {
		return nil, nil
	}
	out := make([]pluginapi.TVEpisode, hi-lo)
	copy(out, s.eps[lo:hi])
	return out, nil
}

// refill fetches the whole window and replaces the index.
//
// REPLACES rather than merges. An episode can be rescheduled or pulled, and a
// merge would keep showing one that is no longer on the schedule -- a calendar
// insisting an episode airs on a day it does not is worse than one that is a
// few hours stale.
func (s *tvSchedule) refill(ctx context.Context) error {
	carried, err := s.carriedSet(ctx)
	if err != nil {
		return err
	}
	day := time.Now().UTC().AddDate(0, 0, -tvBackfillDays).Truncate(24 * time.Hour)
	var got []pluginapi.TVEpisode
	var firstErr error
	for i := 0; i < tvBackfillDays+tvHorizonDays; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		airings, err := s.src.Schedule(ctx, day.AddDate(0, 0, i), s.country)
		if err != nil {
			// One bad day is not a bad pass. Upstream hiccups, and throwing
			// away thirteen good days because the fourteenth 500'd would make
			// the calendar emptier the flakier the network is.
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, a := range airings {
			if carried != nil && !carried[a.ShowID] {
				continue
			}
			got = append(got, pluginapi.TVEpisode{
				ShowExtID: a.ShowID, ShowTitle: a.ShowTitle,
				Season: a.Season, Number: a.Number,
				Title: a.Title, AirsAt: a.AirsAt, URL: a.URL,
			})
		}
	}
	sort.Slice(got, func(i, j int) bool { return got[i].AirsAt.Before(got[j].AirsAt) })

	s.mu.Lock()
	// A pass that produced nothing does NOT clear a good window. Every day
	// failing looks exactly like a quiet fortnight from in here, and the
	// difference matters: one empties the calendar, the other is the truth.
	if len(got) > 0 || firstErr == nil {
		s.eps, s.filled = got, time.Now()
	}
	s.lastErr = firstErr
	s.mu.Unlock()
	return firstErr
}

// carriedSet resolves the show ids this site has entries for.
func (s *tvSchedule) carriedSet(ctx context.Context) (map[string]bool, error) {
	if s.carried == nil {
		return nil, nil
	}
	set, err := s.carried(ctx)
	if err != nil {
		return nil, err
	}
	// An EMPTY set is not the same as no filter, and getting this backwards is
	// how a calendar shows all of television. A site with no TV entries should
	// show no TV, so an empty-but-non-nil set filters everything out.
	if set == nil {
		set = map[string]bool{}
	}
	return set, nil
}

// calTV contributes the airings to /calendar.
//
// Not per-viewer, and that is a decision rather than an omission: nothing here
// lets a member follow a show, so "your shows" has no meaning yet. What the
// site CARRIES is the honest scope, and it is the one an indexer's calendar is
// for -- did last week's episode land here, and what is coming.
func (w *web) calTV() calSource {
	return calSource{
		Name: "tv",
		Fn: func(ctx context.Context, _ int64, from, to time.Time) []calEvent {
			if w.tv == nil {
				return nil
			}
			eps, err := w.tv.Upcoming(ctx, from, to)
			if err != nil || len(eps) == 0 {
				return nil
			}
			// Which of them the index has nothing for. Read from the list the
			// job already computed, so a month view costs a map build rather
			// than a query per show-season per viewer.
			missing := w.tv.gapSet(ctx, from, to)
			out := make([]calEvent, 0, len(eps))
			for _, e := range eps {
				kind, icon := "tv", "film"
				if missing[tvEpisodeID(e)] {
					// The whole point of putting television on an INDEXER's
					// calendar: not "this aired" but "this aired and we do not
					// have it".
					kind, icon = "tv-gap", "info"
				}
				out = append(out, calEvent{
					Start: e.AirsAt,
					// End == Start: a broadcast is an instant on this grid, not
					// a span. Giving it a runtime would make a 22:00 episode
					// cover two cells, which is not what "it airs Tuesday"
					// means to anybody reading a calendar.
					End:   e.AirsAt,
					Kind:  kind,
					Icon:  icon,
					Label: e.ShowTitle + " " + tvEpisodeLabel(e),
					// The search, not the upstream episode page. This calendar
					// belongs to an indexer and the question it raises is
					// "have we got it?" -- sending somebody off-site to read a
					// synopsis answers a question they did not ask.
					Href: "/search?q=" + url.QueryEscape(e.ShowTitle),
				})
			}
			return out
		},
	}
}

// tvEpisodeLabel is "S01E04 — Title", or just the code when the episode has no
// name of its own, which daily programming usually does not.
func tvEpisodeLabel(e pluginapi.TVEpisode) string {
	code := tvEpisodeCode(e)
	if e.Title == "" {
		return code
	}
	return code + " — " + e.Title
}

// wireTVSchedule builds the schedule service, publishes it and starts its loop.
//
// Registered on the Core under pluginapi.TVScheduleName even though the host
// is also its only consumer today. The next consumer is the one this exists
// for -- something that files a request when a carried show airs -- and it
// should ask a capability rather than reach into a host field.
func (w *web) wireTVSchedule(c *core.Core, src *tvmaze.Source) {
	if src == nil || w.store == nil {
		return
	}
	w.tv = &tvSchedule{
		src:     src,
		country: getenvDefault("LOON_TV_COUNTRY", "US"),
		carried: func(ctx context.Context) (map[string]bool, error) {
			return w.data.CarriedShowIDs(ctx, "tvmaze")
		},
	}
	w.tv.seriesIndex = func() pluginapi.SeriesIndex { return w.series }
	w.tv.crossIDs = w.data.TVCrossIDs
	if err := c.Register(pluginapi.TVScheduleName, pluginapi.TVScheduleProvider(w.tv)); err != nil {
		w.log.Error("register tv schedule", "err", err)
	}
	// The join, published separately -- the thing that will file requests asks
	// for gaps, not for a schedule it would have to join itself.
	if err := c.Register(pluginapi.TVGapsName, pluginapi.TVGapFinder(w.tv)); err != nil {
		w.log.Error("register tv gaps", "err", err)
	}
	// The auto-request filer, if any board published one BEFORE this ran.
	// Resolved once here and re-checked each pass is overkill; a board that
	// registers late is a restart away from being seen, same as every other
	// capability wired at boot.
	if filer, ok := pluginapi.LookupRequestFiler(c); ok {
		w.tv.requestFiler = filer
	}

	job := schedule.RegisterJob("TV Schedule",
		"Fetches the broadcast schedule for the shows this site carries, so the calendar can show when the next episode airs.")
	job.IntervalMin = tvIntervalMin
	job.SetTrigger(func() { go w.runTVSchedule(context.Background(), job) })
	// Registered everywhere so /admin/jobs can list it and Run now has
	// something to enqueue; the loop runs only where jobs run. Same split as
	// the sitemap job, and for the same reason.
	if config.RunsJobs() {
		go schedule.ServiceLoop(context.Background(), job,
			tvBootDelay, tvIntervalMin*time.Minute,
			func(ctx context.Context) { w.runTVSchedule(ctx, job) })
	}
}

func (w *web) runTVSchedule(ctx context.Context, job *schedule.JobInfo) {
	job.SetRunning()
	next := time.Now().Add(tvIntervalMin * time.Minute)
	if err := w.tv.refill(ctx); err != nil {
		// SetError, not SetIdle: a pass where every day failed leaves the
		// window as it was, and a job card reading "idle" would say that was
		// fine. One bad day inside a good pass is absorbed by refill and never
		// reaches here.
		job.SetError(err.Error())
		w.log.Error("tv schedule", "err", err)
		return
	}
	// AFTER the refill and inside the same pass: the join is only as good as
	// the window it ran against, and computing it on render would be a query
	// per show-season per viewer.
	w.tv.recomputeGaps(ctx)

	// The auto-request trigger, right after the gaps it reads. Files nothing
	// when no board is wired -- see tvgapsrequest_web.go -- but always
	// computes what it WOULD file, so the page can show it.
	req := w.runGapRequests(ctx, w.tv.requestFiler)
	w.tv.mu.Lock()
	w.tv.lastReq = req
	w.tv.mu.Unlock()

	w.tv.mu.RLock()
	n, missing, ok := len(w.tv.eps), len(w.tv.gaps), w.tv.gapsOK
	w.tv.mu.RUnlock()
	job.Log("Schedule holds %d episode(s) across %d days", n, tvBackfillDays+tvHorizonDays)
	if ok {
		job.Log("%d aired episode(s) have no matching release", missing)
	} else {
		job.Log("No series index wired; gaps not computed")
	}
	if req.FilerWired {
		job.Log("Auto-request: %d filed, %d already open (%d requestable)", req.Filed, req.Deduped, req.Requestable)
	} else if req.Requestable > 0 {
		job.Log("Auto-request: %d gap(s) requestable, no request board wired to file them", req.Requestable)
	}
	job.SetIdle(next)
}
