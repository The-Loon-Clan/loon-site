package handlers

import (
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// What an NZB's file list PROVES about a release.
//
// The honest starting point for anything MediaInfo-shaped on an index: this
// site holds pointers to Usenet articles, not the bytes, so nothing here can
// open a file and read its bitrate. What it can do is read the file LIST, which
// an NZB always carries — names and sizes — and that turns out to answer a
// surprising amount of what somebody choosing between six copies of one episode
// actually wants:
//
//	is it a straight mkv or a rar set I have to unpack
//	are there subtitle files, and in what languages
//	how much recovery is posted with it
//	is it one episode or a pack
//	is a chunk of what I am downloading not the video
//
// Everything here is DERIVED and therefore true. What it cannot know — bitrate,
// audio channels, muxed subtitle tracks, chapters — is left to somebody who has
// actually downloaded the thing (the mediainfo plugin), and the two are drawn as
// separate panels so a reader can tell which is which.

// fileKind buckets a filename by what it is FOR.
type fileKind int

const (
	kindOther fileKind = iota
	kindVideo
	kindArchive
	kindRecovery
	kindSubtitle
	kindSample
	kindImage
	kindNFO
	kindAudio
)

// Extension sets. Deliberately literal rather than clever: a release is named by
// whoever posted it, and the only reliable thing about the name is its suffix.
var (
	videoExt   = map[string]bool{".mkv": true, ".mp4": true, ".avi": true, ".m4v": true, ".ts": true, ".m2ts": true, ".wmv": true, ".mov": true, ".iso": true, ".img": true}
	subExt     = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".idx": true, ".vtt": true, ".sup": true, ".smi": true}
	imageExt   = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true}
	// Audio is media too. Without this set an audiobook's files fell to
	// kindOther, which counts as neither media nor payload -- so the panel had
	// nothing to say about 16,000 releases and computed every one of them as
	// 0% payload. .m4b is the audiobook container and is NOT .m4v.
	audioExt = map[string]bool{".mp3": true, ".m4a": true, ".m4b": true, ".flac": true, ".ogg": true, ".opus": true, ".wav": true, ".aac": true, ".wma": true, ".ape": true, ".aiff": true, ".mka": true}
	archiveExt = map[string]bool{".rar": true, ".zip": true, ".7z": true, ".tar": true, ".gz": true}
)

// rarPart matches the numbered members of a rar set (.r00, .part01.rar).
var rarPart = regexp.MustCompile(`(?i)\.(r\d{2,3}|part\d+\.rar|\d{3})$`)

// classify buckets one filename.
func classify(name string) fileKind {
	lower := strings.ToLower(name)
	ext := path.Ext(lower)
	switch {
	// Sample FIRST: a sample is a video file, and counting it as one is how a
	// single-episode release reports two.
	case strings.Contains(lower, "sample"):
		return kindSample
	case ext == ".par2":
		return kindRecovery
	case ext == ".nfo":
		return kindNFO
	case subExt[ext]:
		return kindSubtitle
	case videoExt[ext]:
		return kindVideo
	case audioExt[ext]:
		return kindAudio
	case imageExt[ext]:
		return kindImage
	case archiveExt[ext], rarPart.MatchString(lower):
		return kindArchive
	}
	return kindOther
}

