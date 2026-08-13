package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/schedule"
)

// The staff dashboard at /admin — UNIT3D's staff landing page.
//
// /admin was a 404. Twenty-odd admin routes existed under it and nothing served
// the root, so the usenet settings page's own "Back" button pointed at a dead
// URL and there was no answer to "is anything wrong right now?" short of
// opening four pages.
//
// EVERY figure here is read from a real table or from the live scheduler. There
// is no entry in docs/MOCKS.md for this page and there must not be: a staff
// dashboard is the one surface where a fabricated number would be acted on. A
// tile whose source is unwired renders as "—" rather than as zero, because zero
// open tickets and no ticket system are very different facts.

// statTile is one figure on the dashboard.
type statTile struct {
	Label string
	Value string
	Sub   string // optional qualifier under the value
	Href  string // optional drill-down
	Warn  bool   // render as attention-needed
}

// dashVM is the page's view model. Tiles and Groups are always non-nil so the
// template can range them unguarded.
type dashVM struct {
	Tiles  []statTile
	Alerts []statTile
}

// adminDashboard renders /admin.
func (w *web) adminDashboard(c *gin.Context) {
	ctx := c.Request.Context()
	vm := dashVM{Tiles: []statTile{}, Alerts: []statTile{}}

	// ── jobs: the liveness question a staff page exists to answer ──
	snaps := schedule.GetAllSnapshots()
	var running, paused, failing int
	for _, s := range snaps {
		if s.Status == "running" || s.ElapsedSecs > 0 {
			running++
		}
		if s.Paused {
			paused++
		}
		if s.LastError != "" {
			failing++
		}
	}
	vm.Tiles = append(vm.Tiles, statTile{
		Label: "Jobs", Value: itoa(len(snaps)),
		Sub: itoa(running) + " running", Href: "/admin/jobs",
	})
	if failing > 0 {
		vm.Alerts = append(vm.Alerts, statTile{
			Label: "Jobs reporting an error", Value: itoa(failing),
			Href: "/admin/jobs", Warn: true,
		})
	}
	if paused > 0 {
		vm.Alerts = append(vm.Alerts, statTile{
			Label: "Jobs paused", Value: itoa(paused), Href: "/admin/jobs", Warn: true,
		})
	}

	// ── plugins ──
	if w.rt != nil {
		vm.Tiles = append(vm.Tiles, statTile{
			Label: "Plugins", Value: itoa(len(w.rt.Plugins())), Href: "/admin/plugins",
		})
	}

	// ── members ──
	// w.data.DB() is nil only in a test harness; a dash is honest there.
	vm.Tiles = append(vm.Tiles, statTile{
		Label: "Members",
		Value: w.countOrDash(ctx, w.data.CountUsers),
		Sub:   plural7d(w.countOrDash(ctx, w.data.CountUsersJoinedLastWeek)),
	})

	// ── support ──
	// Open tickets are the one figure here that is a WORK QUEUE rather than a
	// measurement, so it doubles as an alert when non-zero.
	open := w.countOrDash(ctx, w.data.CountOpenTickets)
	vm.Tiles = append(vm.Tiles, statTile{
		Label: "Open tickets", Value: open, Href: "/admin/tickets",
	})
	if open != "—" && open != "0" {
		vm.Alerts = append(vm.Alerts, statTile{
			Label: "Tickets awaiting a reply", Value: open, Href: "/admin/tickets", Warn: true,
		})
	}

	// ── index ──
	// homeGroups, NOT UsenetIndex.Feed. Feed's total for an unfiltered query is
	// a pg_class.reltuples ESTIMATE — the plugin says so in as many words
	// ("a pagination hint, not an accounting figure") — and it read 7,184
	// against a real 7,829 here. Presenting a planner estimate as a count is
	// the exact bug that stalled this deployment's crawler for hours.
	//
	// homeGroups sums the per-group NZB counts, which is exact, is already
	// cached for a minute, and is the same figure /stats prints — two pages
	// disagreeing about how many releases the site has would be its own bug.
	if w.usenet != nil {
		if gs, ok := w.homeGroups(ctx); ok {
			vm.Tiles = append(vm.Tiles,
				statTile{Label: "Releases", Value: itoa(int(gs.Stats.Releases)), Href: "/browse"},
				statTile{Label: "Newsgroups", Value: itoa(gs.Stats.Groups), Href: "/groups"})
		}
	}

	// ── forum ──
	// Omitted rather than dashed when unreadable: the tiles above are figures
	// the site always has, so "—" means "not measurable"; a forum that is not
	// there has no tile to qualify.
	if w.data != nil {
		if threads, ok := w.data.CountForumThreads(ctx); ok {
			vm.Tiles = append(vm.Tiles, statTile{
				Label: "Forum threads", Value: itoa(threads), Href: "/community/forums",
			})
		}
	}

	// ── failed sign-ins ──
	// A burst of failures is the one security signal this host can honestly
	// report; it has no rate limiter or ban list to summarise.
	if w.loginLog != nil {
		if entries, err := w.loginLog.RecentAll(ctx, 200); err == nil {
			cutoff := time.Now().Add(-24 * time.Hour)
			var bad int
			for _, e := range entries {
				if !e.Success && e.CreatedAt.After(cutoff) {
					bad++
				}
			}
			if bad > 0 {
				vm.Alerts = append(vm.Alerts, statTile{
					Label: "Failed sign-ins (24h)", Value: itoa(bad),
					Sub: "of the last 200 attempts", Href: "/admin/p/login-log", Warn: true,
				})
			}
		}
	}

	w.render(c, "admin_dashboard.html", map[string]any{
		"Title": "Dashboard", "Dash": vm,
	})
}

// countOrDash formats one dashboard figure, or an em dash if it cannot be read.
//
// It used to take a SQL string, which put the schema in the page that renders
// it. It takes a store method now: the query lives beside the schema it names,
// and this keeps the only decision that is genuinely the PAGE's — that an
// unmeasurable figure shows "—" rather than "0", because on a staff page a
// wrong number is one that gets acted on.
//
// A nil store means a test harness (dashWeb builds one with no data sources at
// all, to assert exactly this behaviour), and a dash is honest there.
func (w *web) countOrDash(ctx context.Context, count func(context.Context) (int, bool)) string {
	if w.data == nil {
		return "—"
	}
	n, ok := count(ctx)
	if !ok {
		return "—"
	}
	return itoa(n)
}

func plural7d(v string) string {
	if v == "—" {
		return ""
	}
	return v + " in the last 7 days"
}

func itoa(n int) string {
	// Thousands separators: these are read at a glance, and 26737749 is not.
	s := ""
	if n < 0 {
		return "—"
	}
	if n == 0 {
		return "0"
	}
	for i := 0; n > 0; i++ {
		if i > 0 && i%3 == 0 {
			s = "," + s
		}
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

// mountDashboard registers /admin. Called from wireViews, which already holds
// the admin group and runs after Boot (so w.rt and w.usenet are resolved).
func (w *web) mountDashboard(admin *gin.RouterGroup) {
	admin.GET("", w.adminDashboard)
	// Without this, /admin/ 301s to /admin and gin's redirectTrailingSlash
	// would loop on a group whose own root is the handler.
	admin.GET("/", func(c *gin.Context) { c.Redirect(http.StatusMovedPermanently, "/admin") })
}
