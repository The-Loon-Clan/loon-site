package handlers

import (
	site "github.com/the-loon-clan/loon-site"

	"bytes"
	"io/fs"
	"strings"
	"testing"
)

// Every template under web/templates/plugin/ is rendered by a PLUGIN through
// gin's HTML set, which means nothing in this repo calls it and no build, vet
// or parse check exercises it. The failure mode is execute-time and silent:
// the handler logs, and the browser gets a truncated page.
//
// Three real bugs of exactly that shape were caught by hand while these
// templates were being written:
//
//	{{int64 .X}}                  — not a template function
//	{{.CSRFToken}} inside a range — resolves to nothing, every delete 403s
//	{{.Theme.Href}} with no Theme — aborts mid-document
//
// So this sweeps every one of them twice: once with ONLY the chrome keys plus
// whatever the handler structurally guarantees (the shape a fresh install
// renders), and once populated (the shape with rows in it). Between them they
// exercise both branches of every {{if}} guarding a list.
//
// Uses the plugins' REAL row types rather than maps on purpose: a map lookup
// for a missing key yields nil, so a field-name typo would pass. A struct field
// that does not exist is a compile error, which is the point.

// pluginFixture is one template plus the two data shapes to render it with.
type pluginFixture struct {
	page string
	// structural is what the handler ALWAYS sets. A nil list counts; an absent
	// key does not. Merged over chromeKeys().
	structural map[string]any
	// populated adds rows. Merged over structural. nil = no second pass.
	populated map[string]any
}

func pluginFixtures() []pluginFixture {
	return []pluginFixture{
		// error.html is the ONLY plugin template this host still owns. news,
		// communities, donations and playlists each render their own markup
		// now, and each plugin's views_test.go executes its pages over fuller
		// data than these fixtures ever carried — including the flash test
		// that pinned "You need 100 points" against "You need  points", which
		// moved to communities with the template rather than being dropped.

		// ── wiki: NOT here. Third plugin to take ownership of its own markup
		// (after store and tickets); the host now sets RenderPage and its
		// copies are gone.

		// ── messages: NOT here. Fifth plugin to own its markup.

		// ── store: NOT here. The store plugin now embeds and parses its own
		// templates (store/views.go pageTmpl) and asks the host only to wrap
		// the finished fragment (Deps.RenderPage), so the host's copies were
		// dead markup and are gone. Its pages are covered by exercising them
		// against the running site, not by this sweep, which only sees
		// templates the HOST renders.

		// ── tickets: NOT here, for the same reason as store. The plugin
		// embeds and parses its own four templates and asks the host only for
		// chrome (RenderPage), the shared editor (RenderEditor), the pager and
		// Markdown. The host's copies were dead markup and are gone.

		// ── donations

		// ── communities
		//
		// postView is unexported in the plugin (handlers.go), so the fixture
		// mirrors its shape: the model plus the already-rendered body. That is
		// deliberate — BodyHTML is what the template must render, and
		// .Body is the untrusted source it must never touch.
		// The refusal page. Two passes cover both halves of the mapping: an
		// unknown code must still say something (silence reads as success),
		// and a known one must reach its own sentence rather than the
		// fallback -- which is the failure a bare {{if eq}} chain makes
		// invisible, because a typo'd code renders the generic branch and
		// looks fine.
		// ── playlists
		//
		// The plugin has NO templates of its own — it renders through this set
		// via Deps.BaseData — so its error page lives here rather than beside
		// the handler that raises it. Two passes: an unknown code must still
		// say something, and a known one must reach its own sentence, which a
		// bare {{if eq}} chain hides when a code is misspelled.

		// ── playlists

		// ── shared error page
		{"error.html",
			map[string]any{},
			map[string]any{"Code": 503, "Title": "T", "Message": "M"}},
	}
}

// TestPluginTemplatesExecute renders every plugin template in both its empty
// and populated shape. This is the check that would have caught the three
// execute-time bugs described above, and the one that keeps the next plugin
// wiring honest.
func TestPluginTemplatesExecute(t *testing.T) {
	tmpl, err := pluginTemplates()
	if err != nil {
		t.Fatalf("pluginTemplates: %v", err)
	}
	for _, f := range pluginFixtures() {
		if tmpl.Lookup(f.page) == nil {
			t.Errorf("%s: not in the plugin set", f.page)
			continue
		}
		shapes := []string{"empty"}
		if f.populated != nil {
			shapes = append(shapes, "populated")
		}
		for _, shape := range shapes {
			data := chromeKeys()
			for k, v := range f.structural {
				data[k] = v
			}
			if shape == "populated" {
				for k, v := range f.populated {
					data[k] = v
				}
			}
			var buf bytes.Buffer
			if err := tmpl.ExecuteTemplate(&buf, f.page, data); err != nil {
				t.Errorf("%s [%s]: execute: %v", f.page, shape, err)
				continue
			}
			// An execute error aborts mid-write, so the bytes already flushed
			// stay flushed. Asserting the document CLOSED is what distinguishes
			// a complete page from a truncated one.
			if !strings.Contains(buf.String(), "</html>") {
				t.Errorf("%s [%s]: no </html> — render aborted mid-document", f.page, shape)
			}
		}
	}
}

// TestEveryPluginTemplateHasAFixture keeps the sweep honest: adding a template
// without a fixture would otherwise leave it untested, which is exactly how the
// 20-of-24 gap this file closes came about.
func TestEveryPluginTemplateHasAFixture(t *testing.T) {
	// Files holding only {{define}} blocks are never executed by name, so they
	// are legitimately fixture-free.
	partialsOnly := map[string]bool{
		"wiki_shared.html": true, "tickets_shared.html": true,
	}
	covered := map[string]bool{}
	for _, f := range pluginFixtures() {
		covered[f.page] = true
	}
	ents, err := fs.ReadDir(site.FS, "web/templates/plugin")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".html") || partialsOnly[n] {
			continue
		}
		if !covered[n] {
			t.Errorf("web/templates/plugin/%s has no fixture in pluginFixtures()", n)
		}
	}
}
