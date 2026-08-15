package handlers

import (
	"strings"
	"testing"
)

// A scene series name often carries a disambiguating YEAR, and the catalogue
// does not.
//
// Found by measuring, not by reading: of the twelve largest TV series in this
// index with ZERO covered releases, eight were "<name> <year>" — The Flash
// 2014 (265 releases), CID 1998 (188), The Middle 2009 (182), Hot in Cleveland
// 2010, Monk 2002, The Walking Dead 2010, Ben 10 2016, JoJos Bizarre Adventure
// 2012. Every one of those shows was ALREADY in catalog_entry, under the bare
// name with its year in the year column. Hundreds of releases sat uncovered
// beside the poster that belonged to them, and no API call was ever going to
// fix it, because nothing was wrong with the data on either side — only with
// the key joining them.
//
// The year is not simply dropped. Two different shows share a name and differ
// only by year (The Flash 1990 and 2014 both exist), so the bare form carries
// the year as a CONSTRAINT: match the name, and require the entry's year to
// agree. Dropping it outright would hand one show the other's poster, which
// this file's own movieKeys comment already calls worse than no poster.
func TestSeriesKeysOfferTheBareNameWhenTheSceneNameCarriesAYear(t *testing.T) {
	for _, tc := range []struct {
		release  string
		wantNorm string
		wantYear int
	}{
		{"Monk.2002.S06E09.1080p.NF.WEB-DL.H264.SDR.DDP.5.1.English-HONE", "monk", 2002},
		{"The.Flash.2014.S05E01.1080p.WEB-DL.DDP5.1.H.264-NTb", "the flash", 2014},
		{"The.Walking.Dead.2010.S03E04.720p.BluRay.x264-GRP", "the walking dead", 2010},
		{"Ben.10.2016.S02E11.1080p.HMAX.WEB-DL.DD2.0.H.264-NTb", "ben 10", 2016},
	} {
		keys := seriesKeys(tc.release)
		if len(keys) == 0 {
			t.Errorf("%s: no keys at all", tc.release)
			continue
		}
		// The full parsed form stays FIRST. It is the more specific match, and
		// a catalogue that really does hold "Monk 2002" under that name should
		// win over the bare one.
		if strings.Contains(keys[0].Norm, tc.wantNorm) == false {
			t.Errorf("%s: first key is %q, expected it to contain %q",
				tc.release, keys[0].Norm, tc.wantNorm)
		}
		var found *catalogKey
		for i := range keys {
			if keys[i].Norm == tc.wantNorm {
				found = &keys[i]
			}
		}
		if found == nil {
			var got []string
			for _, k := range keys {
				got = append(got, k.Norm)
			}
			t.Errorf("%s: no key %q — got %v. The catalogue holds this show under "+
				"the bare name, so nothing here can ever match it.",
				tc.release, tc.wantNorm, got)
			continue
		}
		if found.Year != tc.wantYear {
			t.Errorf("%s: bare key %q carries year %d, want %d — without the year "+
				"it could take another show's poster",
				tc.release, found.Norm, found.Year, tc.wantYear)
		}
	}
}

// A series whose name genuinely ends in a number must not lose it.
//
// "Ben 10" is the trap in both directions: the 2016 reboot is "Ben.10.2016",
// and the show itself is "Ben 10". A rule that strips any trailing number
// turns the second into "ben", which matches nothing — or worse, something.
// Only a plausible four-digit YEAR is a candidate for stripping.
func TestSeriesKeysDoNotStripNumbersThatAreNotYears(t *testing.T) {
	for _, release := range []string{
		"Ben.10.S03E05.720p.WEB-DL.x264-GRP",
		"Kids.Incorporated.S01E01.480p.DVD.x264-GRP",
		"Catch.22.S01E02.1080p.AMZN.WEB-DL-NTb",
		"Blake.7.S02E01.576p.DVD.x264-GRP",
	} {
		for _, k := range seriesKeys(release) {
			if k.Year != 0 && (k.Year < 1900 || k.Year > 2100) {
				t.Errorf("%s: produced key %q with implausible year %d", release, k.Norm, k.Year)
			}
		}
		keys := seriesKeys(release)
		if len(keys) > 0 && keys[0].Norm == "" {
			t.Errorf("%s: normalised to nothing", release)
		}
	}
}
