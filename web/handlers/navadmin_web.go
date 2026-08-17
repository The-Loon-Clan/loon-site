package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/storage"

	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// The navigation editor — /admin/nav, and the data-driven menu behind the
// four main dropdowns. WordPress's Menus screen at Ghost's complexity: one
// level deep, the four groups fixed, everything INSIDE them a row the
// operator may relabel, reorder, move between groups, hide, or extend with
// custom links (a /pages/ page, an external URL).
//
// Three layers keep this safe to lean on:
//
//	defaults  the shipped menu, in code. The seed for a fresh table, the
//	          reconciliation source for an upgraded one (a new builtin href
//	          is ENSURED into old databases without touching operator
//	          edits), and the render fallback when the table is unreadable —
//	          a broken settings table must never cost the site its nav.
//	mirror    the rows, loaded at boot and on save into an atomic — the menu
//	          is read on every request and must not be a query per page.
//	assembly  per request: drop hidden rows and rows whose CONDITION says no
//	          (the flavour gates), compute active states, group and sort.
//
// Icons and conditions stay in code, keyed by href. They are the system's
// knowledge — what sprite fits, whether the indexer half is on — and an
// operator row carries only the operator's decisions: label, place, order.

// The four groups, in tab order. Fixed: the editor moves items BETWEEN
// dropdowns, it does not invent new top-level tabs — the bar's width is a
// design constraint, not a setting (see site_chrome.html's "Other" comment).
var navGroups = []string{"releases", "community", "support", "other"}

// navDefaults is the shipped menu — the same items site_chrome.html carried
// as literals, in the same order.
var navDefaults = []storage.NavEntry{
	{Href: "/browse", Label: "Browse", Grp: "releases", Ordinal: 10, Builtin: true},
	{Href: "/search", Label: "Search", Grp: "releases", Ordinal: 20, Builtin: true},
	{Href: "/groups", Label: "Newsgroups", Grp: "releases", Ordinal: 30, Builtin: true},
	{Href: "/trending", Label: "Trending", Grp: "releases", Ordinal: 40, Builtin: true},
	{Href: "/tracker", Label: "Torrents", Grp: "releases", Ordinal: 50, Builtin: true},
	{Href: "/api", Label: "Newznab API", Grp: "releases", Ordinal: 60, Builtin: true},

	{Href: "/community/forums", Label: "Forums", Grp: "community", Ordinal: 10, Builtin: true},
	{Href: "/c", Label: "Communities", Grp: "community", Ordinal: 20, Builtin: true},
	{Href: "/playlists", Label: "Playlists", Grp: "community", Ordinal: 30, Builtin: true},

	{Href: "/news", Label: "News", Grp: "support", Ordinal: 10, Builtin: true},
	{Href: "/rules", Label: "Rules", Grp: "support", Ordinal: 20, Builtin: true},
	{Href: "/faq", Label: "FAQ", Grp: "support", Ordinal: 30, Builtin: true},
	{Href: "/wiki", Label: "Wiki", Grp: "support", Ordinal: 40, Builtin: true},
	{Href: "/support", Label: "Helpdesk", Grp: "support", Ordinal: 50, Builtin: true},
	{Href: "/staff", Label: "Staff", Grp: "support", Ordinal: 60, Builtin: true},

	{Href: "/stats", Label: "Stats", Grp: "other", Ordinal: 10, Builtin: true},
	{Href: "/about", Label: "About", Grp: "other", Ordinal: 20, Builtin: true},
	{Href: "/sitemap", Label: "Sitemap", Grp: "other", Ordinal: 30, Builtin: true},
}

// navIcons maps hrefs to sprite ids. A custom link gets the tag glyph —
// same as an ungrouped plugin page.
var navIcons = map[string]string{
	"/browse": "browse", "/search": "search", "/groups": "groups",
	"/trending": "database", "/tracker": "download", "/api": "code",
	"/community/forums": "forums", "/c": "users", "/playlists": "music",
	"/news": "rss", "/rules": "shield", "/faq": "info", "/wiki": "book",
	"/support": "comment", "/staff": "shield",
	"/stats": "database", "/about": "logo", "/sitemap": "globe",
}

// navConditions are the system's gates — the flavour halves. An operator's
// "hidden" and a failing condition both remove the item; the difference is
// who decided.
var navConditions = map[string]func() bool{
	"/groups":  flavourIndexer,
	"/api":     flavourIndexer,
	"/tracker": flavourTracker,
}

// The mirror. Empty means "fall back to navDefaults" — boot not reached, or
// the table unreadable.
var navMirror atomic.Value // []storage.NavEntry

// loadSiteNav ensures every builtin default exists, then mirrors the table.
func loadSiteNav(ctx context.Context, data *storage.Store) error {
	for _, d := range navDefaults {
		if err := data.EnsureSiteNav(ctx, d); err != nil {
			return err
		}
	}
	return refreshNavMirror(ctx, data)
}

func refreshNavMirror(ctx context.Context, data *storage.Store) error {
	rows, err := data.ListSiteNav(ctx)
	if err != nil {
		return err
	}
	navMirror.Store(rows)
	return nil
}

// navRows returns the mirror, or the shipped defaults when it is empty.
func navRows() []storage.NavEntry {
	rows, _ := navMirror.Load().([]storage.NavEntry)
	if len(rows) == 0 {
		return navDefaults
	}
	return rows
}

// chromeNavItem is one dropdown entry as site_chrome.html renders it.
type chromeNavItem struct {
	Label  string
	Href   string
	Icon   string
	Active bool
}

