package site

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// /admin/access — the two mode switches, and a map of what they do to every
// page.
//
// The map is the point. "Members only" and "invite only" are easy to set and
// hard to verify: an operator flips a switch and then has to take on trust that
// the release pages went dark and the login page did not. This page answers
// that by listing every route with the access it actually has UNDER THE CURRENT
// MODE, so turning the site private and watching the table change is the check.
//
// Derived from the same predicates the middleware uses, never from a
// hand-written list. A documentation table that describes the gate rather than
// asking it is a table that goes stale silently, and stale access documentation
// is worse than none — it is read as an assurance.

// pageAccess is one row of the map.
type pageAccess struct {
	Path  string
	Label string
	// Access is what an ANONYMOUS visitor gets right now.
	Access string // "public" | "members" | "staff"
	Note   string
}

// accessRoutes are the pages worth showing an operator, with what gates them.
// Admin routes are collapsed to one row rather than listed individually: they
// all carry the same gate and forty rows saying "staff" teaches nothing.
var accessRoutes = []struct{ Path, Label, Note string }{
	{"/", "Home", ""},
	{"/browse", "Browse", ""},
	{"/search", "Search", ""},
	{"/groups", "Newsgroups", ""},
	{"/trending", "Trending", ""},
	{"/release/:id", "Release detail", ""},
	{"/community/forums", "Forums", ""},
	{"/c", "Communities", ""},
	{"/news", "News", ""},
	{"/wiki", "Wiki", ""},
	{"/rules", "Rules", ""},
	{"/faq", "FAQ", ""},
	{"/staff", "Staff", ""},
	{"/stats", "Stats", ""},
	{"/about", "About", ""},
	{"/sitemap", "Sitemap", ""},
	{"/u/:name", "Member profile", "A member may also set their own profile private."},
	{"/login", "Sign in", "A door: always reachable, or nobody could get in."},
	{"/register", "Register", "Also gated by the registration mode above."},
	{"/forgot", "Forgot password", "A door."},
	{"/api", "Newznab API", "Authenticates with an API key, not a session."},
	{"/rss", "RSS feed", "Authenticates with an API key, not a session."},
	{"/robots.txt", "robots.txt", "Generated from the browsing mode."},
	{"/healthz", "Health check", "Must answer regardless of site state."},
	{"/admin/*", "Admin area", "Staff only, in every mode."},
	{"/settings/*", "Your settings", ""},
	{"/subscriptions", "Subscriptions", ""},
	{"/bookmarks", "Bookmarks", ""},
	{"/inbox", "Inbox", ""},
}

// buildAccessMap answers the access question for each route by ASKING the
// predicates the middleware uses, so this table cannot describe a gate the site
// does not actually have.
func buildAccessMap() []pageAccess {
	out := make([]pageAccess, 0, len(accessRoutes))
	for _, r := range accessRoutes {
		a := pageAccess{Path: r.Path, Label: r.Label, Note: r.Note}
		switch {
		case strings.HasPrefix(r.Path, "/admin"):
			a.Access = "staff"
		case alwaysPublic(strings.TrimSuffix(r.Path, "/*")):
			a.Access = "public"
		case isPerViewer(r.Path):
			// These need an account in EVERY mode — not because of the site
			// setting but because they are about you, and there is no "you"
			// without a session.
			a.Access = "members"
			if a.Note == "" {
				a.Note = "Always needs an account: the page is about the viewer."
			}
		case browsingMode() == BrowseMembers:
			a.Access = "members"
		default:
			a.Access = "public"
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Access < out[j].Access })
	return out
}

// isPerViewer reports whether a page is about the viewer, and so needs an
// account whatever the site mode is.
func isPerViewer(p string) bool {
	for _, pre := range []string{"/settings/", "/subscriptions", "/bookmarks", "/inbox", "/achievements", "/calendar", "/rewards"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// adminAccess serves GET /admin/access.
func (w *web) adminAccess(c *gin.Context) {
	w.render(c, "admin_access.html", map[string]any{
		"Title":        "Access",
		"Registration": registrationMode(),
		"Browsing":     browsingMode(),
		"Pages":        buildAccessMap(),
		"Saved":        c.Query("saved") == "1",
		"Err":          c.Query("err"),
	})
}

// adminAccessSave serves POST /admin/access.
func (w *web) adminAccessSave(c *gin.Context) {
	reg := c.PostForm("registration")
	browse := c.PostForm("browsing")
	if err := saveAccessSettings(c.Request.Context(), reg, browse); err != nil {
		w.log.Error("save access settings", "registration", reg, "browsing", browse, "err", err)
		c.Redirect(http.StatusFound, "/admin/access?err=could+not+save")
		return
	}
	w.log.Info("access settings changed", "registration", reg, "browsing", browse)
	c.Redirect(http.StatusFound, "/admin/access?saved=1")
}
