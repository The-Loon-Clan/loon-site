package main

import (
	"fmt"
	"html/template"

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
