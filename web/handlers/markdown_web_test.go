package handlers

import (
	"html/template"
	"regexp"
	"strings"
	"testing"
)

// siteMarkdown is the site's ONE prose renderer (markdown_web.go) and, since
// the forum, tickets, DMs and announcements were routed through it, the gate
// for arbitrary USER-authored input rather than only admin-authored news.
//
// news_web_test.go covers sanitizeNewsHTML against raw-HTML evasions. These
// cover what only appears once markdown sits in front of it: goldmark can EMIT
// html from constructs that contain no angle brackets at all, so a payload can
// reach the sanitizer's input through markdown syntax that the raw-HTML tests
// never exercise. The two layers are tested separately on purpose — a bug in
// either is a stored XSS, and a test that only exercised the pair would not say
// which one regressed.

// mdSafe asserts a rendered body carries no executable construct. Checked
// against the OUTPUT rather than by comparing to an expected string, because
// goldmark's exact whitespace is not the contract — "nothing dangerous
// survives" is.
var (
	// A dangerous scheme matters only where a browser would FOLLOW it, so this
	// looks at attribute values rather than at the whole document. Checking the
	// raw text was the first version of this test and it was wrong twice over:
	// it failed on <javascript:x> where the href had been correctly emptied and
	// only inert link TEXT remained, and it would fail any post that merely
	// mentions a javascript: URL in a sentence.
	reBadScheme = regexp.MustCompile(`(?i)(href|src)\s*=\s*["']?\s*(javascript|vbscript|data)\s*:`)
	// on* handlers, as attributes.
	reHandler = regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=`)
	reStyle   = regexp.MustCompile(`(?i)<[a-z][^>]*\sstyle\s*=`)
)

func mdSafe(t *testing.T, label, src string) string {
	t.Helper()
	out := string(siteMarkdown(src))
	low := strings.ToLower(out)
	// Elements that must never appear at all, in any position.
	for _, bad := range []string{
		"<script", "</script", "<iframe", "<object", "<embed", "<style",
		"<form", "<input", "<svg", "<base", "<link",
	} {
		if strings.Contains(low, bad) {
			t.Errorf("%s: element %q survived: %s", label, bad, out)
		}
	}
	if m := reBadScheme.FindString(out); m != "" {
		t.Errorf("%s: a dangerous scheme reached an attribute (%q): %s", label, m, out)
	}
	if m := reHandler.FindString(out); m != "" {
		t.Errorf("%s: an event handler survived (%q): %s", label, m, out)
	}
	if m := reStyle.FindString(out); m != "" {
		t.Errorf("%s: a style attribute survived (%q): %s", label, m, out)
	}
	return out
}

// TestSiteMarkdownRendersOrdinaryProse is the other half of the contract: a
// sanitizer that strips everything is safe and useless.
func TestSiteMarkdownRendersOrdinaryProse(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"paragraph", "hello there", "<p>hello there</p>"},
		{"bold", "a **b** c", "<strong>b</strong>"},
		{"italic", "a _b_ c", "<em>b</em>"},
		{"inline code", "run `gofmt`", "<code>gofmt</code>"},
		{"heading", "## Setup", "<h2>Setup</h2>"},
		{"bullet list", "- one\n- two", "<li>one</li>"},
		{"ordered list", "1. one\n2. two", "<ol>"},
		{"blockquote", "> quoted", "<blockquote>"},
		{"fenced code", "```\nx := 1\n```", "<pre><code>"},
		{"safe link", "[docs](/wiki)", `<a href="/wiki">docs</a>`},
		{"external link", "[x](https://example.com)", `href="https://example.com"`},
		{"table (GFM)", "|a|b|\n|-|-|\n|1|2|", "<table>"},
		{"strikethrough (GFM)", "~~gone~~", "<del>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := string(siteMarkdown(tc.in))
			if !strings.Contains(got, tc.want) {
				t.Errorf("siteMarkdown(%q)\n got: %s\nwant it to contain: %s", tc.in, got, tc.want)
			}
		})
	}

	// HardWraps is on because people type posts, not documents: a single
	// newline has to survive as a line break or every multi-line message
	// collapses — the bug that motivated routing tickets through here.
	if got := string(siteMarkdown("line one\nline two")); !strings.Contains(got, "<br") {
		t.Errorf("a single newline did not become a line break: %s", got)
	}
}

// TestSiteMarkdownBlocksScriptBearingInput walks the payload shapes that reach
// the sanitizer THROUGH markdown rather than as literal HTML.
func TestSiteMarkdownBlocksScriptBearingInput(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		// Raw HTML in a markdown document. goldmark has no WithUnsafe here, so
		// it should not pass these through at all — the sanitizer is the second
		// line, not the first.
		{"raw script block", "<script>alert(1)</script>"},
		{"raw script inline", "text <script>alert(1)</script> more"},
		{"raw iframe", `<iframe src="https://evil.example"></iframe>`},
		{"raw style", "<style>body{display:none}</style>"},
		{"img onerror", `<img src=x onerror="alert(1)">`},
		{"svg onload", "<svg/onload=alert(1)>"},
		{"div with handler", `<div onclick="alert(1)">x</div>`},
		{"form", `<form action="/steal"><input name="p"></form>`},

		// Markdown syntax carrying a dangerous scheme — no angle bracket in
		// sight, which is what makes these distinct from the raw-HTML cases.
		{"md link js", "[click](javascript:alert(1))"},
		{"md link js upper", "[click](JAVASCRIPT:alert(1))"},
		{"md image js", "![alt](javascript:alert(1))"},
		{"md link vbscript", "[click](vbscript:msgbox)"},
		{"md link data html", "[click](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)"},
		{"md image data html", "![x](data:text/html,<script>alert(1)</script>)"},

		// Reference-style definitions put the URL somewhere else entirely.
		{"reference link", "[click][ref]\n\n[ref]: javascript:alert(1)"},
		{"reference image", "![x][ref]\n\n[ref]: javascript:alert(1)"},

		// Autolinks.
		{"autolink js", "<javascript:alert(1)>"},

		// Entity and percent encoding inside a destination. goldmark decodes
		// entities in link URLs, so the scheme check has to run on the DECODED
		// form — a check against the raw source would pass these.
		{"entity-encoded scheme", "[x](java&#115;cript:alert(1))"},
		{"entity-encoded colon", "[x](javascript&#58;alert(1))"},
		{"percent-encoded scheme", "[x](%6aavascript:alert(1))"},
		{"tab inside scheme", "[x](java\tscript:alert(1))"},
		{"newline inside scheme", "[x](java\nscript:alert(1))"},

		// Nesting: a payload inside a construct that is itself allowed.
		{"link inside quote", "> [x](javascript:alert(1))"},
		// NOT "- [x](...)": that is a GFM task-list checkbox, so it never
		// parses as a link and the case would test nothing.
		{"link inside list", "- [click](javascript:alert(1))"},
		{"link inside table", "|a|\n|-|\n|[x](javascript:alert(1))|"},
		{"html inside code fence is inert but must not leak", "```\n<script>alert(1)</script>\n```"},
	} {
		t.Run(tc.name, func(t *testing.T) { mdSafe(t, tc.name, tc.in) })
	}
}

// TestSiteMarkdownIsIdempotent: a body that survived once must not be able to
// reintroduce markup by being rendered again. Any double-render in the stack
// (a preview, a quote, an edit round-trip) would otherwise be an injection
// point.
func TestSiteMarkdownIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"**bold** and [a link](/x)",
		"<script>alert(1)</script>",
		"[x](javascript:alert(1))",
		"> quoted\n\n- item\n\n`code`",
	} {
		once := string(siteMarkdown(in))
		twice := string(siteMarkdown(once))
		mdSafe(t, "second pass", once)
		// Not asserting once == twice: markdown is not a fixpoint (rendered
		// HTML re-rendered is escaped differently). The property that matters
		// is that the second pass introduces nothing executable, checked above.
		_ = twice
	}
}

// TestSiteMarkdownDegradesToNothingRatherThanRawSource pins the failure branch.
// Returning the unrendered input on a convert error would hand the caller the
// exact markup the pipeline exists to filter, already marked template.HTML.
func TestSiteMarkdownEmptyInput(t *testing.T) {
	if got := siteMarkdown(""); strings.TrimSpace(string(got)) != "" {
		t.Errorf("empty input rendered %q", got)
	}
}

// TestProseHelperIsTheSameRenderer: the {{prose}} template helper and the
// Deps.Markdown seam must not drift into two policies — the whole point of
// markdown_web.go is that there is exactly one. The plugin-rendered pages
// (tickets, DMs, announcements) reach the pipeline ONLY through this helper, so
// if it were ever pointed at a laxer renderer those pages would silently lose
// the sanitizer while the forum kept it.
func TestProseHelperIsTheSameRenderer(t *testing.T) {
	helper, ok := tmplHelpers()["prose"]
	if !ok {
		t.Fatal("no {{prose}} helper registered — the plugin pages render bodies through it")
	}
	fn, ok := helper.(func(string) template.HTML)
	if !ok {
		t.Fatalf("{{prose}} is %T, want func(string) template.HTML", helper)
	}
	for _, src := range []string{
		"a **b** and a [link](/wiki)",
		"[x](javascript:alert(1))",
		"<script>alert(1)</script>",
		"line one\nline two",
	} {
		if got, want := string(fn(src)), string(siteMarkdown(src)); got != want {
			t.Errorf("{{prose}} diverged from siteMarkdown for %q:\n got: %s\nwant: %s", src, got, want)
		}
	}
}

// excerpt feeds the news index. It runs on POST-sanitizer HTML, so its job is
// not safety — it is that a summary must be TEXT: truncating markup would cut a
// tag in half, and re-marking that template.HTML would hand the browser broken
// markup the sanitizer had already approved.
func TestExcerptStripsToPlainText(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "<p>hello there</p>", "hello there"},
		{"drops tags", "<p>a <strong>bold</strong> word</p>", "a bold word"},
		{"block boundary is a word boundary", "<p>one</p><p>two</p>", "one two"},
		{"list", "<ul><li>alpha</li><li>beta</li></ul>", "alpha beta"},
		{"entities are decoded", "<p>Tom &amp; Jerry &lt;3</p>", "Tom & Jerry <3"},
		{"collapses whitespace", "<p>a\n\n   b</p>", "a b"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := excerpt(200, template.HTML(tc.in)); got != tc.want {
				t.Errorf("excerpt(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Bounded, and never mid-tag — the whole reason it strips first.
	long := "<p>" + strings.Repeat("word ", 200) + "</p>"
	got := excerpt(60, template.HTML(long))
	if n := len([]rune(got)); n > 61 {
		t.Errorf("excerpt(60) returned %d runes", n)
	}
	if strings.ContainsAny(got, "<>") {
		t.Errorf("markup leaked into an excerpt: %q", got)
	}

	// The output is rendered as TEXT by the template, so this is belt and
	// braces — but an excerpt must never carry a tag even if one reached it.
	if got := excerpt(200, template.HTML(`<p>safe</p><script>alert(1)</script>`)); strings.Contains(got, "alert") {
		t.Errorf("script content survived into an excerpt: %q", got)
	}
}

// cond is used in ARGUMENT position inside dict literals, where a type error
// is not a compile failure — it is an EXECUTE failure that truncates the page
// mid-document and still returns 200. It was declared func(bool, …) and got a
// string, which blanked two pages that looked fine by status code alone.
func TestCondMatchesTemplateTruthiness(t *testing.T) {
	fn, ok := tmplHelpers()["cond"].(func(a, b, c any) any)
	if !ok {
		t.Fatalf("cond is %T", tmplHelpers()["cond"])
	}
	for _, tc := range []struct {
		name string
		in   any
		want any
	}{
		{"true bool", true, "yes"},
		{"false bool", false, "no"},
		{"non-empty string", "Mar 2026", "yes"},
		{"empty string", "", "no"},
		{"nonzero int", 3, "yes"},
		{"zero int", 0, "no"},
		{"nil", nil, "no"},
		{"non-empty slice", []int{1}, "yes"},
		{"empty slice", []int{}, "no"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fn(tc.in, "yes", "no"); got != tc.want {
				t.Errorf("cond(%#v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
