package handlers

import (
	"strings"
	"testing"
)

// The catalogue is read from the sprite sheet so it cannot disagree with what
// the site can actually draw. That is the whole reason it is not a hand-kept
// list in Go, and this is the check that keeps it honest — a picker offering
// an id with no symbol behind it hands an operator a blank badge.
func TestSiteIconsComeFromTheSpriteSheet(t *testing.T) {
	icons := siteIcons()
	if len(icons) < 20 {
		t.Fatalf("%d icons parsed, want the sprite sheet's ~40 — has the markup changed?", len(icons))
	}
	// Ids the rest of the codebase draws by name; if the parse breaks, these
	// go first and every picker built on it empties out.
	for _, want := range []string{"shield", "star", "verified", "coin", "check", "users"} {
		found := false
		for _, got := range icons {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the catalogue is missing %q, which templates already use", want)
		}
	}
	// Sorted, because a dropdown is scanned by eye.
	if !sortedStrings(icons) {
		t.Error("the catalogue is not alphabetical")
	}
	// No duplicates and nothing malformed: each entry ends up in an href="#id".
	seen := map[string]bool{}
	for _, id := range icons {
		if seen[id] {
			t.Errorf("duplicate id %q", id)
		}
		seen[id] = true
		if strings.ContainsAny(id, ` "<>#`) {
			t.Errorf("id %q would not survive being written into an href", id)
		}
	}
}

func sortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

// An unlocked achievement takes the operator's icon INSTEAD of the state tick,
// so a name with no symbol behind it does not degrade to a default — it draws
// an empty space, and only on the unlocked row. That is how the "Night Watch"
// badge came to have no icon while every locked one looked right: its row held
// "moon", which this sprite sheet does not have.
//
// The check is at the DRAW rather than at the admin form because a row can
// hold a name the form would never have offered — a seed, an SQL fix and an
// older version of the form all write straight past a picker.
func TestDrawableIconGuardsWhatTheSiteCanActuallyDraw(t *testing.T) {
	// If this ever fails the guard below would pass by finding nothing, so it
	// is asserted rather than assumed.
	if !drawableIcon("verified") {
		t.Fatal("the sprite sheet has no #verified")
	}
	for _, bad := range []string{
		"moon",               // the real one: on night-watch, drew nothing
		"trophy",             // the icon a badge most obviously wants and this sheet lacks
		"",                   // no icon set at all
		"Star",               // right symbol, wrong case — ids are exact
		"/uploads/badge.png", // an image path in an icon field
	} {
		if drawableIcon(bad) {
			t.Errorf("drawableIcon(%q) claims this site can draw it", bad)
		}
	}
}
