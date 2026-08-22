package handlers

import (
	"fmt"
	"html/template"
	"strings"

	"context"
	"github.com/the-loon-clan/loon-site/internal/middleware"

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
// The LAST plugin off Deps.BaseData. Every legacy render branch in forum,
// communities, donations and playlists is now dead code from this host's point
// of view.
func wirePlaylistsPlugin(w *web) error {
	// Parsed once and closed over. pluginTemplates() re-reads the embedded FS
	// on every call, and a playlist index lists twenty usernames.
	chrome, err := pluginTemplates()
	if err != nil {
		return fmt.Errorf("playlists: parse chrome for user-tag: %w", err)
	}

	playlists.SetDeps(playlists.Deps{
		// RenderPage, not BaseData. site_fragment.html rather than
		// site_page.html: these pages carry their own panel header, and the
		// wrapper would print the title again above it.
		RenderPage: func(gc *gin.Context, status int, title string, body template.HTML) {
			w.renderStatus(gc, status, "site_fragment.html",
				map[string]any{"Title": title, "Fragment": body})
		},
		CSRFToken:    middleware.Token,
		RelativeTime: relativeTime,
		RenderPagination: func(page, pageSize, totalItems int, baseURL string) template.HTML {
			return w.renderPagination(hostPagination(page, pageSize, totalItems, baseURL))
		},
		// The site's username chip. The plugin's markup used to invoke this
		// partial directly, which worked only because pluginTemplates() parses
		// site_chrome.html and every plugin template into ONE namespace. It is
		// the host's to render, and this is the seam that says so.
		RenderUserTag: func(name string) template.HTML {
			var sb strings.Builder
			if err := chrome.ExecuteTemplate(&sb, "user-tag", map[string]any{"Name": name}); err != nil {
				// A chip that fails to render must not take the page with it;
				// the plugin falls back to a plain profile link on empty.
				return ""
			}
			return template.HTML(sb.String())
		},
		PageOffset: func(page, pageSize int) int {
			if page < 1 {
				page = 1
			}
			return (page - 1) * pageSize
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
	return nil
}
