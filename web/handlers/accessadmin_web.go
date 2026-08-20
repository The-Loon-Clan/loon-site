package handlers

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
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
	{"/series", "Series", ""},
	{"/series/:key", "Series detail", ""},
	{"/search", "Search", ""},
	{"/groups", "Newsgroups", ""},
	{"/trending", "Trending", ""},
	{"/release/:id", "Release detail", ""},
	// The NZB itself. Listed because it is the most consequential route on an
	// indexer — the actual payload, not a page about it — and it was the one
	// route missing from this table. An operator auditing access would have
	// read every row here and still not known whether the files were public.
	//
	// It follows the browsing mode like any other page: public here, and behind
	// the login the moment browsing is set to members. That is worth stating
	// rather than leaving to be inferred from the absence of a row.
	{"/nzb/:id", "NZB download", "The file itself, not a page about it. Follows the browsing mode."},
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
	{"/playlists", "Playlists", ""},
	{"/help/donate", "Donate", "Dev-only: needs LOON_DONATIONS=1."},
	{"/u/:name", "Member profile", "A member may also set their own profile private."},
	{"/login", "Sign in", "A door: always reachable, or nobody could get in."},
	{"/register", "Register", "Also gated by the registration mode above."},
	{"/forgot", "Forgot password", "A door."},
	// The other two halves of the same door. Both were missing, which meant the
	// table listed where you ask for a reset and not where you complete one.
	{"/reset", "Reset password", "A door: reached from the emailed link."},
	{"/verify", "Verify email", "A door: reached from the emailed link."},
	// Public pages nobody had written down. Found by audit_access.py, which
	// asks the running app which routes a stranger can read and compares that
	// with this list — the check this table could not perform on itself.
	{"/credits", "Data sources", "Attribution for the metadata sources."},
	{"/store", "Points store", "Readable by anyone; spending needs an account."},
	{"/support/public", "Public tickets", ""},
	{"/wiki/recent", "Recent wiki changes", ""},
	{"/p/guestbook", "Guestbook", "A plugin page."},
	{"/p/apply", "Apply to join", "A plugin page, and PUBLIC by necessity: the audience is somebody with no account and no invite. Only meaningful while the registration mode above is set to Apply."},
	{"/p/achievements", "Achievements", "A plugin page. The catalogue half — what can be earned here — is public by the plugin's own choice; the personal half renders only for whoever is signed in."},
	{"/p/downloads", "Download reports", "A plugin page: the setup for a download client's callback script. Members only — it hands over a file carrying your API key."},
	{"/api/downloads/report", "Download report callback", "POST from a member's SABnzbd or NZBGet. Authenticates with an API key, not a session, so it is exempt from the CSRF gate like /api."},
	{"/c/new", "New community", "The FORM is public; the write is not."},
	{"/community/forums/new", "New thread", "The FORM is public; the write is not."},
	{"/sitemap.xml", "sitemap.xml", "The crawler's copy of the sitemap."},
	{"/api", "Newznab API", "Authenticates with an API key, not a session."},
	{"/rss", "RSS feed", "Authenticates with an API key, not a session."},
	{"/robots.txt", "robots.txt", "Generated from the browsing mode."},
	{"/healthz", "Health check", "Must answer regardless of site state."},
	{"/admin/*", "Admin area", "Staff only, in every mode."},
	{"/settings/*", "Your settings", ""},
	{"/subscriptions", "Subscriptions", ""},
	{"/bookmarks", "Bookmarks", ""},
	{"/inbox", "Inbox", ""},
	// The tracker. Six routes an operator most needs written down, because two
	// of them are the only ones on this site that take no session at all — and
	// because the whole feature appears and disappears with an env flag, so its
	// absence from this table read as "no such pages" rather than "switched
	// off".
	//
	// The member pages are members-only in EVERY browsing mode, and not for the
	// reason the rest of that group is: they carry a second gate the site
	// setting cannot reach — the tracker.access entitlement — so a member
	// without it is sent to / rather than to the login page.
	{"/tracker", "Tracker", "Needs a site flavour with the tracker on (above), an account, and the tracker.access entitlement."},
	{"/tracker/my", "Your tracker stats", "Your ratio, your passkey. Same gates as /tracker."},
	{"/tracker/download/:hash", "Torrent download", "The .torrent, with YOUR passkey baked into its announce URL."},
	// Public on purpose, and the one row here worth reading twice. The caller is
	// a torrent client: it has no cookie, cannot follow a login redirect, and
	// would parse a login page as a bencoded response. The passkey in the path
	// IS the credential — which is why rotating one invalidates every .torrent a
	// member has already downloaded.
	{"/api/tracker/announce/:passkey", "Announce", "No session by design: the passkey is the credential."},
	{"/api/tracker/scrape/:passkey", "Scrape", "As announce."},
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

