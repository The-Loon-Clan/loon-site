package handlers

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The Newznab endpoint's plumbing.
//
// /api and /rss are the contract with Sonarr, Radarr and every other tool
// somebody has pointed at this site. They are also the only endpoints reachable
// with an api key rather than a session, which means they answer to callers the
// site has no other relationship with.
//
// None of these four functions had a test. One of them is a security boundary.

func TestAFilenameCannotBreakOutOfItsHeader(t *testing.T) {
	// Content-Disposition is built by concatenation:
	//
	//     `attachment; filename="` + sanitizeFilename(name) + `"`
	//
	// and the name comes from a release title, which comes from a Usenet post,
	// which comes from anybody with a news account. A quote closes the
	// parameter early; a CR or LF ends the header and starts writing whatever
	// comes next as though the server had sent it.
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"a quote closes the filename", `Movie".mkv`},
		{"a backslash escapes the quote", `Movie\".mkv`},
		{"CRLF starts a new header", "Movie\r\nX-Injected: yes.nzb"},
		{"a bare newline", "Movie\nX-Injected: yes.nzb"},
		{"a carriage return", "Movie\rX-Injected: yes.nzb"},
		{"a NUL", "Movie\x00.nzb"},
		{"a tab", "Movie\t.nzb"},
		{"an escape", "Movie\x1b[31m.nzb"},
	} {
		got := sanitizeFilename(tc.in)
		for _, bad := range []string{"\r", "\n", `"`, `\`, "\x00", "\t", "\x1b"} {
			if strings.Contains(got, bad) {
				t.Errorf("%s: sanitizeFilename(%q) = %q, still contains %q",
					tc.name, tc.in, got, bad)
			}
		}
	}
}

func TestSanitizingKeepsTheNameReadable(t *testing.T) {
	// Stripping is only defensible if what survives is still the file somebody
	// asked for. Deleting too much would leave a downloads folder full of
	// ".nzb" and no way to tell them apart.
	got := sanitizeFilename("Blade.Runner.2049.2017.2160p.UHD.BluRay-GROUP.nzb")
	if got != "Blade.Runner.2049.2017.2160p.UHD.BluRay-GROUP.nzb" {
		t.Errorf("an ordinary release name was altered: %q", got)
	}

	// Non-ASCII survives too: releases are named in every language, and a
	// filename is not a place to enforce an alphabet.
	if got := sanitizeFilename("Дюна.2021.nzb"); got != "Дюна.2021.nzb" {
		t.Errorf("a non-ASCII name was mangled: %q", got)
	}
}

func TestTheHeaderIsBuiltFromTheSanitisedName(t *testing.T) {
	// The unit above proves the function; this proves it is actually the thing
	// on the wire. A sanitiser that exists and is not called is worse than
	// none, because it reads like the problem is handled.
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api", nil)

	writeNewznab(c, pluginapi.NewznabResult{
		Filename:    "Movie\r\nX-Injected: yes\".nzb",
		ContentType: "application/x-nzb",
		Body:        []byte("<nzb/>"),
	}, "MISS")

	cd := rec.Header().Get("Content-Disposition")

	// The LINE BREAKS are what matter, not the words between them. Once the CR
	// and LF are gone, "X-Injected: yes" is a peculiar filename and nothing
	// more. This test asserted the phrase was absent on its first outing, which
	// was a stricter rule than the defence needs — and a test that demands more
	// than the thing it guards is a test that gets relaxed later by somebody
	// who cannot tell which half was load-bearing.
	for _, bad := range []string{"\r", "\n"} {
		if strings.Contains(cd, bad) {
			t.Errorf("Content-Disposition = %q, still carries %q", cd, bad)
		}
	}
	// The property underneath: no second header reached the response.
	if rec.Header().Get("X-Injected") != "" {
		t.Error("the injected header was actually set on the response")
	}
	// No header appeared that this response did not mean to send. Asserting a
	// COUNT would be brittle — c.Data sets Content-Length too — so the check is
	// against the set of names, which is the thing header injection would add
	// to.
	expected := map[string]bool{
		"Content-Disposition": true, "Content-Type": true,
		"X-Cache": true, "Content-Length": true,
	}
	for name := range rec.Header() {
		if !expected[name] {
			t.Errorf("unexpected header %q on the response: %v", name, rec.Header())
		}
	}
	if !strings.HasPrefix(cd, `attachment; filename="`) || !strings.HasSuffix(cd, `"`) {
		t.Errorf("Content-Disposition = %q, want a well-formed attachment header", cd)
	}
}

func TestNoFilenameMeansNoDispositionHeader(t *testing.T) {
	// A search response is not a download. Sending an attachment header for one
	// makes a browser offer to save the XML instead of showing it.
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api", nil)

	writeNewznab(c, pluginapi.NewznabResult{
		ContentType: "application/rss+xml",
		Body:        []byte("<rss/>"),
	}, "HIT")

	if cd := rec.Header().Get("Content-Disposition"); cd != "" {
		t.Errorf("Content-Disposition = %q for a response with no filename", cd)
	}
	if got := rec.Header().Get("X-Cache"); got != "HIT" {
		t.Errorf("X-Cache = %q, want HIT", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "rss") {
		t.Errorf("Content-Type = %q, want the result's own", ct)
	}
}

// ── cat= parsing, which every downloader sends ──────────────────────────

func TestCategoriesParseFromWhatDownloadersSend(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []int
	}{
		{"5070", []int{5070}},
		{"5070,2040", []int{5070, 2040}},
		{" 5070 , 2040 ", []int{5070, 2040}}, // Sonarr pads them
		{"", nil},
	} {
		got := parseCats(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("parseCats(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseCats(%q) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestRubbishCategoriesAreDroppedRatherThanFailing(t *testing.T) {
	// A malformed cat= must not take the whole query down. These arrive from
	// tools this site does not control, and a 500 to a downloader is reported
	// as "the indexer is broken" — which, from where they are standing, it is.
	for _, in := range []string{"abc", "-5", "0", ",,,", "5070,abc,2040", "9e9"} {
		got := parseCats(in)
		for _, n := range got {
			if n <= 0 {
				t.Errorf("parseCats(%q) produced %d", in, n)
			}
		}
	}
	// The valid ids in a partly-rubbish list still come through: dropping the
	// whole filter because one entry was junk would silently widen the search.
	got := parseCats("5070,abc,2040")
	if len(got) != 2 || got[0] != 5070 || got[1] != 2040 {
		t.Errorf("parseCats(\"5070,abc,2040\") = %v, want the two valid ids", got)
	}
}

// ── the base URL baked into generated links ─────────────────────────────

func TestTheBaseURLFollowsTheRequestScheme(t *testing.T) {
	// Generated NZB and .torrent links carry this. Getting it wrong produces
	// links that work in a browser and fail in a downloader, or mixed-content
	// warnings on an HTTPS site.
	gin.SetMode(gin.TestMode)

	plain, _ := gin.CreateTestContext(httptest.NewRecorder())
	plain.Request = httptest.NewRequest(http.MethodGet, "http://indexer.example/api", nil)
	if got := requestBaseURL(plain); got != "http://indexer.example" {
		t.Errorf("plain request gave %q", got)
	}

	secure, _ := gin.CreateTestContext(httptest.NewRecorder())
	secure.Request = httptest.NewRequest(http.MethodGet, "https://indexer.example/api", nil)
	secure.Request.TLS = &tls.ConnectionState{}
	if got := requestBaseURL(secure); got != "https://indexer.example" {
		t.Errorf("TLS request gave %q, want the https scheme", got)
	}
}

func TestTheBaseURLKeepsANonDefaultPort(t *testing.T) {
	// The demo serves on 8090 and plenty of deployments sit on something other
	// than 80. Dropping the port produces links to a server that is not there.
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "http://indexer.example:8090/api", nil)

	if got := requestBaseURL(c); got != "http://indexer.example:8090" {
		t.Errorf("requestBaseURL = %q, want the port kept", got)
	}
}
