package main

import "testing"

// The button order is fixed. A map would render them in a different order on
// every request, which reads as the page flickering rather than as a feature.
func TestExternalLinkOrderIsStable(t *testing.T) {
	ids := map[string]string{
		"tvmaze": "82",
		"imdb":   "tt0944947",
		"tvdb":   "121361",
	}
	want := []string{"IMDb", "TheTVDB", "TVmaze"}
	for i := 0; i < 20; i++ {
		got := buildExternalLinks("tv", ids)
		if len(got) != len(want) {
			t.Fatalf("got %d links, want %d", len(got), len(want))
		}
		for j, w := range want {
			if got[j].Label != w {
				t.Fatalf("link %d = %q, want %q", j, got[j].Label, w)
			}
		}
	}
}

func TestExternalLinkURLs(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		ns   string
		id   string
		want string
	}{
		{"imdb keeps its tt prefix", "tv", "imdb", "tt0944947", "https://www.imdb.com/title/tt0944947/"},
		{"imdb gains a missing prefix", "tv", "imdb", "0944947", "https://www.imdb.com/title/tt0944947/"},
		{"tvmaze", "tv", "tvmaze", "82", "https://www.tvmaze.com/shows/82"},
		{"tvdb series", "tv", "tvdb", "121361", "https://thetvdb.com/dereferrer/series/121361"},
		{"anidb", "anime", "anidb", "5114", "https://anidb.net/anime/5114"},
		{"openlibrary work", "book", "openlibrary", "OL45804W", "https://openlibrary.org/works/OL45804W"},
		{"wikipedia page key", "movie", "wikipedia", "The_Matrix", "https://en.wikipedia.org/wiki/The_Matrix"},
		// TMDB keys the path off the media type, so the same id points at two
		// different pages depending on kind.
		{"tmdb film", "movie", "tmdb", "603", "https://www.themoviedb.org/movie/603"},
		{"tmdb series", "tv", "tmdb", "1399", "https://www.themoviedb.org/tv/1399"},
		{"tmdb anime is a series", "anime", "tmdb", "1399", "https://www.themoviedb.org/tv/1399"},
	} {
		// Looked up by URL rather than by position: a single id can now yield
		// more than one button, since a TMDB id also derives the Letterboxd
		// link. Asserting a count here would break every time a site is added.
		got := buildExternalLinks(tc.kind, map[string]string{tc.ns: tc.id})
		found := false
		for _, l := range got {
			if l.URL == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no link with URL %q (got %+v)", tc.name, tc.want, got)
		}
	}
}

// An id that arrives with a slash or a space must not break out of the path it
// is interpolated into.
func TestExternalLinkIDsAreEscaped(t *testing.T) {
	got := buildExternalLinks("movie", map[string]string{"wikipedia": "Fast/Furious 9"})
	if len(got) != 1 {
		t.Fatalf("got %d links, want 1", len(got))
	}
	if want := "https://en.wikipedia.org/wiki/Fast%2FFurious%209"; got[0].URL != want {
		t.Errorf("URL = %q, want %q", got[0].URL, want)
	}
}

// A namespace with no id, and one we have no link builder for, are both simply
// absent — not rendered as a dead button.
func TestExternalLinksSkipWhatItCannotLink(t *testing.T) {
	got := buildExternalLinks("tv", map[string]string{
		"imdb":      "tt0944947",
		"tvmaze":    "", // present but empty
		"someothdb": "999",
		"":          "abc",
	})
	if len(got) != 1 || got[0].Label != "IMDb" {
		t.Fatalf("got %+v, want only the IMDb link", got)
	}
}

// Rotten Tomatoes and Metacritic store their ids WITH the section prefix
// ("m/matrix", "movie/the-matrix"), so the slash is part of the path and must
// survive. Escaping it produces a 404.
func TestPrefixedIDsKeepTheirSlash(t *testing.T) {
	for _, tc := range []struct{ ns, id, want string }{
		{"rottentomatoes", "m/matrix", "https://www.rottentomatoes.com/m/matrix"},
		{"rottentomatoes", "m/parasite_2019", "https://www.rottentomatoes.com/m/parasite_2019"},
		{"metacritic", "movie/the-matrix", "https://www.metacritic.com/movie/the-matrix"},
	} {
		got := buildExternalLinks("movie", map[string]string{tc.ns: tc.id})
		if len(got) != 1 || got[0].URL != tc.want {
			t.Errorf("%s: got %+v, want %s", tc.ns, got, tc.want)
		}
	}
}

// Letterboxd is derived from the TMDB id: Wikidata's own Letterboxd property
// is a bare number that letterboxd.com/film/<n>/ rejects with a 404, while
// /tmdb/<id>/ resolves for every film checked.
func TestLetterboxdIsDerivedFromTMDB(t *testing.T) {
	ids := map[string]string{"tmdb": "545611"}
	got := buildExternalLinks("movie", ids)
	var found string
	for _, l := range got {
		if l.Label == "Letterboxd" {
			found = l.URL
		}
	}
	if want := "https://letterboxd.com/tmdb/545611/"; found != want {
		t.Errorf("Letterboxd URL = %q, want %q (all: %+v)", found, want, got)
	}
	// The caller's map is theirs — deriving a link must not mutate it.
	if _, leaked := ids["letterboxd"]; leaked {
		t.Error("buildExternalLinks wrote letterboxd back into the caller's map")
	}
	// Television is not on Letterboxd, so a TV entry gets no such button.
	for _, l := range buildExternalLinks("tv", map[string]string{"tmdb": "1399"}) {
		if l.Label == "Letterboxd" {
			t.Error("a TV entry got a Letterboxd button")
		}
	}
}
