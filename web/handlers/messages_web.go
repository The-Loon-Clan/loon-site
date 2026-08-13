package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/markdown"
	"github.com/the-loon-clan/loon-site/internal/storage"

	"github.com/the-loon-clan/loon-site/internal/middleware"

	"context"
	"fmt"
	"html/template"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/messages"
)

// Messages (loon-plugins/messages) host wiring — UNIT3D's users.conversations
// area. The plugin owns /inbox/* and /admin/messages/*, and renders the HOST's
// templates (web/templates/plugin/, parsed by pluginTemplates()).
//
// This is the real threaded-PM surface. It does NOT replace /p/inbox, which is
// loon-baseline's NOTIFICATION inbox — a different thing that happens to share
// the word. The plugin's own inbox is unified: DM threads and admin
// announcements in one list.
//
// Like forum/news/wiki it ships no migration, so the host creates the tables.

// wireMessagesPlugin installs the SetDeps seams. Store is left nil on purpose:
// the plugin then builds a PGStore over core.Storage.DB(), which is what a host
// with the schema wants, and is one less thing for the demo to reimplement.
func wireMessagesPlugin(c *core.Core, w *web) error {
	if db := c.Storage.DB(); db != nil {
		if err := w.data.MigrateMessages(); err != nil {
			return fmt.Errorf("messages migrate: %w", err)
		}
	}
	messages.SetDeps(messages.Deps{
		// Fifth plugin today to own its markup. Same seams as tickets, which
		// is the point of having built renderPartial: the pager and the prose
		// editor are the site's, rendered from the HOST's set, so a plugin
		// fragment carries the same ones the forum does.
		RenderPage: func(gc *gin.Context, title string, body template.HTML) {
			w.render(gc, "site_page.html", map[string]any{"Title": title, "Fragment": body})
		},
		CSRFToken:    middleware.Token,
		RelativeTime: relativeTime,
		RenderEditor: w.renderEditor,
		RenderPagination: func(page, pageSize, totalItems int, baseURL string) template.HTML {
			return w.renderPagination(hostPagination(page, pageSize, totalItems, baseURL))
		},
		// Crosses the seam because it sanitises — one allowlist for the site.
		Markdown: markdown.Render,
		// ListUsers backs the admin composer's recipient dropdown. It is
		// optional precisely because core has no "list every user" method — on
		// a real site that query breaks the page. The demo has two seeded
		// accounts, so a bounded list is safe here and saves typing usernames;
		// the send path resolves by username either way, so this only ever adds
		// convenience.
		ListUsers: func(ctx context.Context) ([]messages.UserOption, error) {
			return demoUserOptions(ctx, c)
		},
	})
	return nil
}

// demoUserOptions lists accounts for the composer dropdown, hard-capped. The
// cap is the point: it keeps a seam that is unbounded by nature from becoming a
// full-table scan if this demo is ever pointed at a real user table.
func demoUserOptions(ctx context.Context, c *core.Core) ([]messages.UserOption, error) {
	db := storage.Wrap(c.Storage.DB())
	if !db.Valid() {
		return nil, nil
	}
	var rows []struct {
		ID       int64  `db:"id"`
		Username string `db:"username"`
	}
	if err := db.SelectContext(ctx, &rows,
		`SELECT id, username FROM users ORDER BY username LIMIT 200`); err != nil {
		return nil, err
	}
	out := make([]messages.UserOption, 0, len(rows))
	for _, r := range rows {
		out = append(out, messages.UserOption{ID: int(r.ID), Username: r.Username})
	}
	return out, nil
}
