package handlers

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/playlists"
)

// Playlists (loon-plugins/playlists) host wiring — UNIT3D's playlist area.
//
// The first plugin in this repo WRITTEN rather than wired, and its seams are
// shaped by what the earlier wirings taught: it stores release ids and user ids
// and asks the host to resolve them, instead of joining tables whose shape it
// cannot know. That is the fix for exactly the problem that blocked
// communities, which assumes users.role is TEXT and cannot run here.
//
// Self-migrating (Metadata.Migrations owns the "playlists" schema), so there is
// no DDL in this file.

// wirePlaylistsPlugin installs the SetDeps seams.
func wirePlaylistsPlugin(w *web) {
	playlists.SetDeps(playlists.Deps{
		BaseData: func(gc *gin.Context, extra gin.H) gin.H { return w.chromeData(gc, extra) },
		PageOffset: func(page, pageSize int) int {
			if page < 1 {
				page = 1
			}
			return (page - 1) * pageSize
		},
		Pagination: func(page, pageSize, totalItems int, baseURL string) any {
			return hostPagination(page, pageSize, totalItems, baseURL)
		},
		// Resolve release ids through the usenet capability. Ids that no longer
		// exist are simply ABSENT from the map — retention removes releases, and
		// the plugin renders a missing one as unavailable rather than dropping
		// the row, so a curator can see what they lost.
		LookupReleases: func(ctx context.Context, ids []int64) (map[int64]playlists.Release, error) {
			out := make(map[int64]playlists.Release, len(ids))
			if w.usenet == nil {
				return out, nil
			}
			// N lookups rather than one batched read: the capability exposes
			// ReleaseByID only, and a playlist page is one screen of items. If
			// this ever pages to hundreds, the capability needs a batch method
			// — do not paper over it with a bigger loop.
			for _, id := range ids {
				d, ok, err := w.usenet.ReleaseByID(ctx, id)
				if err != nil || !ok {
					continue // gone: leave it out, the plugin handles the gap
				}
				out[id] = playlists.Release{
					ID:       d.Release.ID,
					Title:    d.Release.Title,
					Size:     humanBytes(d.Release.Size),
					Category: d.Release.Category,
				}
			}
			return out, nil
		},
		LookupUsername: func(ctx context.Context, userID int64) (string, bool) {
			u, err := w.store.ByID(ctx, userID)
			if err != nil || u == nil {
				return "", false
			}
			return u.ToCore().Username, true
		},
	})
}
