package handlers

import (
	"context"
	"html/template"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/storage"
	"github.com/the-loon-clan/loon/core"
)

// Widget regions: the places on this site an operator may drop a widget.
//
// The host owns this list, not core and not the plugins — a region is a fact
// about THIS layout, and a plugin that knew about "sidebar-left" would be
// asserting something about a site it has never seen. Plugins register widgets
// (core.RegisterWidget) and say nothing about where they go; the arrangement
// below is what the operator edits at /admin/widgets.
//
// Adding a region is two steps and no plugin changes: add it here, and render
// {{widgets "<key>"}} wherever it belongs in the templates.

// widgetRegion is one placeable area.
type widgetRegion struct {
	Key   string // stable id stored in placements — renaming one orphans its rows
	Label string // what the editor calls it
	Note  string // one line telling an operator what the area is and where
}

// widgetRegions is the whole set, in the order the editor lists them.
//
// Ordered by where they appear on a page rather than alphabetically, because an
// operator arranging a site is thinking about the page, not the alphabet.
var widgetRegions = []widgetRegion{
	{"header-bar", "Header bar", "The strip beside the search box, on every page. Keep these SMALL — a figure or two."},
	{"sidebar-left", "Left sidebar", "A column beside the main content. Empty by default, so the content stays full width until you put something here."},
	{"sidebar-right", "Right sidebar", "As the left, on the other side."},
	{"profile", "Profile page", "Under a member's details. Widgets here can read whose profile it is."},
	{"release", "Release page (aside)", "Beside a release's details, in the narrow column. Small things — a swarm figure, a badge. Widgets here can read which release it is."},
	{"release-main", "Release page (below the files)", "The full-width column under the file list. Where anything that needs room goes — a conversation, a table. Widgets here can read which release it is."},
	{"listing", "Listing pages", "Above browse and search results."},
	{"register", "Sign-up page", "Beside the registration form. Where a plugin that owns how people join — an application queue, a waiting list — puts its call to action."},
	{"footer", "Footer", "The bottom of every page."},
}

// widgetRegionByKey resolves a stored key. Unknown keys are refused rather than
// invented: a placement naming a region this layout does not have is a leftover
// from an older template, and rendering it somewhere arbitrary would be worse
// than not rendering it.
func widgetRegionByKey(key string) (widgetRegion, bool) {
	for _, r := range widgetRegions {
		if r.Key == key {
			return r, true
		}
	}
	return widgetRegion{}, false
}

// ── placements ──────────────────────────────────────────────────────────────

// ── rendering ───────────────────────────────────────────────────────────────

// renderedWidget is one widget's output, ready for a template.
type renderedWidget struct {
	Slug  string
	Title string
	Body  template.HTML
}

// renderRegion resolves and renders a region's widgets for THIS viewer.
//
// Three filters, all of which have to be here rather than in the placement:
// the widget must still be registered (a plugin can be switched off after an
// operator placed it), the viewer must be allowed to see it (an operator says
// where, the widget says who), and the widget must be willing to render in the
// region.
//
// A widget returning an empty fragment is DROPPED rather than framed. That is
// the documented way for a widget to say "nothing to show here" — a tracker
// widget on a site with no tracker, a release widget on the footer — and an
// empty box with a heading would be worse than silence.
//
// An error from a widget is swallowed, not propagated. This runs inside the
// chrome of somebody else's page: a broken widget must cost its own box, never
// the page it was placed on.
func (w *web) renderRegion(c *gin.Context, region string) []renderedWidget {
	if w.data == nil {
		return nil // no store wired: a page with no widgets, not a 500
	}
	return w.renderPlaced(c, region, w.data.ReadPlacements(c.Request.Context(), region))
}

// renderRegions renders several regions from ONE read of the placement table.
//
// The chrome asks for four on every page view; asking four times to learn that
// a site has placed nothing is a cost with no benefit. Returns nil when nothing
// is placed anywhere, so a template's {{range index .Widgets "footer"}} simply
// does nothing.
func (w *web) renderRegions(c *gin.Context, regions ...string) map[string][]renderedWidget {
	// Absence of a store is not an error here — see renderRegion. Both halves
	// of "absent" count: no Store at all, and a Store holding no connection.
	//
	// Only the first was checked, which made the intent above true for one of
	// the two ways it can happen. It matters because this runs from chromeData,
	// on EVERY page: the other two reads there (the avatar, the pending-avatar
	// count) already guard on Valid(), so a partially-wired host degraded
	// everywhere except here, where it took down every page at once.
	if w.data == nil || !w.data.DB().Valid() {
		return nil
	}
	all := w.data.ReadAllPlacements(c.Request.Context())
	if len(all) == 0 {
		return nil
	}
	var out map[string][]renderedWidget
	for _, region := range regions {
		rendered := w.renderPlaced(c, region, all[region])
		if len(rendered) == 0 {
			continue
		}
		if out == nil {
			out = make(map[string][]renderedWidget, len(regions))
		}
		out[region] = rendered
	}
	return out
}

// renderPlaced is the shared body: resolve, filter, render.
func (w *web) renderPlaced(c *gin.Context, region string, placements []storage.WidgetPlacement) []renderedWidget {
	if w.rt == nil || len(placements) == 0 {
		return nil
	}
	viewer, _ := w.currentUser(c)
	out := make([]renderedWidget, 0, len(placements))
	for _, p := range placements {
		if !p.Enabled {
			continue
		}
		widget, ok := w.rt.Core().WidgetBySlug(p.Slug)
		if !ok || !widget.AllowsUser(viewer) || !widget.FitsRegion(region) {
			continue
		}
		// The setting for THIS placement, set immediately before Render so two
		// placements of one widget cannot see each other's value.
		core.SetWidgetConfig(c, p.Config)
		body, err := widget.Render(c)
		if err != nil {
			// Dropped, but SAID. A widget that fails and one that has nothing
			// to show look identical on the page, and only one of them is
			// something an operator can fix — so the difference has to exist
			// somewhere, and the log is where.
			w.log.Error("widget", "slug", widget.Slug, "region", region, "err", err)
			continue
		}
		if strings.TrimSpace(string(body)) == "" {
			continue
		}
		out = append(out, renderedWidget{Slug: widget.Slug, Title: widget.Title, Body: body})
	}
	return out
}

// availableWidgets lists what an operator may add to a region: every
// registered widget that fits it and is not already placed there.
func (w *web) availableWidgets(ctx context.Context, region string) []core.Widget {
	if w.rt == nil {
		return nil
	}
	placed := map[string]bool{}
	for _, p := range w.data.ReadPlacements(ctx, region) {
		placed[p.Slug] = true
	}
	var out []core.Widget
	for _, cand := range w.rt.Core().Widgets() {
		if !placed[cand.Slug] && cand.FitsRegion(region) {
			out = append(out, cand)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}
