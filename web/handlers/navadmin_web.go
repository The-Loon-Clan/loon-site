package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/storage"

	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// The navigation editor — /admin/nav — and the assembly behind everything
// the chrome draws from it: the top bar's dropdown tabs AND the footer's
// link columns. Both storeys are rows now:
//
//	groups   the tabs/columns themselves — default label, an optional
//	         message-catalogue slug that localises the label per viewer,
//	         icon, order, placement (top | footer), hidden. Builtin groups
//	         hide rather than delete (a boot would re-ensure them); custom
//	         groups delete when empty.
//	entries  the links inside them — label, target, group, order, hidden.
//
// The shipped menu stays in code below, as three things at once: the seed
// for a fresh database, the reconciliation source for an upgraded one (new
// builtin KEYS are ensured in without touching operator edits), and the
// render fallback when the tables are unreadable — a broken settings table
// must never cost the site its navigation.
//
// Icons and flavour conditions stay keyed in code where they are the
// system's knowledge; a group's icon is the one exception, editable because
// a custom tab has to get its glyph from somewhere.

// navGroupDefaults is the shipped set of tabs and footer columns.
var navGroupDefaults = []storage.NavGroup{
	{Key: "releases", Label: "Releases", Icon: "browse", Ordinal: 10, Placement: "top", Builtin: true},
	{Key: "community", Label: "Community", Icon: "forums", Ordinal: 20, Placement: "top", Builtin: true},
	{Key: "support", Label: "Support", Icon: "info", Ordinal: 30, Placement: "top", Builtin: true},
	{Key: "other", Label: "Other", Icon: "folder", Ordinal: 40, Placement: "top", Builtin: true},

	{Key: "footer-index", Label: "Index", Ordinal: 110, Placement: "footer", Builtin: true},
	{Key: "footer-community", Label: "Community", Ordinal: 120, Placement: "footer", Builtin: true},
}

// navDefaults is the shipped set of entries. Keys carry the storey because
// the same href legitimately sits in both — /browse is a tab item and a
// footer link.
var navDefaults = []storage.NavEntry{
	{Key: "top:/browse", Href: "/browse", Label: "Browse", Grp: "releases", Ordinal: 10, Builtin: true},
	// Beside Browse rather than under it: the two are different questions of
	// the same index — "what is new" and "what do you have of this show".
	{Key: "top:/series", Href: "/series", Label: "Series", Grp: "releases", Ordinal: 15, Builtin: true},
	{Key: "top:/search", Href: "/search", Label: "Search", Grp: "releases", Ordinal: 20, Builtin: true},
	{Key: "top:/groups", Href: "/groups", Label: "Newsgroups", Grp: "releases", Ordinal: 30, Builtin: true},
	{Key: "top:/trending", Href: "/trending", Label: "Trending", Grp: "releases", Ordinal: 40, Builtin: true},
	{Key: "top:/tracker", Href: "/tracker", Label: "Torrents", Grp: "releases", Ordinal: 50, Builtin: true},
	{Key: "top:/api", Href: "/api", Label: "Newznab API", Grp: "releases", Ordinal: 60, Builtin: true},

	{Key: "top:/community/forums", Href: "/community/forums", Label: "Forums", Grp: "community", Ordinal: 10, Builtin: true},
	{Key: "top:/c", Href: "/c", Label: "Communities", Grp: "community", Ordinal: 20, Builtin: true},
	{Key: "top:/playlists", Href: "/playlists", Label: "Playlists", Grp: "community", Ordinal: 30, Builtin: true},

	{Key: "top:/news", Href: "/news", Label: "News", Grp: "support", Ordinal: 10, Builtin: true},
	{Key: "top:/rules", Href: "/rules", Label: "Rules", Grp: "support", Ordinal: 20, Builtin: true},
	{Key: "top:/faq", Href: "/faq", Label: "FAQ", Grp: "support", Ordinal: 30, Builtin: true},
	{Key: "top:/wiki", Href: "/wiki", Label: "Wiki", Grp: "support", Ordinal: 40, Builtin: true},
	{Key: "top:/support", Href: "/support", Label: "Helpdesk", Grp: "support", Ordinal: 50, Builtin: true},
	{Key: "top:/staff", Href: "/staff", Label: "Staff", Grp: "support", Ordinal: 60, Builtin: true},

	{Key: "top:/stats", Href: "/stats", Label: "Stats", Grp: "other", Ordinal: 10, Builtin: true},
	{Key: "top:/about", Href: "/about", Label: "About", Grp: "other", Ordinal: 20, Builtin: true},
	{Key: "top:/sitemap", Href: "/sitemap", Label: "Sitemap", Grp: "other", Ordinal: 30, Builtin: true},

	{Key: "footer:/browse", Href: "/browse", Label: "Browse", Grp: "footer-index", Ordinal: 10, Builtin: true},
	{Key: "footer:/search", Href: "/search", Label: "Search", Grp: "footer-index", Ordinal: 20, Builtin: true},
	{Key: "footer:/groups", Href: "/groups", Label: "Groups", Grp: "footer-index", Ordinal: 30, Builtin: true},
	// /sitemap, not /sitemap.xml — this column is pages a person reads, and
	// the XML link it once carried delivered raw feed to a human.
	{Key: "footer:/sitemap", Href: "/sitemap", Label: "Sitemap", Grp: "footer-index", Ordinal: 40, Builtin: true},

	{Key: "footer:/community/forums", Href: "/community/forums", Label: "Forums", Grp: "footer-community", Ordinal: 10, Builtin: true},
	{Key: "footer:/community/forums/new", Href: "/community/forums/new", Label: "Start a thread", Grp: "footer-community", Ordinal: 20, Builtin: true},
}

