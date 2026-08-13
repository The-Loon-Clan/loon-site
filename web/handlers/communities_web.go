package handlers

import (
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/blob"
	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/communities"
)

// Communities (loon-plugins/communities) host wiring — user-owned sub-forums at
// /c/*. No UNIT3D equivalent: it is this stack's own feature, and the largest
// plugin here (8 tables, 20 routes, 7 host templates).
//
// Every seam it wants already exists from earlier wirings: chromeData,
// siteMarkdown (goldmark with raw HTML off, then the allowlist sanitizer),
// hostPagination, and blob.NewLocal. The only new work is the schema.
//
// Its queries join the HOST's users table for u.username, u.role, u.created_at,
// u.avatar_path, u.points and u.reputation_tier. All six exist — avatar_path
// from the messages wiring, points and reputation_tier from the points work —
// which is why this could not have been wired first.

// communitiesMigrate creates the plugin's eight tables. Columns are taken from
// its INSERT lists and model db tags.
func communitiesMigrate(db *sqlx.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS communities (
		    id                   SERIAL PRIMARY KEY,
		    slug                 TEXT NOT NULL UNIQUE,
		    name                 TEXT NOT NULL,
		    description          TEXT NOT NULL DEFAULT '',
		    sidebar_md           TEXT NOT NULL DEFAULT '',
		    banner_url           TEXT NOT NULL DEFAULT '',
		    banner_position      INTEGER NOT NULL DEFAULT 50,
		    icon_url             TEXT NOT NULL DEFAULT '',
		    accent_color         TEXT NOT NULL DEFAULT '',
		    owner_user_id        BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    release_group_id     INTEGER,
		    nsfw                 BOOLEAN NOT NULL DEFAULT false,
		    -- Join gating. join_type is checked in Go against the plugin's
		    -- CommunityJoin* constants, so no CHECK here: a constraint that
		    -- disagrees with the plugin's enum breaks writes the plugin
		    -- considers valid.
		    join_type            TEXT NOT NULL DEFAULT 'open',
		    min_account_age_days INTEGER NOT NULL DEFAULT 0,
		    min_role_level       INTEGER NOT NULL DEFAULT 0,
		    join_points_cost     INTEGER NOT NULL DEFAULT 0,
		    hidden_at            TIMESTAMPTZ,
		    hidden_by            BIGINT,
		    hidden_reason        TEXT NOT NULL DEFAULT '',
		    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS community_subscribers (
		    community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
		    user_id      BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		    PRIMARY KEY (community_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS community_mods (
		    community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
		    user_id      BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    added_by     BIGINT,
		    added_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		    PRIMARY KEY (community_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS community_rules (
		    id           SERIAL PRIMARY KEY,
		    community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
		    position     INTEGER NOT NULL DEFAULT 0,
		    title        TEXT NOT NULL,
		    body         TEXT NOT NULL DEFAULT '',
		    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS community_threads (
		    id             SERIAL PRIMARY KEY,
		    community_id   INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
		    user_id        BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    title          TEXT NOT NULL,
		    body           TEXT NOT NULL DEFAULT '',
		    pinned         BOOLEAN NOT NULL DEFAULT false,
		    locked         BOOLEAN NOT NULL DEFAULT false,
		    reply_count    INTEGER NOT NULL DEFAULT 0,
		    last_post_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		    removed_at     TIMESTAMPTZ,
		    removed_by     BIGINT,
		    removed_reason TEXT NOT NULL DEFAULT '',
		    hidden_at      TIMESTAMPTZ,
		    hidden_by      BIGINT,
		    hidden_reason  TEXT NOT NULL DEFAULT '',
		    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_community_threads_community
		     ON community_threads (community_id, last_post_at DESC)`,
		`CREATE TABLE IF NOT EXISTS community_posts (
		    id             SERIAL PRIMARY KEY,
		    thread_id      INTEGER NOT NULL REFERENCES community_threads(id) ON DELETE CASCADE,
		    user_id        BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    body           TEXT NOT NULL,
		    quoted_post_id INTEGER REFERENCES community_posts(id) ON DELETE SET NULL,
		    removed_at     TIMESTAMPTZ,
		    removed_by     BIGINT,
		    removed_reason TEXT NOT NULL DEFAULT '',
		    hidden_at      TIMESTAMPTZ,
		    hidden_by      BIGINT,
		    hidden_reason  TEXT NOT NULL DEFAULT '',
		    edited_at      TIMESTAMPTZ,
		    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_community_posts_thread
		     ON community_posts (thread_id, created_at ASC)`,
		`CREATE TABLE IF NOT EXISTS community_join_requests (
		    id               SERIAL PRIMARY KEY,
		    community_id     INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
		    user_id          BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    message          TEXT NOT NULL DEFAULT '',
		    status           TEXT NOT NULL DEFAULT 'pending',
		    response_message TEXT NOT NULL DEFAULT '',
		    -- points_held is points ESCROWED with the request: the plugin
		    -- deducts on apply and refunds on denial, so this is the amount to
		    -- give back, not a display figure.
		    points_held      INTEGER NOT NULL DEFAULT 0,
		    decided_by       BIGINT,
		    decided_at       TIMESTAMPTZ,
		    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_community_join_requests_community
		     ON community_join_requests (community_id, status, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS community_invites (
		    id           SERIAL PRIMARY KEY,
		    community_id INTEGER NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
		    code         TEXT NOT NULL UNIQUE,
		    note         TEXT NOT NULL DEFAULT '',
		    created_by   BIGINT,
		    max_uses     INTEGER NOT NULL DEFAULT 0,
		    use_count    INTEGER NOT NULL DEFAULT 0,
		    expires_at   TIMESTAMPTZ,
		    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// wireCommunitiesPlugin installs the SetDeps seams.
func wireCommunitiesPlugin(c *core.Core, w *web) error {
	if db := c.Storage.DB(); db != nil {
		if err := communitiesMigrate(db); err != nil {
			return fmt.Errorf("communities migrate: %w", err)
		}
	}
	communities.SetDeps(communities.Deps{
		BaseData: func(gc *gin.Context, extra gin.H) gin.H { return w.chromeData(gc, extra) },
		// The SAME renderer the wiki uses: goldmark with raw HTML disabled,
		// then the host's allowlist sanitizer. Community bodies are written by
		// ANY member, not a mod, so the second pass matters more here than it
		// does on the wiki.
		Markdown: siteMarkdown,
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
