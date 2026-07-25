package main

import (
	"html/template"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/forum"
)

// Forum (loon-plugins/forum) host wiring. The plugin registers its own routes
// (/community/forums/*, /admin/forum-categories/*) and renders the HOST's
// templates by name; this file supplies everything the host owes it: the
// tables, the five templates (web/templates/forum/, loaded into gin's HTML
// set — nothing else in the demo uses c.HTML), and the SetDeps seams. Seed
// data makes a fresh install show a living board instead of an empty shell.

// forumMigrate creates the plugin's tables (idempotent). Same shape as prod's
// numbered migrations; when the plugin ships its own migrations (planned for
// the PG17 consolidation window) this moves there and no-ops here.
func forumMigrate(db *sqlx.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS forum_categories (
		    id          SERIAL PRIMARY KEY,
		    name        TEXT NOT NULL UNIQUE,
		    description TEXT NOT NULL DEFAULT '',
		    ordinal     INTEGER NOT NULL DEFAULT 0,
		    color       TEXT NOT NULL DEFAULT 'blue',
		    icon        TEXT NOT NULL DEFAULT 'chat-square-text',
		    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS forum_threads (
		    id            SERIAL PRIMARY KEY,
		    category_id   INTEGER NOT NULL REFERENCES forum_categories(id) ON DELETE CASCADE,
		    user_id       BIGINT NOT NULL REFERENCES users(id),
		    title         TEXT NOT NULL,
		    thread_type   TEXT NOT NULL DEFAULT 'discussion' CHECK (thread_type IN ('discussion','recruitment')),
		    pinned        BOOLEAN NOT NULL DEFAULT false,
		    locked        BOOLEAN NOT NULL DEFAULT false,
		    reply_count   INTEGER NOT NULL DEFAULT 0,
		    last_post_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
		    hidden_at     TIMESTAMPTZ,
		    hidden_reason TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS forum_posts (
		    id             SERIAL PRIMARY KEY,
		    thread_id      INTEGER NOT NULL REFERENCES forum_threads(id) ON DELETE CASCADE,
		    user_id        BIGINT NOT NULL REFERENCES users(id),
		    body           TEXT NOT NULL,
		    quoted_post_id INTEGER REFERENCES forum_posts(id) ON DELETE SET NULL,
		    edited_at      TIMESTAMPTZ,
		    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		    hidden_at      TIMESTAMPTZ,
		    hidden_reason  TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS forum_post_reactions (
		    post_id    INTEGER NOT NULL REFERENCES forum_posts(id) ON DELETE CASCADE,
		    user_id    BIGINT  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    emoji      TEXT    NOT NULL,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    PRIMARY KEY (post_id, user_id, emoji)
		)`,
		// Access gates (prod migration 278) — ALTERs so pre-gate demo
		// installs pick them up; the CREATE above carries them implicitly
		// for fresh installs via these same statements running after it.
		`ALTER TABLE forum_categories ADD COLUMN IF NOT EXISTS see_role   TEXT NOT NULL DEFAULT 'all'`,
		`ALTER TABLE forum_categories ADD COLUMN IF NOT EXISTS read_role  TEXT NOT NULL DEFAULT 'all'`,
		`ALTER TABLE forum_categories ADD COLUMN IF NOT EXISTS write_role TEXT NOT NULL DEFAULT 'user'`,
		`ALTER TABLE forum_categories ADD COLUMN IF NOT EXISTS see_tier   SMALLINT NOT NULL DEFAULT 0`,
		`ALTER TABLE forum_categories ADD COLUMN IF NOT EXISTS read_tier  SMALLINT NOT NULL DEFAULT 0`,
		`ALTER TABLE forum_categories ADD COLUMN IF NOT EXISTS write_tier SMALLINT NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS idx_forum_threads_category ON forum_threads (category_id, last_post_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_forum_posts_thread ON forum_posts (thread_id, created_at ASC)`,
		`CREATE INDEX IF NOT EXISTS idx_forum_post_reactions_post_emoji ON forum_post_reactions (post_id, emoji)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// forumSeed pre-populates a fresh board: three categories and a few starter
// threads under the demo accounts, so the forum shows life on first boot.
// Runs only when forum_categories is empty — an existing board is never
// touched. reply_count follows the plugin's invariant: the OP post does not
// count as a reply (total posts = reply_count + 1).
func forumSeed(db *sqlx.DB, log *slog.Logger) {
	var n int
	if err := db.Get(&n, `SELECT COUNT(*) FROM forum_categories`); err != nil || n > 0 {
		return
	}
	var alice, bob int64
	if err := db.Get(&alice, `SELECT id FROM users WHERE username = 'alice'`); err != nil {
		log.Warn("forum seed: no demo users yet — categories only")
	}
	_ = db.Get(&bob, `SELECT id FROM users WHERE username = 'bob'`)

	if _, err := db.Exec(`INSERT INTO forum_categories (name, description, ordinal) VALUES
		('General',  'Anything about this indexer.',              0),
		('Support',  'Setup questions and troubleshooting.',      1),
		('Off Topic','Everything else.',                          2)
		ON CONFLICT DO NOTHING`); err != nil {
		log.Error("forum seed categories", "err", err)
		return
	}
	if alice == 0 || bob == 0 {
		return
	}
	seed := func(cat string, author int64, title, body string, replyBy int64, reply string) {
		var catID int
		if err := db.Get(&catID, `SELECT id FROM forum_categories WHERE name = $1`, cat); err != nil {
			return
		}
		var threadID int
		if err := db.Get(&threadID, `INSERT INTO forum_threads (category_id, user_id, title, last_post_at)
			VALUES ($1, $2, $3, now()) RETURNING id`, catID, author, title); err != nil {
			log.Error("forum seed thread", "title", title, "err", err)
			return
		}
		if _, err := db.Exec(`INSERT INTO forum_posts (thread_id, user_id, body) VALUES ($1, $2, $3)`,
			threadID, author, body); err != nil {
			return
		}
		if reply != "" {
			if _, err := db.Exec(`INSERT INTO forum_posts (thread_id, user_id, body) VALUES ($1, $2, $3)`,
				threadID, replyBy, reply); err != nil {
				return
			}
			_, _ = db.Exec(`UPDATE forum_threads SET reply_count = 1, last_post_at = now() WHERE id = $1`, threadID)
		}
	}
	seed("General", alice, "Welcome to the demo board",
		"This forum is served by the loon-plugins forum plugin — the same code the production indexer runs. Categories, threads, quote-replies, reactions and moderation all work here.\n\nSign in as alice/alice (admin) or bob/bob to try it.",
		bob, "Replying works too. Try the Quote button on this post.")
	seed("Support", bob, "How do I point the crawler at a provider?",
		"Asking here so the thread shows what a support question looks like.",
		alice, "Head to Setup, add your NNTP server, pick a few groups, then hit Crawl now on the Crawlers page.")
	seed("Off Topic", alice, "What are you watching this season?",
		"Empty-thread example — no replies yet. Be the first.", 0, "")
	log.Info("forum seeded", "categories", 3, "threads", 3)
}

// forumPagination is the view-model the demo's pagination partial consumes
// (handed to the plugin as `any` via Deps.Paginate).
type forumPagination struct {
	Page       int
	TotalPages int
	BaseURL    string // ends in '?' or '&'; templates append page=N
}

func (p forumPagination) HasPrev() bool { return p.Page > 1 }
func (p forumPagination) HasNext() bool { return p.Page < p.TotalPages }
func (p forumPagination) Prev() int     { return p.Page - 1 }
func (p forumPagination) Next() int     { return p.Page + 1 }

// demoForumMarkdown is the demo's sanitized renderer: HTML-escape everything,
// then paragraphs + line breaks. Deliberately minimal — the contract is
// "safe HTML", not "rich markdown"; a host wanting more swaps this func.
func demoForumMarkdown(src string) template.HTML {
	esc := template.HTMLEscapeString(strings.ReplaceAll(src, "\r\n", "\n"))
	paras := strings.Split(esc, "\n\n")
	var b strings.Builder
	for _, p := range paras {
		if strings.TrimSpace(p) == "" {
			continue
		}
		b.WriteString("<p>")
		b.WriteString(strings.ReplaceAll(p, "\n", "<br>"))
		b.WriteString("</p>\n")
	}
	return template.HTML(b.String())
}

// wireForumPlugin installs the SetDeps seams and loads the five forum
// templates into gin's HTML set. Call after core.New (the BaseData closure
// resolves the session user through c.Auth) and before core.Boot (SetDeps is
// checked at Provision).
func wireForumPlugin(c *core.Core, engine *gin.Engine) error {
	t, err := template.ParseGlob("web/templates/forum/*.html")
	if err != nil {
		return err
	}
	engine.SetHTMLTemplate(t)

	forum.SetDeps(forum.Deps{
		BaseData: func(gc *gin.Context, extra gin.H) gin.H {
			data := gin.H{}
			for k, v := range extra {
				data[k] = v
			}
			if u, ok := c.Auth.CurrentUser(gc); ok {
				data["User"] = u
				data["IsAdmin"] = u.AtLeast(core.RoleAdmin)
				// The forum's moderation routes (pin/lock, category admin)
				// gate at RoleMod — the templates must show the buttons to
				// the role that can actually use them.
				data["IsMod"] = u.AtLeast(core.RoleMod)
			}
			data["CSRFToken"] = csrfToken(gc)
			data["Path"] = gc.Request.URL.Path
			return data
		},
		Markdown: demoForumMarkdown,
		Paginate: func(page, totalPages int, baseURL string) any {
			return forumPagination{Page: page, TotalPages: totalPages, BaseURL: baseURL}
		},
	})
	return nil
}
