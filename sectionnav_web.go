package main

// The account area — its bar, and what the avatar menu keeps.
//
// FOUR places can hold a per-viewer link, and each answers one question. The
// rule is written down because without it things land wherever they were added,
// which is how the API key ended up beside Log out and the points ledger ended
// up with two tab strips arguing over it:
//
//	top nav      a LIVE FIGURE you want visible without clicking: the points
//	             pill, the unread bell. Not a menu — a number.
//	avatar menu  you, from ANY page, plus session controls. Deliberately short:
//	             profile, notifications, theme, log out (and admin settings).
//	account bar  the account AREA — the pages you move between while managing
//	             your account. Only on those pages (accountAreaPrefixes).
//	page strip   a sub-area with its own pages, rendered by whoever owns them:
//	             the points economy is Store | History | Rewards.
//
// The points ledger is the case that decides the shape. It is per-viewer, so it
// looks like an account page — but it belongs to the points economy, which has
// its own strip, and it is already one click from anywhere via the pill in the
// top nav. Listing it a third time on the account bar put two tab rows on one
// page. So the account bar covers who you ARE and how you are CONFIGURED, and
// the points economy is the store's.
//
// This file used to build a SECOND full-width bar under the main nav, the one
// live UNIT3D sites carry (Profile · Settings · Torrents · Activity when you
// are in the user area, a different set when you are not). It was removed,
// because on this site it was almost entirely a restatement of the dropdown
// the reader had just used: four of its five sections — Releases, Community,
// Support, Site — listed exactly the pages already inside the main nav
// dropdown of the same name, so opening "Releases ▾" and landing on /browse
// showed you Browse · Search · Newsgroups · Trending a second time, in a
// second bar, one row lower. Where you are is now a breadcrumb (base.html),
// which is what that row was being read as anyway.
//
// The ACCOUNT half was the exception and is why this file still exists: its
// pages had no other route in. They live in the avatar dropdown now, which is
// where a reader looks for their own things, and this is the list.

import "strings"

// sectionTab is one entry. Active is resolved against the request path here
// rather than in the template, so the matching rule lives in one place.
type sectionTab struct {
	Label  string
	Href   string
	Active bool
	// Items, when non-empty, makes this a GROUP rather than a destination: it
	// renders as a labelled run inside the menu and its own Href is ignored.
	//
	// UNIT3D's second bar is five dropdowns (Profile, Settings, Torrents,
	// Activity, Bonus Points) over ~25 pages; ours was a flat row, which works
	// at six entries and falls apart at fifteen. Grouping is what lets the area
	// grow without the menu becoming an undifferentiated list.
	Items []sectionTab
}

// accountMenu is what the avatar dropdown offers below "My profile", grouped
// the way the account area actually divides: things sent TO you, things you
// have DONE, and things you CONFIGURE. Points stays a plain entry — one
// destination has nothing to group.
//
// Per-viewer PLUGIN pages (your API key, your sign-ins, your purchases) are not
// here: they come from the view registry at runtime via SiteNavAccount, since
// which of them exist depends on what is wired.
var accountMenu = []sectionTab{
	{Label: "Messages", Items: []sectionTab{
		{Label: "Inbox", Href: "/inbox"},
		{Label: "Notifications", Href: "/p/inbox"},
	}},
	{Label: "Activity", Items: []sectionTab{
		{Label: "Achievements", Href: "/achievements"},
		{Label: "Topics", Href: "/p/topics"},
		{Label: "Posts", Href: "/p/posts"},
		{Label: "Bookmarks", Href: "/bookmarks"},
		{Label: "Gifts", Href: "/gifts"},
		{Label: "Wishlist", Href: "/wishlist"},
		{Label: "Subscriptions", Href: "/subscriptions"},
		{Label: "Calendar", Href: "/calendar"},
	}},
	// No Points entry. The ledger lives in the points area with its own strip
	// (Store | History | Rewards) and is already one click from every page via
	// the top nav's points pill — a third listing here is what gave
	// /store/history two tab rows.
	{Label: "Settings", Items: []sectionTab{
		{Label: "Account", Href: "/p/account"},
		{Label: "About you", Href: "/settings/profile"},
		{Label: "Privacy", Href: "/settings/privacy"},
		{Label: "Security", Href: "/settings/security"},
		{Label: "Alerts", Href: "/settings/notifications"},
		// A plugin page, listed here by hand rather than left to the generic
		// tail: it configures your account, so it belongs under Settings with
		// the rest of it and not loose underneath it. See navPlacedByHost
		// (admin_views.go), which keeps the generic nav from adding a second
		// copy.
		{Label: "API key", Href: "/p/api-key"},
	}},
}

