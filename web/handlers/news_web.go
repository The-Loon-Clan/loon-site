package handlers

import (
	"github.com/the-loon-clan/loon-demo-site/internal/sanitize"

	"fmt"
	"html/template"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

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

// newsMigrate creates the plugin's table (idempotent). Mirrors the DDL the
// plugin's store_pg.go queries and its integration test creates.
func newsMigrate(db *sqlx.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS news_posts (
		    id         BIGSERIAL PRIMARY KEY,
		    title      TEXT NOT NULL,
		    slug       TEXT NOT NULL UNIQUE,
		    body       TEXT NOT NULL,
		    published  BOOLEAN NOT NULL DEFAULT false,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// The public feed reads published posts newest-first; the admin list
		// reads all of them. One partial index covers the hot path.
		`CREATE INDEX IF NOT EXISTS idx_news_posts_published
		     ON news_posts (created_at DESC) WHERE published`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// wireNewsPlugin installs the SetDeps seams. Call after core.New (chromeData
// resolves the session user) and before core.Boot (SetDeps is checked at
// Provision), exactly like wireForumPlugin.
func wireNewsPlugin(c *core.Core, w *web) error {
	if db := c.Storage.DB(); db != nil {
		if err := newsMigrate(db); err != nil {
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
