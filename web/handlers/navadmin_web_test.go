package handlers

import (
	"testing"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

// assembleNav is what stands between the operator's rows and the site's
// menus; these hold its four filters and its bookkeeping still.
func TestAssembleNavFiltersAndMarks(t *testing.T) {
	defer flavourMode.Store(siteFlavour())
	flavourMode.Store(FlavourIndexer) // tracker half off

	rows := []storage.NavEntry{
		{Href: "/browse", Label: "Browse", Grp: "releases"},
		{Href: "/tracker", Label: "Torrents", Grp: "releases"},          // condition: off
		{Href: "/secret", Label: "Hidden", Grp: "releases", Hidden: true},
		{Href: "/pages/privacy", Label: "Privacy", Grp: "support"},
		{Href: "/lost", Label: "Lost", Grp: "no-such-group"},
	}
	items, active := assembleNav(rows, "/pages/privacy")

	if got := len(items["releases"]); got != 1 {
		t.Fatalf("releases has %d items, want 1 (hidden and flavour-gated rows dropped)", got)
	}
	if items["releases"][0].Href != "/browse" || items["releases"][0].Icon != "browse" {
		t.Errorf("releases[0] = %+v, want /browse with its own icon", items["releases"][0])
	}
	// A custom link gets the tag glyph; the row on the current path is
	// active, and lights its group.
	sup := items["support"]
	if len(sup) != 1 || sup[0].Icon != "tag" || !sup[0].Active {
		t.Fatalf("support = %+v, want the active tag-iconed privacy link", sup)
	}
	if !active["support"] || active["releases"] {
		t.Errorf("group actives = %v, want support only", active)
	}
	// An unknown group must not swallow the row: it surfaces under Other.
	if len(items["other"]) != 1 || items["other"][0].Href != "/lost" {
		t.Errorf("other = %+v, want the mis-grouped row rescued into it", items["other"])
	}
}

// With the tracker flavour on, the same rows keep the Torrents entry — the
// condition belongs to the system and follows the flavour live.
func TestAssembleNavFollowsTheFlavour(t *testing.T) {
	defer flavourMode.Store(siteFlavour())
	flavourMode.Store(FlavourBoth)
	items, _ := assembleNav([]storage.NavEntry{
		{Href: "/tracker", Label: "Torrents", Grp: "releases"},
	}, "/")
	if len(items["releases"]) != 1 {
		t.Fatal("the Torrents entry vanished with its flavour ON")
	}
}

// An empty mirror serves the shipped defaults — a broken settings table must
// never cost the site its navigation.
func TestNavRowsFallBackToDefaults(t *testing.T) {
	prev, _ := navMirror.Load().([]storage.NavEntry)
	defer navMirror.Store(prev)
	navMirror.Store([]storage.NavEntry{})
	if len(navRows()) != len(navDefaults) {
		t.Fatalf("empty mirror served %d rows, want the %d defaults", len(navRows()), len(navDefaults))
	}
}

func TestValidNavHref(t *testing.T) {
	for _, ok := range []string{"/pages/privacy", "/c", "https://example.org/x", "http://example.org"} {
		if !validNavHref(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "//evil.example", "javascript:alert(1)", "pages/privacy", "ftp://x"} {
		if validNavHref(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
