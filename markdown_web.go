package main

import (
	"bytes"
	"html"
	"html/template"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

// The site's ONE prose renderer — everywhere a person types more than a line.
//
// It used to be three. The wiki and communities got goldmark; the forum got
// demoForumMarkdown, which escaped and wrapped in <p> and nothing else; and
// tickets, DMs and announcements got nothing at all, so a multi-paragraph
// support ticket rendered as one run-on block because HTML collapses newlines.
// Same kind of content, three capabilities, decided by which file the handler
// happened to live in. UNIT3D has one BBCode pipeline for all of it; this is
// the same idea with the syntax this stack already committed to.
//
// Trust level does NOT vary the policy, and that is deliberate. Wiki authors
// are mods+, forum posters are any signed-in user, and the strict pass is
// applied to both: goldmark refuses raw inline HTML (there is no WithUnsafe
// here) and the result goes through sanitizeNewsHTML's allowlist regardless. A
// moderator account is precisely the level a stored XSS payload is trying to
// reach, so the privileged path is the last one that should get a shortcut.
//
// GFM is on for tables, strikethrough and autolinks — what a knowledge base and
// a forum thread both actually use. HardWraps because people type posts, not
// markdown documents, and expect a newline to be a newline. Typographer is off:
// smart quotes in a page full of config snippets and CLI flags are a liability.
var siteMD = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(gmhtml.WithHardWraps()),
)

// siteMarkdown renders untrusted prose to sanitized HTML. It satisfies the
// Deps.Markdown signature every prose plugin asks the host for, and is also
// registered as the {{prose}} template helper for the plugin-rendered pages
// whose bodies arrive as plain strings.
func siteMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := siteMD.Convert([]byte(src), &buf); err != nil {
		// Render nothing rather than the raw source: on a page that marks its
		// result template.HTML, falling back to unrendered input would hand
		// through exactly the markup the pipeline exists to filter.
		return ""
	}
	return template.HTML(sanitizeNewsHTML(buf.String()))
}

// excerpt turns already-rendered prose into a plain-text summary of n runes.
//
// Truncating the HTML itself would cut a tag in half, and marking the result
// template.HTML again would then hand the browser broken markup that the
// sanitizer had already approved — a page can be XSS-safe and still be wrong.
// So this strips to TEXT and returns a string, which the template escapes on
// the way out: an excerpt is a summary, not a rendering.
//
// The input is post-sanitizer, so the only tags present are the allowlist's.
// Block boundaries become spaces, otherwise "</p><p>" welds the last word of
// one paragraph to the first of the next.
func excerpt(n int, body template.HTML) string {
	var b strings.Builder
	depth, skip := 0, false
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '<':
			depth++
			// <script>/<style> cannot reach here (the sanitizer drops them
			// wholesale), but their CONTENT would be text if they ever did.
			rest := strings.ToLower(string(body[i:min(i+7, len(body))]))
			skip = strings.HasPrefix(rest, "<script") || strings.HasPrefix(rest, "<style")
		case '>':
			if depth > 0 {
				depth--
			}
			b.WriteByte(' ') // a tag boundary is a word boundary
		default:
			if depth == 0 && !skip {
				b.WriteByte(body[i])
			}
		}
	}
	// Entities survive tag-stripping as "&amp;" and would be shown literally.
	text := html.UnescapeString(b.String())
	return ellipsis(n, strings.Join(strings.Fields(text), " "))
}
