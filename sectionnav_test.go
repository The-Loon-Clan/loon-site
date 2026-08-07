package main

import (
	"strings"
	"testing"
)

// Which SECTION a path lands in — which none of the tab tests below pin. They
// assert which TAB is lit, and a page with no tab of its own lights nothing
// wherever it lands, so a plugin page filed under the wrong section looks
// exactly like one filed correctly. /p/stats sat in Account for that reason:
// public site figures under a bar of Messages, Activity, Points and Settings,
// with nothing failing to say so.
func TestSectionNavPutsAPathInTheRightSection(t *testing.T) {
	sectionFor := func(path string) string {
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
	for _, tc := range []struct{ path, want string }{
		{"/stats", "Site"},   // the host's stats page
		{"/p/stats", "Site"}, // the stats PLUGIN's, which is also public
		{"/sitemap", "Site"},
		{"/p/account", "Account"},
		{"/p/topics", "Account"},
		{"/achievements", "Account"},
		// The pair a naive prefix order gets backwards: the viewer's own ledger
		// is Account, the shop it draws on is Community.
		{"/store/history", "Account"},
		{"/store", "Community"},
		{"/browse", "Releases"},
		{"/support", "Support"},
	} {
		if got := sectionFor(tc.path); got != tc.want {
			t.Errorf("%s is in section %q, want %q", tc.path, got, tc.want)
		}
	}
}

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
		"/calendar": true, "/achievements": true, "/p/topics": true, "/p/posts": true,
	}
	// Walks GROUPS too. A group has no Href of its own, so checking only the
	// top level would silently stop covering the account area the moment it
	// was grouped — which is exactly when it gained most of its entries.
	var check func(title string, tabs []sectionTab)
	check = func(title string, tabs []sectionTab) {
		for _, tab := range tabs {
			if len(tab.Items) > 0 {
				if tab.Href != "" {
					t.Errorf("section %q group %q has both an Href and children; "+
						"the Href is ignored when rendered", title, tab.Label)
				}
				check(title, tab.Items)
				continue
			}
			if !served[tab.Href] {
				t.Errorf("section %q tab %q points at %s, which nothing serves",
					title, tab.Label, tab.Href)
			}
		}
	}
	for _, s := range sections {
		check(s.Title, s.Tabs)
	}
}

// A page inside a collapsed group must light the group, or the bar stops
// answering "where am I" the moment the account area got dropdowns.
func TestSectionNavLightsTheGroupAPageIsIn(t *testing.T) {
	for _, tc := range []struct{ path, group, item string }{
		{"/achievements", "Activity", "Achievements"},
		{"/p/topics", "Activity", "Topics"},
		{"/inbox", "Messages", "Inbox"},
		{"/settings/privacy", "Settings", "Privacy"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var found bool
			for _, tab := range sectionNav(tc.path) {
				if tab.Label != tc.group {
					if tab.Active {
						t.Errorf("%s: group %q is lit as well", tc.path, tab.Label)
					}
					continue
				}
				found = true
				if !tab.Active {
					t.Errorf("%s: group %q is not lit", tc.path, tc.group)
				}
				var itemLit bool
				for _, it := range tab.Items {
					if it.Active {
						itemLit = true
						if it.Label != tc.item {
							t.Errorf("%s: lit %q, want %q", tc.path, it.Label, tc.item)
						}
					}
				}
				if !itemLit {
					t.Errorf("%s: no item lit inside %q", tc.path, tc.group)
				}
			}
			if !found {
				t.Fatalf("%s: no group %q in the account bar", tc.path, tc.group)
			}
		})
	}
}

// The package-level `sections` is shared by every request. Marking a tab in
// place would leave one reader's active page lit for everyone — a bug that
// only shows under concurrency, so it is asserted directly instead.
func TestSectionNavDoesNotMutateTheSharedTabs(t *testing.T) {
	_ = sectionNav("/achievements")
	for _, s := range sections {
		for _, tab := range s.Tabs {
			if tab.Active {
				t.Errorf("section %q tab %q was marked active in the shared slice", s.Title, tab.Label)
			}
			for _, it := range tab.Items {
				if it.Active {
					t.Errorf("section %q item %q was marked active in the shared slice", s.Title, it.Label)
				}
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
		// The DESTINATION is what the reader is looking for, and once the
		// account bar grouped its tabs that is a dropdown item rather than a
		// top-level tab. Descend into a lit group and name its lit child, so
		// this keeps asserting "exactly one page is marked current" instead
		// of quietly starting to assert "exactly one group is open".
		var active []string
		for _, tab := range tabs {
			if !tab.Active {
				continue
			}
			if len(tab.Items) == 0 {
				active = append(active, tab.Label)
				continue
			}
			for _, it := range tab.Items {
				if it.Active {
					active = append(active, it.Label)
				}
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
