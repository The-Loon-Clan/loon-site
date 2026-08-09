package main

import (
	"testing"

	"github.com/the-loon-clan/loon-plugins/scraper/sources/tvmaze"
	"github.com/the-loon-clan/loon/catalog"
)

// seriesKey is what linkFromCatalog joins on: a release name reduced to the
// same normalised form catalog_entry stores in norm_title.
func seriesKey(releaseTitle string) string {
	return catalog.DefaultNormalize(tvmaze.ParseReleaseName(releaseTitle).Title)
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
