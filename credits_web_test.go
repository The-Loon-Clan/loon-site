package main

import (
	"os"
	"strings"
	"testing"
)

// Attribution is a licence CONDITION for two of the sources in use — TVmaze
// publishes under CC BY-SA 4.0 and TMDB requires its own disclaimer — so these
// assert the credit exists and is honest, not that it looks nice.

// A credit for a source that is not running claims a provenance the data does
// not have, which is worse than no credit at all.
func TestCreditsFollowTheRegisteredSources(t *testing.T) {
	prev := sourceCredits()
	t.Cleanup(func() { activeCredits.Store(prev) })

	setSourceCredits([]string{"tv", "book"})
	got := creditNames(sourceCredits())
	if got != "TVmaze,Open Library" {
		t.Errorf("credits = %q, want TVmaze,Open Library", got)
	}

	// Nothing registered: credit nobody.
	setSourceCredits(nil)
	if n := len(sourceCredits()); n != 0 {
		t.Errorf("%d credits with no sources registered", n)
	}

	// A domain with no credit entry (a source added later) must not panic or
	// invent one.
	setSourceCredits([]string{"music", "games"})
	if n := len(sourceCredits()); n != 0 {
		t.Errorf("%d credits for domains with no attribution defined", n)
	}
}

// TMDB registers TWICE off one key (movie and tv are separate sources), so a
// TMDB-enabled host would otherwise thank it twice in the footer.
func TestTMDBIsCreditedOnce(t *testing.T) {
	prev := sourceCredits()
	t.Cleanup(func() { activeCredits.Store(prev) })

	setSourceCredits([]string{"movie", "tv"})
	// With TMDB serving both domains the map yields TMDB for movie and TVmaze
	// for tv; the duplicate guard is on NAME, so this checks the shape rather
	// than the specific pair.
	names := sourceCredits()
	seen := map[string]bool{}
	for _, c := range names {
		if seen[c.Name] {
			t.Errorf("%s credited twice", c.Name)
		}
		seen[c.Name] = true
	}
}

// The wording providers specify is not ours to paraphrase.
func TestRequiredWordingIsPresent(t *testing.T) {
	tv := creditsByDomain["tv"]
	if !strings.Contains(tv.Text, "TVmaze") || !strings.Contains(tv.Text, "CC BY-SA") {
		t.Errorf("TVmaze credit %q must name TVmaze and its licence", tv.Text)
	}
	// TMDB asks specifically that the site not imply endorsement.
	movie := creditsByDomain["movie"]
	if !strings.Contains(movie.Text, "not endorsed or certified by TMDB") {
		t.Errorf("TMDB credit %q is missing the required disclaimer", movie.Text)
	}
	for domain, c := range creditsByDomain {
		if c.Name == "" || c.URL == "" || c.Text == "" {
			t.Errorf("credit for %q is incomplete: %+v", domain, c)
		}
		if !strings.HasPrefix(c.URL, "https://") {
			t.Errorf("credit for %q links %q, want https", domain, c.URL)
		}
	}
}

// The footer must actually render them, or the whole mechanism is a variable
// nothing reads.
func TestFooterRendersTheCredits(t *testing.T) {
	b, err := os.ReadFile("web/templates/site_chrome.html")
	if err != nil {
		t.Skipf("no chrome template: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "<footer")
	if i < 0 {
		t.Fatal("no <footer> in the chrome")
	}
	if !strings.Contains(src[i:], ".SourceCredits") {
		t.Error("the footer does not render .SourceCredits — attribution is a licence " +
			"condition for TVmaze (CC BY-SA 4.0) and TMDB, and this is where it is met")
	}
}

func creditNames(cs []sourceCredit) string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}
