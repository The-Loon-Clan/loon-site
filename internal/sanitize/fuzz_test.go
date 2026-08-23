package sanitize

import (
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Fuzzing the sanitiser, which is the one function on this site where being
// wrong about a string is a cross-site scripting hole rather than a rendering
// bug.
//
// A sanitiser is an unusually good fuzz target because its contract is a
// PROPERTY rather than a value: whatever goes in, certain things must not come
// out. That means there is something to assert about an input nobody wrote
// down, which is exactly what a fuzzer produces.
//
// Run it:
//
//	go test ./internal/sanitize/ -run Fuzz -fuzz FuzzHTML -fuzztime 60s
//
// The corpus below is the seed, not the test. `go test` without -fuzz replays
// these plus anything in testdata/fuzz/, so a crash found once is a regression
// test from then on and costs nothing in CI.

// The three things the policy promises never to emit, checked on the OUTPUT
// rather than reasoned about from the input: the whole point of rebuilding
// from a parse is that the input's tricks stop mattering, and the only way to
// know that held is to look at what came out.
//
// AND CHECKED BY PARSING IT, not by matching the text. The first version of
// this grepped the output for "javascript:" and the fuzzer broke it in one
// second with `jAvAsCript:0` -- which is plain text, contains no URL, and is
// entirely safe to render. A string check cannot tell an attribute value from
// a sentence, so it reports the sentence. Parsing the output asks the question
// a browser would ask.
var scriptTag = regexp.MustCompile(`(?i)<\s*script`)

// dangerousURL reports whether an attribute value would navigate to script.
// Entity decoding is the parser's job by the time we see it; what is left is
// the whitespace and control characters browsers strip before matching a
// scheme, which is the oldest bypass there is.
func dangerousURL(v string) bool {
	var b strings.Builder
	for _, r := range v {
		if r > 0x20 {
			b.WriteRune(r)
		}
	}
	s := strings.ToLower(b.String())
	return strings.HasPrefix(s, "javascript:") || strings.HasPrefix(s, "vbscript:") ||
		strings.HasPrefix(s, "data:text/html")
}

// unsafeAttrs walks the parsed output and returns what a browser would act on.
func unsafeAttrs(t *testing.T, out string) []string {
	t.Helper()
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(out), ctx)
	if err != nil {
		return nil // unparseable output is caught by the idempotence check
	}
	var bad []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				k := strings.ToLower(a.Key)
				if strings.HasPrefix(k, "on") {
					bad = append(bad, "<"+n.Data+" "+a.Key+"=")
				}
				if (k == "href" || k == "src" || k == "xlink:href") && dangerousURL(a.Val) {
					bad = append(bad, "<"+n.Data+" "+a.Key+"="+a.Val)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return bad
}

func FuzzHTML(f *testing.F) {
	for _, seed := range []string{
		"",
		"plain text",
		"<b>bold</b> and <i>italic</i>",
		"<a href=\"https://example.org\">link</a>",
		// The classics, as seeds rather than as the test: a fuzzer mutates
		// from here, and starting it at "<script>" is what makes the first
		// thousand mutations relevant instead of random.
		"<script>alert(1)</script>",
		"<img src=x onerror=alert(1)>",
		"<a href=\"javascript:alert(1)\">x</a>",
		"<a href=\"jav&#x09;ascript:alert(1)\">x</a>",
		"<svg/onload=alert(1)>",
		"<style>body{background:url(javascript:alert(1))}</style>",
		"<div><span>nested</span></div>",
		// Malformed on purpose: the parser's error path returns EscapeString,
		// and that path deserves exercise too.
		"<<<<>>>>",
		"<a href=",
		"<b><i></b></i>",
		// Not ASCII. A sanitiser that slices bytes rather than runes breaks
		// here, and the break is silent.
		"<b>日本語</b>",
		"\x00\x01\x02",
		strings.Repeat("<div>", 200),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		out := HTML(in)

		if scriptTag.MatchString(out) {
			t.Fatalf("a <script> survived sanitising\n  in:  %q\n  out: %q", in, out)
		}
		if bad := unsafeAttrs(t, out); len(bad) > 0 {
			t.Fatalf("an attribute a browser would act on survived sanitising: %v\n  in:  %q\n  out: %q",
				bad, in, out)
		}

		// IDEMPOTENCE, which is not a tidiness property. A sanitiser whose
		// output differs when fed back to itself is one where the first pass
		// CREATED markup out of text — the mutation-XSS shape, where the
		// browser's parse of the output is not the parse the sanitiser made.
		// Anything that survives one pass must survive the second unchanged.
		if again := HTML(out); again != out {
			t.Fatalf("sanitising is not idempotent — the first pass produced "+
				"markup the second pass changed, which is the shape a mutation "+
				"XSS takes\n  in:    %q\n  once:  %q\n  twice: %q", in, out, again)
		}
	})
}
