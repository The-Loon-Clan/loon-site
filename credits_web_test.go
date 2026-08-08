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

	setSourceCredits([]string{"tvmaze", "openlibrary"})
	got := creditNames(sourceCredits())
	if got != "TVmaze,Open Library" {
		t.Errorf("credits = %q, want TVmaze,Open Library", got)
	}

	// Nothing registered: credit nobody.
	setSourceCredits(nil)
	if n := len(sourceCredits()); n != 0 {
		t.Errorf("%d credits with no sources registered", n)
	}

	// A provider with no credit entry (a source added later) must not panic or
	// invent one.
	setSourceCredits([]string{"musicbrainz", "igdb"})
	if n := len(sourceCredits()); n != 0 {
		t.Errorf("%d credits for providers with no attribution defined", n)
	}
}

// The bug that made these credits provider-keyed instead of domain-keyed: two
// providers serve the "movie" domain — TMDB with a key, Wikipedia without — so
// a domain-keyed lookup credited TMDB for data Wikipedia supplied. Naming the
// wrong source is a false statement about provenance, which is worse than
// saying nothing.
func TestTheMovieDomainCreditsWhicheverProviderRan(t *testing.T) {
	prev := sourceCredits()
	t.Cleanup(func() { activeCredits.Store(prev) })

	setSourceCredits([]string{"tvmaze", "wikipedia"})
	got := creditNames(sourceCredits())
	if got != "TVmaze,Wikipedia" {
		t.Errorf("keyless host credits %q, want TVmaze,Wikipedia", got)
	}
	if strings.Contains(got, "TMDB") {
		t.Error("credited TMDB on a host that never registered it")
	}

	setSourceCredits([]string{"tmdb", "tmdb"})
	if got := creditNames(sourceCredits()); got != "TMDB" {
		t.Errorf("keyed host credits %q, want TMDB alone", got)
	}
}

// TMDB registers TWICE off one key (movie and tv are separate sources), so a
// TMDB-enabled host would otherwise thank it twice in the footer.
func TestTMDBIsCreditedOnce(t *testing.T) {
	prev := sourceCredits()
	t.Cleanup(func() { activeCredits.Store(prev) })

	// TMDB registers once per domain off ONE key, so the provider key arrives
	// twice and must collapse to a single credit.
	setSourceCredits([]string{"tmdb", "tmdb"})
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
	tv := creditsByProvider["tvmaze"]
	if !strings.Contains(tv.Text, "TVmaze") || !strings.Contains(tv.Text, "CC BY-SA") {
		t.Errorf("TVmaze credit %q must name TVmaze and its licence", tv.Text)
	}
	// TMDB asks specifically that the site not imply endorsement.
	movie := creditsByProvider["tmdb"]
	if !strings.Contains(movie.Text, "not endorsed or certified by TMDB") {
		t.Errorf("TMDB credit %q is missing the required disclaimer", movie.Text)
	}
	for domain, c := range creditsByProvider {
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
