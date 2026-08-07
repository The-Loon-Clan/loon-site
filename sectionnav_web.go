package main

// The account menu — the contents of the avatar dropdown in the top nav.
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
		{Label: "Calendar", Href: "/calendar"},
	}},
	{Label: "Points", Href: "/store/history"},
	{Label: "Settings", Items: []sectionTab{
		{Label: "Account", Href: "/p/account"},
		{Label: "Privacy", Href: "/settings/privacy"},
		{Label: "Alerts", Href: "/settings/notifications"},
		// Both are plugin pages, listed here by hand rather than left to the
		// generic tail: they configure your account, so they belong under
		// Settings with the rest of it and not loose underneath it. See
		// navPlacedByHost (admin_views.go), which keeps the generic nav from
		// adding a second copy.
		{Label: "API key", Href: "/p/api-key"},
		{Label: "Sign-ins", Href: "/p/sign-ins"},
	}},
}

// accountNav returns the menu with the page you are on marked.
func accountNav(path string) []sectionTab {
	return markActive(accountMenu, path)
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