// trackerAccountGroup is the member's own tracker standing: what they owe, what
// they hold, and where they are seeding from.
//
// Built at call time rather than declared with the rest of accountMenu, because
// which of these pages EXIST depends on what the operator switched on. A static
// entry would be a dead link on a site with no tracker — and these three are
// exactly the pages a member is sent to by an error message, so a 404 here
// lands on somebody already confused.
//
// It follows the dropdown rule this site set for itself: the account menu
// carries the viewer's own things. All three are that — your seeding debts,
// your tokens, your locks — and none is a queue, a tool or a staff list.
func trackerAccountGroup() (sectionTab, bool) {
	if !trackerEnabled() {
		// No tracker, no tracker pages. The plugins may still be compiled in,
		// but a member has nothing to seed and nothing to owe.
		return sectionTab{}, false
	}
	items := []sectionTab{
		// Both mount whenever the host wired their render seams, independently
		// of whether the RULES are enabled: hit-and-run still shows what you
		// have seeded, and the wallet still shows tokens you hold.
		{Label: "Seeding requirements", Href: "/hitrun"},
		{Label: "Perks", Href: "/perks"},
	}
	// The lock page only exists when the rule is armed — the plugin mounts it
	// inside that branch — so listing it otherwise would be the dead link this
	// function exists to avoid.
	if seedLockEnabled() {
		items = append(items, sectionTab{Label: "Seeding locks", Href: "/seedlock"})
	}
	return sectionTab{Label: "Tracker", Items: items}, true
}

// accountNav returns the account entries with the page you are on marked.
func accountNav(path string) []sectionTab {
	menu := accountMenu
	if g, ok := trackerAccountGroup(); ok {
		// Copied before appending: accountMenu is a package-level slice shared
		// by every request, and appending to it in place would grow the menu by
		// one group per page load.
		menu = append(append([]sectionTab{}, accountMenu...), g)
	}
	return markActive(menu, path)
}

// accountAreaPrefixes select the pages the account BAR covers — the second row
// of the header, beside the breadcrumb, the way UNIT3D carries Profile ·
// Settings · Torrents · Activity across its user area.
//
// Matched by prefix rather than declared per page so a page cannot forget to
// say it is in the area. /u/ is here because the profile is the area's landing
// page: the avatar menu points at it, and arriving there is what puts the rest
// of the account within one click.
// /store/history and /rewards are deliberately NOT here: they are the points
// economy's, they carry its strip, and the account bar on top of that was the
// second tab row.
var accountAreaPrefixes = []string{
	"/u/", "/inbox", "/p/inbox", "/p/account", "/p/api-key",
	"/p/topics", "/p/posts", "/settings/",
	"/bookmarks", "/calendar", "/achievements", "/subscriptions", "/gifts", "/wishlist",
	// The member's own tracker standing — see trackerAccountGroup.
	"/hitrun", "/perks", "/seedlock",
}

// inAccountArea reports whether the account bar belongs on a path.
func inAccountArea(path string) bool {
	for _, p := range accountAreaPrefixes {
		if path == p || hasPathPrefix(path, p) {
			return true
		}
		// A trailing slash is a prefix on purpose ("/settings/", "/u/"): those
		// have no bare landing page, only children.
		if len(p) > 0 && p[len(p)-1] == '/' && len(path) >= len(p) && path[:len(p)] == p {
			return true
		}
	}
	return false
}