// navIcons maps entry hrefs to sprite ids — the system's knowledge of what
// glyph fits a shipped page. A custom link gets the tag glyph.
var navIcons = map[string]string{
	"/browse": "browse", "/series": "tv", "/search": "search", "/groups": "groups",
	"/trending": "database", "/tracker": "download", "/api": "code",
	"/community/forums": "forums", "/c": "users", "/playlists": "music",
	"/news": "rss", "/rules": "shield", "/faq": "info", "/wiki": "book",
	"/support": "comment", "/staff": "shield",
	"/stats": "database", "/about": "logo", "/sitemap": "globe",
}

// navConditions are the flavour gates. An operator's "hidden" and a failing
// condition both remove an entry; the difference is who decided.
var navConditions = map[string]func() bool{
	"/groups": flavourIndexer,
	// /series reads the NZB index, so a pure-tracker site has no series pages
	// to offer — the same gate /groups and /api sit behind.
	"/series":  flavourIndexer,
	"/api":     flavourIndexer,
	"/tracker": flavourTracker,
}

// navPluginGroups maps builtin TOP group keys to the NavHint group names
// plugins register under (core.View.Nav) — how a plugin page's request to
// sit in "Community" finds the community tab even after it is renamed.
var navPluginGroups = map[string]string{
	"releases": "Releases", "community": "Community",
	"support": "Support", "other": "Other",
}

// navState is what the mirror holds: both tables, read together at boot and
// after every save so the chrome never queries per request.
type navState struct {
	Groups  []storage.NavGroup
	Entries []storage.NavEntry
}

var navMirror atomic.Value // navState

// loadSiteNav ensures every builtin default exists, then mirrors both
// tables.
func loadSiteNav(ctx context.Context, data *storage.Store) error {
	for _, g := range navGroupDefaults {
		if err := data.EnsureSiteNavGroup(ctx, g); err != nil {
			return err
		}
	}
	for _, d := range navDefaults {
		if err := data.EnsureSiteNavKeyed(ctx, d); err != nil {
			return err
		}
	}
	return refreshNavMirror(ctx, data)
}

func refreshNavMirror(ctx context.Context, data *storage.Store) error {
	groups, err := data.ListSiteNavGroups(ctx)
	if err != nil {
		return err
	}
	entries, err := data.ListSiteNav(ctx)
	if err != nil {
		return err
	}
	navMirror.Store(navState{Groups: groups, Entries: entries})
	return nil
}

