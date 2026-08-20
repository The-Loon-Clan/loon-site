package handlers

import (
	"reflect"
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

func f(name string, mb int64) pluginapi.ReleaseFile {
	return pluginapi.ReleaseFile{Filename: name, Bytes: mb * 1024 * 1024}
}

// TestClassifyPutsSampleBeforeVideo is the one ordering that matters in
// classify: a sample IS an .mkv, so checking the extension first counts it as a
// second episode and a single-file release reports two media files.
func TestClassifyPutsSampleBeforeVideo(t *testing.T) {
	if got := classify("Show.S01E01.sample.mkv"); got != kindSample {
		t.Errorf("sample.mkv classified as %v, want kindSample", got)
	}
	if got := classify("Show.S01E01.mkv"); got != kindVideo {
		t.Errorf("plain mkv classified as %v, want kindVideo", got)
	}
}

func TestClassifyRarSets(t *testing.T) {
	for _, name := range []string{"x.rar", "x.r00", "x.r15", "x.part01.rar", "x.001"} {
		if got := classify(name); got != kindArchive {
			t.Errorf("%s classified as %v, want kindArchive", name, got)
		}
	}
}

func TestSubLangReadsTheTokenBeforeTheExtension(t *testing.T) {
	cases := map[string]string{
		"Show.S01E01.eng.srt":        "English",
		"Show.S01E01.nl.srt":         "Dutch",
		"Show.S01E01.Dutch.srt":      "Dutch",
		"Show_S01E01_fre.ass":        "French",
		"Show.S01E01.eng.forced.srt": "English (Forced)",
		"Show.S01E01.srt":            "", // no tag: unlabelled, never guessed
		"Show.S01E01.zz.srt":         "", // unknown code: reported as unknown
	}
	for name, want := range cases {
		if got := subLang(name); got != want {
			t.Errorf("subLang(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestSubLangDoesNotScanTheWholeName is the bug the "last token only" rule
// exists to prevent: a language code hides inside ordinary words, and matching
// anywhere files a German subtitle for a Spanish release.
func TestSubLangDoesNotScanTheWholeName(t *testing.T) {
	if got := subLang("Der.Untergang.2004.spa.srt"); got != "Spanish" {
		t.Errorf("got %q, want Spanish — 'Der' must not win over the real tag", got)
	}
}

func TestDescribeFilesOnAStraightMKV(t *testing.T) {
	got := describeFiles([]pluginapi.ReleaseFile{
		f("Show.S01E01.1080p.mkv", 1000),
		f("Show.S01E01.1080p.mkv.par2", 1),
		f("Show.S01E01.1080p.mkv.vol000+200.par2", 99),
	})
	if got.Container != "MKV" {
		t.Errorf("container = %q, want MKV", got.Container)
	}
	if got.MediaFiles != 1 {
		t.Errorf("media files = %d, want 1", got.MediaFiles)
	}
	if got.RecoveryPct != 9 {
		t.Errorf("recovery = %d%%, want 9%%", got.RecoveryPct)
	}
	if got.PayloadPct != 91 {
		t.Errorf("payload = %d%%, want 91%%", got.PayloadPct)
	}
}

func TestDescribeFilesOnARarSet(t *testing.T) {
	got := describeFiles([]pluginapi.ReleaseFile{
		f("abc.rar", 500), f("abc.r00", 500), f("abc.r01", 500),
		f("abc.nfo", 1),
		f("abc.par2", 1), f("abc.vol000+100.par2", 200),
	})
	// The video's name is inside the archive and not in the NZB, so "how many
	// episodes" is genuinely unknown — and must be reported as unknown rather
	// than as zero, which would read as "there is no video in this".
	if got.Container != "RAR set" {
		t.Errorf("container = %q, want RAR set", got.Container)
	}
	if got.MediaFiles != 0 {
		t.Errorf("media files = %d, want 0 (unknowable inside an archive)", got.MediaFiles)
	}
	if !got.HasNFO {
		t.Error("nfo not spotted")
	}
}

func TestDescribeFilesCountsLanguagesNotFiles(t *testing.T) {
	got := describeFiles([]pluginapi.ReleaseFile{
		f("Show.S01E01.mkv", 1000),
		f("Show.S01E01.eng.srt", 1),
		f("Show.S01E01.eng.forced.srt", 1),
		f("Show.S01E01.nl.srt", 1),
		f("Show.S01E01.srt", 1),
	})
	if got.SubtitleFiles != 4 {
		t.Errorf("subtitle files = %d, want 4", got.SubtitleFiles)
	}
	want := []string{"Dutch", "English", "English (Forced)", "Unlabelled"}
	if !reflect.DeepEqual(got.Subtitles, want) {
		t.Errorf("languages = %v, want %v", got.Subtitles, want)
	}
}

// TestUnlabelledSortsLast — a reader scanning for their own language should not
// have to step over the entry that tells them nothing.
func TestUnlabelledSortsLast(t *testing.T) {
	got := describeFiles([]pluginapi.ReleaseFile{
		f("a.srt", 1), f("a.zulu.srt", 1), f("a.eng.srt", 1),
	})
	if len(got.Subtitles) == 0 || got.Subtitles[len(got.Subtitles)-1] != "Unlabelled" {
		t.Errorf("languages = %v, want Unlabelled last", got.Subtitles)
	}
}

// TestSampleIsNotAnEpisode — the whole reason classify checks for "sample"
// before it checks the extension.
func TestSampleIsNotAnEpisode(t *testing.T) {
	got := describeFiles([]pluginapi.ReleaseFile{
		f("Show.S01E01.mkv", 1000),
		f("Show.S01E01-sample.mkv", 20),
	})
	if got.MediaFiles != 1 {
		t.Errorf("media files = %d, want 1 — the sample is not an episode", got.MediaFiles)
	}
	if !got.HasSample {
		t.Error("sample not spotted")
	}
}

// TestNoFileListDrawsNothing. An NZB without a file list is common enough on an
// old index, and a panel of blanks reads as broken rather than as unknown.
func TestNoFileListDrawsNothing(t *testing.T) {
	if describeFiles(nil).Meaningful() {
		t.Error("an empty file list should not be meaningful")
	}
}
