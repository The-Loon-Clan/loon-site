package markdown

import (
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Fuzzing the markdown pipeline, which is where MEMBER-authored text becomes
// template.HTML — the moment the site stops escaping a string and starts
// trusting it. Every forum post, comment, wiki page and profile bio goes
// through here.
//
//	go test ./internal/markdown/ -run Fuzz -fuzz FuzzRender -fuzztime 60s

// unsafeAttrs is the property, and it is deliberately the same question the
// sanitiser's own fuzz test asks: parse the OUTPUT and look at what a browser
// would act on. Duplicated rather than shared because a test helper cannot
// cross a package boundary, and the alternative — exporting it from sanitize
// so a test can reach it — puts test scaffolding in the production API.
func unsafeAttrs(out string) []string {
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(out), ctx)
	if err != nil {
		return nil
	}
	dangerous := func(v string) bool {
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
	var bad []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.DataAtom == atom.Script {
				bad = append(bad, "<script>")
			}
			for _, a := range n.Attr {
				k := strings.ToLower(a.Key)
				if strings.HasPrefix(k, "on") {
					bad = append(bad, "<"+n.Data+" "+a.Key+"=")
				}
				if (k == "href" || k == "src" || k == "xlink:href") && dangerous(a.Val) {
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

func FuzzRender(f *testing.F) {
	for _, seed := range []string{
		"", "hello", "# heading", "**bold** _italic_ ~~struck~~",
		"[link](https://example.org)",
		"[xss](javascript:alert(1))",
		"![img](x\" onerror=\"alert(1))",
		"<script>alert(1)</script>",
		"<img src=x onerror=alert(1)>",
		"> quote\n\n- a\n- b\n\n```\ncode\n```",
		"| a | b |\n|---|---|\n| 1 | 2 |",
		// Markdown that produces HTML which produces markdown. The round trip
		// is where a renderer and a sanitiser disagree.
		"[a](<javascript:alert(1)>)",
		"日本語のテキスト",
		"\x00\x01",
		strings.Repeat("> ", 500) + "deep",
		strings.Repeat("*", 2000),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, src string) {
		out := string(Render(src))

		if bad := unsafeAttrs(out); len(bad) > 0 {
			t.Fatalf("member markdown rendered something a browser would run: %v\n"+
				"  in:  %q\n  out: %q", bad, src, out)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("rendered invalid UTF-8\n  in: %q\n  out: %q", src, out)
		}
	})
}

// Ellipsis truncates for display. The interesting property is not the length
// but the ENCODING: a truncator that slices bytes cuts a multi-byte rune in
// half, and the half-rune renders as a replacement character on a page that
// looked fine in every ASCII test anybody wrote.
func FuzzEllipsis(f *testing.F) {
	for _, s := range []string{"", "short", strings.Repeat("a", 500),
		"日本語のテキストです", "égal", "🎬🎬🎬🎬", "a\xffb"} {
		for _, n := range []int{0, 1, 3, 5, 10, 200} {
			f.Add(n, s)
		}
	}
	f.Fuzz(func(t *testing.T, n int, s string) {
		if n < 0 || n > 1<<16 {
			t.Skip() // not a length any caller passes
		}
		out := Ellipsis(n, s)
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatalf("Ellipsis(%d, %q) = %q — valid UTF-8 in, broken rune out",
				n, s, out)
		}
		// Shorter input must come back untouched, or the caller's "…" is a lie
		// about there being more.
		if utf8.RuneCountInString(s) <= n && out != s {
			t.Fatalf("Ellipsis(%d, %q) = %q — truncated a string that fitted", n, s, out)
		}
	})
}
