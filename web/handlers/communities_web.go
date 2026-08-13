package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/markdown"

	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/blob"
	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/communities"
)

// Communities (loon-plugins/communities) host wiring — user-owned sub-forums at
// /c/*. No UNIT3D equivalent: it is this stack's own feature, and the largest
// plugin here (8 tables, 20 routes, 7 host templates).
//
// Every seam it wants already exists from earlier wirings: chromeData,
// markdown.Render (goldmark with raw HTML off, then the allowlist sanitizer),
// hostPagination, and blob.NewLocal. The only new work is the schema.
//
// Its queries join the HOST's users table for u.username, u.role, u.created_at,
// u.avatar_path, u.points and u.reputation_tier. All six exist — avatar_path
// from the messages wiring, points and reputation_tier from the points work —
// which is why this could not have been wired first.

// wireCommunitiesPlugin installs the SetDeps seams.
func wireCommunitiesPlugin(c *core.Core, w *web) error {
	if db := c.Storage.DB(); db != nil {
		if err := w.data.MigrateCommunities(); err != nil {
			return fmt.Errorf("communities migrate: %w", err)
		}
	}
	communities.SetDeps(communities.Deps{
		BaseData: func(gc *gin.Context, extra gin.H) gin.H { return w.chromeData(gc, extra) },
		// The SAME renderer the wiki uses: goldmark with raw HTML disabled,
		// then the host's allowlist sanitizer. Community bodies are written by
		// ANY member, not a mod, so the second pass matters more here than it
		// does on the wiki.
		Markdown: markdown.Render,
		PageOffset: func(page, pageSize int) int {
			if page < 1 {
				page = 1
			}
			return (page - 1) * pageSize
		},
		Pagination: func(page, pageSize, totalItems int, baseURL string) any {
			return hostPagination(page, pageSize, totalItems, baseURL)
		},
		// Same upload root and served path as the wiki: each plugin saves into
		// its own namespace beneath it, so one static route serves both.
		Files: blob.NewLocal(uploadRoot, uploadURL),
	})
	return nil
}