// currentNav returns the mirror, or the shipped defaults when it is empty.
func currentNav() navState {
	st, _ := navMirror.Load().(navState)
	if len(st.Groups) == 0 || len(st.Entries) == 0 {
		return navState{Groups: navGroupDefaults, Entries: navDefaults}
	}
	return st
}

// chromeNavItem is one link as the chrome renders it.
type chromeNavItem struct {
	Label  string
	Href   string
	Icon   string
	Active bool
}

// navTabVM is one top-bar dropdown, assembled: localised label, icon,
// items (operator rows plus plugin-merged pages), active state, and whether
// the menu anchors to its right edge (the bar's right half does, so a menu
// near the viewport edge does not overflow it).
type navTabVM struct {
	Key    string
	Label  string
	Icon   string
	Active bool
	End    bool
	Items  []chromeNavItem
	// PluginItems render through the shared nav-plugin-items partial, which
	// owns their active markup.
	PluginItems []navItem
}

// footerColVM is one footer column.
type footerColVM struct {
	Label string
	Items []chromeNavItem
}

// assembleNav builds both storeys for one request. Pure in everything but
// its inputs: rows from the mirror, the request path, the viewer's label
// resolver (nil = no catalogue), and the plugin pages that asked to sit in
// a host group.
func assembleNav(st navState, path string, resolve func(string) (string, bool),
	pluginGroups map[string][]navItem, ungrouped []navItem) (tabs []navTabVM, footer []footerColVM) {

	// Entries first, bucketed by group key.
	items := map[string][]chromeNavItem{}
	active := map[string]bool{}
	rescueKey := ""
	for _, g := range st.Groups {
		if g.Key == "other" {
			rescueKey = "other"
		}
	}
	known := map[string]bool{}
	for _, g := range st.Groups {
		known[g.Key] = true
	}
	for _, e := range st.Entries {
		if e.Hidden {
			continue
		}
		if cond, ok := navConditions[e.Href]; ok && !cond() {
			continue
		}
		grp := e.Grp
		if !known[grp] {
			// A row whose group was deleted must not vanish silently; it
			// surfaces under Other when Other still exists.
			if rescueKey == "" {
				continue
			}
			grp = rescueKey
		}
		icon := navIcons[e.Href]
		if icon == "" {
			icon = "tag"
		}
		on := navActive(path, e.Href)
		items[grp] = append(items[grp], chromeNavItem{Label: e.Label, Href: e.Href, Icon: icon, Active: on})
		if on {
			active[grp] = true
		}
	}

	// Then the groups, in operator order.
	groups := append([]storage.NavGroup{}, st.Groups...)
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Ordinal < groups[j].Ordinal })

	var topCount int
	for _, g := range groups {
		if g.Placement != "footer" {
			topCount++
		}
	}
	topSeen := 0
	for _, g := range groups {
		if g.Hidden {
			continue
		}
		label := g.Label
		if g.LabelSlug != "" && resolve != nil {
			if t, ok := resolve(g.LabelSlug); ok && t != "" {
				label = t
			}
		}
		if g.Placement == "footer" {
			if len(items[g.Key]) == 0 {
				continue
			}
			footer = append(footer, footerColVM{Label: label, Items: items[g.Key]})
			continue
		}
		topSeen++
		tab := navTabVM{
			Key: g.Key, Label: label, Icon: g.Icon,
			Items:       items[g.Key],
			PluginItems: pluginGroups[navPluginGroups[g.Key]],
			// The right half of the bar anchors menus to their right edge.
			End: topSeen > topCount/2,
		}
		if tab.Icon == "" {
			tab.Icon = "folder"
		}
		// Ungrouped plugin site pages keep their home under Other.
		if g.Key == "other" {
			for _, it := range ungrouped {
				tab.Items = append(tab.Items, chromeNavItem{
					Label: it.Label, Href: it.Href, Icon: "tag",
					Active: navActive(path, it.Href),
				})
			}
		}
		tab.Active = active[g.Key]
		for _, it := range tab.Items {
			if it.Active {
				tab.Active = true
			}
		}
		for _, it := range tab.PluginItems {
			if navActive(path, it.Href) {
				tab.Active = true
			}
		}
		// The release detail pages belong to Releases but no row carries
		// their prefix — the one special case the old literals encoded.
		if g.Key == "releases" && strings.HasPrefix(path, "/release/") {
			tab.Active = true
		}
		// A tab with nothing in it renders as a dead dropdown; skip it.
		if len(tab.Items) == 0 && len(tab.PluginItems) == 0 {
			continue
		}
		tabs = append(tabs, tab)
	}
	return tabs, footer
}

