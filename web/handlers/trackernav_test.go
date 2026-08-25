package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/config"

	"os"
	"testing"
)

// The account menu and the sitemap must not offer a page the site does not
// serve.
//
// This matters more than a usual dead link. These three are exactly the pages a
// member is SENT to by an error message — "clear the lock on the site", "see
// /hitrun for what you owe" — so a 404 here lands on somebody who is already
// confused and now has reason to think the site is broken.
func TestTrackerNavOnlyOffersPagesThatExist(t *testing.T) {
	// The group reads the site FLAVOUR now, not LOON_TRACKER — the env flag
	// only seeds a fresh database's first answer (flavour_web.go).
	defer flavourMode.Store(siteFlavour())
	flavourMode.Store(FlavourIndexer)
	t.Setenv("LOON_SEEDLOCK", "")
	if _, ok := trackerAccountGroup(); ok {
		t.Error("the tracker group appeared with no tracker — every link would 404")
	}

	// Tracker on, lock off: the two always-mounted pages, and NOT the lock page
	// — the plugin only mounts /seedlock inside its enabled branch.
	flavourMode.Store(FlavourBoth)
	g, ok := trackerAccountGroup()
	if !ok {
		t.Fatal("no tracker group with the tracker on")
	}
	if got := hrefs(g); len(got) != 2 || got[0] != "/hitrun" || got[1] != "/perks" {
		t.Errorf("hrefs = %v, want /hitrun and /perks only", got)
	}

	// Lock armed: the third page exists, so it may be listed.
	t.Setenv("LOON_SEEDLOCK", "1")
	g, _ = trackerAccountGroup()
	if got := hrefs(g); len(got) != 3 || got[2] != "/seedlock" {
		t.Errorf("hrefs = %v, want /seedlock appended", got)
	}
}

// accountMenu is a package-level slice shared by every request. Appending to it
// in place would grow the menu by one group per page load — a bug that only
// shows up on a busy site, and then looks like a rendering fault.
func TestAccountNavDoesNotGrowTheSharedMenu(t *testing.T) {
	t.Setenv("LOON_TRACKER", "1")
	before := len(accountMenu)
	for i := 0; i < 5; i++ {
		accountNav("/hitrun", true)
	}
	if got := len(accountMenu); got != before {
		t.Errorf("accountMenu grew from %d to %d across five renders", before, got)
	}
}

// The pages have to be inside the account AREA, or they render without the
// account bar the rest of a member's own pages carry.
func TestTrackerPagesAreInTheAccountArea(t *testing.T) {
	for _, p := range []string{"/hitrun", "/perks", "/seedlock"} {
		if !inAccountArea(p) {
			t.Errorf("%s is not in the account area — it would lose the account bar", p)
		}
	}
}

func hrefs(g sectionTab) []string {
	out := make([]string, 0, len(g.Items))
	for _, it := range g.Items {
		out = append(out, it.Href)
	}
	return out
}

// Guard against the env flags drifting apart from what compose forwards.
func TestTrackerFlagsAreReadable(t *testing.T) {
	for _, v := range []string{"1", "true", "yes"} {
		t.Setenv("LOON_SEEDLOCK", v)
		if !config.SeedLockEnabled() {
			t.Errorf("config.SeedLockEnabled() = false for %q", v)
		}
	}
	for _, v := range []string{"", "0", "no", "off"} {
		t.Setenv("LOON_SEEDLOCK", v)
		if config.SeedLockEnabled() {
			t.Errorf("config.SeedLockEnabled() = true for %q", v)
		}
	}
	_ = os.Getenv
}

// The announce base a .torrent carries, and the order it is resolved in.
//
// LOON_BASE_URL is the case worth pinning: it was added after the doc in
// .env.example claimed the fallback already worked that way. Before it, a
// deployment that had set its address correctly still minted torrents
// announcing to localhost, and nothing said so — the .torrent parses, the row
// is right, and the member's client simply reports the tracker dead.
func TestTrackerSiteURLPrefersTheMostSpecificAddress(t *testing.T) {
	for _, tc := range []struct {
		name, site, base, want string
	}{
		{"explicit wins", "https://tracker.example", "https://www.example", "https://tracker.example"},
		{"falls back to the site's own base", "", "https://www.example", "https://www.example"},
		{"and to localhost, so a bare compose up works", "", "", "http://localhost:8090"},
	} {
		t.Setenv("LOON_SITE_URL", tc.site)
		t.Setenv("LOON_BASE_URL", tc.base)
		if got := trackerSiteURL(); got != tc.want {
			t.Errorf("%s: trackerSiteURL() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
