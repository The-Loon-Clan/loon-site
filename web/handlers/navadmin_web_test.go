package handlers

import (
	"testing"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

func navTestState() navState {
	return navState{
		Groups: []storage.NavGroup{
			{Key: "releases", Label: "Releases", Icon: "browse", Ordinal: 10, Placement: "top", Builtin: true},
			{Key: "other", Label: "Other", Icon: "folder", Ordinal: 40, Placement: "top", Builtin: true},
			{Key: "footer-index", Label: "Index", Ordinal: 110, Placement: "footer", Builtin: true},
		},
		Entries: []storage.NavEntry{
			{ID: 1, Href: "/browse", Label: "Browse", Grp: "releases"},
			{ID: 2, Href: "/tracker", Label: "Torrents", Grp: "releases"},
			{ID: 3, Href: "/secret", Label: "Hidden", Grp: "releases", Hidden: true},
			{ID: 4, Href: "/pages/privacy", Label: "Privacy", Grp: "no-such-group"},
			{ID: 5, Href: "/browse", Label: "Browse", Grp: "footer-index"},
		},
	}
}

// assembleNav is what stands between the operator's rows and everything the
// chrome draws; these hold its filters and bookkeeping still.
func TestAssembleNavFiltersAndMarks(t *testing.T) {
	defer flavourMode.Store(siteFlavour())
	flavourMode.Store(FlavourIndexer) // tracker half off

	tabs, footer := assembleNav(navTestState(), "/pages/privacy", nil, nil, nil)

	if len(tabs) != 2 {
		t.Fatalf("got %d tabs, want 2 (releases + other)", len(tabs))
	}
	rel := tabs[0]
	if len(rel.Items) != 1 || rel.Items[0].Href != "/browse" || rel.Items[0].Icon != "browse" {
		t.Fatalf("releases items = %+v, want /browse alone (hidden and flavour-gated dropped)", rel.Items)
	}
	// The orphaned row is rescued into Other, active on this path, tag-iconed.
	oth := tabs[1]
	if len(oth.Items) != 1 || oth.Items[0].Icon != "tag" || !oth.Items[0].Active || !oth.Active {
		t.Fatalf("other = %+v (active=%v), want the active tag-iconed rescue", oth.Items, oth.Active)
	}
	// The footer column renders separately with its own row.
	if len(footer) != 1 || footer[0].Label != "Index" || footer[0].Items[0].Href != "/browse" {
		t.Fatalf("footer = %+v, want the Index column with /browse", footer)
	}
}

// A group label with a catalogue slug resolves per viewer; the plain label
// is every other viewer's fallback.
func TestAssembleNavLocalisesGroupLabels(t *testing.T) {
	st := navTestState()
	st.Groups[0].LabelSlug = "nav.releases"
	resolve := func(slug string) (string, bool) {
		if slug == "nav.releases" {
			return "リリース", true
		}
		return "", false
	}
	tabs, _ := assembleNav(st, "/", resolve, nil, nil)
	if tabs[0].Label != "リリース" {
		t.Errorf("label = %q, want the resolved catalogue text", tabs[0].Label)
	}
	tabs, _ = assembleNav(st, "/", nil, nil, nil)
	if tabs[0].Label != "Releases" {
		t.Errorf("label = %q, want the plain fallback with no resolver", tabs[0].Label)
	}
}

// A hidden group vanishes whole; an empty tab does not render at all — a
// dropdown with nothing in it is a dead control.
func TestAssembleNavSkipsHiddenAndEmptyGroups(t *testing.T) {
	st := navTestState()
	st.Groups[0].Hidden = true  // releases hidden
	st.Entries = st.Entries[:0] // nothing anywhere
	tabs, footer := assembleNav(st, "/", nil, nil, nil)
	if len(tabs) != 0 || len(footer) != 0 {
		t.Fatalf("tabs=%d footer=%d, want nothing rendered", len(tabs), len(footer))
	}
}

// Plugin pages merged into a tab count toward its active state and keep the
// tab alive even when every operator row is hidden.
func TestAssembleNavMergesPluginPages(t *testing.T) {
	st := navState{
		Groups: []storage.NavGroup{
			{Key: "community", Label: "Community", Ordinal: 10, Placement: "top", Builtin: true},
		},
	}
	plug := map[string][]navItem{"Community": {{Href: "/p/events", Label: "Events"}}}
	tabs, _ := assembleNav(st, "/p/events", nil, plug, nil)
	if len(tabs) != 1 || !tabs[0].Active || len(tabs[0].PluginItems) != 1 {
		t.Fatalf("tabs = %+v, want the community tab alive and active via its plugin page", tabs)
	}
}

// An empty mirror serves the shipped defaults — a broken settings table must
// never cost the site its navigation.
func TestCurrentNavFallsBackToDefaults(t *testing.T) {
	prev, _ := navMirror.Load().(navState)
	defer navMirror.Store(prev)
	navMirror.Store(navState{})
	st := currentNav()
	if len(st.Groups) != len(navGroupDefaults) || len(st.Entries) != len(navDefaults) {
		t.Fatalf("empty mirror served %d/%d rows, want the shipped %d/%d",
			len(st.Groups), len(st.Entries), len(navGroupDefaults), len(navDefaults))
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
