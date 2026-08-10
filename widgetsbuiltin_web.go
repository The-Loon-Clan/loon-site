package main

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The host's own placeable widgets.
//
// Plugins register theirs from Provision; these are the ones that belong to the
// site itself — figures the host already reads for its chrome and its pages,
// offered as things an operator can put somewhere else.
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
	// Through siteMarkdown, which is the site's ONE prose renderer: goldmark
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
			return siteMarkdown(src), nil
		},
	})

	// ── the viewer's tracker standing ───────────────────────────────────────
	//
	// The same figures the top bar carries, offered as a widget so an operator
	// can also put them in a sidebar, the footer, or a profile. Reads through
	// the host's own tracker helper, which is already gated on the tracker
	// being switched on AND the member having announced — so this renders
	// nothing on a site without a tracker, and nothing for a member who has
	// never used it.
	reg(core.Widget{
		Slug:        "tracker-standing",
		Title:       "Your tracker standing",
		Description: "Upload, download and ratio for the signed-in member.",
		Weight:      10,
		Render: func(gc *gin.Context) (template.HTML, error) {
			u, _ := w.currentUser(gc)
			if u == nil {
				return "", nil
			}
			tt, ok := readTrackerTotals(gc.Request.Context(), usersDB, u.ID)
			if !ok {
				return "", nil
			}
			return template.HTML(fmt.Sprintf(
				`<dl class="key-value">`+
					`<div class="key-value__group"><dt>Uploaded</dt><dd>%s</dd></div>`+
					`<div class="key-value__group"><dt>Downloaded</dt><dd>%s</dd></div>`+
					`<div class="key-value__group"><dt>Ratio</dt><dd>%s</dd></div>`+
					`<div class="key-value__group"><dt>Seeding</dt><dd>%d</dd></div>`+
					`</dl>`,
				template.HTMLEscapeString(humanBytes(tt.Uploaded)),
				template.HTMLEscapeString(humanBytes(tt.Downloaded)),
				template.HTMLEscapeString(tt.RatioLabel()),
				tt.Seeding,
			)), nil
		},
	})

	// ── the tracker's swarm totals ──────────────────────────────────────────
	//
	// Public, because it describes the site rather than a member — the figure a
	// front page or footer would carry.
	reg(core.Widget{
		Slug:        "tracker-swarm",
		Title:       "Tracker",
		Description: "Site-wide torrent, peer and snatch totals.",
		Public:      true,
		Weight:      20,
		Render: func(gc *gin.Context) (template.HTML, error) {
			ts, ok := readTrackerSiteStats(gc.Request.Context(), usersDB)
			if !ok {
				return "", nil
			}
			return template.HTML(fmt.Sprintf(
				`<dl class="key-value">`+
					`<div class="key-value__group"><dt>Torrents</dt><dd>%d</dd></div>`+
					`<div class="key-value__group"><dt>Seeders</dt><dd>%d</dd></div>`+
					`<div class="key-value__group"><dt>Leechers</dt><dd>%d</dd></div>`+
					`<div class="key-value__group"><dt>Snatches</dt><dd>%d</dd></div>`+
					`</dl>`,
				ts.Torrents, ts.Seeders, ts.Leechers, ts.Snatches)), nil
		},
	})

	// ── this release's swarm ────────────────────────────────────────────────
	//
	// Regions is stated here, unusually, and the reason is the point of
	// WidgetItem: the widget is meaningless anywhere the host has not said what
	// the page is about. Narrowing it keeps the editor from offering it for the
	// footer, where it could only ever render nothing.
	reg(core.Widget{
		Slug:        "release-swarm",
		Title:       "Swarm",
		Description: "Seeders and leechers for the release being viewed.",
		Public:      true,
		Regions:     []string{"release", "sidebar-left", "sidebar-right"},
		Weight:      30,
		Render: func(gc *gin.Context) (template.HTML, error) {
			ref, ok := core.WidgetItem(gc)
			if !ok || ref.Kind != "release" {
				return "", nil
			}
			sw, ok := readTrackerSwarm(gc.Request.Context(), usersDB, ref.ID)
			if !ok {
				return "", nil
			}
			return template.HTML(fmt.Sprintf(
				`<dl class="key-value">`+
					`<div class="key-value__group"><dt>Seeders</dt><dd>%d</dd></div>`+
					`<div class="key-value__group"><dt>Leechers</dt><dd>%d</dd></div>`+
					`<div class="key-value__group"><dt>Snatches</dt><dd>%d</dd></div>`+
					`</dl>`,
				sw.Seeders, sw.Leechers, sw.Snatches)), nil
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
