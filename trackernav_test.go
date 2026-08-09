package main

import (
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
	t.Setenv("LOON_DEMO_TRACKER", "")
	t.Setenv("LOON_DEMO_SEEDLOCK", "")
	if _, ok := trackerAccountGroup(); ok {
		t.Error("the tracker group appeared with no tracker — every link would 404")
	}

	// Tracker on, lock off: the two always-mounted pages, and NOT the lock page
	// — the plugin only mounts /seedlock inside its enabled branch.
	t.Setenv("LOON_DEMO_TRACKER", "1")
	g, ok := trackerAccountGroup()
	if !ok {
		t.Fatal("no tracker group with the tracker on")
	}
	if got := hrefs(g); len(got) != 2 || got[0] != "/hitrun" || got[1] != "/perks" {
		t.Errorf("hrefs = %v, want /hitrun and /perks only", got)
	}

	// Lock armed: the third page exists, so it may be listed.
	t.Setenv("LOON_DEMO_SEEDLOCK", "1")
	g, _ = trackerAccountGroup()
	if got := hrefs(g); len(got) != 3 || got[2] != "/seedlock" {
		t.Errorf("hrefs = %v, want /seedlock appended", got)
	}
}

// accountMenu is a package-level slice shared by every request. Appending to it
// in place would grow the menu by one group per page load — a bug that only
// shows up on a busy site, and then looks like a rendering fault.
func TestAccountNavDoesNotGrowTheSharedMenu(t *testing.T) {
	t.Setenv("LOON_DEMO_TRACKER", "1")
	before := len(accountMenu)
	for i := 0; i < 5; i++ {
		accountNav("/hitrun")
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
		t.Setenv("LOON_DEMO_SEEDLOCK", v)
		if !seedLockEnabled() {
			t.Errorf("seedLockEnabled() = false for %q", v)
		}
	}
	for _, v := range []string{"", "0", "no", "off"} {
		t.Setenv("LOON_DEMO_SEEDLOCK", v)
		if seedLockEnabled() {
			t.Errorf("seedLockEnabled() = true for %q", v)
		}
	}
	_ = os.Getenv
}
