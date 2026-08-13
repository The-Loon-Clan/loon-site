package handlers

import (
	"testing"

	"github.com/the-loon-clan/loon-site/internal/storage"
	"github.com/the-loon-clan/loon/catalog"
)

// seriesKey is the single TV key, for the tests below that compare one value.
func seriesKey(releaseTitle string) string {
	ks := seriesKeys(releaseTitle)
	if len(ks) == 0 {
		return ""
	}
	return ks[0]
}

// Every episode of a show must reduce to the show — that is the whole basis for
// handing them a poster with no network call.
//
// The reduction is the SOURCE's ParseReleaseName rather than a second guess at
// it, because the entry on the other side of the join was created from that
// same function's output. Two different reductions would drift, and the failure
// would be a release wearing another show's poster.
func TestSiblingEpisodesReduceToTheSameSeries(t *testing.T) {
	for _, group := range [][]string{
		// Real Gullak releases from the index, in four different naming styles.
		{
			"Gullak.S01.E01-05.1080p.SONYLIV.WEB-DL.AAC2.0.H.264",
			"Gullak.S04E01.Kaaran.Batau.Notice.2160p.SONYLIV.WEB-DL",
			"Gullak S03E02 LTA 1080p SLIV WEB-DL x264 (AAC 2.0) [MULTi]-T0Bi",
			"Gullak.S01E01.Tehri.SonyLiv.WEB.DL.AVC.1080p.AAC.2.0",
		},
		{
			"Breaking.Bad.S05E14.Ozymandias.1080p.BluRay.x264-GRP",
			"Breaking Bad S01E01 720p HDTV",
		},
	} {
		want := seriesKey(group[0])
		if want == "" {
			t.Fatalf("%q reduced to nothing", group[0])
		}
		for _, title := range group[1:] {
			if got := seriesKey(title); got != want {
				t.Errorf("seriesKey(%q) = %q, want %q — siblings must share a key", title, got, want)
			}
		}
	}
}

// The key has to match what the catalog STORED, which is
// DefaultNormalize(entry title). If these disagree the join finds nothing and
// the pass silently does no work — the quiet failure, not a loud one.
func TestReleaseKeyMatchesTheStoredEntryKey(t *testing.T) {
	for _, tc := range []struct{ release, entryTitle string }{
		{"Gullak S03E02 LTA 1080p SLIV WEB-DL x264", "Gullak"},
		{"Breaking.Bad.S05E14.1080p.BluRay.x264-GRP", "Breaking Bad"},
		{"The.Office.US.S03E10.HDTV.XviD", "The Office US"},
		{"Doctor.Who.2005.S01E01.1080p.WEB-DL", "Doctor Who 2005"},
	} {
		got, want := seriesKey(tc.release), catalog.DefaultNormalize(tc.entryTitle)
		if got != want {
			t.Errorf("release %q -> %q, but entry %q stores %q",
				tc.release, got, tc.entryTitle, want)
		}
	}
}

// Distinct shows must NOT collide. A wrong poster is worse than none: it is a
// confident, plausible, incorrect statement that nothing downstream disagrees
// with, where no poster just leaves the page as it is.
func TestDifferentShowsDoNotShareAKey(t *testing.T) {
	keys := map[string]string{}
	for _, title := range []string{
		"Gullak.S01E01.1080p.WEB-DL",
		"The.Office.US.S03E10.HDTV",
		"The.Office.UK.S01E01.HDTV",
		"Doctor.Who.S01E01.1080p",
		"Doctor.Who.2005.S01E01.1080p",
		"Breaking.Bad.S01E01.720p",
		"Better.Call.Saul.S01E01.720p",
	} {
		k := seriesKey(title)
		if prev, clash := keys[k]; clash {
			t.Errorf("%q and %q both reduce to %q", prev, title, k)
		}
		keys[k] = title
	}
}

