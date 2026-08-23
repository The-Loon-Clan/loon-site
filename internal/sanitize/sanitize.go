// Package sanitize holds the site's HTML sanitisation policy.
//
// Its own package because more than one renderer needs it — the markdown
// pipeline and the news plugin's stored HTML both pass through here — and
// because a sanitiser is exactly the kind of thing that should be readable,
// testable and reviewable on its own rather than buried in a page handler.
// The production indexer keeps the same split (pkg/sanitize).
package sanitize

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// HTML is the host's news sanitization policy — the Sanitize seam
// the plugin renders bodies through before marking them template.HTML.
//
// Allowlist, not denylist: parse with x/net/html and re-serialise ONLY the tags
// and attributes named above. A denylist over raw strings is the classic way to
// get this wrong, since it has to anticipate every encoding trick; rebuilding
// from a real parse means anything unrecognised simply never reaches the output.
//
// Disallowed ELEMENTS keep their text children (so stripping <span> does not
// delete the sentence inside it) EXCEPT for <script>/<style>, whose contents are
// code rather than prose and are dropped wholesale.
//
// NOTE the plugin sanitizes only on the detail page; its list handler marks
// bodies template.HTML unsanitized. That is a plugin-side bug, reported
// upstream — this policy cannot defend the path it is never called on.
func HTML(in string) string {
	// Parse as a fragment inside <div> so the parser does not synthesise a
	// full <html><head><body> skeleton around the body text.
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(in), ctx)
	if err != nil {
		// Unparseable input: fall back to escaping it entirely rather than
		// passing anything through. Safe by construction.
		return html.EscapeString(in)
	}
	var sb strings.Builder
	for _, n := range nodes {
		writeSanitized(&sb, n, false)
	}
	return sb.String()
}

// writeSanitized re-serialises one node.
//
// inAnchor carries HTML's own rule down the tree: an <a> may not contain
// another <a>. The parser will happily hand us a nested pair -- from misnested
// input, or after a disallowed wrapper between them is dropped -- and writing
// it back out produces markup that a BROWSER re-parses differently from the
// tree we just sanitised. That gap between "what the sanitiser saw" and "what
// the browser builds" is the shape a mutation XSS takes, and it is worth
// closing even where the concrete instance is only a restructured link.
//
// Found by fuzzing (fuzz_test.go), after 8.8 million executions, as a failure
// of the idempotence property rather than of any of the specific checks.
func writeSanitized(sb *strings.Builder, n *html.Node, inAnchor bool) {
	switch n.Type {
	case html.TextNode:
		sb.WriteString(html.EscapeString(n.Data))
		return
	case html.ElementNode:
		// Code-bearing elements: drop the element AND its contents.
		if n.DataAtom == atom.Script || n.DataAtom == atom.Style {
			return
		}
		// A nested <a> is treated exactly like a disallowed wrapper: the tag
		// goes, the words stay. Dropping the text instead would delete the
		// link's own label, which is prose.
		if n.DataAtom == atom.A && inAnchor {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				writeSanitized(sb, c, true)
			}
			return
		}
		if !newsAllowedTags[n.DataAtom] {
			// Unknown wrapper: keep the prose inside it.
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				writeSanitized(sb, c, inAnchor)
			}
			return
		}
		sb.WriteString("<" + n.Data)
		allowed := newsAllowedAttrs[n.DataAtom]
		for _, a := range n.Attr {
			if a.Namespace != "" || !allowed[strings.ToLower(a.Key)] {
				continue
			}
			if (a.Key == "href" || a.Key == "src") && !safeURL(a.Val) {
				continue
			}
			sb.WriteString(" " + a.Key + `="` + html.EscapeString(a.Val) + `"`)
		}
		// Void elements have no closing tag and no children to walk.
		if n.DataAtom == atom.Br || n.DataAtom == atom.Img || n.DataAtom == atom.Hr {
			sb.WriteString(">")
			return
		}
		sb.WriteString(">")
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeSanitized(sb, c, inAnchor || n.DataAtom == atom.A)
		}
		sb.WriteString("</" + n.Data + ">")
	default:
		// Comments, doctypes and anything else contribute nothing.
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeSanitized(sb, c, inAnchor)
		}
	}
}

// newsAllowedTags is the tag allowlist for admin-authored news bodies. Chosen
// to cover what a news post legitimately needs and nothing else: no <script>,
// no <style>, no <iframe>, no <object>, no form elements, no event handlers.
var newsAllowedTags = map[atom.Atom]bool{
	atom.P: true, atom.Br: true, atom.Strong: true, atom.B: true,
	atom.Em: true, atom.I: true, atom.U: true, atom.S: true,
	// <del> as well as <s>: GFM strikethrough (~~x~~) renders as <del>, and
	// with only <s> here the tag was stripped and ~~x~~ silently did nothing
	// — an enabled markdown feature quietly discarded on its way out.
	atom.Del: true,
	atom.Ul:  true, atom.Ol: true, atom.Li: true,
	atom.Blockquote: true, atom.Code: true, atom.Pre: true,
	atom.H2: true, atom.H3: true, atom.H4: true,
	atom.A: true, atom.Img: true, atom.Hr: true,
	atom.Table: true, atom.Thead: true, atom.Tbody: true,
	atom.Tr: true, atom.Th: true, atom.Td: true,
}

// newsAllowedAttrs is the per-tag attribute allowlist. Everything not named
// here is dropped, which is what keeps on* handlers and style out.
var newsAllowedAttrs = map[atom.Atom]map[string]bool{
	atom.A:   {"href": true, "title": true},
	atom.Img: {"src": true, "alt": true, "title": true},
}

// safeURL rejects any scheme that can execute, while still allowing the
// relative and fragment links a news post actually uses. javascript: and data:
// are the two that matter; the check is on the scheme prefix AFTER trimming
// leading control characters and whitespace, which is how "java\tscript:" and
// " javascript:" slip past naive prefix tests.
func safeURL(raw string) bool {
	s := strings.Map(func(r rune) rune {
		if r <= 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, raw)
	s = strings.ToLower(s)
	if i := strings.Index(s, ":"); i >= 0 {
		// A colon before any / ? # is a scheme.
		if j := strings.IndexAny(s, "/?#"); j == -1 || i < j {
			switch s[:i] {
			case "http", "https", "mailto":
				return true
			default:
				return false
			}
		}
	}
	return true // relative, absolute-path, or fragment
}
