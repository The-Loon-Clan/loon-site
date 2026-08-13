package site

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
// BLOCK boundaries become spaces — otherwise "</p><p>" welds the last word of
// one paragraph to the first of the next — while inline ones do not, because a
// space there splits a word from its own punctuation.
func excerpt(n int, body template.HTML) string {
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '<' {
			b.WriteByte(body[i])
			continue
		}
		end := strings.IndexByte(string(body[i:]), '>')
		if end < 0 {
			break // unterminated tag: nothing after it is trustworthy text
		}
		name := excerptTagName(string(body[i+1 : i+end]))
		i += end
		// <script>/<style> cannot reach here — the sanitizer drops them with
		// their contents — but if one ever did, its SOURCE is not prose, and an
		// excerpt reading "alert(1)" is wrong even when rendered as text. Skip
		// to the matching close rather than treating the body as words.
		if name == "script" || name == "style" {
			rest := strings.ToLower(string(body[i:]))
			closeAt := strings.Index(rest, "</"+name)
			if closeAt < 0 {
				break
			}
			i += closeAt
			continue
		}
		// A BLOCK boundary is a word boundary; an inline one is not. Emitting a
		// space for every tag turned "<strong>body</strong>." into "body .",
		// because the closing tag sat between the word and its full stop.
		if excerptBlockTag(name) {
			b.WriteByte(' ')
		}
	}
	// Entities survive tag-stripping as "&amp;" and would be shown literally.
	text := html.UnescapeString(b.String())
	return ellipsis(n, strings.Join(strings.Fields(text), " "))
}

// excerptBlockTag reports whether a tag name (with any attributes, opening or
// closing) is block-level. The list is the block half of the sanitizer's
// allowlist (news_web.go); anything else — strong, em, a, code, s, del, u —
// is inline and contributes no space.
func excerptBlockTag(name string) bool {
	switch name {
	case "p", "br", "hr", "div", "li", "ul", "ol", "blockquote", "pre",
		"h1", "h2", "h3", "h4", "h5", "h6",
		"table", "thead", "tbody", "tr", "th", "td":
		return true
	}
	return false
}

// excerptTagName reduces "/p", `a href="..."` or "br /" to the bare lowercase
// element name.
func excerptTagName(tag string) string {
	tag = strings.TrimPrefix(strings.TrimSpace(tag), "/")
	if i := strings.IndexAny(tag, " \t\r\n/"); i >= 0 {
		tag = tag[:i]
	}
	return strings.ToLower(tag)
}