// Wikipedia disambiguates film articles and TVmaze does not, so a film release
// has to be allowed to match more than one stored title. 321 of the 1,029 film
// entries here are "Aquaman (film)" or "Dhoom Dhaam (2025 film)" rather than
// the bare name — on equality alone, none of them would ever match.
func TestMovieKeysCoverWikipediaQualifiers(t *testing.T) {
	got := movieKeys("Aquaman.2018.1080p.BluRay.x264-GRP")
	want := map[string]bool{"aquaman": true, "aquaman film": true, "aquaman 2018 film": true}
	if len(got) != len(want) {
		t.Fatalf("movieKeys = %v, want %d forms", got, len(want))
	}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected key %q (all: %v)", k, got)
		}
	}

	// With no year in the release name there is no year form to offer, and
	// inventing one would match an article about a different edition.
	if got := movieKeys("Aquaman.1080p.BluRay.x264"); len(got) != 2 {
		t.Errorf("movieKeys with no year = %v, want just the bare and film forms", got)
	}
}

// Every key is matched by EQUALITY, never as a prefix. "the crow" is a prefix
// of "the crow salvation", which is a different film — and a wrong poster is a
// confident, plausible, incorrect claim that nothing downstream contradicts,
// where no poster just leaves the page alone.
func TestMovieKeysNeverInviteAPrefixMatch(t *testing.T) {
	for _, k := range movieKeys("The.Crow.1994.1080p.BluRay.x264") {
		if k == "the crow salvation" {
			t.Fatalf("key %q would collide with a different film", k)
		}
	}
	// The keys for two genuinely different films must not overlap at all.
	a := map[string]bool{}
	for _, k := range movieKeys("The.Crow.1994.1080p.BluRay") {
		a[k] = true
	}
	for _, k := range movieKeys("The.Crow.Salvation.2000.1080p.BluRay") {
		if a[k] {
			t.Errorf("%q is a key for both The Crow and The Crow Salvation", k)
		}
	}
}

// Each spec sweeps with its OWN cursor. Sharing one would make each kind skip
// whatever the other had just walked past.
func TestLinkSpecsHaveDistinctCursors(t *testing.T) {
	seen := map[string]storage.SQL{}
	for _, s := range linkSpecs() {
		if prev, clash := seen[s.cursorKey]; clash {
			t.Errorf("%s and %s share cursor key %q", prev, s.catWhere, s.cursorKey)
		}
		seen[s.cursorKey] = s.catWhere
		if s.keys == nil || len(s.kinds) == 0 || s.catWhere == "" {
			t.Errorf("spec %+v is incomplete", s)
		}
	}
}

// Anime is parsed by the FANSUB convention, not the scene one. TVmaze's
// reduction expects "Show.S01E02" and would hand back the group tag for
// "[Erai-raws] Ragna Crimson - 04", so anime needs its own key builder.
func TestAnimeKeysUseTheFansubParser(t *testing.T) {
	for _, tc := range []struct{ release, want string }{
		{"[Erai-raws] Ragna Crimson - 04 [1080p][HEVC][2CDF803A]", "ragna crimson"},
		{"[ArAn - Kuraki-Subs] UFO Robo Grendizer - 67 [BD 1080p]", "ufo robo grendizer"},
		{"Crayon Shin-chan - 0059 - Hindi+Tamil+Telugu dub [ATTKC]", "crayon shin chan"},
	} {
		got := animeKeys(tc.release)
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("animeKeys(%q) = %v, want [%q]", tc.release, got, tc.want)
		}
	}
}

// Anime accepts entries of EITHER kind. AniList files a show under "anime" and
// TVmaze files the same show under "tv"; both are correct answers for the
// release, and refusing one would throw away the coverage anime already has.
func TestAnimeAcceptsBothKinds(t *testing.T) {
	var anime *linkSpec
	for i := range linkSpecs() {
		if s := linkSpecs()[i]; s.catWhere == "n.category_id = 5070" {
			anime = &s
		}
	}
	if anime == nil {
		t.Fatal("no spec covers category 5070")
	}
	got := map[string]bool{}
	for _, k := range anime.kinds {
		got[k] = true
	}
	if !got["anime"] || !got["tv"] {
		t.Errorf("anime kinds = %v, want both anime and tv", anime.kinds)
	}
}

// The TV spec must NOT also claim anime, or the two sweeps walk the same rows
// with different parsers and different cursors.
func TestTelevisionSpecExcludesAnime(t *testing.T) {
	for _, s := range linkSpecs() {
		if s.catWhere == "n.category_id / 1000 = 5 AND n.category_id <> 5070" {
			return
		}
	}
	t.Error("the television spec does not exclude 5070")
}
