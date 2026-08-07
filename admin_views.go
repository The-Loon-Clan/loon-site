package main

import (
	"html/template"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// Host side of loon's view system: plugins render fragments, the demo wraps
// them in its chrome. Four surfaces are wired here:
//
//   /admin/settings   one page aggregating every SlotAdminSettings section
//   /admin/p/<slug>   standalone SlotAdminPage pages
//   jobs page         SlotJobsWidget fragments override a group's default table
//   /p/<slug>         SlotSitePage pages, gated by each view's Public/MinRole
//
// Plus SlotSiteWidget cards on the home page and the nav lists built from the
// registries. All generic — zero plugin-specific code.

type navItem struct {
	Href  string
	Label string
}

// wireViews mounts every registered view and stores the lookup tables the
// render path needs. Called once after core.Boot.
func (w *web) wireViews(c *core.Core, engine *gin.Engine, admin *gin.RouterGroup) {
	w.settingsViews = c.Views(core.SlotAdminSettings)
	w.sitePages = c.Views(core.SlotSitePage)
	w.siteWidgets = c.Views(core.SlotSiteWidget)
	w.userWidgets = c.Views(core.SlotUserWidget) // /u/<name> profile cards
	w.jobsWidgets = map[string]core.View{}
	for _, v := range c.Views(core.SlotJobsWidget) {
		w.jobsWidgets[v.Anchor] = v
	}

	// The staff dashboard at /admin — the root the whole admin group lacked.
	w.mountDashboard(admin)

	// Admin subnav: Dashboard, Settings, each admin page, then the host's own.
	w.adminNav = []navItem{
		{Href: "/admin", Label: "Dashboard"},
		{Href: "/admin/settings", Label: "Settings"},
	}
	for _, v := range c.Views(core.SlotAdminPage) {
		w.adminNav = append(w.adminNav, navItem{Href: "/admin/p/" + v.Slug, Label: v.Title})
	}
	w.adminNav = append(w.adminNav,
		navItem{Href: "/admin/jobs", Label: "Jobs"},
		navItem{Href: "/admin/plugins", Label: "Plugins"})

	// Settings: one PAGE per section, plus each section's actions. The bare
	// /admin/settings keeps working (it lands on the first section) because it
	// is what the admin subnav, bookmarks, and plugin redirects all point at.
	admin.GET("/settings", w.adminSettings)
	for _, v := range w.settingsViews {
		v := v
		admin.GET("/settings/"+v.Slug, w.adminSettingsSection(v.Slug))
		for name, fn := range v.Actions {
			admin.POST("/settings/"+v.Slug+"/"+name, w.settingsAction(v, fn))
		}
	}

	// Standalone admin pages.
	for _, v := range c.Views(core.SlotAdminPage) {
		v := v
		admin.GET("/p/"+v.Slug, w.viewPage(v))
		for name, fn := range v.Actions {
			admin.POST("/p/"+v.Slug+"/"+name, w.viewAction(v, fn))
		}
	}

	// Public-facing pages, gated per view (Public / MinRole).
	for _, v := range w.sitePages {
		v := v
		engine.GET("/p/"+v.Slug, w.sitePage(v))
		for name, fn := range v.Actions {
			engine.POST("/p/"+v.Slug+"/"+name, w.siteAction(v, fn))
		}
	}

	// Build the site-nav entry list ONCE, sorted by group then weight then
	// registration order (a plugin's Nav hint suggests placement — the
	// WordPress/Drupal pattern). Per request we only role-filter this, never
	// re-sort. NavHidden views are mounted above but omitted from the nav.
	w.siteNavEntries = w.siteNavEntries[:0]
	for _, v := range w.sitePages {
		if v.Nav.Menu == core.NavHidden {
			continue
		}
		w.siteNavEntries = append(w.siteNavEntries, siteNavEntry{
			href: "/p/" + v.Slug, label: v.Title,
			group: v.Nav.Group, weight: v.Nav.Weight, view: v,
		})
	}
	sort.SliceStable(w.siteNavEntries, func(i, j int) bool {
		a, b := w.siteNavEntries[i], w.siteNavEntries[j]
		if a.group != b.group {
			return a.group < b.group // "" (ungrouped) sorts first
		}
		return a.weight < b.weight
	})
}

// siteNavEntry is a site page pre-resolved for the nav (built at boot).
type siteNavEntry struct {
	href, label, group string
	weight             int
	view               core.View
}

// navNode is one rendered top-level nav item: a plain link (Children nil) or a
// dropdown (Children set, Href "").
type navNode struct {
	Label    string
	Href     string
	Children []navItem
}

// canView applies a view's visibility to the current request.
func (w *web) canView(v core.View, c *gin.Context) bool {
	u, _ := w.auth.Current(c)
	return v.AllowsUser(u)
}

// hostNavGroups are the dropdowns site_chrome.html writes out itself. A plugin
// that names one of these in its NavHint is asking to live INSIDE it — the
// stats plugin says Group:"Community", and before this the bar carried two
// separate Community dropdowns, the host's and the plugin's, which reads as a
// broken menu rather than as a section.
var hostNavGroups = map[string]bool{
	"Releases": true, "Community": true, "Support": true, "Other": true,
}

// accountPluginPages are the per-viewer PLUGIN pages — your API key, your
// sign-ins, your purchases. An ungrouped plugin page at one of these belongs on
// the account menu, not in the site nav's Other bucket where it sat next to
// About and Sitemap.
//
// An explicit list. It used to be derived from the section table's Account
// prefixes, which was free while that table also drove a section bar; with the
// bar gone the table is the account menu itself, and deriving one from the
// other would make a page appear on the menu twice. A plugin page is personal
// because of what it SHOWS, which no path pattern was really answering anyway.
var accountPluginPages = map[string]bool{
	"/p/api-key":  true,
	"/p/sign-ins": true,
	"/p/store":    true,
}

// accountMenuBuiltins are account-menu rows the menu already carries by name
// (accountMenu in sectionnav_web.go, or written into site_chrome.html) — with
// their unread count and their own icon. The generic list must not add a
// second, plainer copy.
var accountMenuBuiltins = map[string]bool{"/p/inbox": true, "/p/account": true}

// siteNav builds the top nav for the current viewer from the pre-sorted
// entries: ungrouped pages become plain links; a named group with 2+ visible
// pages collapses into a dropdown; a group with a single visible page flattens
// to a plain link (no one-item dropdowns). The user is resolved ONCE and the
// entries are already sorted, so this is a linear role-filter — nothing hot.
//
// Two side-buckets come back with the nodes: merged is the by-name merge into
// the host's own dropdowns (entries whose group is one of hostNavGroups never
// become a top-level node), and account is the per-viewer pages above.
func (w *web) siteNav(c *gin.Context) ([]navNode, map[string][]navItem, []navItem) {
	u, _ := w.auth.Current(c)
	var nodes []navNode
	var account []navItem
	merged := map[string][]navItem{}
	for i := 0; i < len(w.siteNavEntries); {
		e := w.siteNavEntries[i]
		if e.group == "" {
			switch {
			case !e.view.AllowsUser(u):
			case accountMenuBuiltins[e.href]:
				// already on the account menu, written by hand
			case accountPluginPages[e.href]:
				account = append(account, navItem{Href: e.href, Label: e.label})
			default:
				nodes = append(nodes, navNode{Label: e.label, Href: e.href})
			}
			i++
			continue
		}
		// entries are contiguous by group — gather the run, keep visible ones
		var kids []navItem
		for i < len(w.siteNavEntries) && w.siteNavEntries[i].group == e.group {
			ge := w.siteNavEntries[i]
			if ge.view.AllowsUser(u) {
				kids = append(kids, navItem{Href: ge.href, Label: ge.label})
			}
			i++
		}
		switch {
		case len(kids) == 0:
			// nothing visible in this group
		case hostNavGroups[e.group]:
			// Even a single entry merges: the whole point is that it appears
			// under the host menu it named, not beside it.
			merged[e.group] = append(merged[e.group], kids...)
		case len(kids) == 1:
			nodes = append(nodes, navNode{Label: kids[0].Label, Href: kids[0].Href})
		default:
			nodes = append(nodes, navNode{Label: e.group, Children: kids})
		}
	}
	return nodes, merged, account
}

// homeWidgets renders the site widgets the current viewer may see.
type widgetVM struct {
	Title    string
	Fragment template.HTML
}

func (w *web) homeWidgets(c *gin.Context) []widgetVM {
	var out []widgetVM
	for _, v := range w.siteWidgets {
		if !w.canView(v, c) {
			continue
		}
		frag, err := v.Render(c)
		if err != nil {
			w.log.Error("site widget", "slug", v.Slug, "err", err)
			continue
		}
		out = append(out, widgetVM{Title: v.Title, Fragment: frag})
	}
	return out
}

// ── /admin/settings (aggregated sections) ───────────────────────────

type settingsSection struct {
	Slug     string
	Title    string
	Fragment template.HTML
}

// settingsTab is one entry in the settings strip. Each is a real page rather
// than an in-page anchor: a section is a whole plugin fragment (its own tables,
// forms, and sometimes its own <script>), so stacking every one of them behind
// jump links turned Settings into a thing you scrolled instead of a thing you
// navigated — and a section's action re-rendered all the others with it.
type settingsTab struct {
	Href   string
	Label  string
	Active bool
}

func (w *web) adminSettings(c *gin.Context) {
	w.renderSettingsPage(c, "", nil)
}

func (w *web) adminSettingsSection(slug string) gin.HandlerFunc {
	return func(c *gin.Context) { w.renderSettingsPage(c, slug, nil) }
}

// settingsView resolves the section a request is for: the named slug, else the
// first registered section. Falling back rather than 404ing is what keeps the
// bare /admin/settings meaningful, and it survives a plugin being removed while
// a bookmark still points at its tab.
func (w *web) settingsView(slug string) (core.View, bool) {
	for _, v := range w.settingsViews {
		if v.Slug == slug {
			return v, true
		}
	}
	if len(w.settingsViews) > 0 {
		return w.settingsViews[0], true
	}
	return core.View{}, false
}

// renderSettingsPage renders ONE settings section plus the strip that switches
// between them. override is non-nil only when an action just produced a
// form-preserving fragment for this same section.
func (w *web) renderSettingsPage(c *gin.Context, slug string, override *template.HTML) {
	cur, ok := w.settingsView(slug)
	tabs := make([]settingsTab, 0, len(w.settingsViews))
	for _, v := range w.settingsViews {
		tabs = append(tabs, settingsTab{
			Href:   "/admin/settings/" + v.Slug,
			Label:  v.Title,
			Active: ok && v.Slug == cur.Slug,
		})
	}
	data := map[string]any{"Title": "Settings", "Tabs": tabs}
	if ok {
		var frag template.HTML
		switch {
		case override != nil:
			frag = *override
		default:
			f, err := cur.Render(c)
			if err != nil {
				w.log.Error("settings section", "slug", cur.Slug, "err", err)
				f = template.HTML(`<div class="alert">section failed to render — see logs</div>`)
			}
			frag = f
		}
		data["Section"] = &settingsSection{Slug: cur.Slug, Title: cur.Title, Fragment: frag}
	}
	w.render(c, "admin_settings.html", data)
}

func (w *web) settingsAction(v core.View, fn func(*gin.Context) (template.HTML, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		frag, err := fn(c)
		if err != nil {
			w.log.Error("settings action", "slug", v.Slug, "err", err)
			c.String(http.StatusInternalServerError, "action on %s failed", v.Slug)
			return
		}
		if frag == "" {
			return // action already responded (redirect)
		}
		w.renderSettingsPage(c, v.Slug, &frag)
	}
}

// ── /admin/p/<slug> (standalone admin pages) ────────────────────────

func (w *web) viewPage(v core.View) gin.HandlerFunc {
	return func(c *gin.Context) {
		frag, err := v.Render(c)
		if err != nil {
			w.log.Error("admin view render", "slug", v.Slug, "err", err)
			c.String(http.StatusInternalServerError, "view %s failed", v.Slug)
			return
		}
		w.render(c, "admin_view.html", map[string]any{"Title": v.Title, "Fragment": frag})
	}
}

func (w *web) viewAction(v core.View, fn func(*gin.Context) (template.HTML, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		frag, err := fn(c)
		if err != nil {
			w.log.Error("admin view action", "slug", v.Slug, "err", err)
			c.String(http.StatusInternalServerError, "action on %s failed", v.Slug)
			return
		}
		if frag == "" {
			return
		}
		w.render(c, "admin_view.html", map[string]any{"Title": v.Title, "Fragment": frag})
	}
}

// ── /p/<slug> (public-facing pages, per-view visibility) ────────────

func (w *web) siteGate(v core.View, c *gin.Context) bool {
	if w.canView(v, c) {
		return true
	}
	if _, loggedIn := w.auth.Current(c); !loggedIn && strings.Contains(c.GetHeader("Accept"), "text/html") {
		c.Redirect(http.StatusSeeOther, "/login")
		c.Abort()
		return false
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"ok": false, "error": "insufficient role"})
	return false
}

func (w *web) sitePage(v core.View) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !w.siteGate(v, c) {
			return
		}
		frag, err := v.Render(c)
		if err != nil {
			w.log.Error("site page render", "slug", v.Slug, "err", err)
			c.String(http.StatusInternalServerError, "page %s failed", v.Slug)
			return
		}
		w.render(c, "site_page.html", map[string]any{"Title": v.Title, "Fragment": frag})
	}
}

func (w *web) siteAction(v core.View, fn func(*gin.Context) (template.HTML, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !w.siteGate(v, c) {
			return
		}
		frag, err := fn(c)
		if err != nil {
			w.log.Error("site page action", "slug", v.Slug, "err", err)
			c.String(http.StatusInternalServerError, "action on %s failed", v.Slug)
			return
		}
		if frag == "" {
			return
		}
		w.render(c, "site_page.html", map[string]any{"Title": v.Title, "Fragment": frag})
	}
}
