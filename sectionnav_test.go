package main

import "testing"

// Section tabs are matched by PATH PREFIX rather than declared per page, which
// means the prefixes and hrefs here can drift from the routes without anything
// noticing — a tab would simply 404. These assert the two stay in step.

// TestSectionNavTabsAreReachable checks every tab href is a route the site
// actually serves. The list mirrors what mount()/mountSitePages()/mountSettings()
// register plus the plugin routes; a tab pointing anywhere else is dead.
func TestSectionNavTabsAreReachable(t *testing.T) {
	// Routes registered by the host or by a wired plugin. Kept explicit rather
	// than reflected off gin, because the point is to catch a typo in EITHER
	// list — deriving one from the other would make them agree by construction.
	served := map[string]bool{
		"/browse": true, "/search": true, "/groups": true, "/trending": true,
		"/community/forums": true, "/playlists": true, "/c": true, "/news": true, "/store": true,
		"/store/history": true, "/rules": true, "/faq": true, "/wiki": true,
		"/support": true, "/staff": true, "/stats": true, "/about": true,
		"/sitemap": true, "/inbox": true, "/p/inbox": true, "/p/account": true,
		"/p/api-key": true, "/p/sign-ins": true,
		"/settings/privacy": true, "/settings/notifications": true, "/bookmarks": true,
		"/calendar": true,
	}
	for _, s := range sections {
		for _, tab := range s.Tabs {
			if !served[tab.Href] {
				t.Errorf("section %q tab %q points at %s, which nothing serves",
					s.Title, tab.Label, tab.Href)
			}
		}
	}
}

// TestSectionNavSelectsOneTab is the behaviour that makes the bar useful: the
// page you are on is marked, and only that one.
func TestSectionNavSelectsOneTab(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/browse", "Browse"},
		{"/search?q=x", ""}, // query strings never reach here; Path is clean
		{"/groups", "Newsgroups"},
		{"/community/forums", "Forums"},
		{"/community/forums/thread/7", "Forums"},
		{"/news/some-post", "News"},
		{"/wiki/guides/setup", "Wiki"},
		{"/settings/privacy", "Privacy"},
		{"/p/api-key", "API key"},
		// The case a naive prefix match gets wrong: /store/history is the
		// viewer's own ledger, so it must mark Points and NOT Store.
		{"/store/history", "Points"},
		{"/store", "Store"},
	} {
		tabs := sectionNav(tc.path)
		if tc.want == "" {
			continue
		}
		if tabs == nil {
			t.Errorf("%s: no section", tc.path)
			continue
		}
		var active []string
		for _, tab := range tabs {
			if tab.Active {
				active = append(active, tab.Label)
			}
		}
		if len(active) != 1 {
			t.Errorf("%s: %d active tabs %v, want exactly 1", tc.path, len(active), active)
			continue
		}
		if active[0] != tc.want {
			t.Errorf("%s: active tab is %q, want %q", tc.path, active[0], tc.want)
		}
	}
}

// TestSectionNavAbsentWhereItWouldBeNoise: a bar with nothing to offer is
// furniture, and /admin already has its own subnav.
func TestSectionNavAbsentWhereItWouldBeNoise(t *testing.T) {
	for _, p := range []string{"/", "/login", "/register", "/admin/settings", "/admin/news"} {
		if tabs := sectionNav(p); tabs != nil {
			t.Errorf("%s: got a section bar (%d tabs), want none", p, len(tabs))
		}
	}
}

// The "/c" section prefix would swallow /community/forums under a bare
// strings.HasPrefix, since "/community…" starts with "/c". Segment matching is
// what prevents it, and this is the case that proves it.
func TestSectionNavDoesNotConfusePrefixes(t *testing.T) {
	if got := sectionTitle("/community/forums"); got != "Community" {
		t.Errorf("/community/forums is in section %q, want Community", got)
	}
	tabs := sectionNav("/community/forums")
	for _, tab := range tabs {
		if tab.Href == "/c" && tab.Active {
			t.Error("/community/forums marked the Communities tab active")
		}
	}
}
