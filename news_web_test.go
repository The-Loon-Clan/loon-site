package main

import "testing"

// sanitizeNewsHTML is the one place in this host where stored input is rendered
// UNESCAPED (news.Handlers.NewsDetail marks its result template.HTML). Bodies
// are admin-authored, so this is defence in depth rather than the only gate —
// but a moderator account is exactly the level an XSS payload wants to reach,
// so the policy is tested against the standard evasions rather than the happy
// path alone.
func TestSanitizeNewsHTML(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		// --- allowed content survives, structure intact
		{"plain text", "hello", "hello"},
		{"paragraph", "<p>hi</p>", "<p>hi</p>"},
		{"inline marks", "<p><strong>a</strong> <em>b</em></p>", "<p><strong>a</strong> <em>b</em></p>"},
		{"list", "<ul><li>one</li><li>two</li></ul>", "<ul><li>one</li><li>two</li></ul>"},
		{"heading", "<h2>Title</h2>", "<h2>Title</h2>"},
		{"safe link", `<a href="https://example.com">x</a>`, `<a href="https://example.com">x</a>`},
		{"relative link", `<a href="/news">x</a>`, `<a href="/news">x</a>`},
		{"fragment link", `<a href="#top">x</a>`, `<a href="#top">x</a>`},
		{"mailto", `<a href="mailto:a@b.c">x</a>`, `<a href="mailto:a@b.c">x</a>`},
		{"image", `<img src="/i.png" alt="cover">`, `<img src="/i.png" alt="cover">`},
		{"void br", "a<br>b", "a<br>b"},

		// --- script-bearing elements are dropped WITH their contents
		{"script", "<p>a</p><script>alert(1)</script>", "<p>a</p>"},
		{"style", "<style>body{}</style>ok", "ok"},
		{"script in p", "<p>a<script>alert(1)</script>b</p>", "<p>ab</p>"},

		// --- disallowed wrappers are unwrapped, prose kept
		{"span unwrapped", "<span>keep me</span>", "keep me"},
		{"div unwrapped", "<div><p>x</p></div>", "<p>x</p>"},
		{"iframe dropped", `<iframe src="//evil"></iframe>hi`, "hi"},

		// --- event handlers and style attributes never survive
		{"onclick", `<p onclick="alert(1)">x</p>`, "<p>x</p>"},
		{"onerror on img", `<img src="/i.png" onerror="alert(1)">`, `<img src="/i.png">`},
		{"style attr", `<p style="position:fixed">x</p>`, "<p>x</p>"},
		{"unknown attr", `<a href="/a" target="_blank" rel="x">l</a>`, `<a href="/a">l</a>`},

		// --- dangerous URL schemes on both href and src
		{"javascript href", `<a href="javascript:alert(1)">x</a>`, "<a>x</a>"},
		{"JavaScript case", `<a href="JaVaScRiPt:alert(1)">x</a>`, "<a>x</a>"},
		{"javascript spaced", `<a href="  javascript:alert(1)">x</a>`, "<a>x</a>"},
		{"data uri img", `<img src="data:text/html;base64,PHNjcmlwdD4=">`, "<img>"},
		{"vbscript", `<a href="vbscript:msgbox">x</a>`, "<a>x</a>"},

		// --- text is escaped, so injection through text nodes is impossible
		{"escapes angle brackets", "5 < 6 & 7 > 2", "5 &lt; 6 &amp; 7 &gt; 2"},
		{"escapes quotes in attr", `<a href="/a" title="he said &#34;hi&#34;">x</a>`,
			`<a href="/a" title="he said &#34;hi&#34;">x</a>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeNewsHTML(tc.in); got != tc.want {
				t.Errorf("sanitizeNewsHTML(%q)\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
		})
	}
}

// A body that survives sanitization must not be able to reintroduce markup by
// being fed through again — otherwise a double-render anywhere in the stack
// becomes an injection point.
func TestSanitizeNewsHTMLIsIdempotent(t *testing.T) {
	for _, in := range []string{
		`<p onclick="x">a<script>b</script></p>`,
		`<a href="javascript:alert(1)">x</a>`,
		"5 < 6 & 7 > 2",
		`<div><span>plain</span></div>`,
	} {
		once := sanitizeNewsHTML(in)
		twice := sanitizeNewsHTML(once)
		if once != twice {
			t.Errorf("not idempotent for %q:\n once: %q\ntwice: %q", in, once, twice)
		}
	}
}

func TestSafeURL(t *testing.T) {
	safe := []string{"https://a.b", "http://a.b", "/rel", "rel", "#frag", "mailto:a@b.c", "?q=1"}
	unsafe := []string{
		"javascript:alert(1)", "JAVASCRIPT:alert(1)", " javascript:alert(1)",
		"java\tscript:alert(1)", "data:text/html,<script>", "vbscript:x", "file:///etc/passwd",
	}
	for _, u := range safe {
		if !safeURL(u) {
			t.Errorf("safeURL(%q) = false, want true", u)
		}
	}
	for _, u := range unsafe {
		if safeURL(u) {
			t.Errorf("safeURL(%q) = true, want false", u)
		}
	}
}
