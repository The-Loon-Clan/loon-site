package handlers

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The operator's view of what the index missed (tvgaps_web.go does the join).
//
// Read-only, and deliberately so at this stage. The next step files requests
// for these; putting a button here first would mean an operator clicking one
// row at a time forever, which is the manual version of the thing being built.
// What this page is for now is deciding whether the detection is any good --
// look at the list, see whether the episodes on it really are missing, and only
// then let something act on it automatically.

// tvGapRow is one line on the page.
type tvGapRow struct {
	Show   string
	Code   string
	Title  string
	Aired  time.Time
	Age    string
	Search string
	// Find asks the external trackers (tvgapsadmin find handler); "" when no
	// searcher is wired, and the template drops the button rather than offer
	// a dead one.
	Find    string
	Indexed bool
}

// tvGapsVM is the whole page.
type tvGapsVM struct {
	Missed   []tvGapRow // shows we carry, episode absent
	Unheld   []tvGapRow // shows with no releases at all
	Judged   int
	Window   int
	Computed bool
	Filled   time.Time
	// Req is what the last auto-request pass decided, so the dormant trigger
	// is visible: "3 requestable, no request board wired".
	Req tvRequestOutcome
	// Grabs is the top torrent chosen for the oldest few gaps, ready to hand
	// to an agent -- the "request from the top tracker" step made concrete.
	Grabs []tvGrabRow
}

// tvGrabRow is one chosen torrent as the dispatch table shows it.
type tvGrabRow struct {
	Show       string
	Code       string
	Age        string
	Found      bool
	Title      string
	Source     string
	Via        string
	Size       string
	Seeders    int
	Magnet     string
	Dispatched bool
}

func (w *web) adminTVGaps(c *gin.Context) {
	if w.tv == nil {
		// No schedule provider means no page rather than an empty table: an
		// operator reading "0 missing" would take it as an answer.
		c.Redirect(http.StatusSeeOther, "/admin/jobs")
		return
	}
	vm := w.tvGapsVM(c.Request.Context())
	w.render(c, "admin_tvgaps.html", map[string]any{
		"Title": "Missing episodes",
		"VM":    vm,
	})
}

func (w *web) tvGapsVM(ctx context.Context) tvGapsVM {
	vm := tvGapsVM{Window: tvBackfillDays}
	if w.tv == nil {
		return vm
	}
	w.tv.mu.RLock()
	vm.Computed, vm.Filled, vm.Judged = w.tv.gapsOK, w.tv.filled, len(w.tv.eps)
	vm.Req = w.tv.lastReq
	grabs := make([]tvGrab, len(w.tv.grabs))
	copy(grabs, w.tv.grabs)
	w.tv.mu.RUnlock()
	for _, g := range grabs {
		row := tvGrabRow{Show: g.Show, Code: g.Code, Age: g.Age, Found: g.Found, Dispatched: g.Dispatched}
		if g.Found {
			row.Title = g.Best.Title
			row.Source = g.Best.TrackerSlug
			row.Via = g.Best.Via
			row.Seeders = g.Best.Seeders
			row.Magnet = g.Best.Magnet
			if g.Best.SizeBytes > 0 {
				row.Size = humanSize(g.Best.SizeBytes)
			}
		}
		vm.Grabs = append(vm.Grabs, row)
	}

	gaps, err := w.tv.Gaps(ctx, time.Now().AddDate(0, 0, -tvBackfillDays), time.Now())
	if err != nil {
		return vm
	}
	now := time.Now()
	for _, g := range gaps {
		row := tvGapRow{
			Show:  g.Episode.ShowTitle,
			Code:  tvEpisodeCode(g.Episode),
			Title: g.Episode.Title,
			Aired: g.Episode.AirsAt,
			Age:   humanAge(now.Sub(g.Episode.AirsAt)),
			// The search this operator would type anyway, pre-typed. Show and
			// episode code together, because searching the show name alone on
			// a long-running series returns a screenful of the wrong seasons.
			Search:  "/search?q=" + url.QueryEscape(g.Episode.ShowTitle+" "+tvEpisodeCode(g.Episode)),
			Indexed: g.Indexed,
		}
		if w.trackers != nil {
			row.Find = "/admin/tv-gaps/find?title=" + url.QueryEscape(g.Episode.ShowTitle) +
				"&s=" + strconv.Itoa(g.Episode.Season) + "&e=" + strconv.Itoa(g.Episode.Number) +
				"&ext=" + url.QueryEscape(g.Episode.ShowExtID)
		}
		if g.Indexed {
			vm.Missed = append(vm.Missed, row)
		} else {
			vm.Unheld = append(vm.Unheld, row)
		}
	}
	// Within each list, oldest first is already what Gaps returns; the split
	// preserves it.
	sort.SliceStable(vm.Unheld, func(i, j int) bool { return vm.Unheld[i].Show < vm.Unheld[j].Show })
	return vm
}

