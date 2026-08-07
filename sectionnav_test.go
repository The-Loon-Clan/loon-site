package main

import "testing"

// The account menu's entries are hand-written hrefs, so they can drift from the
// routes without anything noticing — an entry would simply 404. These assert
// the two stay in step, and that the menu keeps answering "where am I".
//
// This file used to cover five sections driving a second nav bar. The bar is
// gone (it restated the main nav's dropdowns); what survives is the account
// menu, which is the half that had no other route in.

// TestAccountMenuEntriesAreReachable checks every href is a route the site
// actually serves. The list mirrors what mount()/mountSitePages()/mountSettings()
// register plus the plugin routes; an entry pointing anywhere else is dead.
func TestAccountMenuEntriesAreReachable(t *testing.T) {
	// Routes registered by the host or by a wired plugin. Kept explicit rather
	// than reflected off gin, because the point is to catch a typo in EITHER
	// list — deriving one from the other would make them agree by construction.
	served := map[string]bool{
		"/store/history": true, "/inbox": true, "/p/inbox": true,
		"/p/account": true, "/settings/privacy": true, "/settings/notifications": true,
		"/bookmarks": true, "/calendar": true, "/achievements": true,
		"/p/topics": true, "/p/posts": true,
		// A plugin page named on the menu by hand rather than left to the
		// generic tail — see navPlacedByHost (admin_views.go). /p/sign-ins is
		// deliberately absent: it is no longer registered, so a menu entry
		// pointing at it would 404.
		"/p/api-key": true,
	}
	// Walks GROUPS too. A group has no Href of its own, so checking only the
	// top level would cover almost nothing — most of the menu is grouped.
	var check func(tabs []sectionTab)
	check = func(tabs []sectionTab) {
		for _, tab := range tabs {
			if len(tab.Items) > 0 {
				if tab.Href != "" {
					t.Errorf("group %q has both an Href and children; "+
						"the Href is ignored when rendered", tab.Label)
				}
				check(tab.Items)
				continue
			}
			if !served[tab.Href] {
				t.Errorf("entry %q points at %s, which nothing serves", tab.Label, tab.Href)
			}
		}
	}
	check(accountMenu)
}

// Every account page must be ON the menu. This is the invariant that broke when
// the second bar was removed: eight pages — achievements, calendar, topics,
// posts, messages, points, privacy, alerts — were reachable ONLY from that bar,
// and deleting it without moving them would have orphaned every one.
func TestEveryAccountPageIsOnTheMenu(t *testing.T) {
	on := map[string]bool{}
	for _, tab := range accountMenu {
		if tab.Href != "" {
			on[tab.Href] = true
		}
		for _, it := range tab.Items {
			on[it.Href] = true
		}
	}
	for _, href := range []string{
		"/inbox", "/p/inbox", "/achievements", "/p/topics", "/p/posts",
		"/bookmarks", "/calendar", "/store/history",
		"/p/account", "/settings/privacy", "/settings/notifications",
	} {
		if !on[href] {
			t.Errorf("%s is on no menu entry — the page has no route in", href)
		}
	}
}

// A page inside a group must light the group, or the menu stops answering
// "where am I" for everything below the top level.
func TestAccountMenuLightsTheGroupAPageIsIn(t *testing.T) {
	for _, tc := range []struct{ path, group, item string }{
		{"/achievements", "Activity", "Achievements"},
		{"/p/topics", "Activity", "Topics"},
		{"/inbox", "Messages", "Inbox"},
		{"/settings/privacy", "Settings", "Privacy"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var found bool
			for _, tab := range accountNav(tc.path) {
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
				t.Fatalf("%s: no group %q on the account menu", tc.path, tc.group)
			}
		})
	}
}

// The package-level accountMenu is shared by every request. Marking an entry in
// place would leave one reader's active page lit for everyone — a bug that only
// shows under concurrency, so it is asserted directly instead.
func TestAccountMenuDoesNotMutateTheSharedEntries(t *testing.T) {
	_ = accountNav("/achievements")
	_ = accountNav("/store/history")
	for _, tab := range accountMenu {
		if tab.Active {
			t.Errorf("entry %q was marked active in the shared slice", tab.Label)
		}
		for _, it := range tab.Items {
			if it.Active {
				t.Errorf("item %q was marked active in the shared slice", it.Label)
			}
		}
	}
}

// The page you are on is marked, and only that one.
func TestAccountMenuSelectsOneEntry(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/settings/privacy", "Privacy"},
		{"/settings/notifications", "Alerts"},
		{"/achievements", "Achievements"},
		{"/calendar", "Calendar"},
		{"/p/inbox", "Notifications"},
		{"/inbox", "Inbox"},
		// The case a naive prefix match gets wrong: /store/history is the
		// viewer's own ledger. Nothing on this menu points at /store, so a
		// shorter href must not be invented for it either.
		{"/store/history", "Points"},
	} {
		// The DESTINATION is what the reader is looking for, and most of the
		// menu is grouped — so descend into a lit group and name its lit child,
		// keeping this an assertion about pages rather than about groups.
		var active []string
		for _, tab := range accountNav(tc.path) {
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
			t.Errorf("%s: %d active entries %v, want exactly 1", tc.path, len(active), active)
			continue
		}
		if active[0] != tc.want {
			t.Errorf("%s: active entry is %q, want %q", tc.path, active[0], tc.want)
		}
	}
}

// A page that is not on the menu at all must light nothing, rather than the
// nearest thing that shares a few characters with it.
func TestAccountMenuLightsNothingOffMenu(t *testing.T) {
	for _, p := range []string{"/", "/browse", "/community/forums", "/admin/settings", "/store"} {
		for _, tab := range accountNav(p) {
			if tab.Active {
				t.Errorf("%s: lit account entry %q", p, tab.Label)
			}
			for _, it := range tab.Items {
				if it.Active {
					t.Errorf("%s: lit account item %q", p, it.Label)
				}
			}
		}
	}
}

// Segment matching, not a bare string prefix: /settings/privacy must not be lit
// by a hypothetical /settings/privacy-policy, and /store must not light Points
// at /store/history's expense.
func TestAccountMenuMatchesWholeSegments(t *testing.T) {
	for _, tab := range accountNav("/settings/privacy-policy") {
		for _, it := range tab.Items {
			if it.Active {
				t.Errorf("/settings/privacy-policy lit %q on a partial-segment match", it.Label)
			}
		}
	}
}
