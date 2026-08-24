package handlers

import (
	"context"
	"net/http"
	"net/url"
	"sort"
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
	Show    string
	Code    string
	Title   string
	Aired   time.Time
	Age     string
	Search  string
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
	w.tv.mu.RUnlock()

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
