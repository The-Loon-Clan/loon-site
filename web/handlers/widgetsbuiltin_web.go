package handlers

import (
	"github.com/the-loon-clan/loon-demo-site/internal/markdown"

	"fmt"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The host's own placeable widgets.
//
// Plugins register theirs from Provision — the tracker's three live in
// tracker/widgets.go and read its own store. What is left here belongs to the
// SITE: text an operator writes, and the index's own figures.
//
// The tracker widgets used to be here, reading tracker.user_stats and
// tracker.torrents directly. That worked and it meant the host hardcoded
// another plugin's schema, where a column rename would have turned the cards
// silently blank rather than loudly broken.
//
// Every one returns an empty fragment when it has nothing to say, which is the
// contract that lets an operator place a widget anywhere without having to know
// whether it applies: a tracker widget on a site with no tracker renders
// nothing and is dropped, rather than drawing an empty box or erroring.

// registerBuiltinWidgets publishes them. Called before core.Boot, so they are
// in the registry by the time the editor or any page asks.
func (w *web) registerBuiltinWidgets(c *core.Core) {
	// Each RegisterWidget error is logged rather than fatal: a duplicate slug
	// is a programming mistake worth seeing, but it is not a reason to refuse
	// to serve a site whose other widgets are fine.
	reg := func(x core.Widget) {
		if err := c.RegisterWidget(x); err != nil {
			c.Logger.Error("register widget", "slug", x.Slug, "err", err)
		}
	}

	// ── free text, written by the operator ──────────────────────────────────
	//
	// The one widget with no data source: whatever an operator typed for THIS
	// placement, rendered as markdown. A rules notice in the footer, a welcome
	// line in a sidebar, a maintenance warning above the listings — the things
	// a site wants to say that no plugin can know in advance.
	//
	// Through markdown.Render, which is the site's ONE prose renderer: goldmark
	// with raw inline HTML refused, then the allowlist sanitizer. An admin is
	// exactly the account a stored XSS payload is trying to reach, so the
	// privileged path is the last one that should get a shortcut — the same
	// argument markdown_web.go makes for the wiki and the forum.
	//
	// Public, because a notice nobody can read is not a notice. An operator who
	// wants a members-only message has the member regions to put it in.
	reg(core.Widget{
		Slug:        "text",
		Title:       "Text",
		Description: "Your own words, written in markdown.",
		Public:      true,
		Weight:      1,
		ConfigLabel: "Markdown",
		ConfigHint:  "Rendered with the site's markdown. Leave blank to show nothing.",
		Render: func(gc *gin.Context) (template.HTML, error) {
			// Empty means NOT CONFIGURED, so the widget renders nothing and is
			// dropped. No sample text: an operator who cleared the field meant
			// to clear it, and a widget that reverts to a placeholder is one
			// nobody can switch off without removing it.
			src := strings.TrimSpace(core.WidgetConfig(gc))
			if src == "" {
				return "", nil
			}
			return markdown.Render(src), nil
		},
	})

	// ── what the indexer holds ──────────────────────────────────────────────
	reg(core.Widget{
		Slug:        "indexer-stats",
		Title:       "Index",
		Description: "Releases and groups currently indexed.",
		Public:      true,
		Weight:      40,
		Render: func(gc *gin.Context) (template.HTML, error) {
			if w.usenet == nil {
				return "", nil
			}
			gs, ok := w.homeGroups(gc.Request.Context())
			if !ok || gs.Stats.Releases == 0 {
				return "", nil
			}
			return template.HTML(fmt.Sprintf(
				`<dl class="key-value">`+
					`<div class="key-value__group"><dt>Releases</dt><dd>%d</dd></div>`+
					`<div class="key-value__group"><dt>Groups</dt><dd>%d</dd></div>`+
					`</dl>`,
				gs.Stats.Releases, gs.Stats.Groups)), nil
		},
	})
}
