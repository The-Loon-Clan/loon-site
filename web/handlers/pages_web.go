package handlers

import (
	"context"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// Site pages + the stats hub — UNIT3D's page/* and stats/index areas.
//
// These are HOST pages, not plugin surfaces: the content is about this
// deployment, so there is no plugin that could own it. The prose pages are
// UNIT3D's admin-editable `pages` table now (pagesadmin_web.go) — with the
// difference that the shipped templates stay as the fallback, so a fresh
// database serves the curated prose unchanged and an operator's saved copy
// replaces it per page. NOT the wiki, deliberately: the wiki is member-facing
// reference with topics and recent-changes, and "Rules" does not belong in
// its index between articles.
//
// Nothing here is mocked. /staff is real users read by role, /stats is the same
// capability figures the home strip uses, and the prose pages describe this
// demo truthfully.

// staffMember is one row on /staff.
type staffMember struct {
	Username string
	Role     core.Role
	RoleName string
	// Since is the account's creation date — the closest honest thing this
	// stack has to UNIT3D's "staff since", which it does not record.
	Since string
	// Avatar is users.avatar_path; empty renders as the initials tile.
	Avatar string
}

// staffGroup is one role's worth of members. UNIT3D groups by its editable
// `groups` table; roles are a fixed enum here, so the grouping is by role and
// the order is by authority, highest first.
type staffGroup struct {
	Name    string
	Members []staffMember
}

// staffPage lists everyone at RoleMod or above.
//
// Reads the user store directly rather than through a capability because no
// plugin owns "who is staff" — it is a property of the host's own users table.
// Bounded by design: a site's staff is a short list, and the query is capped
// regardless so this can never become the unbounded scan ListUsers exists to
// avoid.
func (w *web) staffPage(c *gin.Context) {
	// EditHref on the FALLBACK too, so an admin reading the page it shipped with
	// can reach the editor from it — the same affordance the other prose pages
	// have. Without it the only route to editing /staff is knowing that
	// /admin/pages lists it, which is not a route anybody finds.
	data := map[string]any{"Title": "Staff", "EditHref": "/admin/pages?edit=staff"}
	if groups, err := w.staffGroups(c.Request.Context()); err == nil && len(groups) > 0 {
		data["Groups"] = groups
	}
	w.render(c, "staff.html", data)
}

// staffFloorRole reads the staff widget's config: one role slug meaning "this
// role and above". Blank is the staff floor, which is the same test the rest of
// the site's chrome uses for IsMod.
//
// Roles BELOW contributor are refused rather than supported. "Everyone from
// member up" is the whole membership, which is a directory and not a staff
// list, and a widget that would happily render one on a public page is a
// mistake waiting for a typo.
func staffFloorRole(cfg string) (core.Role, bool) {
	switch strings.ToLower(strings.TrimSpace(cfg)) {
	case "", "staff", "mod", "moderator":
		return core.RoleMod, true
	case "admin":
		return core.RoleAdmin, true
	case "contributor":
		return core.RoleContributor, true
	}
	return 0, false
}

func (w *web) staffGroups(ctx context.Context) ([]staffGroup, error) {
	return w.staffGroupsFrom(ctx, core.RoleMod)
}

func (w *web) staffGroupsFrom(ctx context.Context, floor core.Role) ([]staffGroup, error) {
	type row struct {
		Username  string `db:"username"`
		Role      int    `db:"role"`
		CreatedAt string `db:"created_at"`
		Avatar    string `db:"avatar_path"`
	}
	var rows []row
	// A FLOOR, not a set: the caller names the lowest role to include and gets
	// it and everything above, which is what a role ladder means. Default
	// RoleMod is the staff test the rest of the site uses (see chromeData's
	// IsMod). Ordered highest-authority first so the page reads top-down like
	// the org it describes.
	if err := w.data.DB().SelectContext(ctx, &rows,
		`SELECT username, role, COALESCE(avatar_path, '') AS avatar_path,
		        to_char(created_at, 'DD Mon YYYY') AS created_at
		   FROM users WHERE role >= $1 ORDER BY role DESC, username ASC LIMIT 200`,
		int(floor)); err != nil {
		return nil, err
	}
	byRole := map[core.Role][]staffMember{}
	for _, r := range rows {
		role := core.Role(r.Role)
		byRole[role] = append(byRole[role], staffMember{
			Username: r.Username, Role: role,
			RoleName: roleLabel(role), Since: r.CreatedAt, Avatar: r.Avatar,
		})
	}
	out := make([]staffGroup, 0, len(byRole))
	for role, members := range byRole {
		out = append(out, staffGroup{Name: roleLabel(role), Members: members})
	}
	sort.Slice(out, func(i, j int) bool {
		// Same order as the query: highest role first. Compare by the first
		// member's role, which every member of the group shares.
		return out[i].Members[0].Role > out[j].Members[0].Role
	})
	return out, nil
}

// statsPage is UNIT3D's stats/index: a hub of what the site can actually
// measure. Most of UNIT3D's 24 stat pages rank peers and ratios and have no
// analogue here, so this shows the figures the capabilities really answer and
// links to the pages that already exist rather than inventing sub-pages.
func (w *web) statsPage(c *gin.Context) {
	ctx := c.Request.Context()
	data := map[string]any{"Title": "Stats"}

	if w.usenet != nil {
		if gs, ok := w.homeGroups(ctx); ok {
			data["Stats"] = gs.Stats
			if len(gs.Top) > 0 {
				data["TopGroups"] = gs.Top
			}
		}
	}
	if w.catalog != nil {
		if cats, err := w.catalog.Enabled(ctx); err == nil {
			data["Categories"] = cats
		}
	}
	// Member and forum figures, from the same counts the staff dashboard uses.
	// Has* on each: a template cannot tell an absent key from a zero one, and
	// "0 members" is a different claim from "not measurable here".
	if w.data != nil {
		if n, ok := w.data.CountUsers(ctx); ok {
			data["Members"], data["HasMembers"] = n, true
		}
		if threads, ok := w.data.CountForumThreads(ctx); ok {
			data["ForumThreads"], data["HasForum"] = threads, true
		}
		if posts, ok := w.data.CountForumPosts(ctx); ok {
			data["ForumPosts"] = posts
		}
	}
	// The tracker, when the site HAS one and it holds something. Absent
	// otherwise, so a site without it reads exactly as it did rather than
	// advertising a tracker with nothing on it.
	//
	// The flavour check is not redundant with the read. These figures come
	// from the tracker's own tables, which SURVIVE the tracker being switched
	// off — so a site that ran as "both" and moved to indexer-only kept
	// reporting torrents and linking to /tracker, which by then was a 404.
	// Found by running the link audit against a flavour nobody had audited.
	if ts, ok := w.data.ReadTrackerSiteStats(ctx); ok && flavourTracker() {
		data["HasTrackerStats"] = true
		data["TrackerTorrents"] = ts.Torrents
		data["TrackerSeeders"] = ts.Seeders
		data["TrackerLeechers"] = ts.Leechers
		data["TrackerPeers"] = ts.Peers
		data["TrackerSnatches"] = ts.Snatches
		data["TrackerUploaded"] = humanBytes(ts.Uploaded)
	}
	w.render(c, "stats.html", data)
}

// mountSitePages wires the fixed pages. Called from wireViews so they land
// alongside the plugin site pages rather than in main's route soup.
func (w *web) mountSitePages(e *gin.Engine) {
	// robots.txt is GENERATED from the browsing mode (access_web.go), never a
	// static file: a members-only site that still invites crawlers is not
	// members-only, and a stale allow rule is how a private catalogue ends up
	// in a public index.
	e.GET("/robots.txt", w.robotsTxt)
	// Editable like the other prose pages, with the computed listing as the
	// fallback — see prosePageOr.
	e.GET("/staff", w.prosePageOr("staff", w.staffPage))
	e.GET("/stats", w.statsPage)
	// The prose pages: template-backed until an admin saves a replacement at
	// /admin/pages (pagesadmin_web.go), and operator-created pages beside
	// them. sitePagePlain is gone — the fallback lives inside prosePage now.
	e.GET("/rules", w.prosePage("rules", "rules.html"))
	e.GET("/faq", w.prosePage("faq", "faq.html"))
	e.GET("/about", w.prosePage("about", "about.html"))
	e.GET("/pages/:slug", w.customSitePage)
	// Attribution (credits_web.go). Its own page rather than a line along every
	// footer — the credit is a licence condition and still one click away, but
	// it does not need to be on the screen while somebody reads a release.
	e.GET("/credits", w.creditsPage)
	// The HUMAN sitemap (sitemappage_web.go). /sitemap.xml is the crawler's,
	// wired separately in main.go — the nav has linked this one since before
	// anything served it.
	e.GET("/sitemap", w.sitemapPage)
	// UNIT3D links /help/donate from the same Support menu; that route belongs
	// to the donations plugin and is registered there.
}
