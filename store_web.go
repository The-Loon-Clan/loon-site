package main

import (
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/store"
)

// Store (loon-plugins/store) host wiring — UNIT3D's bon_exchanges area. The
// plugin owns /store, /store/history, /store/buy/:id and /admin/store/*, and
// renders the HOST's templates through gin's set (web/templates/plugin/).
//
// Cheaper than the other gin-template plugins for two reasons: it self-migrates
// (Metadata.Migrations creates the `store` schema and its tables, so there is no
// host DDL here), and its points spending rides on the PointsService this host
// already wires — the same balance the nav tile reads.
//
// Not to be confused with /p/store, which is the pointstore plugin's own view.

// wireStorePlugin installs the SetDeps seams. The two pagination seams are
// deliberately separate in the plugin's contract: the offset is needed BEFORE
// the query and the view-model after it, and the host's helper only produces
// both at once when the total is already known.
func wireStorePlugin(w *web) {
	store.SetDeps(store.Deps{
		// The plugin renders its OWN markup now and hands back a finished
		// fragment, so the host's job is chrome rather than a data map. This is
		// the same wrap /p/<slug> gives a view-system page — site_page.html —
		// which is why there is no second template for it.
		RenderPage: func(gc *gin.Context, title string, body template.HTML) {
			w.render(gc, "site_page.html", map[string]any{"Title": title, "Fragment": body})
		},
		// Host-minted and host-validated: the plugin must never mint its own,
		// or the token it embeds is not the one csrf.go will check.
		CSRFToken: csrfToken,
		// The pager as ready HTML. A plugin fragment is rendered by the
		// plugin's own template set and cannot reach the host's partials, so
		// the host executes its own — one pager for the whole site rather than
		// a second copy living in the plugin.
		RenderPagination: func(page, pageSize, totalItems int, baseURL string) template.HTML {
			return w.renderPagination(hostPagination(page, pageSize, totalItems, baseURL))
		},
		// Separate from the pager on purpose: the offset is needed BEFORE the
		// query and the view-model after it.
		PageOffset: func(page, pageSize int) int {
			if page < 1 {
				page = 1
			}
			return (page - 1) * pageSize
		},
	})
}

// renderPagination executes the site's one pagination partial to HTML. The
// partial lives in the forum chrome (fpagination) because the forum was the
// first page to need it; it is the site's only pager, and rendering it here is
// what lets a plugin fragment carry the same one.
func (w *web) renderPagination(p forumPagination) template.HTML {
	t, err := pluginTemplates()
	if err != nil {
		w.log.Error("pagination template", "err", err)
		return ""
	}
	var sb strings.Builder
	if err := t.ExecuteTemplate(&sb, "fpagination", p); err != nil {
		w.log.Error("render pagination", "err", err)
		return ""
	}
	return template.HTML(sb.String())
}