// langNames maps the codes that actually turn up in subtitle filenames.
//
// Both the two- and three-letter forms, because posters use both and a reader
// wants a language rather than a code. Not exhaustive by design: an unknown tag
// is reported as itself rather than guessed at, since "sv" rendered as the
// wrong language is worse than "sv".
var langNames = map[string]string{
	"en": "English", "eng": "English", "english": "English",
	"nl": "Dutch", "dut": "Dutch", "nld": "Dutch", "dutch": "Dutch",
	"fr": "French", "fre": "French", "fra": "French", "french": "French",
	"de": "German", "ger": "German", "deu": "German", "german": "German",
	"es": "Spanish", "spa": "Spanish", "spanish": "Spanish",
	"it": "Italian", "ita": "Italian", "italian": "Italian",
	"pt": "Portuguese", "por": "Portuguese", "portuguese": "Portuguese",
	"sv": "Swedish", "swe": "Swedish", "swedish": "Swedish",
	"no": "Norwegian", "nor": "Norwegian", "norwegian": "Norwegian",
	"da": "Danish", "dan": "Danish", "danish": "Danish",
	"fi": "Finnish", "fin": "Finnish", "finnish": "Finnish",
	"pl": "Polish", "pol": "Polish", "polish": "Polish",
	"ru": "Russian", "rus": "Russian", "russian": "Russian",
	"ja": "Japanese", "jpn": "Japanese", "japanese": "Japanese",
	"ko": "Korean", "kor": "Korean", "korean": "Korean",
	"zh": "Chinese", "chi": "Chinese", "zho": "Chinese", "chinese": "Chinese",
	"ar": "Arabic", "ara": "Arabic", "arabic": "Arabic",
	"cs": "Czech", "cze": "Czech", "czech": "Czech",
	"hu": "Hungarian", "hun": "Hungarian", "hungarian": "Hungarian",
	"tr": "Turkish", "tur": "Turkish", "turkish": "Turkish",
	"ro": "Romanian", "rum": "Romanian", "romanian": "Romanian",
	"el": "Greek", "gre": "Greek", "greek": "Greek",
	"he": "Hebrew", "heb": "Hebrew", "hebrew": "Hebrew",
	"th": "Thai", "tha": "Thai", "thai": "Thai",
}

// subModifiers are the tags that describe a subtitle rather than name its
// language, and they are a SEPARATE map for a reason a test found: with
// "forced" sitting in langNames, Show.S01E01.eng.forced.srt matched on the last
// token, returned "Forced" as though that were a language, and never looked
// back for the English behind it.
//
// "hi" is deliberately absent. It means hearing-impaired in one convention and
// Hindi in another, and there is no way to tell from a filename which was
// meant — so it falls through to the language map, where at least the guess is
// one somebody can recognise as wrong.
var subModifiers = map[string]string{
	"forced": "Forced", "sdh": "SDH", "cc": "SDH",
}

// subLang guesses a subtitle file's language from its name.
//
// The convention is the token before the extension — Show.S01E01.eng.srt — so
// that is what is read, and only that: scanning the whole filename for anything
// resembling a language code matches "de" inside "Der" and files a German
// subtitle for a Spanish release. Unrecognised comes back empty, and the caller
// says "unlabelled" rather than inventing one.
func subLang(name string) string {
	base := strings.TrimSuffix(path.Base(name), path.Ext(name))
	parts := strings.FieldsFunc(base, func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == ' '
	})
	if len(parts) == 0 {
		return ""
	}
	last := strings.ToLower(parts[len(parts)-1])
	// A MODIFIER first, because the language is then one token further back —
	// Show.S01E01.eng.forced.srt. Checked before the language map rather than
	// after, which is the ordering the eng.forced case needs.
	if mod, isMod := subModifiers[last]; isMod {
		if len(parts) >= 2 {
			if n, ok := langNames[strings.ToLower(parts[len(parts)-2])]; ok {
				return n + " (" + mod + ")"
			}
		}
		// A forced track whose language is not stated is still worth saying:
		// "Forced" tells a reader what the file is for.
		return mod
	}
	if n, ok := langNames[last]; ok {
		return n
	}
	return ""
}

