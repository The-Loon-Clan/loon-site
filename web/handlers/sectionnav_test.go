package handlers

import (
	site "github.com/the-loon-clan/loon-site"

	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

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
		"/settings/profile": true, "/settings/security": true,
		"/bookmarks": true, "/calendar": true, "/achievements": true, "/subscriptions": true,
		"/gifts": true, "/wishlist": true,
		"/p/topics": true, "/p/posts": true,
		// A plugin page named on the menu by hand rather than left to the
		// generic tail — see navPlacedByHost (admin_views.go). /p/sign-ins is
		// deliberately absent: it is no longer registered, so a menu entry
		// pointing at it would 404.
		"/p/api-key": true,
		// The points economy, served by the store plugin rather than by
		// w.mount — same category as /p/api-key above: named by hand because
		// the host cannot see the route, verified by hand because of that.
		"/store": true, "/rewards": true, // /store/history is already listed above
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
	// /store/history is absent on purpose: it belongs to the points economy,
	// which carries its own strip, and it is one click from every page via the
	// top nav's points pill. Listing it here as well is what gave that page two
	// tab rows.
	for _, href := range []string{
		"/inbox", "/p/inbox", "/achievements", "/p/topics", "/p/posts",
		"/bookmarks", "/calendar",
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
		// Bonus Points, moved here out of Community. /store/history is the
		// interesting one: a shorter /store must not steal it, which is the
		// longest-match rule this menu shares with the site nav.
		{"/store", "Points store"},
		{"/store/history", "History"},
		{"/rewards", "Rewards"},
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
	// /store used to be here and is not any more: it is a menu destination
	// under Bonus Points, so lighting is what it is SUPPOSED to do. The
	// positive case for it lives in the highlight table above.
	for _, p := range []string{"/", "/browse", "/community/forums", "/admin/settings"} {
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

// The account bar appears across the account area and nowhere else. Both halves
// matter: absent on a release page it would be the site-wide second bar coming
// back, and absent on an account page every entry it carries is unreachable,
// because the avatar menu no longer lists them.
func TestAccountBarScope(t *testing.T) {
	// Signed in, and on /u/ it is your own profile — the case the bar is for.
	for _, p := range []string{
		"/u/alice", "/achievements", "/calendar", "/bookmarks", "/inbox",
		"/p/inbox", "/p/topics", "/p/posts", "/p/account", "/p/api-key",
		"/settings/privacy", "/settings/notifications",
	} {
		if accountBar(p, true, true) == nil {
			t.Errorf("%s is in the account area but gets no bar", p)
		}
	}
	for _, p := range []string{
		"/", "/browse", "/search", "/community/forums", "/store", "/stats",
		"/about", "/login", "/admin/settings", "/p/stats", "/p/store",
	} {
		if tabs := accountBar(p, true, true); tabs != nil {
			t.Errorf("%s got an account bar (%d entries) — that is the site-wide second bar again", p, len(tabs))
		}
	}
}

// The bar is the VIEWER's own navigation, so it needs a viewer. Reported as
// "the public profile view is broken": every entry — Inbox, Privacy, Security,
// API key — was rendered to anonymous visitors on every member's profile,
// because the bar was chosen by path alone. Nothing leaked (the links are
// viewer-relative and all bounce to /login), but a stranger was shown a menu of
// someone else's settings.
func TestAccountBarNeedsAViewer(t *testing.T) {
	for _, p := range []string{
		"/u/alice", "/achievements", "/inbox", "/settings/privacy", "/p/api-key",
	} {
		if tabs := accountBar(p, false, false); tabs != nil {
			t.Errorf("signed OUT on %s got %d account entries — a personal menu shown to a stranger", p, len(tabs))
		}
	}
}

// Someone else's profile is not your account area. The /u/ prefix earns its
// place as the area's landing page only on the way to your OWN profile; on
// another member's it is a menu about you attached to a page about them.
func TestAccountBarOnlyOnYourOwnProfile(t *testing.T) {
	if tabs := accountBar("/u/bob", true, false); tabs != nil {
		t.Errorf("viewing another member's profile got %d account entries", len(tabs))
	}
	if accountBar("/u/alice", true, true) == nil {
		t.Error("your own profile must still carry the bar — it is the area's landing page")
	}
	// The children of someone else's profile behave the same way.
	if tabs := accountBar("/u/bob/followers", true, false); tabs != nil {
		t.Errorf("another member's followers page got %d account entries", len(tabs))
	}
}

// profileNameFromPath is what decides "own profile", so its edges are pinned:
// a non-profile path yields nothing, and the children of a profile still
// resolve to the profile's owner.
func TestProfileNameFromPath(t *testing.T) {
	cases := map[string]string{
		"/u/alice":           "alice",
		"/u/alice/followers": "alice",
		"/u/alice/following": "alice",
		"/u/bob/friends":     "bob",
		"/u/":                "",
		"/u":                 "",
		"/inbox":             "",
		"/":                  "",
		"/browse":            "",
	}
	for path, want := range cases {
		if got := profileNameFromPath(path); got != want {
			t.Errorf("profileNameFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// The points pages carry the STORE's strip (Store | History | Rewards), which
// its own templates render. They must not also be in the account area, or the
// page gets two rows of tabs disagreeing about where the reader is — which is
// exactly what happened when the account bar first went in.
func TestPointsPagesGetOneStripNotTwo(t *testing.T) {
	for _, p := range []string{"/store", "/store/history", "/rewards"} {
		if tabs := accountBar(p, true, true); tabs != nil {
			t.Errorf("%s gets the account bar AND the store strip — two tab rows on one page", p)
		}
	}
}

// Every destination the bar carries must be in the area the bar covers, or
// clicking it navigates away from the bar that offered it.
func TestAccountBarKeepsItsOwnDestinationsInScope(t *testing.T) {
	var check func(tabs []sectionTab)
	check = func(tabs []sectionTab) {
		for _, tab := range tabs {
			if len(tab.Items) > 0 {
				check(tab.Items)
				continue
			}
			if !inAccountArea(tab.Href) {
				t.Errorf("bar entry %q -> %s leaves the account area, so the bar vanishes on arrival",
					tab.Label, tab.Href)
			}
		}
	}
	check(accountMenu)
}

// Every internal link the CHROME offers must be a route the site serves.
//
// This is the check that used to exist as part of the section-nav test and went
// with it when that table shrank to the account menu — and /sitemap 404'd from
// the moment it did, linked from both the Other dropdown and the footer with
// nothing serving it. Restored against the chrome itself rather than against a
// table, so it covers whatever the chrome links, not whatever a table happens
// to list.
func TestChromeLinksAreServed(t *testing.T) {
	b, err := fs.ReadFile(site.FS, "web/templates/site_chrome.html")
	if err != nil {
		t.Fatal(err)
	}
	// Routes the host or a wired plugin registers. Explicit on purpose: the
	// point is to catch a typo in EITHER list, and deriving one from the other
	// would make them agree by construction.
	served := map[string]bool{
		"/": true, "/browse": true, "/search": true, "/groups": true, "/trending": true,
		"/community/forums": true, "/community/forums/new": true, "/c": true,
		"/news": true, "/playlists": true, "/store": true, "/store/history": true,
		"/rules": true, "/faq": true, "/wiki": true, "/support": true, "/staff": true,
		"/stats": true, "/about": true, "/sitemap": true, "/sitemap.xml": true,
		"/inbox": true, "/p/inbox": true, "/p/account": true, "/p/api-key": true,
		"/p/topics": true, "/p/posts": true, "/bookmarks": true, "/calendar": true,
		"/achievements": true, "/rewards": true, "/subscriptions": true,
		"/gifts": true, "/invites": true, "/wishlist": true,
		"/settings/privacy": true, "/settings/notifications": true, "/settings/profile": true,
		"/settings/security": true,
		"/login":             true, "/logout": true, "/register": true, "/forgot": true,
		"/admin/settings": true, "/verify/resend": true,
		// The admin dashboard: the account dropdown's single staff door for an
		// admin, and the landing page whose subnav names every queue and tool.
		// See docs/NAVIGATION.md.
		"/admin": true,
		// The dropdown's staff door for a MODERATOR, who cannot be sent to
		// /admin (it gates at RoleAdmin) and so lands on the one queue they can
		// reach.
		"/moderation/avatars": true,
		// Member-facing: the community votes here.
		"/moderation": true,
		// Admin-only, linked from the admin subnav.
		"/admin/contracts": true,
		// The Newznab endpoint, linked bare from the footer as well as with a
		// ?t=caps query.
		"/api": true,
		// Rendered only while .DonateEnabled, which is the env flag AND the
		// admin toggle — but when it renders, it has to resolve.
		"/help/donate": true,
		// Data-source attribution (credits_web.go), linked from the footer.
		// Rendered only when a source registered, but when it renders it has
		// to resolve — the link is how the licence's credit is reachable.
		"/credits": true,
		// The tracker plugin's own index, linked from the top bar's ratio
		// figures. Served by the plugin rather than the host, and mounted only
		// when LOON_TRACKER is set — which is exactly the condition under
		// which the figures render at all (chromeData -> storage.ReadTrackerTotals), so
		// the link and the route appear and disappear together.
		"/tracker": true,
	}
	// Skip anything with template syntax in it (/u/{{...}}), a query or a
	// fragment — those are not paths this list can speak about.
	href := regexp.MustCompile(`href="(/[^"?#{]*)"`)
	seen := map[string]bool{}
	for _, m := range href.FindAllStringSubmatch(string(b), -1) {
		p := m[1]
		if p == "" || seen[p] || strings.HasPrefix(p, "/static/") {
			continue
		}
		seen[p] = true
		if !served[p] {
			t.Errorf("the chrome links %s, which nothing in this list serves — "+
				"either the route is missing or this list is stale", p)
		}
	}
	if len(seen) < 15 {
		t.Errorf("only %d chrome links found; the scan is probably not matching", len(seen))
	}
}

// One word must not name two destinations.
//
// Written after the third time it happened. "Store" was the flair shop in the
// account menu and the points shop one menu away; "Stats" was the host's hub in
// Other and the plugin's snapshot in Community. Each was reported by a reader
// who could not tell from the menu which one they wanted, which is the only way
// this gets found — nothing 404s, nothing errors, the menu simply lies about
// how many places there are to go.
//
// A collision is the same label pointing at DIFFERENT hrefs. The same label
// twice for the same page is not one: the footer and the nav are allowed to
// offer the same destination, and usually should.
func TestNoTwoDestinationsShareALabel(t *testing.T) {
	b, err := fs.ReadFile(site.FS, "web/templates/site_chrome.html")
	if err != nil {
		t.Fatal(err)
	}

	// label -> the hrefs it leads to, and where each was written, so a failure
	// names the two files to open rather than just the word.
	dest := map[string]map[string]string{}
	add := func(label, href, where string) {
		label = strings.TrimSpace(label)
		if label == "" || href == "" {
			return
		}
		if dest[label] == nil {
			dest[label] = map[string]string{}
		}
		dest[label][href] = where
	}

	// The chrome's hand-written links. The label sits after the icon, so the
	// tags and template actions come out and what is left is what a reader
	// sees: <a ... href="/stats" ...><svg ...><use .../></svg>Stats</a>.
	link := regexp.MustCompile(`(?s)<a [^>]*href="(/[^"?#{]*)"[^>]*>(.*?)</a>`)
	strip := regexp.MustCompile(`(?s)<[^>]*>|\{\{.*?\}\}`)
	for _, m := range link.FindAllStringSubmatch(string(b), -1) {
		if strings.HasPrefix(m[1], "/static/") {
			continue
		}
		add(strip.ReplaceAllString(m[2], ""), m[1], "site_chrome.html")
	}

	// The account menu, which the template renders from this slice.
	var walk func(tabs []sectionTab)
	walk = func(tabs []sectionTab) {
		for _, tab := range tabs {
			if len(tab.Items) > 0 {
				walk(tab.Items) // the GROUP is a heading, not a destination
				continue
			}
			add(tab.Label, tab.Href, "sectionnav_web.go")
		}
	}
	walk(accountMenu)

	// And the host's renames of plugin pages, which is where both collisions
	// were fixed and so the most likely place to reintroduce one.
	for href, p := range navPlacement {
		add(p.Label, href, "navPlacement (admin_views.go)")
	}

	for label, hrefs := range dest {
		if len(hrefs) < 2 {
			continue
		}
		var where []string
		for href, src := range hrefs {
			where = append(where, href+" ("+src+")")
		}
		sort.Strings(where) // map order, and a failing test must read the same twice
		t.Errorf("%q names %d different destinations: %s — a reader picking from "+
			"the menu cannot tell which one they are opening",
			label, len(hrefs), strings.Join(where, ", "))
	}

	if len(dest) < 20 {
		t.Errorf("only %d labels collected; the scan is probably not matching", len(dest))
	}
}
