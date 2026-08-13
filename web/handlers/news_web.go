package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/sanitize"

	"fmt"
	"html/template"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/news"
)

// News (loon-plugins/news) host wiring. The plugin owns its routes (/news,
// /news/:slug, /admin/news/*) and renders the HOST's templates by name through
// gin's HTML set, the same contract the forum plugin uses — so its four
// templates live in web/templates/plugin/ and are parsed by pluginTemplates().
//
// The plugin ships no migration (its table appears only in its own integration
// test), so the host creates it here, the same way forumMigrate does.

// wireNewsPlugin installs the SetDeps seams. Call after core.New (chromeData
// resolves the session user) and before core.Boot (SetDeps is checked at
// Provision), exactly like wireForumPlugin.
func wireNewsPlugin(c *core.Core, w *web) error {
	if db := c.Storage.DB(); db != nil {
		if err := w.data.MigrateNews(); err != nil {
			return fmt.Errorf("news migrate: %w", err)
		}
	}
	news.SetDeps(news.Deps{
		// Fourth plugin today to take its markup back and ask for chrome
		// instead of a data map. No status parameter on this one — none of its
		// four pages re-render on a validation failure.
		RenderPage: func(gc *gin.Context, title string, body template.HTML) {
			w.render(gc, "site_page.html", map[string]any{"Title": title, "Fragment": body})
		},
		Sanitize: sanitize.HTML,
	})
	return nil
}