// contentsVM is what the release page draws.
type contentsVM struct {
	// Container is what you actually get: "MKV", "RAR set", "ISO".
	Container string
	// MediaFiles is how many playable files, samples excluded.
	MediaFiles int
	// Subtitles are the separate subtitle files, by language.
	Subtitles []string
	// SubtitleFiles is how many there are, which is not len(Subtitles) — six
	// files can be three languages, and a reader wants both figures.
	SubtitleFiles int
	HasSample     bool
	HasNFO        bool
	// RecoveryPct is par2 bytes as a share of the whole post, rounded. The
	// figure a member reads as "how much damage can this survive".
	RecoveryPct int
	// PayloadPct is the media and archive share. Its absence from 100 is what
	// the structural-junk work is about: a post that is 40% recovery and 5%
	// images is a post where most of what you download is not the film.
	PayloadPct int
	// Files is the total count, for the panel's heading.
	Files int
}

// Meaningful reports whether there is anything worth drawing.
//
// It used to be Files > 0, which is not the same question and was letting
// through exactly the panel the comment promised to prevent: every fact below
// renders conditionally, so a post whose file list names nothing recognisable
// drew a heading, a file count, and an empty list. Audiobooks are full of
// them -- posters name the parts from the subject ("Lady Clementine 44-46"),
// so there is no extension to classify, and the reader got a panel that said
// less than the sidebar did. The file list itself is already drawn in its own
// panel directly below, so nothing is lost by staying quiet here.
//
// The conditions mirror the template's. That coupling is real, and pinned by
// a test rather than by hoping: a fact added to one and not the other either
// hides a panel that has something to say or draws an empty one again.
func (v contentsVM) Meaningful() bool {
	if v.Files == 0 {
		return false
	}
	return v.Container != "" || v.MediaFiles > 0 || v.SubtitleFiles > 0 ||
		v.RecoveryPct > 0 || (v.PayloadPct > 0 && v.PayloadPct < 70) ||
		v.HasSample || v.HasNFO
}

// describeFiles derives everything the file list can prove.
//
// Pure: it takes the list and returns the view model, which is what makes it
// testable without a database and is most of why it lives in its own file.
func describeFiles(files []pluginapi.ReleaseFile) contentsVM {
	var vm contentsVM
	vm.Files = len(files)
	if len(files) == 0 {
		return vm
	}

	var total, recovery, payload int64
	langs := map[string]bool{}
	mediaExts := map[string]bool{}
	archives := 0

	for _, f := range files {
		total += f.Bytes
		switch classify(f.Filename) {
		case kindVideo, kindAudio:
			vm.MediaFiles++
			payload += f.Bytes
			mediaExts[strings.ToUpper(strings.TrimPrefix(path.Ext(strings.ToLower(f.Filename)), "."))] = true
		case kindArchive:
			archives++
			payload += f.Bytes
		case kindRecovery:
			recovery += f.Bytes
		case kindSubtitle:
			vm.SubtitleFiles++
			if l := subLang(f.Filename); l != "" {
				langs[l] = true
			} else {
				langs["Unlabelled"] = true
			}
		case kindSample:
			vm.HasSample = true
		case kindNFO:
			vm.HasNFO = true
		}
	}

	for l := range langs {
		vm.Subtitles = append(vm.Subtitles, l)
	}
	// "Unlabelled" last, whatever it sorts as: it is the least useful entry and
	// a reader scanning for their language should not have to step over it.
	sort.Slice(vm.Subtitles, func(i, j int) bool {
		a, b := vm.Subtitles[i], vm.Subtitles[j]
		if (a == "Unlabelled") != (b == "Unlabelled") {
			return b == "Unlabelled"
		}
		return a < b
	})

	switch {
	case archives > 0 && vm.MediaFiles == 0:
		// A rar set: the video is inside and its name is not in the NZB.
		vm.Container = "RAR set"
	case len(mediaExts) == 1:
		for e := range mediaExts {
			vm.Container = e
		}
	case len(mediaExts) > 1:
		vm.Container = "Mixed"
	}

	if total > 0 {
		vm.RecoveryPct = int(float64(recovery)/float64(total)*100 + 0.5)
		vm.PayloadPct = int(float64(payload)/float64(total)*100 + 0.5)
	}
	return vm
}
