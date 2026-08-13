package handlers

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/storage"
	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// /trending — UNIT3D's trending page, over the one signal this indexer really
// has: NZB downloads (release_grab, grabs_web.go).
//
// It resolves grabbed IDS back through the index rather than ranking a page the
// site already fetched, which is what the home block does (popularRows) and why
// that one is not reusable here. The difference matters: a release grabbed
// heavily last week must appear even when it is no longer in the newest 200,
// and on the home page it must not, because that block is explicitly a view of
// the rows on screen.
//
// Nothing here invents a number. A release with no grabs cannot rank, and a
// window with no grabs renders an empty state rather than a list of zeroes —
// there is no seeding, no peers and no popularity score to fall back on.

// trendingWindow is one selectable period.
type trendingWindow struct {
	Days   int
	Label  string
	Href   string
	Active bool
}

// trendingWindows are the periods offered. Fixed set, not a free-form ?days=:
// the value goes into a SQL interval, and an allowlist means the handler never
// has to sanitise an integer into a date range.
var trendingWindows = []struct {
	Days  int
	Label string
}{
	{1, "Today"},
	{7, "This week"},
	{30, "This month"},
	{365, "This year"},
}

const (
	trendingDefaultDays = 7
	trendingRows        = 50
)

func (w *web) trending(c *gin.Context) {
	ctx := c.Request.Context()
	days := trendingDefaultDays
	if v, err := strconv.Atoi(c.Query("days")); err == nil {
		for _, win := range trendingWindows {
			if win.Days == v {
				days = v
			}
		}
	}

	windows := make([]trendingWindow, 0, len(trendingWindows))
	for _, win := range trendingWindows {
		windows = append(windows, trendingWindow{
			Days:   win.Days,
			Label:  win.Label,
			Href:   "/trending?days=" + strconv.Itoa(win.Days),
			Active: win.Days == days,
		})
	}

	data := map[string]any{
		"Title":   "Trending",
		"Windows": windows,
		"Days":    days,
		"Results": []searchRow{},
	}
	rows := w.trendingRows(ctx, days, trendingRows)
	data["Results"] = rows
	w.render(c, "trending.html", data)
}

// trendingRows ranks releases by grab count over the window and resolves each
// back to a row.
//
// One index read PER RANKED ID, which is why the row count is bounded and the
// result is cached: UsenetIndex has no bulk by-ids read, and adding a fake one
// by pulling a huge Feed page and filtering would be a bigger query that still
// missed anything past the page. Ranked ids are few by construction — the top
// N of a grab tally — so N small reads is the cheaper shape.
func (w *web) trendingRows(ctx context.Context, days, limit int) []searchRow {
	if w.usenet == nil {
		return []searchRow{}
	}
	key := "trending:" + strconv.Itoa(days) + ":" + strconv.Itoa(limit)
	var cached []searchRow
	if w.cacheGet(ctx, key, &cached) {
		return cached
	}

	// Over-fetch ids: some will have aged out of the index since they were
	// grabbed, and those drop out silently rather than leaving gaps.
	ids, counts := storage.PopularGrabs(ctx, days, limit*2)
	rows := make([]searchRow, 0, limit)
	for _, id := range ids {
		detail, ok, err := w.usenet.ReleaseByID(ctx, id)
		if err != nil || !ok {
			continue // deleted from the index; the grab row outlived it
		}
		row := toSearchRows([]pluginapi.Release{detail.Release})[0]
		row.Grabs = counts[id]
		rows = append(rows, row)
		if len(rows) == limit {
			break
		}
	}
	w.attachCovers(ctx, rows)
	// Short TTL: this is a leaderboard, not a feed. A minute keeps the page off
	// the index for a burst of traffic without letting the order go stale.
	w.cacheSet(ctx, key, rows, time.Minute)
	return rows
}
