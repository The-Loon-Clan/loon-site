package handlers

import (
	"context"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

// usersDB is the host's own users table, for the two reads no capability
// covers: who is staff, and how many members there are. Same pattern as
// forumDB — core has no "list users" method by design, so a host that wants
// one owns the query AND the bound on it.
var usersDB *sqlx.DB

// Static site pages + the stats hub — UNIT3D's page/* and stats/index areas.
//
// These are HOST pages, not plugin surfaces: the content is about this
// deployment, so there is no plugin that could own it. UNIT3D backs its
// equivalents with an admin-editable `pages` table (Staff/page/*); that is a
// CMS feature, and building one to hold four pages of prose would be the wrong
// trade. If the demo ever needs editable pages it should adopt the wiki plugin,
// which is already wired and does exactly this.
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
	data := map[string]any{"Title": "Staff"}
	if groups, err := w.staffGroups(c.Request.Context()); err == nil && len(groups) > 0 {
		data["Groups"] = groups
	}
	w.render(c, "staff.html", data)
}

func (w *web) staffGroups(ctx context.Context) ([]staffGroup, error) {
	type row struct {
		Username  string `db:"username"`
		Role      int    `db:"role"`
		CreatedAt string `db:"created_at"`
		Avatar    string `db:"avatar_path"`
	}
	var rows []row
	// Role >= RoleMod is the staff test the rest of the site uses (see
	// chromeData's IsMod). Ordered highest-authority first so the page reads
	// top-down like the org it describes.
	if err := usersDB.SelectContext(ctx, &rows,
		`SELECT username, role, COALESCE(avatar_path, '') AS avatar_path,
		        to_char(created_at, 'DD Mon YYYY') AS created_at
		   FROM users WHERE role >= $1 ORDER BY role DESC, username ASC LIMIT 200`,
		int(core.RoleMod)); err != nil {
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
	// Member count is the host's own figure. Capped like the staff read.
	if usersDB != nil {
		var n int
		if err := usersDB.GetContext(ctx, &n, `SELECT COUNT(*) FROM users`); err == nil {
			data["Members"], data["HasMembers"] = n, true
		}
	}
	if forumDB != nil {
		var threads, posts int
		if err := forumDB.GetContext(ctx, &threads,
			`SELECT COUNT(*) FROM forum_threads WHERE hidden_at IS NULL`); err == nil {
			data["ForumThreads"], data["HasForum"] = threads, true
		}
		if err := forumDB.GetContext(ctx, &posts,
			`SELECT COUNT(*) FROM forum_posts WHERE hidden_at IS NULL`); err == nil {
			data["ForumPosts"] = posts
		}
	}
	// The tracker, when there is one and it holds something. Absent otherwise,
	// so a site without it reads exactly as it did rather than advertising a
	// tracker with nothing on it.
	if ts, ok := readTrackerSiteStats(ctx, usersDB); ok {
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

// sitePagePlain renders one of the fixed prose pages. The content lives in the
// template, not the database: it describes this demo, so it changes when the
// demo does — in the same commit.
func (w *web) sitePagePlain(page, title string) gin.HandlerFunc {
	return func(c *gin.Context) {
		w.render(c, page, map[string]any{"Title": title})
	}
}

// mountSitePages wires the fixed pages. Called from wireViews so they land
// alongside the plugin site pages rather than in main's route soup.
func (w *web) mountSitePages(e *gin.Engine) {
	// robots.txt is GENERATED from the browsing mode (access_web.go), never a
	// static file: a members-only site that still invites crawlers is not
	// members-only, and a stale allow rule is how a private catalogue ends up
	// in a public index.
	e.GET("/robots.txt", w.robotsTxt)
	e.GET("/staff", w.staffPage)
	e.GET("/stats", w.statsPage)
	e.GET("/rules", w.sitePagePlain("rules.html", "Rules"))
	e.GET("/faq", w.sitePagePlain("faq.html", "FAQ"))
	e.GET("/about", w.sitePagePlain("about.html", "About"))
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
