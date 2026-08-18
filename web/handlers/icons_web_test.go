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
