package main

import (
	"context"
	"html/template"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/render"
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

// ── home page: forum panels ─────────────────────────────────────────

// forumReads is the plugin's own store, opened host-side for READ-ONLY home
// page panels. The plugin publishes exactly one extension (forum.SpotlightName
// → recent threads); the poster tally has no capability, so rather than adding
// schema or an interface to the plugin we read it through the same exported
// Store the plugin itself uses. Package-level because main.go hands
// wireForumPlugin the Core (which owns the DB handle) but not the *web, and the
// demo runs exactly one of each; nil until wireForumPlugin runs, which the
// home page tolerates by dropping both panels.
var forumReads forum.Store

const (
	homeForumThreads = 5               // rows in the recent-threads panel
	homeForumPosters = 5               // rows in the top-posters panel
	homeForumKey     = "home:forum:v1" // homeForumVM
)

// forumThreadVM is one row in the home page's recent-forum-activity panel.
// NOTE there is no view count: the forum records replies, not views, so the
// mockup's second metric column has no honest source and is not filled.
type forumThreadVM struct {
	ID         int       // forum thread id
	Title      string    // thread title
	URL        string    // /community/forums/thread/<id>
	Author     string    // OP username
	AuthorRole string    // OP role from user_display ("admin"/"mod"/"user"/…)
	Category   string    // category name the thread sits in
	CategoryID int       // /community/forums/category/<id>
	Replies    int       // reply_count — the OP post is not counted
	LastPostAt time.Time // last activity; feed {{timeAgo}}
	Pinned     bool      // shows the PINNED badge
}

// forumPosterVM is one row in the top-posters panel — the truthful stand-in for
// the mockup's "top contributors", which wanted per-user upload totals this
// indexer does not track. Counts every visible post ever made, not a window.
type forumPosterVM struct {
	Rank     int    // 1-based position
	UserID   int    // forum user id
	Username string // display name
	Role     string // role from user_display
	URL      string // /u/<username>
	Posts    int    // visible post count (hidden posts excluded)
}

// homeForumVM is the cached shape of both forum panels — one cache entry, two
// queries behind it.
type homeForumVM struct {
	Threads []forumThreadVM
	Posters []forumPosterVM
}

// homeForum reads the two forum panels. Each query is independent: one failing
// (or the board simply being empty) drops that panel and keeps the other. ok is
// false when there is nothing at all to show, so the caller can skip the row.
func (w *web) homeForum(ctx context.Context) (homeForumVM, bool) {
	if forumReads == nil {
		return homeForumVM{}, false
	}
	var vm homeForumVM
	if w.cacheGet(ctx, homeForumKey, &vm) {
		return vm, len(vm.Threads) > 0 || len(vm.Posters) > 0
	}
	var okT, okP bool
	if ts, err := forumReads.GetRecentForumThreads(ctx, homeForumThreads); err != nil {
		w.logger().Error("home forum threads", "err", err)
	} else {
		okT = true
		for _, t := range ts {
			vm.Threads = append(vm.Threads, forumThreadVM{
				ID: t.ID, Title: t.Title,
				URL:        "/community/forums/thread/" + strconv.Itoa(t.ID),
				Author:     t.Username,
				AuthorRole: t.Role,
				Category:   t.CategoryName,
				CategoryID: t.CategoryID,
				Replies:    t.ReplyCount,
				LastPostAt: t.LastPostAt,
				Pinned:     t.Pinned,
			})
		}
	}
	if cs, err := forumReads.GetTopForumContributors(ctx, homeForumPosters); err != nil {
		w.logger().Error("home forum posters", "err", err)
	} else {
		okP = true
		for i, ct := range cs {
			vm.Posters = append(vm.Posters, forumPosterVM{
				Rank: i + 1, UserID: ct.UserID, Username: ct.Username,
				Role: ct.Role, URL: "/u/" + url.PathEscape(ct.Username), Posts: ct.PostCount,
			})
		}
	}
	// Only a read that actually answered is worth caching. Caching the empty
	// result of a failed read would hide both panels for the whole TTL after
	// the database recovered; a healthy-but-empty board still caches (okT/okP
	// are set on success, not on "returned rows").
	if okT || okP {
		w.cacheSet(ctx, homeForumKey, vm, time.Minute)
	}
	return vm, len(vm.Threads) > 0 || len(vm.Posters) > 0
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

// forumTemplates parses the forum plugin's template set: the shared site chrome
// plus the forum's own full documents. Reads through siteFS (the embedded copy
// by default) rather than the filesystem — the runtime image is distroless and
// carries ONLY the binary, so a disk-relative ParseGlob here finds no files and
// takes the whole process down at boot via main.go's os.Exit(1).
func forumTemplates() (*template.Template, error) {
	return template.New("forum").Funcs(tmplHelpers()).ParseFS(siteFS,
		"web/templates/site_chrome.html", "web/templates/forum/*.html")
}

// devForumRender re-parses the forum set on every render so a template edit
// shows on refresh (LOON_DEMO_DEV=1). Each request gets its own *Template, so
// there is no shared mutable state to race on — which re-calling
// engine.SetHTMLTemplate per request would have introduced. A parse error
// renders as text instead of panicking the way template.Must would.
type devForumRender struct{}

func (devForumRender) Instance(name string, data any) render.Render {
	t, err := forumTemplates()
	if err != nil {
		return render.String{Format: "forum template: %v", Data: []any{err}}
	}
	return render.HTML{Template: t, Name: name, Data: data}
}

// wireForumPlugin installs the SetDeps seams and loads the five forum
// templates into gin's HTML set. Call after core.New and after newWeb (the
// BaseData closure enriches through the host's chromeData) and before core.Boot
// (SetDeps is checked at Provision).
func wireForumPlugin(c *core.Core, engine *gin.Engine, w *web) error {
	// The forum templates are a SEPARATE set: full documents rendered by name
	// through gin's HTML set, not the demo's per-page map. They still need the
	// site header/footer/sprite, so this set names web/templates/site_chrome.html
	// explicitly — the SAME file views.go parses next to every page, which is
	// what keeps the two shells from drifting. They also get the same pure
	// helpers base.html has, since the shared chrome calls them. {{captcha}} is
	// host state and is deliberately NOT in this set; no forum page has a
	// captcha-gated form.
	t, err := forumTemplates()
	if err != nil {
		return err
	}
	engine.SetHTMLTemplate(t)
	if devReload {
		engine.HTMLRender = devForumRender{}
	}

	// Read-only handle for the home page's forum panels (see forumReads).
	if db := c.Storage.DB(); db != nil {
		forumReads = forum.NewPGStore(db)
	}

	forum.SetDeps(forum.Deps{
		// The forum's pages render the SAME site_chrome.html the host pages do,
		// so they need the same data. This used to hand-roll five keys against
		// render()'s ten and the shared chrome silently degraded: on a forum
		// page the same signed-in user lost the plugin site-nav, the admin
		// subnav, the points/unread tiles and the bell badge. It now calls the
		// host's own enrichment (views.go chromeData) — one function, two
		// callers, no way to drift. Theme comes with it, which is what keeps a
		// forum page from rendering unthemed.
		BaseData: func(gc *gin.Context, extra gin.H) gin.H {
			data := gin.H{}
			for k, v := range extra {
				data[k] = v
			}
			w.chromeData(gc, data)
			return data
		},
		Markdown: demoForumMarkdown,
		Paginate: func(page, totalPages int, baseURL string) any {
			return forumPagination{Page: page, TotalPages: totalPages, BaseURL: baseURL}
		},
	})
	return nil
}
