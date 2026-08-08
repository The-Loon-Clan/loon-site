package main

import "sync/atomic"

// Attribution for the metadata sources actually in use.
//
// This is a licence obligation, not a courtesy. TVmaze publishes its data under
// CC BY-SA 4.0 — "data can freely be used for any purpose, as long as TVmaze is
// properly credited as source" — and the site was using it, on 80,000 TV
// releases, while crediting nobody. TMDB requires a specific disclaimer of its
// own. Both are conditions of use, and neither is met by intending to.
//
// Built from the sources the host ACTUALLY registered rather than hardcoded,
// because a credit for a source that is not running is worse than none: it
// claims a provenance the data does not have. A deployment with only a TMDB key
// should not thank TVmaze, and one with no keys at all should not thank TMDB.

// sourceCredit is one provider's required attribution.
type sourceCredit struct {
	Name string
	URL  string
	// Text is the wording the provider asks for, or a plain credit where they
	// ask for nothing specific. Rendered as written — a licence that specifies
	// wording means that wording.
	Text string
}

// creditsByProvider maps a PROVIDER key to its attribution.
//
// Keyed by provider, not by domain, and that distinction has teeth: two
// different providers can serve one domain. "movie" is TMDB when a key is set
// and Wikipedia when it is not, so a domain-keyed credit thanked TMDB for
// Wikipedia's data — an attribution naming the wrong source is worse than
// none, since it is a false statement about provenance rather than a missing
// one. main.go names the provider as it registers it.
var creditsByProvider = map[string]sourceCredit{
	"tvmaze": {
		Name: "TVmaze",
		URL:  "https://www.tvmaze.com",
		// The licence names the condition: credit TVmaze as the source. It is
		// also ShareAlike, which matters if this data is ever redistributed.
		Text: "TV data from TVmaze, CC BY-SA 4.0",
	},
	"tmdb": {
		Name: "TMDB",
		URL:  "https://www.themoviedb.org",
		// TMDB's required disclaimer, near enough verbatim: the point is that
		// the site must not imply endorsement.
		Text: "This product uses the TMDB API but is not endorsed or certified by TMDB",
	},
	"wikipedia": {
		Name: "Wikipedia",
		URL:  "https://en.wikipedia.org",
		// Also CC BY-SA, like TVmaze, and for the same reason it is named
		// rather than thanked vaguely: the licence asks for the source.
		Text: "Film data from Wikipedia, CC BY-SA",
	},
	"openlibrary": {
		Name: "Open Library",
		URL:  "https://openlibrary.org",
		Text: "Book data from Open Library, an Internet Archive project",
	},
	"anidb": {
		Name: "AniDB",
		URL:  "https://anidb.net",
		Text: "Anime data from AniDB",
	},
	"theporndb": {
		Name: "ThePornDB",
		URL:  "https://theporndb.net",
		Text: "Data from ThePornDB",
	},
}

// activeCredits holds the credits for this process's registered sources,
// resolved once at boot and read on every page render.
var activeCredits atomic.Value // []sourceCredit

func init() { activeCredits.Store([]sourceCredit{}) }

// setSourceCredits records which providers are live. main.go passes a provider
// key for each source it registers.
//
// Duplicates are dropped because TMDB registers TWICE off one key (movie and tv
// are separate sources), and a host would otherwise thank it twice in the
// footer.
func setSourceCredits(providers []string) {
	seen := map[string]bool{}
	out := []sourceCredit{}
	for _, d := range providers {
		c, ok := creditsByProvider[d]
		if !ok || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		out = append(out, c)
	}
	activeCredits.Store(out)
}

// sourceCredits returns the live credits for the footer.
func sourceCredits() []sourceCredit {
	c, _ := activeCredits.Load().([]sourceCredit)
	return c
}