// assembleNav turns rows into per-group render lists plus per-group active
// flags, for the given request path. Pure, so the tests can hold it still.
func assembleNav(rows []storage.NavEntry, path string) (map[string][]chromeNavItem, map[string]bool) {
	items := map[string][]chromeNavItem{}
	active := map[string]bool{}
	for _, r := range rows {
		if r.Hidden {
			continue
		}
		if cond, ok := navConditions[r.Href]; ok && !cond() {
			continue
		}
		grp := r.Grp
		if !validNavGroup(grp) {
			grp = "other" // an unknown group must not swallow the row silently
		}
		icon := navIcons[r.Href]
		if icon == "" {
			icon = "tag"
		}
		on := navActive(path, r.Href)
		items[grp] = append(items[grp], chromeNavItem{Label: r.Label, Href: r.Href, Icon: icon, Active: on})
		if on {
			active[grp] = true
		}
	}
	// Rows arrive grp-then-ordinal ordered from ListSiteNav (and navDefaults
	// is declared in order), so the lists are already sorted.
	return items, active
}

func validNavGroup(g string) bool {
	for _, k := range navGroups {
		if g == k {
			return true
		}
	}
	return false
}

// ── the editor ──────────────────────────────────────────────────────────

// adminNavEditor serves GET /admin/nav.
func (w *web) adminNavEditor(c *gin.Context) {
	rows, err := w.data.ListSiteNav(c.Request.Context())
	if err != nil {
		c.String(http.StatusInternalServerError, "could not read the menu")
		return
	}
	byGroup := map[string][]storage.NavEntry{}
	for _, r := range rows {
		g := r.Grp
		if !validNavGroup(g) {
			g = "other"
		}
		byGroup[g] = append(byGroup[g], r)
	}
	var groups []map[string]any
	for _, g := range navGroups {
		groups = append(groups, map[string]any{"Key": g, "Rows": byGroup[g]})
	}
	w.render(c, "admin_nav.html", map[string]any{
		"Title":  "Navigation",
		"Groups": groups,
		"Saved":  c.Query(querySaved) == "1",
		"Err":    c.Query(queryErr),
	})
}

// adminNavSave handles POST /admin/nav: the whole grid in one submit, plus
// optionally one new custom link. Field names are keyed by href — "l/<href>"
// label, "g/<href>" group, "o/<href>" ordinal, "h/<href>" hidden,
// "rm/<href>" remove (custom rows only).
func (w *web) adminNavSave(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		c.Redirect(http.StatusSeeOther, "/admin/nav?"+queryErr+"=could+not+read+the+form")
		return
	}
	ctx := c.Request.Context()
	rows, err := w.data.ListSiteNav(ctx)
	if err != nil {
		c.Redirect(http.StatusSeeOther, "/admin/nav?"+queryErr+"=could+not+read+the+menu")
		return
	}

	// The new custom link first, so a bad one refuses before anything moves.
	if href := strings.TrimSpace(c.PostForm("new_href")); href != "" {
		if !validNavHref(href) {
			c.Redirect(http.StatusSeeOther, "/admin/nav?"+queryErr+"=links+are+local+paths+like+%2Fpages%2Fprivacy+or+http(s)+URLs")
			return
		}
		label := strings.TrimSpace(c.PostForm("new_label"))
		if label == "" {
			label = href
		}
		grp := c.PostForm("new_grp")
		if !validNavGroup(grp) {
			grp = "other"
		}
		if err := w.data.EnsureSiteNav(ctx, storage.NavEntry{
			Href: href, Label: label, Grp: grp, Ordinal: 1000,
		}); err != nil {
			w.log.Error("nav add", "err", err)
			c.Redirect(http.StatusSeeOther, "/admin/nav?"+queryErr+"=could+not+add+the+link")
			return
		}
	}

	// Then every existing row the form carries.
	form := c.Request.PostForm
	for _, r := range rows {
		if !r.Builtin && form.Get("rm/"+r.Href) != "" {
			if err := w.data.DeleteSiteNav(ctx, r.Href); err != nil {
				w.log.Error("nav delete", "href", r.Href, "err", err)
			}
			continue
		}
		if _, sent := form["g/"+r.Href]; !sent {
			continue // not on the form (stale row added mid-edit): leave it
		}
		if label := strings.TrimSpace(form.Get("l/" + r.Href)); label != "" {
			r.Label = label
		}
		if g := form.Get("g/" + r.Href); validNavGroup(g) {
			r.Grp = g
		}
		if n, err := strconv.Atoi(strings.TrimSpace(form.Get("o/" + r.Href))); err == nil {
			r.Ordinal = n
		}
		r.Hidden = form.Get("h/"+r.Href) != ""
		if err := w.data.UpsertSiteNav(ctx, r); err != nil {
			w.log.Error("nav save", "href", r.Href, "err", err)
			c.Redirect(http.StatusSeeOther, "/admin/nav?"+queryErr+"=save+failed")
			return
		}
	}
	if err := refreshNavMirror(ctx, w.data); err != nil {
		w.log.Error("nav mirror refresh", "err", err)
	}
	c.Redirect(http.StatusSeeOther, "/admin/nav?"+querySaved+"=1")
}

// validNavHref accepts a local path or an absolute http(s) URL. "//host"
// is refused: it is scheme-relative and points off-site while LOOKING local.
func validNavHref(href string) bool {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return true
	}
	return strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//")
}
