package main

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-baseline/session"
	"github.com/the-loon-clan/loon-baseline/webauth"
	"github.com/the-loon-clan/loon/core"
)

// The nav helpers decide what the header says about where you are. They fail
// silently — a wrong answer just unlights a tab or duplicates a menu — so the
// rules are pinned here rather than left to a screenshot.

// TestNavActiveMatchesChildPages is the reason navActive exists: /admin/settings
// grew per-section pages, and an equality test left the whole admin subnav
// unlit on every one of them.
func TestNavActiveMatchesChildPages(t *testing.T) {
	for _, tc := range []struct {
		path, href string
		want       bool
	}{
		{"/admin/settings", "/admin/settings", true},
		{"/admin/settings/usenet", "/admin/settings", true},
		{"/admin/settings/catalog", "/admin/settings", true},
		{"/admin/settings", "/admin/jobs", false},
		{"/admin/settings/usenet", "/admin/jobs", false},
		// Segment-aware, not a bare prefix: /admin/plugins is NOT a child of
		// /admin/plug, and /browse2 is not a child of /browse.
		{"/admin/plugins", "/admin/plug", false},
		{"/browse2", "/browse", false},
		// A parent is not lit by a sibling that merely shares a prefix.
		{"/admin/p/usenetx", "/admin/p/usenet", false},
		// Trailing slash on the href must not change the answer.
		{"/admin/settings/usenet", "/admin/settings/", true},
	} {
		if got := navActive(tc.path, tc.href); got != tc.want {
			t.Errorf("navActive(%q, %q) = %v, want %v", tc.path, tc.href, got, tc.want)
		}
	}
}

func TestInGroupFindsMergedPluginPages(t *testing.T) {
	m := map[string][]navItem{
		"Community": {{Href: "/p/stats", Label: "Site stats"}},
	}
	for _, tc := range []struct {
		group, path string
		want        bool
	}{
		{"Community", "/p/stats", true},
		// Child pages of a merged entry light the same menu.
		{"Community", "/p/stats/detail", true},
		{"Community", "/p/guestbook", false},
		// A group nothing merged into is simply absent from the map.
		{"Support", "/p/stats", false},
	} {
		if got := inGroup(m, tc.group, tc.path); got != tc.want {
			t.Errorf("inGroup(%q, %q) = %v, want %v", tc.group, tc.path, got, tc.want)
		}
	}
	// A nil map is what chromeData hands the template before any plugin
	// registers a grouped page; it must answer false, not panic.
	if inGroup(nil, "Community", "/p/stats") {
		t.Error("inGroup on a nil map should be false")
	}
}

// TestSiteNavMergesHostGroups is the duplicate-menu bug: the stats plugin
// declares Nav.Group "Community", the host writes its own Community dropdown,
// and the bar carried both.
func TestSiteNavMergesHostGroups(t *testing.T) {
	// Public views, so the anonymous viewer below sees every one of them —
	// role filtering is not what this test is about.
	pub := core.View{Public: true}
	w := &web{siteNavEntries: []siteNavEntry{
		// Sorted the way wireViews sorts them: by group, then weight.
		{href: "/p/loose", label: "Loose", group: "", view: pub},
		{href: "/p/stats", label: "Site stats", group: "Community", view: pub},
		{href: "/p/logs", label: "Logs", group: "Operations", view: pub},
		{href: "/p/backup", label: "Backup", group: "Operations", view: pub},
		{href: "/p/solo", label: "Solo", group: "Solitary", view: pub},
	}}
	// siteNav resolves the viewer off the request, and that read goes through
	// the session middleware — so this runs a real (anonymous) request rather
	// than a bare test context.
	gin.SetMode(gin.TestMode)
	w.auth = webauth.Auth{Session: session.Config{Secret: []byte("test-secret-test-secret-abc")}}
	var nodes []navNode
	var merged map[string][]navItem
	e := gin.New()
	e.Use(w.auth.Session.Middleware())
	e.GET("/", func(c *gin.Context) { nodes, merged = w.siteNav(c) })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	if got := merged["Community"]; len(got) != 1 || got[0].Href != "/p/stats" {
		t.Errorf(`merged["Community"] = %+v, want the one /p/stats entry`, got)
	}
	for _, n := range nodes {
		if n.Label == "Community" {
			t.Error("Community became a top-level node as well as a merge — that is the duplicate dropdown")
		}
	}

	// Everything else keeps the old shape: ungrouped pages stay plain links, a
	// multi-page group stays its own dropdown, a one-page group flattens.
	byLabel := map[string]navNode{}
	for _, n := range nodes {
		byLabel[n.Label] = n
	}
	if n, ok := byLabel["Loose"]; !ok || n.Href != "/p/loose" || n.Children != nil {
		t.Errorf("ungrouped page should stay a plain link, got %+v", n)
	}
	if n, ok := byLabel["Operations"]; !ok || len(n.Children) != 2 {
		t.Errorf("multi-page group should stay a dropdown, got %+v", n)
	}
	if n, ok := byLabel["Solo"]; !ok || n.Href != "/p/solo" || n.Children != nil {
		t.Errorf("one-page group should flatten to a link (no one-item dropdowns), got %+v", n)
	}
	if _, ok := byLabel["Solitary"]; ok {
		t.Error("a one-page group should not also keep its group node")
	}
}

// TestSettingsViewFallsBack: /admin/settings has no slug, and a bookmark can
// outlive the plugin it points at. Both land on a section rather than nothing.
func TestSettingsViewFallsBack(t *testing.T) {
	w := &web{}
	if _, ok := w.settingsView("usenet"); ok {
		t.Error("no registered sections should resolve to nothing, not to a zero View")
	}
}