// navGroupExists reports whether a group key is real, for form validation.
func navGroupExists(key string) bool {
	for _, g := range currentNav().Groups {
		if g.Key == key {
			return true
		}
	}
	return false
}

// ── the editor ──────────────────────────────────────────────────────────

var navGroupKeyPattern = sitePageSlugPattern // same vocabulary: lowercase, dashes

// adminNavEditor serves GET /admin/nav.
func (w *web) adminNavEditor(c *gin.Context) {
	ctx := c.Request.Context()
	groups, err := w.data.ListSiteNavGroups(ctx)
	if err != nil {
		c.String(http.StatusInternalServerError, "could not read the menus")
		return
	}
	entries, err := w.data.ListSiteNav(ctx)
	if err != nil {
		c.String(http.StatusInternalServerError, "could not read the menu entries")
		return
	}
	byGroup := map[string][]storage.NavEntry{}
	for _, e := range entries {
		byGroup[e.Grp] = append(byGroup[e.Grp], e)
	}
	type groupVM struct {
		storage.NavGroup
		Rows []storage.NavEntry
		// Deletable: custom and empty — a group with entries would strand
		// them, and a builtin would be re-ensured at the next boot anyway.
		Deletable bool
	}
	var vms []groupVM
	for _, g := range groups {
		vms = append(vms, groupVM{NavGroup: g, Rows: byGroup[g.Key],
			Deletable: !g.Builtin && len(byGroup[g.Key]) == 0})
	}
	// The localization slugs, for the label-slug dropdowns — best effort,
	// same as the achievements form: no catalogue degrades to free text.
	slugs, _ := w.data.I18nSlugs(ctx)
	w.render(c, "admin_nav.html", map[string]any{
		"Title":     "Navigation",
		"Groups":    vms,
		"L10nSlugs": slugs,
		"Saved":     c.Query(querySaved) == "1",
		"Err":       c.Query(queryErr),
	})
}

