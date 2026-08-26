package handlers

import (
	"html/template"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	agentplugin "github.com/the-loon-clan/loon-plugins/agent"
	"github.com/the-loon-clan/loon-plugins/pluginapi"
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
	// Feature is the core.Feature key this entry belongs to, empty for most.
	// Carried rather than resolved at build time because the bar is built ONCE
	// at boot and a feature toggle has to move it on the next request.
	Feature string
}

// wireViews mounts every registered view and stores the lookup tables the
// render path needs. Called once after core.Boot.
func (w *web) wireViews(c *core.Core, engine *gin.Engine, admin *gin.RouterGroup) {
	// AllViews, not Views, everywhere in this function — and the distinction is
	// the whole of how a runtime toggle works. Views hides anything whose
	// feature is off, and this runs ONCE at boot: a feature switched off at
	// boot would never have its route mounted, and switching it back on could
	// then do nothing until a restart. So everything is mounted, and the
	// feature is consulted per request instead — in the nav filters below, and
	// in the gate wrapped around each handler.
	w.settingsViews = c.AllViews(core.SlotAdminSettings)
	w.sitePages = c.AllViews(core.SlotSitePage)
	// The agent plugin's member page, when it ships one: the "My Agents" entry
	// on the account bar points here (sectionnav_web.go, agentsMemberHref).
	// Detected rather than assumed so the entry cannot exist before the page
	// does — the pin may lag the plugin, and a menu entry that 404s lands on
	// somebody an error message just sent there.
	for _, v := range w.sitePages {
		if v.Slug == "agents" {
			agentsMemberHref = "/p/" + v.Slug
			break
		}
	}
	w.siteWidgets = c.AllViews(core.SlotSiteWidget)
	w.userWidgets = c.AllViews(core.SlotUserWidget) // /u/<name> profile cards
	w.userTabs = c.AllViews(core.SlotUserTab)       // /u/<name> profile panels
	w.jobsWidgets = map[string]core.View{}
	for _, v := range c.AllViews(core.SlotJobsWidget) {
		w.jobsWidgets[v.Anchor] = v
	}

	// The staff dashboard at /admin — the root the whole admin group lacked.
	w.mountDashboard(admin)

	// Admin subnav: Dashboard, Settings, each admin page, then the host's own.
	w.adminNav = []navItem{
		{Href: "/admin", Label: "Dashboard"},
		{Href: "/admin/settings", Label: "Settings"},
	}
	for _, v := range c.AllViews(core.SlotAdminPage) {
		w.adminNav = append(w.adminNav, navItem{
			Href: "/admin/p/" + v.Slug, Label: v.Title, Feature: v.Feature,
		})
	}
	// Plugin admin surfaces that are a route GROUP rather than a single view.
	//
	// SlotAdminPage mounts one GET plus POST actions, which is the whole of a
	// plugin whose admin surface is one page. It is not enough for wiki (topic
	// and post editors), donations (costs, log, points) or news (an edit page),
	// so those mount their own groups — and until this loop existed they were
	// served and named in no nav at all. Seventeen admin routes were reachable
	// only by knowing the URL; see scripts/audit_adminnav.py, which fails if
	// that comes back.
	//
	// A contributed entry is a LINK and nothing else: the route is already
	// mounted and already gated by whatever mounted it, so appearing here
	// cannot widen anything.
	for _, e := range pluginapi.AdminNavEntries(c) {
		w.adminNav = append(w.adminNav, navItem{
			Href: e.Href, Label: e.Label, Feature: e.Feature,
		})
	}

	w.adminNav = append(w.adminNav,
		// The moderation queues are not /admin routes (they gate at RoleMod and
		// RoleAdmin respectively), but this is where staff look for them — and
		// since the account dropdown now carries one door instead of a list
		// (docs/NAVIGATION.md), this bar is the only place they are named.
		navItem{Href: "/moderation/avatars", Label: "New avatars"},
		navItem{Href: "/moderation", Label: "Community"},
		navItem{Href: "/admin/access", Label: "Access"},
		navItem{Href: "/admin/covers", Label: "Cover art"},
		navItem{Href: "/admin/invites", Label: "Invite chain"},
		navItem{Href: "/admin/contracts", Label: "Contracts"},
		navItem{Href: "/admin/pages", Label: "Pages"},
		navItem{Href: "/admin/nav", Label: "Navigation"},
		navItem{Href: "/admin/i18n", Label: "Localization"},
		navItem{Href: "/admin/widgets", Label: "Widgets"},
		navItem{Href: "/admin/jobs", Label: "Jobs"},
		navItem{Href: "/admin/tv-gaps", Label: "Missing episodes"},
		navItem{Href: "/admin/trackers", Label: "Trackers"},
		navItem{Href: "/admin/tracker-keys", Label: "Tracker keys"},
		navItem{Href: "/admin/agents", Label: "Agents"},
		// Both of these shipped without a link and were reachable only by URL —
		// features on 20 Aug with the runtime toggles, metrics the same day
		// with /metrics. A page nobody can find is a page nobody uses.
		navItem{Href: "/admin/features", Label: "Features"},
		navItem{Href: "/admin/metrics", Label: "Metrics"},
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

	// Standalone admin pages. Each carries the feature gate in front of its
	// handler, because the route is mounted whether the feature is on or not.
	for _, v := range c.AllViews(core.SlotAdminPage) {
		v := v
		admin.GET("/p/"+v.Slug, w.viewFeatureGate(v.Feature), w.viewPage(v))
		for name, fn := range v.Actions {
			admin.POST("/p/"+v.Slug+"/"+name, w.viewFeatureGate(v.Feature), w.viewAction(v, fn))
		}
	}

	// Public-facing pages, gated per view (Public / MinRole) and per feature.
	for _, v := range w.sitePages {
		v := v
		engine.GET("/p/"+v.Slug, w.viewFeatureGate(v.Feature), w.sitePage(v))
		for name, fn := range v.Actions {
			engine.POST("/p/"+v.Slug+"/"+name, w.viewFeatureGate(v.Feature), w.siteAction(v, fn))
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
		href, label, group := "/p/"+v.Slug, v.Title, v.Nav.Group
		// A host override of the plugin's own placement. Applied HERE, before
		// the sort, so a re-homed page is grouped and ordered like any other
		// rather than being special-cased at render time.
		if p, ok := navPlacement[href]; ok {
			group, label = p.Group, p.Label
		}
		w.siteNavEntries = append(w.siteNavEntries, siteNavEntry{
			href: href, label: label,
			group: group, weight: v.Nav.Weight, view: v,
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

// viewLive answers the per-request question about a plugin page: may this
// viewer see it, and is its feature on?
//
// Both, in one place, because the nav is built once at boot and filtered on
// every render — so anything that can change between those two moments has to
// be asked here or it is never asked at all.
func (w *web) viewLive(v core.View, u *core.User) bool {
	return v.AllowsUser(u) && core.FeatureOn(w.registry(), v.Feature)
}

// navNode is one rendered top-level nav item: a plain link (Children nil) or a
// dropdown (Children set, Href "").
type navNode struct {
	Label    string
	Href     string
	Children []navItem
}

// agentEntitlementKey is the access decision behind every fleet surface: may
// this member run agents at all?
//
// An ENTITLEMENT rather than a role check or a group read, because that is the
// split loon draws and this is the first place the host has needed it. Groups
// answer "what badge do I draw"; core.Entitlements answers "may this user do
// X", and pluginapi/groups.go is explicit that a badge consumer must never
// start making authorization decisions. So the reader here asks one boolean and
// knows nothing about who grants it: the host's own role baseline (admins, see
// main.go) or any group the ranks plugin has marked with this key, ORed
// together by core.
// Taken from the PLUGIN rather than spelled again here. Two copies of a
// registry key are two things to keep in step, and the failure when they drift
// is silent in the worst direction: the host would grant "agent.use" while the
// plugin asked about something else, so every gate would answer no and the
// feature would simply be gone with nothing logging why.
const agentEntitlementKey = agentplugin.EntitlementKey

// viewEntitlement gates a plugin view on an entitlement its own declaration
// cannot express. core.View has Public and MinRole -- a ladder -- and "members
// of the uploader group" is not a rung on it.
//
// Keyed by SLUG, and the agent plugin's three views share one prefix by
// design: the member page and the profile card are both the member's own fleet
// and stand or fall together. The admin dispatch panel is deliberately absent:
// it is already MinRole admin, and it reports fleet-wide counts an operator
// needs whether or not they personally run an agent.
var viewEntitlement = map[string]string{
	"agents":      agentEntitlementKey, // SlotSitePage  /p/agents
	"agent-fleet": agentEntitlementKey, // SlotUserWidget profile card
}

// canView applies a view's visibility to the current request.
//
// ONE chokepoint, which is why the entitlement check belongs here rather than
// in each caller: site pages, their POST actions, profile widgets, profile
// tabs, home widgets and the sitemap all ask this question, and a gate that
// covered the page but not its actions would be a lock on a door with the
// window open.
func (w *web) canView(v core.View, c *gin.Context) bool {
	u, _ := w.auth.Current(c)
	if !v.AllowsUser(u) {
		return false
	}
	key, gated := viewEntitlement[v.Slug]
	if !gated {
		return true
	}
	// Fails CLOSED, in both directions: an anonymous viewer holds no
	// entitlements, and a host that somehow reached here without the service
	// denies rather than waving everyone through.
	if u == nil || w.ents == nil {
		return false
	}
	return w.ents.Has(c.Request.Context(), u.ID, key)
}

// hostNavGroups are the dropdowns site_chrome.html writes out itself. A plugin
// that names one of these in its NavHint is asking to live INSIDE it — the
// stats plugin says Group:"Community", and before this the bar carried two
// separate Community dropdowns, the host's and the plugin's, which reads as a
// broken menu rather than as a section.
var hostNavGroups = map[string]bool{
	"Releases": true, "Community": true, "Support": true, "Other": true,
}

// navPlacedByHost are plugin pages the HOST puts somewhere specific itself, so
// the generic nav must not bucket them a second time. Each says where it went:
//
//	/p/inbox     account menu, hand-written so it can carry its unread count
//	/p/account   account menu, Settings group
//	/p/api-key   account menu, Settings group — it configures your account, so
//	             it belongs with the other things that do, not loose at the
//	             bottom of the menu under them
//
// /p/sign-ins is NOT here because it is no longer registered at all: staff keep
// the full login log at /admin/p/login-log, and the member-facing copy showed a
// hashed fingerprint no member could act on. See main.go.
var navPlacedByHost = map[string]bool{
	"/p/inbox":   true,
	"/p/account": true,
	"/p/api-key": true,
	// account bar, Settings group. Appearance is the other half of "About
	// you" -- your words and your look -- and Download reports carries your
	// own key for a downloader to use, which is the same shape as API key
	// directly above it.
	"/p/cosmetics": true,
	"/p/downloads": true,
	// account menu, Activity group — your medal cabinet. Re-homed out of the
	// plugin's Community hint by navPlacement below; this is the half that
	// stops it appearing a second time as a loose site link.
	"/p/medals": true,
	// account bar, "My Agents" (sectionnav_web.go, agentsMemberHref). Listed
	// here AHEAD of the plugin shipping the page: the entry itself is gated on
	// the view being registered, and this line only stops the generic nav
	// adding a second copy on the day it is.
	"/p/agents": true,
}

// navPlacement re-homes and re-labels a plugin page whose own NavHint puts it
// somewhere this site does not want it. The plugin still owns the page; the
// host owns where it is advertised and what the menu calls it.
//
// /p/store is the POINTSTORE plugin's shop and /store is the STORE plugin's:
// two unrelated shops whose routes differ by two characters, so the menu has to
// do the telling-apart that the URLs do not.
//
// They are named for their CURRENCY, which is the difference a buyer acts on:
//
//	Store          /p/store   cosmetic flair
//	Points store   /store     invites and ranks, spent from your balance
//
// Today both debit points, so the pair reads as a promise the site does not yet
// keep. It is the naming the split in docs/FLAIR-PAYMENTS.md arrives at — flair
// bought outright in USD or crypto, standing kept on points — and renaming the
// menu twice teaches the reader the word twice. The other half of the pair is
// labelled in sectionnav_web.go, under Bonus Points.
//
// Other rather than Community: Community is where other people are — forums,
// groups, news. A shop is somewhere you go, not something you do with members,
// and Other is where the site's remaining destinations already live.
//
// Not made a tab in the points area, which was the other candidate: the view
// registry mounts /p/<slug> in core and the host does not wrap those pages, so
// the strip cannot be rendered on it. The tab would have led to a dead end.
var navPlacement = map[string]struct{ Group, Label string }{
	"/p/store": {Group: "Other", Label: "Store"},

	// The stats plugin asks for Community (NavHint in its views.go) and this
	// overrides it, for the same reason /p/store is here: two pages about the
	// same subject were in two different menus under names a reader could not
	// tell apart.
	//
	// /stats — the host's — is already in Other: a hub of releases, groups,
	// categories, members and forum counts, public, linking on to the pages
	// that answer each. The plugin's is a periodic SNAPSHOT: a flat
	// Label/Value list with a captured-at time, logged-in only. Neither is
	// redundant, but "Stats" in Other and "Site stats" in Community read as
	// the same thing filed twice.
	//
	// Both in Other, named for what they actually are, so the difference is
	// visible in one menu instead of hidden across two.
	"/p/stats": {Group: "Other", Label: "Site snapshot"},

	// Medals asks for Community and is answered with NO group at all, which
	// hands it to the ungrouped branch of siteNav — where navPlacedByHost
	// above catches it, because the host writes it into the account menu by
	// hand (sectionnav_web.go, Activity).
	//
	// Same correction the store needed, for the same reason: your cabinet of
	// medals and which ones you wear is as personal as your balance, and it
	// sat in Community because a shop sounds communal. A member looking for
	// their own badges looks where Achievements is.
	"/p/medals": {Group: "", Label: "Medals"},

	// Both asked for Group "Account" and were answered with none, because the
	// top nav answers "where is the SITE?" and neither of these is the site's
	// -- they are the viewer's. Two of them was enough to collapse into a
	// top-level ACCOUNT dropdown sitting between Other and Donate, which is
	// docs/NAVIGATION.md's first rule broken by the generic bucketing rather
	// than by anybody deciding to.
	//
	// With no group they fall to the ungrouped branch of siteNav, where
	// navPlacedByHost below catches them: the account BAR lists them by hand,
	// under Settings, beside the other things that configure your account.
	"/p/cosmetics": {Group: "", Label: "Appearance"},
	"/p/downloads": {Group: "", Label: "Download reports"},
}

// accountPluginPages is the fallback for an UNGROUPED per-viewer plugin page
// the host has NOT placed by hand: it lands at the tail of the account menu
// rather than in the site nav's Other bucket, next to About and Sitemap.
//
// Ungrouped is the word that matters. A page that names the Account group is
// handled in siteNav's grouped branch instead — this map never saw one, which
// is how two of them built a top-nav ACCOUNT tab nobody chose.
//
// Empty today, because every registered per-viewer page is placed above. It is
// the entry the next one would need, and an empty map here is the difference
// between "nothing needs this" and "a new personal page silently appears in a
// site-info menu".
var accountPluginPages = map[string]bool{}

// accountGroupHint is the NavHint group a plugin uses to say "this page is the
// viewer's own". Matched case-insensitively, because it is a free-text field a
// plugin author types.
const accountGroupHint = "Account"

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
			case !w.viewLive(e.view, u):
			case navPlacedByHost[e.href]:
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
			if w.viewLive(ge.view, u) {
				kids = append(kids, navItem{Href: ge.href, Label: ge.label})
			}
			i++
		}
		switch {
		case len(kids) == 0:
			// nothing visible in this group
		case strings.EqualFold(e.group, accountGroupHint):
			// A plugin naming the Account group is saying the page is the
			// VIEWER'S, and the top nav answers "where is the site?" -- so the
			// hint is honoured by sending it to the account menu rather than
			// by building a tab out of it.
			//
			// This is the branch accountPluginPages was written for and never
			// reached: it only sees pages that arrive UNGROUPED, so a plugin
			// that names a group skipped straight past it. Two of them did,
			// and because siteNav collapses any group of two or more into a
			// dropdown, the second one to ask built an ACCOUNT tab in the top
			// nav between Other and Donate. One would have flattened to a
			// plain link and read as an oversight; two read as a feature.
			//
			// Pages the host has placed BY HAND are skipped, or they would
			// appear twice -- once on the bar where somebody put them, once
			// here at the tail of the menu.
			for _, k := range kids {
				if !navPlacedByHost[k.Href] {
					account = append(account, k)
				}
			}
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

// homeWidgetsExcluded are SlotSiteWidget cards the HOME page does not show.
// A site widget is a card any host page may render; home showing every one of
// them is a default, not a rule.
//
// Every entry is a card that has a PAGE of its own, which is the test: the
// slot is for a card with nowhere better to be, and none of these is that.
//
//	daily-reward   -> /calendar, the page about what you did on which day,
//	                  whose grid already plots the claims this card takes.
//	rewards-claim  -> /rewards, a third tab on the points area beside Store
//	                  and History, which is where points are spent and listed.
//	stats          -> /p/stats, the widget's own "all stats" page, which the
//	                  plugin registers and the site nav already links under
//	                  Community. The card was a five-row preview of it.
//
// Every destination is linked from chrome — the nav's claim control, the
// store's tab strip, the Community menu — so no card is further away than it
// was, and none needed a new home built for it.
//
// This is currently ALL THREE registered site widgets, so home renders no
// widget block today. The slot is kept rather than deleted because it is a
// real extension point with a real purpose: a card that belongs on the front
// page because it has no page of its own — an announcement, a notice. Nothing
// registered has been that yet.
var homeWidgetsExcluded = map[string]bool{
	dailyRewardWidget:  true,
	rewardsClaimWidget: true,
	siteStatsWidget:    true,
}

func (w *web) homeWidgets(c *gin.Context) []widgetVM {
	var out []widgetVM
	for _, v := range w.siteWidgets {
		if homeWidgetsExcluded[v.Slug] || !w.canView(v, c) {
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

// Site widgets this host renders somewhere OTHER than the home page. Named
// rather than spelled inline because each one is referred to from at least two
// places — the exclusion below and the page that adopts it — and a typo in
// either half is silent: the card simply appears in neither.
const (
	dailyRewardWidget  = "daily-reward"
	rewardsClaimWidget = "rewards-claim"
	siteStatsWidget    = "stats"
)

// hasSiteWidget reports whether a slug is registered at all, without rendering
// it. For deciding whether to OFFER a page — a nav entry or a tab — where
// rendering the card just to count it would run the plugin's query for nothing.
//
// Registration only. Whether this viewer may see it, and whether it has
// anything to say today, are questions for the render.
func (w *web) hasSiteWidget(slug string) bool {
	for _, v := range w.siteWidgets {
		if v.Slug == slug {
			return true
		}
	}
	return false
}

// siteWidget renders ONE registered site widget by slug, for a host page that
// wants a specific card rather than the whole set. Reports false when the slug
// is not registered, the viewer may not see it, or it renders nothing — all
// three are ordinary (the plugin may not be wired at all), so the caller drops
// the card rather than showing an empty panel.
func (w *web) siteWidget(c *gin.Context, slug string) (widgetVM, bool) {
	for _, v := range w.siteWidgets {
		if v.Slug != slug {
			continue
		}
		if !w.canView(v, c) {
			return widgetVM{}, false
		}
		frag, err := v.Render(c)
		if err != nil {
			w.log.Error("site widget", "slug", v.Slug, "err", err)
			return widgetVM{}, false
		}
		if frag == "" {
			return widgetVM{}, false
		}
		return widgetVM{Title: v.Title, Fragment: frag}, true
	}
	return widgetVM{}, false
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
			// Log the cause, then land the member back on the page with the
			// bare ?error=1 marker its template reads — the convention the
			// achievements and agents pages are written against ("the host
			// wrapper's bare ?error=1"). A bare 500 text response here turned
			// every refusal a plugin could not name — an agent name already
			// taken, a rotate of somebody else's id — into a white error page,
			// when the page has an error banner waiting for exactly this.
			w.log.Error("site page action", "slug", v.Slug, "err", err)
			c.Redirect(http.StatusSeeOther, "/p/"+v.Slug+"?error=1")
			return
		}
		if frag == "" {
			return
		}
		w.render(c, "site_page.html", map[string]any{"Title": v.Title, "Fragment": frag})
	}
}