// isPerViewer reports whether a page needs an account whatever the site mode
// is, because the browsing setting is not the only thing gating it.
//
// For most of the list below that is because the page is ABOUT the viewer —
// your inbox, your bookmarks — and there is no "you" without a session. The
// tracker is here for a different reason and it is worth not blurring: its
// pages carry a second gate the browsing mode cannot reach, the tracker.access
// entitlement, so opening the site to anonymous browsing does not open them.
// Same answer, different cause, and each of those rows carries its own note.
func isPerViewer(p string) bool {
	for _, pre := range []string{"/settings/", "/subscriptions", "/bookmarks", "/inbox", "/achievements", "/calendar", "/rewards", "/tracker"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// adminAccess serves GET /admin/access.
func (w *web) adminAccess(c *gin.Context) {
	w.render(c, "admin_access.html", map[string]any{
		"Title": "Access",
		// Whatever plugins added, rendered as radio buttons beside the built-in
		// three. Empty on a host with no such plugin, which is most of them —
		// the template ranges, so nothing appears rather than an empty heading.
		"PluginModes":  pluginapi.RegistrationModes(pluginRegistry()),
		"Registration": registrationMode(),
		"Browsing":     browsingMode(),
		"Flavour":      siteFlavour(),
		"Pages":        buildAccessMap(),
		"Saved":        c.Query(querySaved) == "1",
		"Err":          c.Query(queryErr),
	})
}

// adminAccessSave serves POST /admin/access.
func (w *web) adminAccessSave(c *gin.Context) {
	in, _ := readAccessSaveInput(c)
	reg := in.Registration
	browse := in.Browsing
	if err := saveAccessSettings(c.Request.Context(), reg, browse); err != nil {
		w.log.Error("save access settings", "registration", reg, "browsing", browse, "err", err)
		c.Redirect(http.StatusFound, "/admin/access?err=could+not+save")
		return
	}
	if err := saveSiteFlavour(c.Request.Context(), in.Flavour); err != nil {
		w.log.Error("save site flavour", "flavour", in.Flavour, "err", err)
		c.Redirect(http.StatusFound, "/admin/access?err=could+not+save")
		return
	}
	w.log.Info("access settings changed",
		"registration", reg, "browsing", browse, "flavour", in.Flavour)
	c.Redirect(http.StatusFound, "/admin/access?saved=1")
}

// pageTitleFor names a path using the same table the access map is built from.
//
// For the pages that set no title of their own — most of the plugin pages
// rendered through fhead — so they stop sharing one tab label. Longest match
// wins, so /community/forums beats /c, and a path with no entry returns "",
// which the caller reads as "leave it alone" rather than as a name.
func pageTitleFor(path string) string {
	best := ""
	for _, r := range accessRoutes {
		p := r.Path
		if strings.HasSuffix(p, "/*") || strings.Contains(p, ":") {
			continue // patterns name a whole area, not a page
		}
		if path == p || (p != "/" && strings.HasPrefix(path, p+"/")) {
			if len(p) > len(best) {
				best, _ = p, r.Label
			}
		}
	}
	for _, r := range accessRoutes {
		if r.Path == best {
			return r.Label
		}
	}
	return ""
}