// adminNavSave handles POST /admin/nav: both storeys of the grid in one
// submit, plus optionally one new group and one new link.
//
// Field names — groups by key, entries by id:
//
//	gl/<key> gs/<key> gi/<key> go/<key> gp/<key> gh/<key> grm/<key>
//	l/<id> g/<id> o/<id> h/<id> rm/<id>
func (w *web) adminNavSave(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		c.Redirect(http.StatusSeeOther, "/admin/nav?"+queryErr+"=could+not+read+the+form")
		return
	}
	ctx := c.Request.Context()
	form := c.Request.PostForm
	groups, err := w.data.ListSiteNavGroups(ctx)
	if err != nil {
		c.Redirect(http.StatusSeeOther, "/admin/nav?"+queryErr+"=could+not+read+the+menus")
		return
	}
	entries, err := w.data.ListSiteNav(ctx)
	if err != nil {
		c.Redirect(http.StatusSeeOther, "/admin/nav?"+queryErr+"=could+not+read+the+menu+entries")
		return
	}
	entriesByGroup := map[string]int{}
	for _, e := range entries {
		entriesByGroup[e.Grp]++
	}
	// The group set THIS save knows — the rows just read plus the one just
	// added below. Not navGroupExists: that reads the mirror, which is still
	// pre-save, and a "move into the menu I created in this same submit"
	// would be silently dropped against it.
	groupSet := map[string]bool{}
	for _, g := range groups {
		groupSet[g.Key] = true
	}

	// New group first, so a new link can name it in the same submit.
	if key := strings.TrimSpace(c.PostForm("new_group_key")); key != "" {
		if !navGroupKeyPattern.MatchString(key) || groupSet[key] {
			c.Redirect(http.StatusSeeOther, "/admin/nav?"+queryErr+"=group+keys+are+lowercase+with+dashes%2C+and+new")
			return
		}
		label := strings.TrimSpace(c.PostForm("new_group_label"))
		if label == "" {
			label = key
		}
		g := storage.NavGroup{Key: key, Label: label, Ordinal: 500, Placement: "top"}
		if c.PostForm("new_group_placement") == "footer" {
			g.Placement = "footer"
		}
		if icon := strings.TrimSpace(c.PostForm("new_group_icon")); icon != "" {
			g.Icon = icon
		}
		if err := w.data.EnsureSiteNavGroup(ctx, g); err != nil {
			w.log.Error("nav group add", "err", err)
			c.Redirect(http.StatusSeeOther, "/admin/nav?"+queryErr+"=could+not+add+the+menu")
			return
		}
		groups = append(groups, g)
		groupSet[g.Key] = true
	}

	// Group edits.
	for _, g := range groups {
		if _, sent := form["gl/"+g.Key]; !sent {
			continue
		}
		if !g.Builtin && form.Get("grm/"+g.Key) != "" {
			if entriesByGroup[g.Key] > 0 {
				c.Redirect(http.StatusSeeOther, "/admin/nav?"+queryErr+"=empty+a+menu+before+removing+it")
				return
			}
			if err := w.data.DeleteSiteNavGroup(ctx, g.Key); err != nil {
				w.log.Error("nav group delete", "err", err)
			}
			delete(groupSet, g.Key)
			continue
		}
		if label := strings.TrimSpace(form.Get("gl/" + g.Key)); label != "" {
			g.Label = label
		}
		g.LabelSlug = strings.TrimSpace(form.Get("gs/" + g.Key))
		g.Icon = strings.TrimSpace(form.Get("gi/" + g.Key))
		if n, err := strconv.Atoi(strings.TrimSpace(form.Get("go/" + g.Key))); err == nil {
			g.Ordinal = n
		}
		if p := form.Get("gp/" + g.Key); p == "top" || p == "footer" {
			g.Placement = p
		}
		g.Hidden = form.Get("gh/"+g.Key) != ""
		if err := w.data.UpsertSiteNavGroup(ctx, g); err != nil {
			w.log.Error("nav group save", "err", err)
			c.Redirect(http.StatusSeeOther, "/admin/nav?"+queryErr+"=save+failed")
			return
		}
	}

	// New link.
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
		if !groupSet[grp] {
			grp = "other"
		}
		if err := w.data.InsertSiteNav(ctx, storage.NavEntry{
			Href: href, Label: label, Grp: grp, Ordinal: 1000,
		}); err != nil {
			w.log.Error("nav add", "err", err)
			c.Redirect(http.StatusSeeOther, "/admin/nav?"+queryErr+"=could+not+add+the+link")
			return
		}
	}

	// Entry edits.
	for _, e := range entries {
		id := strconv.FormatInt(e.ID, 10)
		if _, sent := form["g/"+id]; !sent {
			continue
		}
		if !e.Builtin && form.Get("rm/"+id) != "" {
			if err := w.data.DeleteSiteNav(ctx, e.ID); err != nil {
				w.log.Error("nav delete", "href", e.Href, "err", err)
			}
			continue
		}
		if label := strings.TrimSpace(form.Get("l/" + id)); label != "" {
			e.Label = label
		}
		if g := form.Get("g/" + id); groupSet[g] {
			e.Grp = g
		}
		if n, err := strconv.Atoi(strings.TrimSpace(form.Get("o/" + id))); err == nil {
			e.Ordinal = n
		}
		e.Hidden = form.Get("h/"+id) != ""
		if err := w.data.UpdateSiteNav(ctx, e); err != nil {
			w.log.Error("nav save", "href", e.Href, "err", err)
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
