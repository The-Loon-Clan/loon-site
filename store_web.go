package main

import (
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
		BaseData: func(gc *gin.Context, extra gin.H) gin.H { return w.chromeData(gc, extra) },
		// Returned as `any` so the plugin never learns the host's type — the
		// template reads it by field name. Reuses forumPagination rather than
		// introducing a second shape, so one pagination partial serves both.
		Paginate: func(page, pageSize, totalItems int, baseURL string) any {
			return hostPagination(page, pageSize, totalItems, baseURL)
		},
		PageOffset: func(page, pageSize int) int {
			if page < 1 {
				page = 1
			}
			return (page - 1) * pageSize
		},
	})
}