// accountBar returns the bar's entries for a path, or nil when the path is not
// in the account area. Nil is the template's guard — no bar rather than an
// empty one.
//
// Path alone is not enough, and /u/ is why. Every entry here is the VIEWER's
// own — /inbox, /settings/security, /p/api-key — so the bar only means
// anything to a signed-in member looking at their own area. A profile is a
// PUBLIC page at an account-area path, and judging by path alone put a
// personal account menu, API key and Security included, in front of anonymous
// visitors on every member's profile. Nothing leaked (the links are
// viewer-relative and every one of them bounces to /login), but a stranger was
// shown a menu of someone else's settings, which reads as the site being
// broken — and is how it was reported.
//
// signedIn gates the whole bar; own narrows the profile case further. Both are
// parameters rather than a *gin.Context so the rule stays testable as a pure
// function, which is what TestAccountBarScope relies on.
func accountBar(path string, signedIn, ownProfile bool) []sectionTab {
	if !signedIn || !inAccountArea(path) {
		return nil
	}
	// Someone else's profile is not your account area. The prefix earns its
	// place as "the area's landing page" only on the way to your OWN profile;
	// on another member's it is a menu about you, attached to a page about
	// them.
	//
	// Detected with profileNameFromPath rather than hasPathPrefix: that helper
	// matches whole segments, so it does NOT match "/u/bob" against "/u/" —
	// which is exactly why inAccountArea carries its own raw-prefix branch for
	// trailing-slash entries. Reusing the parser keeps one rule for "is this a
	// profile, and whose".
	if name := profileNameFromPath(path); name != "" {
		if !ownProfile {
			return nil
		}
	}
	return accountNav(path)
}

// profileNameFromPath pulls the username out of /u/<name> and its children
// (/u/<name>/followers, /following, /friends), or "" when the path is not a
// profile. Deriving it from the PATH rather than from gin's :name param keeps
// accountBar's rule usable from chromeData, which runs for every page and has
// no route params of its own.
func profileNameFromPath(path string) string {
	const p = "/u/"
	if len(path) <= len(p) || path[:len(p)] != p {
		return ""
	}
	rest := path[len(p):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// markActive copies the entries and lights the one the path is on.
//
// Longest matching href wins, so /store/history marks Points rather than also
// lighting a shorter /store — a shorter prefix must not steal a deeper page.
// The search runs across GROUP CHILDREN as well as top-level entries, and a
// matched child lights its parent too: a group that gives no sign the current
// page is inside it has stopped answering "where am I".
func markActive(tabs []sectionTab, path string) []sectionTab {
	out := make([]sectionTab, len(tabs))
	copy(out, tabs)

	bestTab, bestItem, bestLen := -1, -1, -1
	consider := func(href string, ti, ii int) {
		if href == "" || len(href) <= bestLen {
			return
		}
		if path == href || hasPathPrefix(path, href) {
			bestTab, bestItem, bestLen = ti, ii, len(href)
		}
	}
	for i, t := range out {
		consider(t.Href, i, -1)
		for j, it := range t.Items {
			consider(it.Href, i, j)
		}
	}
	if bestTab < 0 {
		return out
	}
	out[bestTab].Active = true
	if bestItem >= 0 {
		// Copy the child slice before writing: the package-level accountMenu is
		// shared across every request, and marking in place would leave one
		// reader's active entry lit for everyone else.
		items := make([]sectionTab, len(out[bestTab].Items))
		copy(items, out[bestTab].Items)
		items[bestItem].Active = true
		out[bestTab].Items = items
	}
	return out
}

// hasPathPrefix reports whether path sits under href as a path SEGMENT, so
// /settings/privacy-policy is not treated as a page under /settings/privacy.
func hasPathPrefix(path, href string) bool {
	return len(path) > len(href) && path[:len(href)] == href && path[len(href)] == '/'
}
