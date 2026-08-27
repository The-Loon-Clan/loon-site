package handlers

import (
	"testing"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The audiobook case. Before kindAudio existed, .mp3 and .m4b fell to
// kindOther, which counts as neither media nor payload -- so "What is in it"
// had nothing to say about 68% of this index's 16,000 audio releases, and
// computed every one of them at 0% payload. The panel was only saved from
// PRINTING that 0% by the template treating zero as absent.
func TestDescribeFilesCountsAudioAsMedia(t *testing.T) {
	vm := describeFiles([]pluginapi.ReleaseFile{
		{Filename: "The.Maze-01.mp3", Bytes: 40 << 20},
		{Filename: "The.Maze-02.mp3", Bytes: 40 << 20},
		{Filename: "The.Maze.par2", Bytes: 8 << 20},
	})
	if vm.MediaFiles != 2 {
		t.Errorf("MediaFiles = %d, want 2", vm.MediaFiles)
	}
	if vm.Container != "MP3" {
		t.Errorf("Container = %q, want MP3", vm.Container)
	}
	// 80 of 88 MB is the audio itself.
	if vm.PayloadPct < 85 || vm.PayloadPct > 95 {
		t.Errorf("PayloadPct = %d, want ~91 (was 0 before audio counted)", vm.PayloadPct)
	}
	if vm.RecoveryPct < 5 || vm.RecoveryPct > 15 {
		t.Errorf("RecoveryPct = %d, want ~9", vm.RecoveryPct)
	}
}

// .m4b is the audiobook container and must not be confused with .m4v, which is
// video. They differ by one character and sit in different maps.
func TestClassifyAudiobookExtensions(t *testing.T) {
	for _, c := range []struct {
		name string
		want fileKind
	}{
		{"book.m4b", kindAudio},
		{"film.m4v", kindVideo},
		{"track.flac", kindAudio},
		{"track.opus", kindAudio},
		{"disc.iso", kindVideo},
		{"cover.jpg", kindImage},
		{"readme.txt", kindOther},
	} {
		if got := classify(c.name); got != c.want {
			t.Errorf("classify(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// A release carrying both kinds reports Mixed rather than picking one. The
// container line answers "what do you actually get", and "MP3" on a release
// that is mostly video would be a lie.
func TestDescribeFilesMixedMediaIsMixed(t *testing.T) {
	vm := describeFiles([]pluginapi.ReleaseFile{
		{Filename: "show.mkv", Bytes: 2 << 30},
		{Filename: "commentary.mp3", Bytes: 60 << 20},
	})
	if vm.Container != "Mixed" {
		t.Errorf("Container = %q, want Mixed", vm.Container)
	}
	if vm.MediaFiles != 2 {
		t.Errorf("MediaFiles = %d, want 2", vm.MediaFiles)
	}
}

// A rar set still reports as one: audio hiding inside an archive is no more
// visible than video is, and the existing rule must not have been widened.
func TestDescribeFilesRarSetStillWins(t *testing.T) {
	vm := describeFiles([]pluginapi.ReleaseFile{
		{Filename: "pack.part01.rar", Bytes: 500 << 20},
		{Filename: "pack.part02.rar", Bytes: 500 << 20},
	})
	if vm.Container != "RAR set" {
		t.Errorf("Container = %q, want RAR set", vm.Container)
	}
}

// A post whose file list names nothing recognisable must draw no panel at all.
// Audiobook posters name the parts from the subject -- "Lady Clementine 44-46",
// no extension -- so every conditional fact is empty and the panel was a
// heading over a blank list, saying less than the sidebar beside it.
func TestContentsNotMeaningfulWithoutAFact(t *testing.T) {
	vm := describeFiles([]pluginapi.ReleaseFile{
		{Filename: "Lady Clementine 44-46", Bytes: 5 << 20},
		{Filename: "Lady Clementine 47-49", Bytes: 5 << 20},
	})
	if vm.Files != 2 {
		t.Fatalf("Files = %d, want 2", vm.Files)
	}
	if vm.Meaningful() {
		t.Error("panel would draw with no fact in it")
	}
}

// ...but one recognisable fact is enough to earn the panel. par2 alongside an
// otherwise unreadable list still answers "how much damage can this survive".
func TestContentsMeaningfulOnASingleFact(t *testing.T) {
	vm := describeFiles([]pluginapi.ReleaseFile{
		{Filename: "unreadable-name-here", Bytes: 90 << 20},
		{Filename: "recovery.par2", Bytes: 10 << 20},
	})
	if !vm.Meaningful() {
		t.Error("a stated recovery share is a fact; the panel should draw")
	}
}

// The coupling between Meaningful() and the template is real: every fact the
// panel can draw must be one Meaningful() counts, or a release with only that
// fact gets no panel (silently losing information) and one with none gets an
// empty panel again. This walks the fields the template branches on.
func TestMeaningfulCoversEveryDrawnFact(t *testing.T) {
	for name, vm := range map[string]contentsVM{
		"container": {Files: 1, Container: "MP3"},
		"media":     {Files: 1, MediaFiles: 1},
		"subtitles": {Files: 1, SubtitleFiles: 1},
		"recovery":  {Files: 1, RecoveryPct: 9},
		"payload":   {Files: 1, PayloadPct: 40}, // drawn only below 70
		"sample":    {Files: 1, HasSample: true},
		"nfo":       {Files: 1, HasNFO: true},
	} {
		if !vm.Meaningful() {
			t.Errorf("%s alone should earn the panel; the template draws it", name)
		}
	}
	// A healthy payload share is NOT drawn (the template hides 70 and above),
	// so on its own it must not earn a panel either.
	if (contentsVM{Files: 1, PayloadPct: 92}).Meaningful() {
		t.Error("a healthy payload share is not drawn, so it cannot justify the panel")
	}
}
