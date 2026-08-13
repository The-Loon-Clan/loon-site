package handlers

import (
	"github.com/the-loon-clan/loon-demo-site/internal/middleware"

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
		CSRFToken: middleware.Token,
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
		// The points area's third tab. The store plugin knows nothing about
		// rewards and should not — it takes tabs from the host rather than
		// linking to a page that may not exist on a site running store alone.
		ExtraTabs: w.pointsAreaTabs,
	})
}

// storeRewardsPath is the claim page. Host-owned and OUTSIDE /store/*, which
// the store plugin owns: a host route inside a plugin's namespace reads like
// the plugin's until you go looking. It appears as a tab on the store's strip
// regardless — the strip is about where the reader can go, not about who
// serves it.
const storeRewardsPath = "/rewards"

// pointsAreaTabs is store.Deps.ExtraTabs: the tabs the HOST adds to the points
// area. Only Rewards, and only while the rewards plugin is wired — a host
// without it gets the store's own two tabs and no dead link.
//
// The pointstore plugin's flair shop is deliberately NOT a tab here. Its page
// is served by the view registry at /p/<slug>, which core mounts and the host
// does not wrap, so the host cannot put this strip on it: a Flair tab would
// lead somewhere with no way back to Store or History. It is in the Community
// menu instead, beside the other shop — see navPlacement (admin_views.go).
func (w *web) pointsAreaTabs(c *gin.Context) []store.Tab {
	if !w.hasSiteWidget(rewardsClaimWidget) {
		return nil
	}
	return []store.Tab{{
		Label:  "Rewards",
		Href:   storeRewardsPath,
		Active: c.Request.URL.Path == storeRewardsPath,
	}}
}

// renderPartial executes one shared partial from the HOST's template set and
// returns it as HTML.
//
// This is what "the site's pager" and "the site's editor" mean once plugins
// render their own markup: a plugin fragment runs in the plugin's template set
// and cannot reach the host's partials, so the host renders them and hands back
// the result. The alternative is every plugin carrying its own copy, which
// works right up until the partial changes.
//
// An error yields empty rather than a panic: a missing pager should cost a
// page its pagination, not the whole request.
func (w *web) renderPartial(name string, data any) template.HTML {
	t, err := pluginTemplates()
	if err != nil {
		w.log.Error("partial template set", "partial", name, "err", err)
		return ""
	}
	var sb strings.Builder
	if err := t.ExecuteTemplate(&sb, name, data); err != nil {
		w.log.Error("render partial", "partial", name, "err", err)
		return ""
	}
	return template.HTML(sb.String())
}

// renderPagination is the site's one pager (fpagination, defined in the forum
// chrome because the forum needed it first).
func (w *web) renderPagination(p forumPagination) template.HTML {
	return w.renderPartial("fpagination", p)
}

// renderEditor is the site's one prose editor (editor.html). The formatting
// toolbar is not in the markup — site-scripts builds it for any
// textarea[data-prose] — so a plugin embedding this gets the toolbar too.
func (w *web) renderEditor(opts map[string]any) template.HTML {
	return w.renderPartial("prose-editor", opts)
}
