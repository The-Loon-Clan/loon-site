package handlers

import (
	"context"
	"fmt"
	"html/template"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

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

// messagesMigrate creates the plugin's five tables (idempotent). Columns come
// from pg.go's INSERT/SELECT lists — those are what actually fail on a
// mismatch, not the model structs.
func messagesMigrate(db *sqlx.DB) error {
	stmts := []string{
		// One row per PAIR: user_lo_id/user_hi_id are stored in canonical
		// (LEAST, GREATEST) order, so the unique index below is what actually
		// enforces "one thread per pair regardless of who started it".
		`CREATE TABLE IF NOT EXISTS dm_threads (
		    id              BIGSERIAL PRIMARY KEY,
		    user_lo_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    user_hi_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    last_message_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    lo_deleted_at   TIMESTAMPTZ,
		    hi_deleted_at   TIMESTAMPTZ,
		    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
		    CHECK (user_lo_id < user_hi_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_dm_threads_pair
		     ON dm_threads (user_lo_id, user_hi_id)`,
		`CREATE TABLE IF NOT EXISTS dm_messages (
		    id         BIGSERIAL PRIMARY KEY,
		    thread_id  BIGINT NOT NULL REFERENCES dm_threads(id) ON DELETE CASCADE,
		    sender_id  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    body       TEXT NOT NULL,
		    read_at    TIMESTAMPTZ,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// "unread for me" is `sender_id != me AND read_at IS NULL`, so the
		// index leads with the thread and carries both.
		`CREATE INDEX IF NOT EXISTS idx_dm_messages_thread
		     ON dm_messages (thread_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS dm_blocks (
		    blocker_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    blocked_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    PRIMARY KEY (blocker_id, blocked_id)
		)`,
		// Admin announcements. target selects the audience; expires_at is
		// nullable for "until dismissed".
		`CREATE TABLE IF NOT EXISTS messages (
		    id         BIGSERIAL PRIMARY KEY,
		    from_name  TEXT NOT NULL DEFAULT '',
		    title      TEXT NOT NULL,
		    body       TEXT NOT NULL,
		    target     TEXT NOT NULL DEFAULT 'all',
		    expires_at TIMESTAMPTZ,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// Per-user read/dismiss state for an announcement.
		`CREATE TABLE IF NOT EXISTS message_reads (
		    message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
		    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    dismissed  BOOLEAN NOT NULL DEFAULT false,
		    read_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
		    PRIMARY KEY (message_id, user_id)
		)`,
		// The plugin's thread-list query selects COALESCE(u.avatar_path, '')
		// from the HOST's users table. loon-baseline's users table has no such
		// column, and the handler discards the error (`threads, _ = ...`), so
		// the whole inbox silently rendered empty while the rows sat in the
		// database. Add the column the plugin's SQL assumes rather than patch
		// the query: prod's users table has it, so this is the demo catching
		// up, and an unset value COALESCEs to the initial-letter fallback the
		// templates already draw.
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_path TEXT`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// wireMessagesPlugin installs the SetDeps seams. Store is left nil on purpose:
// the plugin then builds a PGStore over core.Storage.DB(), which is what a host
// with the schema wants, and is one less thing for the demo to reimplement.
func wireMessagesPlugin(c *core.Core, w *web) error {
	if db := c.Storage.DB(); db != nil {
		if err := messagesMigrate(db); err != nil {
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
		CSRFToken:    csrfToken,
		RelativeTime: relativeTime,
		RenderEditor: w.renderEditor,
		RenderPagination: func(page, pageSize, totalItems int, baseURL string) template.HTML {
			return w.renderPagination(hostPagination(page, pageSize, totalItems, baseURL))
		},
		// Crosses the seam because it sanitises — one allowlist for the site.
		Markdown: siteMarkdown,
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
	db := c.Storage.DB()
	if db == nil {
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
