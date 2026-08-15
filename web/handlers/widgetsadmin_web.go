package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// The page editor: /admin/widgets.
//
// One region at a time, chosen with a tab. The alternative — every region on
// one page — was rejected because the add form needs to know which region it
// is adding to, and a page carrying seven of them invites adding to the wrong
// one. A region in the URL also means an operator can link a colleague straight
// to the area being discussed.
//
// Everything here is a plain form POST and a redirect. No JavaScript, matching
// the rest of this site: drag-to-reorder would be nicer and would also mean a
// page that does nothing without scripts, on the one screen an operator uses to
// fix a layout that is already broken.

// widgetsAdminPage renders the editor for one region.
func (w *web) widgetsAdminPage(c *gin.Context) {
	region := c.Query("region")
	if _, ok := widgetRegionByKey(region); !ok || !w.db().Valid() {
		// Default to the first region rather than erroring: arriving with no
		// query is the normal way in, from the subnav.
		region = widgetRegions[0].Key
	}
	ctx := c.Request.Context()

	w.render(c, "admin_widgets.html", map[string]any{
		"Title":     "Widgets",
		"Regions":   widgetRegions,
		"Region":    region,
		"Placed":    w.placedWidgets(ctx, region),
		"Available": w.availableWidgets(ctx, region),
	})
}

// placedVM is one row of the placed-widgets table.
//
// Package level rather than declared inside the page handler, because the
// action handler now renders the same table as a fragment and the two must
// build identical rows — a second local struct with the same fields is exactly
// how a fragment starts drifting from the page it replaces.
type placedVM struct {
	Slug        string
	Title       string
	Position    int
	Enabled     bool
	Missing     bool
	ConfigLabel string // non-empty when this widget takes a setting
	ConfigHint  string
	Config      string // what the operator typed for THIS placement
}

// placedWidgets resolves a region's placements against the live registry so an
// operator can SEE that a placement has lost its widget. Rendering it as
// "missing" is the whole reason resolution reports rather than skips: a row
// that silently vanished would leave somebody wondering why their layout
// changed.
func (w *web) placedWidgets(ctx context.Context, region string) []placedVM {
	// Runtime.Core() guards a nil receiver and returns nil; Core.WidgetBySlug
	// does NOT — it takes a mutex on the receiver, so a nil Core panics rather
	// than reporting "no such widget". Resolved once here instead of at every
	// row, which also makes the no-runtime case say the true thing: with no
	// registry there is nothing to resolve against, so every placement is
	// unresolvable, which is exactly what Missing means.
	reg := w.rt.Core()

	var placed []placedVM
	for _, p := range w.data.ReadPlacements(ctx, region) {
		vm := placedVM{Slug: p.Slug, Position: p.Position, Enabled: p.Enabled,
			Title: p.Slug, Missing: true, Config: p.Config}
		if reg != nil {
			if wd, ok := reg.WidgetBySlug(p.Slug); ok {
				vm.Title, vm.Missing = wd.Title, false
				vm.ConfigLabel, vm.ConfigHint = wd.ConfigLabel, wd.ConfigHint
			}
		}
		placed = append(placed, vm)
	}
	return placed
}

// widgetsAdminAction applies one edit and returns to the same region.
//
// Add/remove/toggle/move are separate actions rather than one "save the whole
// region" form, because a save-everything form fights the other operator: two
// people arranging the same page would each overwrite the other's work with
// whatever their browser last rendered.
func (w *web) widgetsAdminAction(c *gin.Context) {
	in, _ := readWidgetActionInput(c)
	region := in.Region
	if _, ok := widgetRegionByKey(region); !ok || !w.db().Valid() {
		if isHTMX(c) {
			w.renderRefusal(c, "admin_widgets.html", "That form did not come from this page.")
			return
		}
		c.Redirect(http.StatusSeeOther, "/admin/widgets")
		return
	}
	slug := in.Slug
	ctx := c.Request.Context()

	// Every arm writes, and every write used to discard its error while the
	// handler reported success regardless. Converting this endpoint to htmx
	// made that worse rather than exposing it: the response now SAYS "Layout
	// updated", where a redirect at least returned a page without the change
	// on it. So the error is kept and answered.
	var err error

	switch in.Action {
	case "add":
		// Refuse a slug that is not a registered widget. The dropdown only
		// offers real ones, but a form post is not a promise.
		if _, ok := w.rt.Core().WidgetBySlug(slug); !ok {
			break
		}
		// Appended at the end: position is the count of what is already there.
		// ON CONFLICT DO NOTHING makes a double-submit idempotent rather than
		// moving a widget the operator did not touch.
		err = w.data.PlaceWidget(ctx, region, slug)
	case "remove":
		err = w.data.RemoveWidget(ctx, region, slug)
	case "toggle":
		// Off rather than removed keeps the position, so switching a widget
		// back on puts it where it was instead of at the bottom.
		err = w.data.ToggleWidget(ctx, region, slug)
	case "configure":
		// The setting for one placement. Stored verbatim — a widget decides
		// what its own string means, and the host escaping or parsing it here
		// would break every widget whose value is not what the host guessed.
		// Whatever a widget does with it must be safe at RENDER; see the
		// markdown widget, which runs the site's sanitising renderer.
		err = w.data.ConfigureWidget(ctx, region, slug, in.Config)
	case "move":
		if delta := in.Delta; delta != 0 {
			err = w.moveWidget(c, region, slug, delta)
		}
	}

	if err != nil {
		w.log.Error("widget layout", "action", in.Action, "region", region,
			"slug", slug, "err", err)
		if isHTMX(c) {
			w.renderRefusal(c, "admin_widgets.html", "That change could not be saved.")
			return
		}
		// No-JavaScript: fall through to the redirect. The reloaded page shows
		// the layout unchanged, which is the truth, and it is what this
		// endpoint did before — the failure was never invisible on that path,
		// only unlogged.
	}
	// The whole table, not the row: moving a widget up moves its neighbour
	// down, and toggling one changes only that row but returns through the same
	// path. Re-read through placedWidgets, the page's own builder, so the
	// swapped table is what a reload would have drawn.
	if isHTMX(c) {
		w.renderFragmentWithNotice(c, http.StatusOK, "admin_widgets.html", "placed-widgets",
			map[string]any{"Region": region, "Placed": w.placedWidgets(ctx, region)},
			noticeOK("Layout updated."))
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin/widgets?region="+region)
}

// moveWidget shifts one placement up or down by swapping it with its
// neighbour, then asks the store to renumber the region densely.
//
// The swap is an ORDERING decision — what "up" means, and that the ends are
// not an error — so it stays here. Writing the new order is the store's job;
// see ReorderWidgets for why the whole region is renumbered rather than two
// rows swapped.
//
// Returns an error so the caller can report one. It used to return nothing and
// discard ReorderWidgets, so the "move" arm of widgetsAdminAction reported
// success unconditionally — and once every OTHER arm gained a real error path,
// that was worse than before: five arms honest and one quietly not.
func (w *web) moveWidget(c *gin.Context, region, slug string, delta int) error {
	ctx := c.Request.Context()
	rows := w.data.ReadPlacements(ctx, region)
	idx := -1
	for i, p := range rows {
		if p.Slug == slug {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil // no such placement in this region: nothing to move
	}
	target := idx + delta
	if target < 0 || target >= len(rows) {
		return nil // already at the end; not an error, just nothing to do
	}
	rows[idx], rows[target] = rows[target], rows[idx]
	order := make([]string, len(rows))
	for i, p := range rows {
		order[i] = p.Slug
	}
	return w.data.ReorderWidgets(ctx, region, order)
}
