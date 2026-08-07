package main

import "strings"

// Contextual section tabs — the SECOND bar live UNIT3D sites carry under the
// main nav (Profile · Settings · Torrents · Activity … when you are in the user
// area, a different set when you are not).
//
// The main nav answers "where can I go"; this answers "what else is here". Five
// broad dropdowns up top only work if the detail is one level down, and until
// now the only thing filling that slot was the admin subnav — which is exactly
// this idea, built once for admins and never generalised.
//
// Sections are matched by PATH PREFIX rather than declared per page, so a page
// cannot forget to say which section it is in. The cost is that the prefixes
// have to stay in sync with the routes; the test asserts every tab here
// resolves to a real page, which is what catches drift.

// sectionTab is one tab. Active is resolved against the request path here
// rather than in the template, so the matching rule lives in one place.
type sectionTab struct {
	Label  string
	Href   string
	Active bool
	// Items, when non-empty, makes this a GROUP rather than a destination: it
	// renders as a dropdown and its own Href is ignored.
	//
	// The account area is why. UNIT3D's second bar is five dropdowns (Profile,
	// Settings, Torrents, Activity, Bonus Points) over ~25 pages; ours was a
	// flat row, which works at six entries and falls apart at fifteen — the
	// bar either wraps or scrolls, and either way the thing you want is off
	// the end. Grouping is what lets the area grow without the bar growing.
	Items []sectionTab
}

// section is a named group of tabs plus the prefixes that select it.
type section struct {
	Title    string
	Prefixes []string
	Tabs     []sectionTab
}

// sections are tried IN ORDER and the first prefix match wins, so a longer,
// more specific prefix must come before a shorter one that would also match.
// /store/history is under Account rather than Community for that reason: it is
// the viewer's OWN ledger, and it is listed before the /store prefix below.
var sections = []section{
	{
		Title:    "Account",
		Prefixes: []string{"/u/", "/p/account", "/p/api-key", "/p/sign-ins", "/p/inbox", "/p/store", "/p/stats", "/p/topics", "/p/posts", "/settings/", "/inbox", "/store/history", "/bookmarks", "/calendar", "/achievements"},
		Tabs: []sectionTab{
			// Grouped the way the account area actually divides: things you
			// have DONE, things sent TO you, and things you CONFIGURE. Points
			// stays a plain tab — one destination has nothing to group.
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
				{Label: "API key", Href: "/p/api-key"},
				{Label: "Sign-ins", Href: "/p/sign-ins"},
			}},
		},
	},
	{
		Title:    "Releases",
		Prefixes: []string{"/browse", "/search", "/groups", "/release/", "/trending"},
		Tabs: []sectionTab{
			{Label: "Browse", Href: "/browse"},
			{Label: "Search", Href: "/search"},
			{Label: "Newsgroups", Href: "/groups"},
			{Label: "Trending", Href: "/trending"},
		},
	},
	{
		Title:    "Community",
		Prefixes: []string{"/community/forums", "/c", "/news", "/store", "/playlists"},
		Tabs: []sectionTab{
			{Label: "Forums", Href: "/community/forums"},
			{Label: "Communities", Href: "/c"},
			{Label: "News", Href: "/news"},
			{Label: "Playlists", Href: "/playlists"},
			{Label: "Store", Href: "/store"},
		},
	},
	{
		Title:    "Support",
		Prefixes: []string{"/rules", "/faq", "/wiki", "/support", "/staff"},
		Tabs: []sectionTab{
			{Label: "Rules", Href: "/rules"},
			{Label: "FAQ", Href: "/faq"},
			{Label: "Wiki", Href: "/wiki"},
			{Label: "Helpdesk", Href: "/support"},
			{Label: "Staff", Href: "/staff"},
		},
	},
	{
		Title:    "Site",
		Prefixes: []string{"/stats", "/about", "/sitemap"},
		Tabs: []sectionTab{
			{Label: "Stats", Href: "/stats"},
			{Label: "About", Href: "/about"},
			{Label: "Sitemap", Href: "/sitemap"},
		},
	},
}

// sectionNav returns the tabs for a path, with the current one marked, or nil
// when the path belongs to no section.
//
// Returns nil rather than an empty section for the home page and the auth
// pages: a bar with one tab in it is furniture, and those pages have nowhere
// sideways to go. /admin/* is nil too — it already has its own subnav built
// from the view registry, and two competing section bars would be worse than
// one.
func sectionNav(path string) []sectionTab {
	if strings.HasPrefix(path, "/admin") {
		return nil
	}
	for _, s := range sections {
		if !matchesSection(path, s.Prefixes) {
			continue
		}
		return markActive(s.Tabs, path)
	}
	return nil
}

// markActive copies the tabs and lights the one the path is on.
//
// Longest matching href wins, so /store/history marks Points rather than also
// lighting Store — a shorter prefix must not steal a deeper page. The search
// runs across GROUP CHILDREN as well as top-level tabs, and a matched child
// lights its parent too: a collapsed dropdown that gives no sign the current
// page is inside it is a bar that has stopped answering "where am I".
func markActive(tabs []sectionTab, path string) []sectionTab {
	out := make([]sectionTab, len(tabs))
	copy(out, tabs)

	bestTab, bestItem, bestLen := -1, -1, -1
	consider := func(href string, ti, ii int) {
		if href == "" || len(href) <= bestLen {
			return
		}
		if path == href || strings.HasPrefix(path, href+"/") {
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
		// Copy the child slice before writing: the package-level `sections`
		// is shared across every request, and marking in place would leave
		// one reader's active tab lit for everyone else.
		items := make([]sectionTab, len(out[bestTab].Items))
		copy(items, out[bestTab].Items)
		items[bestItem].Active = true
		out[bestTab].Items = items
	}
	return out
}

// sectionTitle names the current section, for the bar's label.
func sectionTitle(path string) string {
	if strings.HasPrefix(path, "/admin") {
		return ""
	}
	for _, s := range sections {
		if matchesSection(path, s.Prefixes) {
			return s.Title
		}
	}
	return ""
}

// matchesSection reports whether path is in this section. An exact match or a
// path-segment match only: a bare prefix test would put /community/forums in
// the "/c" section, since "/community…" starts with "/c".
func matchesSection(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if path == p {
			return true
		}
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(path, p) {
				return true
			}
			continue
		}
		if strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
