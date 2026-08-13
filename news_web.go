package site

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/news"
)

// News (loon-plugins/news) host wiring. The plugin owns its routes (/news,
// /news/:slug, /admin/news/*) and renders the HOST's templates by name through
// gin's HTML set, the same contract the forum plugin uses — so its four
// templates live in web/templates/plugin/ and are parsed by pluginTemplates().
//
// The plugin ships no migration (its table appears only in its own integration
// test), so the host creates it here, the same way forumMigrate does.

// newsMigrate creates the plugin's table (idempotent). Mirrors the DDL the
// plugin's store_pg.go queries and its integration test creates.
func newsMigrate(db *sqlx.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS news_posts (
		    id         BIGSERIAL PRIMARY KEY,
		    title      TEXT NOT NULL,
		    slug       TEXT NOT NULL UNIQUE,
		    body       TEXT NOT NULL,
		    published  BOOLEAN NOT NULL DEFAULT false,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// The public feed reads published posts newest-first; the admin list
		// reads all of them. One partial index covers the hot path.
		`CREATE INDEX IF NOT EXISTS idx_news_posts_published
		     ON news_posts (created_at DESC) WHERE published`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
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

// sanitizeNewsHTML is the host's news sanitization policy — the Sanitize seam
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
func sanitizeNewsHTML(in string) string {
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
		writeSanitized(&sb, n)
	}
	return sb.String()
}

func writeSanitized(sb *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		sb.WriteString(html.EscapeString(n.Data))
		return
	case html.ElementNode:
		// Code-bearing elements: drop the element AND its contents.
		if n.DataAtom == atom.Script || n.DataAtom == atom.Style {
			return
		}
		if !newsAllowedTags[n.DataAtom] {
			// Unknown wrapper: keep the prose inside it.
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				writeSanitized(sb, c)
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
			writeSanitized(sb, c)
		}
		sb.WriteString("</" + n.Data + ">")
	default:
		// Comments, doctypes and anything else contribute nothing.
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeSanitized(sb, c)
		}
	}
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

// wireNewsPlugin installs the SetDeps seams. Call after core.New (chromeData
// resolves the session user) and before core.Boot (SetDeps is checked at
// Provision), exactly like wireForumPlugin.
func wireNewsPlugin(c *core.Core, w *web) error {
	if db := c.Storage.DB(); db != nil {
		if err := newsMigrate(db); err != nil {
			return fmt.Errorf("news migrate: %w", err)
		}
	}
	news.SetDeps(news.Deps{
		// Fourth plugin today to take its markup back and ask for chrome
		// instead of a data map. No status parameter on this one — none of its
		// four pages re-render on a validation failure.
		RenderPage: func(gc *gin.Context, title string, body template.HTML) {
			w.render(gc, "site_page.html", map[string]any{"Title": title, "Fragment": body})
		},
		Sanitize: sanitizeNewsHTML,
	})
	return nil
}
