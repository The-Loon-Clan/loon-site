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
		got := buildExternalLinks(tc.kind, map[string]string{tc.ns: tc.id})
		if len(got) != 1 {
			t.Fatalf("%s: got %d links, want 1", tc.name, len(got))
		}
		if got[0].URL != tc.want {
			t.Errorf("%s: URL = %q, want %q", tc.name, got[0].URL, tc.want)
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