// tvEpisodeCode is "S03E07" with no title attached, for the places that put the
// show name beside it themselves.
func tvEpisodeCode(e pluginapi.TVEpisode) string {
	return "S" + pad2(e.Season) + "E" + pad2(e.Number)
}

// humanAge is a coarse "how long has this been missing".
//
// Coarse on purpose: the difference between 26 and 27 hours changes no
// decision, and a column of exact durations is harder to scan than one of
// "2 days".
func humanAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return "just now"
	case d < 24*time.Hour:
		h := int(d.Hours())
		return itoa(h) + "h ago"
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "yesterday"
		}
		return itoa(days) + " days ago"
	}
}

// ── Ask the trackers ────────────────────────────────────────────────────────
//
// The "Find" link on a gap row. OPERATOR-TRIGGERED, never on render: a page
// load must not knock on three external doors times twenty rows. One click,
// one search, results above the table -- and each source is spaced by the
// client's own politeness whatever the operator's clicking rate.

// tvGapHitRow is one candidate as the results table shows it.
type tvGapHitRow struct {
	Source  string
	Via     string
	Title   string
	Size    string
	Seeders int
	Leech   int
	Age     string
	Magnet  string
	PageURL string
}

// tvGapSearchVM is the results panel.
type tvGapSearchVM struct {
	Query   string
	Code    string
	Hits    []tvGapHitRow
	Sources []pluginapi.TrackerSource
	IMDb    string
}

// adminTVGapsFind runs one search and re-renders the gaps page with results.
func (w *web) adminTVGapsFind(c *gin.Context) {
	if w.tv == nil || w.trackers == nil {
		c.Redirect(http.StatusSeeOther, "/admin/tv-gaps")
		return
	}
	title := strings.TrimSpace(c.Query("title"))
	season, episode := atoiQuery(c, "s"), atoiQuery(c, "e")
	if title == "" || season <= 0 || episode <= 0 {
		c.Redirect(http.StatusSeeOther, "/admin/tv-gaps")
		return
	}
	q := pluginapi.EpisodeSearch{
		ShowTitle: title, Season: season, Episode: episode,
		TVMazeID: c.Query("ext"),
	}
	// The ids EZTV and friends actually answer by, resolved from the catalog
	// rather than asked of the operator -- they clicked a row, not a form.
	if q.TVMazeID != "" {
		if imdb, tvdb, err := w.data.TVCrossIDs(c.Request.Context(), q.TVMazeID); err == nil {
			q.IMDbID, q.TVDBID = imdb, tvdb
		}
	}
	// Bounded: three sources with a 2s spacing each still finish inside
	// this, and an operator watching a spinner deserves an end.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	hits, err := w.trackers.SearchEpisode(ctx, q)
	search := &tvGapSearchVM{
		Query: title, Code: tvEpisodeCode(pluginapi.TVEpisode{Season: season, Number: episode}),
		Sources: w.trackers.Sources(), IMDb: q.IMDbID,
	}
	if err == nil {
		now := time.Now()
		for _, h := range hits {
			row := tvGapHitRow{
				Source: h.TrackerSlug, Via: h.Via, Title: h.Title,
				Seeders: h.Seeders, Leech: h.Leechers,
				Magnet: h.Magnet, PageURL: h.PageURL,
			}
			if h.SizeBytes > 0 {
				row.Size = humanSize(h.SizeBytes)
			}
			if !h.PostedAt.IsZero() {
				row.Age = humanAge(now.Sub(h.PostedAt))
			}
			search.Hits = append(search.Hits, row)
		}
	}
	vm := w.tvGapsVM(c.Request.Context())
	w.render(c, "admin_tvgaps.html", map[string]any{
		"Title":  "Missing episodes",
		"VM":     vm,
		"Search": search,
	})
}

// atoiQuery is one query parameter as a number, zero when absent or junk.
func atoiQuery(c *gin.Context, name string) int {
	n, _ := strconv.Atoi(c.Query(name))
	return n
}

// humanSize is bytes at the scale a release listing uses.
func humanSize(b int64) string {
	switch {
	case b >= 1<<30:
		return strconv.FormatFloat(float64(b)/float64(1<<30), 'f', 1, 64) + " GB"
	case b >= 1<<20:
		return strconv.FormatFloat(float64(b)/float64(1<<20), 'f', 0, 64) + " MB"
	default:
		return strconv.FormatInt(b, 10) + " B"
	}
}
