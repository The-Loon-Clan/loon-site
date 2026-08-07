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

// accountNav returns the account entries with the page you are on marked.
func accountNav(path string) []sectionTab {
	return markActive(accountMenu, path)
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
func accountBar(path string) []sectionTab {
	if !inAccountArea(path) {
		return nil
	}
	return accountNav(path)
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
