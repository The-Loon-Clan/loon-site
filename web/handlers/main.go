// Package handlers is the HTTP layer of loon-site: routing, the auth gates,
// the view models each page is rendered from, and the boot wiring that hands
// the plugin runtime its host-side seams.
//
// Main below is the whole of what a host does at start-up — it wires every
// core.Deps seam, boots the plugin runtime against Postgres, and mounts both
// the site's own routes and whatever the plugins registered.
//
// Everything in this file is the HOST side of the contract — the
// part a real site implements over its own sessions, job registry,
// and ledger. The plugin side lives in plugins/guestbook.
package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/config"
	"github.com/the-loon-clan/loon-site/internal/storage"

	"github.com/the-loon-clan/loon-site/internal/middleware"

	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"

	goredis "github.com/redis/go-redis/v9"

	"github.com/the-loon-clan/loon-baseline/authtoken"
	"github.com/the-loon-clan/loon-baseline/cache"
	cachememory "github.com/the-loon-clan/loon-baseline/cache/memory"
	cacheredis "github.com/the-loon-clan/loon-baseline/cache/redis"
	"github.com/the-loon-clan/loon-baseline/captcha"
	"github.com/the-loon-clan/loon-baseline/events"
	"github.com/the-loon-clan/loon-baseline/heartbeat"
	"github.com/the-loon-clan/loon-baseline/jobtrigger"
	"github.com/the-loon-clan/loon-baseline/notify"
	"github.com/the-loon-clan/loon-baseline/password"
	"github.com/the-loon-clan/loon-baseline/users"

	// Plugins register themselves Caddy-style at init time. The loon-plugins
	// ones are named imports because the host injects their deps via SetDeps.
	"github.com/the-loon-clan/loon-plugins/backups"
	_ "github.com/the-loon-clan/loon-plugins/catalog"
	_ "github.com/the-loon-clan/loon-plugins/hitrun"
	_ "github.com/the-loon-clan/loon-plugins/perks"

	// forum is imported (and its init runs) via forum_web.go's SetDeps wiring.
	"github.com/the-loon-clan/loon-plugins/pluginapi"
	_ "github.com/the-loon-clan/loon-plugins/pointstore"

	// events owns scheduled windows — the WHEN other plugins hang behaviour on.
	// Lifted out of rewards, which now GATES on it: rewards links its admin page
	// at /admin/p/events, and without this import that link 404s. Self-contained
	// (Storage + Scheduler, no SetDeps), so the import is the wiring.
	_ "github.com/the-loon-clan/loon-plugins/events"
	// ranks + rewards draw their UI through loon's VIEW system
	// (core.RegisterView), not gin templates, so they need no host templates and
	// no SetDeps — a blank import is the whole wiring.
	_ "github.com/the-loon-clan/loon-plugins/ranks"
	"github.com/the-loon-clan/loon-plugins/scraper"
	"github.com/the-loon-clan/loon-plugins/stats"
	"github.com/the-loon-clan/loon-plugins/tracker"
	_ "github.com/the-loon-clan/loon-plugins/usenet"

	_ "github.com/the-loon-clan/loon-site/plugins/guestbook"
)

// Main boots the demo site.
//
// Exported, and called from cmd/loonsite, because the repository root is a
// package rather than the command: //go:embed cannot reference a parent
// directory, and web/templates and web/static are embedded (the runtime image
// is distroless and contains no web/ directory at all). The asset root must
// therefore be a package at the module root, so the command lives under cmd/
// and this is the one symbol that crosses between them.
func Main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// FIRST, before anything that can fail. This was logged further down, after
	// the database connect — so a container that could not reach Postgres
	// exited having never said what it was, which is the exact moment somebody
	// wants to know whether they deployed the version they think they did.
	logger.Info("loon-site starting", "version", BuildInfo())

	dsn := os.Getenv("LOON_DSN")
	if dsn == "" {
		dsn = "postgres://demo:demo@localhost:5544/loon_demo?sslmode=disable"
	}
	db, err := connect(dsn)
	if err != nil {
		logger.Error("database unreachable — run `docker compose up -d db` first", "err", err)
		os.Exit(1)
	}

	// The data layer, built once here and passed to everything that needs
	// it — the migrations, the wirings, and the web struct. There is no
	// package global to reach for any more.
	data := storage.New(db)
	// conn is the same pool, constant-SQL only. Handed to everything of ours
	// that runs statements; the raw db still goes to loon and loon-baseline,
	// whose APIs take a pool.
	conn := data.DB()

	engine := gin.Default()

	// Who is allowed to tell us the client's IP.
	//
	// gin trusts every proxy by default, so ClientIP() returns whatever
	// X-Forwarded-For says, and that header is a request header like any other:
	// anyone talking to the port can set it. Two logins from one machine with
	// two invented values recorded two different addresses in login_logs —
	// which is the page a member opens to check whether somebody else has been
	// in their account, and the record an admin reads after a breach.
	//
	// The default here is to trust NOBODY, so ClientIP() is the peer address
	// and cannot be forged. A deployment behind a proxy names it:
	//
	//   LOON_TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12
	//
	// Failing closed matters more than the convenience: a site that forgets to
	// set this logs the proxy's own address for everybody, which is useless but
	// obvious. Trusting by default is useless and looks correct.
	if err := engine.SetTrustedProxies(trustedProxies()); err != nil {
		logger.Error("trusted proxies", "err", err)
	}

	// Liveness endpoint for a reverse proxy / load balancer health check.
	// Registered before any middleware so it's always cheap and always answers —
	// even while the site is in maintenance mode (the proxy needs a true "is the
	// process up?" signal, independent of the app's maintenance flag).
	// "ok" plus the build, because this is the one endpoint reachable on a site
	// that is otherwise refusing everybody — during maintenance, or behind a
	// members-only gate — and "which version is that container running" is the
	// question being asked when somebody is looking at it.
	engine.GET("/healthz", func(c *gin.Context) {
		c.String(http.StatusOK, "ok %s", BuildInfo())
	})

	// --- Demo users + username/password login. A real host wires its session
	// store + users table here; the demo keeps two in-memory users whose
	// password (bcrypt-verified) equals their username, and signs an HMAC
	// session cookie on login. The web struct (views.go) owns the templates,
	// static assets, session cookie, and the public/login pages.
	// Main is where a boot failure becomes an exit. The helpers return their
	// errors so a test can call them; only this function decides the process
	// cannot continue.
	st, err := wireBaselineStores(conn, logger)
	if err != nil {
		logger.Error("boot", "err", err)
		os.Exit(1)
	}
	if err := migrateSiteTables(data, logger, st.users); err != nil {
		logger.Error("boot", "err", err)
		os.Exit(1)
	}
	wsrv := newWeb(st.users, st.sessionSecret, logger, data)
	wsrv.loginLog = st.loginLog
	wsrv.ipSalt = string(st.sessionSecret) // demo salt; a real host uses a dedicated ip_salt secret
	// Cloudflare Turnstile hook (loon-baseline). Disabled unless both keys are
	// set, so the demo runs without CF; set TURNSTILE_SITEKEY + TURNSTILE_SECRET
	// (or the CF test keys) to see it gate login + register.
	wsrv.captcha = captcha.New(captcha.Config{
		SiteKey: os.Getenv("TURNSTILE_SITEKEY"),
		Secret:  os.Getenv("TURNSTILE_SECRET"),
	})
	// Page cache (loon-baseline). In-memory by default so the demo needs no
	// Redis; set REDIS_ADDR to use the shared redis impl instead — no call site
	// changes, just the backend.
	// One Redis client, shared between the page cache (loon-baseline) and the
	// core Redis seam (core.Redis) that Redis-capable plugins consume — e.g. the
	// usenet plugin's staging: redis mode. In-memory cache + no Redis seam by
	// default (Redis stays nil); set REDIS_ADDR to enable both at once.
	backend, redisClient := chooseCache(os.Getenv("REDIS_ADDR"), logger)
	wsrv.cache = backend

	// Reset/verify flow. The demo "mailer" just logs the message (link included)
	// so you can follow it in the logs; a real host sends via SMTP.
	wsrv.resetFlow = authtoken.Flow{
		Tokens: st.tokens, Users: st.users, Hasher: password.Hasher{},
		MinPwLen: minPasswordLen,
		BaseURL:  getenvDefault("LOON_BASE_URL", "http://localhost:8090"),
		Mail: func(to, subject, body string) error {
			logger.Info("email (demo mailer)", "to", to, "subject", subject, "body", body)
			return nil
		},
	}
	// gin-contrib session middleware (the prod scheme) must be installed before
	// any route that logs in or reads the user.
	//
	// Store() rather than Middleware(), which panics when Redis is unreachable.
	// That is the right default and the baseline says so — "a host must not
	// silently serve loginless" — and it also says a host wanting explicit
	// handling calls Store() itself. This one does.
	//
	// The reason is that Redis here is a CACHE that sessions happen to share.
	// Losing it took the whole site down at boot with a stack trace: not
	// degraded, not read-only, just gone, on a demo whose Redis is optional
	// enough that compose can bring the app up without it. Falling back to
	// cookie-backed sessions keeps logins working; what it costs is
	// server-side revocation and sharing across replicas, which is worth
	// saying loudly and is not worth an outage.
	//
	// Deliberately NOT silent: this is a downgrade in what the session store
	// can promise, so it is an ERROR line naming the address that failed.
	sessionStore, err := wsrv.auth.Session.Store()
	if err != nil {
		logger.Error("session store unavailable — falling back to cookie sessions; "+
			"logins work, but sessions are no longer revocable server-side or shared across replicas",
			"err", err)
		fallback := wsrv.auth.Session
		fallback.RedisAddr = "" // the empty-address branch builds a cookie store
		if sessionStore, err = fallback.Store(); err != nil {
			// A cookie store cannot really fail, so this is unreachable in
			// practice — but silently serving with no session store at all
			// would be the loginless outcome the panic exists to prevent.
			logger.Error("cookie session store failed too", "err", err)
			os.Exit(1)
		}
	}
	engine.Use(sessions.Sessions(wsrv.auth.Session.Name, sessionStore))
	// CSRF double-submit guard (after the session, which it reads/writes). Every
	// state-changing POST must carry the _csrf token; templates embed it.
	engine.Use(middleware.CSRF())
	// Maintenance gate: while ON, visitor pages get the 503 page. Bypass /admin
	// (so the operator can toggle it off), /login+/logout (sign in first),
	// /static, /healthz — and /api+/rss, so the Newznab API keeps serving while
	// the web UI is down. That mirrors a real deployment where the API tier runs
	// without this middleware; here it's one process, so the bypass stands in.
	engine.Use(st.maint.Middleware(func(g *gin.Context) bool {
		p := g.Request.URL.Path
		return strings.HasPrefix(p, "/admin") || strings.HasPrefix(p, "/static") ||
			strings.HasPrefix(p, "/api") || strings.HasPrefix(p, "/rss") ||
			p == "/login" || p == "/logout" || p == "/healthz"
	}))
	// Members-only browsing (access_web.go). After the session middleware,
	// because it has to know who you are, and after maintenance so a site in
	// maintenance says so rather than bouncing you to a login you cannot use.
	engine.Use(wsrv.requireLoginMiddleware())
	// Hit-and-run enforcement (hitrun_web.go). Installed HERE, before the
	// plugins mount, because gin applies middleware to routes registered after
	// it — and the tracker's download route is registered during core.Boot,
	// further down. It matches one path prefix and lets everything else past
	// untouched.
	engine.Use(enforceHitRunBlock(wsrv))
	wsrv.mount(engine)

	// Hand loon the session policy through the baseline's core.Auth adapter.
	auth := wsrv.auth.CoreAuth()
	usersSvc := core.NewUsers(wsrv.usersAdapter())

	// --- In-memory points ledger. A real host writes the ledger
	// row + balance update atomically; the demo keeps a map.
	// Points are DURABLE: users.points for the balance, points_ledger for the
	// history, both moved in one statement pair so they cannot disagree. The
	// previous in-memory map lost every balance on restart, and the communities
	// plugin SELECTs COALESCE(u.points, 0) — a column that exists but never
	// matches the real balance is worse than no column. See points_web.go.
	if err := data.MigratePoints(); err != nil {
		logger.Error("points migrate", "err", err)
		os.Exit(1)
	}
	// Privacy + notification preferences (settings_web.go).
	if err := data.MigrateSettings(); err != nil {
		logger.Error("settings migrate", "err", err)
		os.Exit(1)
	}

	// Grab counting (grabs_web.go) — the source trending, "N downloads" and the
	// economy plugin's uploader bonus were all waiting on.
	if err := data.MigrateGrabs(); err != nil {
		logger.Error("grabs migrate", "err", err)
		os.Exit(1)
	}

	// Bookmarks (bookmarks_web.go) — saved releases, retiring MOCKS M4.
	if err := data.MigrateBookmarks(); err != nil {
		logger.Error("bookmarks migrate", "err", err)
		os.Exit(1)
	}

	// Widget placements (widgets_web.go) — WHERE an operator has put each
	// registered widget. The widgets themselves come from plugins at boot and
	// live in memory; this table only remembers the arrangement.
	if err := data.MigrateWidgets(); err != nil {
		logger.Error("widgets migrate", "err", err)
		os.Exit(1)
	}

	// Last seen (presence_web.go) and follows (follows_web.go) — MOCKS M1 and
	// M3, the last two placeholders on the profile.
	if err := data.MigrateLastSeen(); err != nil {
		logger.Error("last-seen migrate", "err", err)
		os.Exit(1)
	}
	if err := data.MigrateFollows(); err != nil {
		logger.Error("follows migrate", "err", err)
		os.Exit(1)
	}
	// Topics/Posts read forum_threads and forum_posts, which live in
	// `public` and are the host's — no migration of its own to run.

	points := pgPoints{db: conn}
	pointsSvc := core.NewPoints(points.adapter())
	wsrv.points = pointsSvc // navbar balance readout

	// Notification fan-out (loon-baseline): core's single NotifyFn becomes a HOOK
	// point — every registered Sink gets each notification (the bell/inbox store,
	// a logger, and any channel a plugin adds by looking up the fanout capability).
	inbox := notify.NewPGStore(db.DB)
	if err := inbox.Migrate(context.Background()); err != nil {
		logger.Error("notify migrate", "err", err)
		os.Exit(1)
	}
	notifications := notify.NewFanout(
		notify.InboxSink(inbox),
		notify.LogSink(func(userID int64, n core.Notification) {
			logger.Info("notification", "to", userID, "kind", n.Kind, "title", n.Title)
		}),
	)
	wsrv.inbox = inbox // navbar unread-count bell

	// Event bus (loon-baseline): a general publish/subscribe hook point,
	// registered below as the "events" capability so plugins can EmitEvent
	// through it. We subscribe a cache invalidator — when the usenet plugin
	// reports newly-ingested releases, drop the API tier's search-cache
	// namespace so it repopulates from fresh data. In this single-process demo
	// that clears wsrv.cache; in a distributed deployment the SAME wiring in the
	// worker clears the shared Redis the loon-api read tier populated (see
	// LOON-DISTRIBUTED — no message bus, just shared state a subscriber touches).
	bus := events.NewBus()
	bus.Subscribe(pluginapi.EventIngested, func(ctx context.Context, payload any) {
		if pd, ok := wsrv.cache.(cache.PrefixDeleter); ok {
			if err := pd.DeletePrefix(ctx, pluginapi.NewznabCachePrefix); err != nil {
				logger.Warn("invalidate search cache", "err", err)
				return
			}
		}
		logger.Info("search cache invalidated on ingest", "new_releases", payload)
	})

	// The scheduler is loon's batteries-included one: jobs land in
	// schedule.Default (a host admin page would render its
	// GetAllSnapshots), and LogSink mirrors job log lines to stdout
	// so the demo's once-a-minute stats job stays visible.
	// Through jobLogDedup (joblog_web.go): a job that repeats one line in a
	// loop — the usenet builder's "already running — skipping overlap", once
	// per backfill round while a catch-up pass holds the lock — would
	// otherwise be the only thing in the log. Repeats are counted and
	// reported, never dropped.
	jobLog := newJobLogDedup(logger)
	schedule.LogSink = jobLog.Log

	usenetStaging := os.Getenv("USENET_STAGING")
	if usenetStaging == "" {
		usenetStaging = "pg"
	}
	logger.Info("usenet staging", "mode", usenetStaging, "redis", redisClient != nil)

	// Which kind of process this is, and therefore which plugins provision here.
	//
	// core.Boot drops any plugin that does not run in this kind, so the scraper
	// (worker-only) and the tracker (web and api) end up in different processes
	// without either knowing about the other. "all" is the default and bypasses
	// the filter entirely — one container that does everything, which is what
	// `docker compose up` still gives you.
	role := config.Role()
	if !config.RoleIsValid() {
		// Ignored, not fatal — but said out loud. A silently-ignored role is how
		// a deployment ends up with two web processes and no worker, which looks
		// exactly like a working site until somebody notices the crawler has not
		// run for a week.
		logger.Warn("LOON_ROLE is not one of all/web/worker — running as all",
			"mode", os.Getenv("LOON_ROLE"))
	}
	logger.Info("process role", "mode", role, "runs_jobs", config.RunsJobs())

	c, err := core.New(core.Deps{
		Process:   role,
		Users:     usersSvc,
		Auth:      auth,
		RBAC:      core.NewRBAC(),
		Storage:   core.NewStorage(db),
		Scheduler: schedule.CoreScheduler(schedule.Default),
		Router: core.NewRouter(core.RouterAdapter{
			Engine:          engine,
			AdminMiddleware: wsrv.auth.Require(core.RoleAdmin),
		}),
		Logger: logger,
		Config: core.NewConfig(map[string]any{
			"guestbook": map[string]any{"points_per_entry": 5},
			// Catalog matching runs on a much shorter cadence than the plugin's
			// hourly default, because on a LIVE index an hour of idle is the
			// bottleneck rather than a kindness.
			//
			// Measured here: the crawler adds ~1,270 releases/hour and 46,000
			// sit uncovered, while a match run over already-seen titles finishes
			// in ELEVEN SECONDS — the sources cache their misses, so a repeat
			// pass costs almost nothing and then the job sleeps for an hour.
			// At the default cadence the backlog grows faster than it clears,
			// and coverage falls however good the matchers are.
			//
			// This is not a licence to hammer anyone. Every source paces its own
			// requests (250ms Wikipedia, 100ms Open Library) and that is the
			// politeness control; the cadence only decides how much of the time
			// the job is allowed to be working at that rate. A run with 3,000
			// genuinely new titles still takes ~35 minutes, and the next one
			// starts this long after it FINISHES, not after it started.
			"scraper": map[string]any{"interval_min": 10},
			// Usenet staging backend: "pg" (durable Postgres, the default) or
			// "redis" (prod's assembly pipeline — needs REDIS_ADDR so core.Redis
			// is wired, else the plugin refuses to boot rather than silently
			// falling back). Flip with USENET_STAGING=redis.
			"usenet": map[string]any{"staging": usenetStaging},
			// The BitTorrent tracker, OFF unless asked for. Not caution on the
			// host's part — it is the plugin's own default, because a tracker
			// publishes announce endpoints, mints passkeys and starts keeping
			// ratio accounting the moment it is reachable. Everything else here
			// is inert until a member visits it.
			"tracker": map[string]any{
				"enabled":  config.TrackerEnabled(),
				"site_url": trackerSiteURL(),
				// Cheat detection (tracker/cheat.go). OFF unless asked for, and
				// on its own flag: running a tracker is a feature, judging
				// members' accounting is a policy, and the switch that turns on
				// accusations should have to be typed. Thresholds keep the
				// plugin's defaults, which are deliberately generous — the
				// output is read by a person, and a list full of maybes is a
				// list nobody reads.
				"cheat": map[string]any{"enabled": config.CheatCheckEnabled()},
			},
			// Tracker economy: how long a spent token lasts. Seven days by
			// default, matching the hit-and-run seedtime requirement, so a
			// freeleech download is free for exactly as long as a member is
			// required to seed it.
			"perks": map[string]any{"token_hours": 168},
			// Hit-and-run rules over the tracker's accounting. Built from the
			// SAME struct the host's blocking middleware reads, so the job that
			// warns a member and the page that stops them downloading cannot
			// disagree about the limit — see hitrun_web.go.
			"hitrun": hitRunConfig(),
			// One host per torrent per member. OFF by default like the rest of
			// the rules that can refuse somebody something, and it needs Redis
			// — a claim has to be shared across the web and api processes,
			// which both serve announce.
			"seedlock": map[string]any{
				"enabled":      config.SeedLockEnabled(),
				"lock_minutes": 30,
				"identify_by":  "ip",
			},
		}),
		// prefFiltered enforces per-kind notification preferences at the ONE
		// entry point every plugin's Notify goes through — see settings_web.go.
		Notifications: core.NewNotifications(core.NotificationsAdapter{
			NotifyFn: prefFiltered(conn, notifications.Deliver),
		}),
		Points: pointsSvc,
		// In-memory grants (lost on restart) — a real host backs this
		// with a user_entitlements table.
		//
		// The BASELINE is what actually matters here. The messages plugin
		// gates "may this user start a DM" purely on ents.Has("dm.initiate")
		// — its error text mentions roles, but the code delegates the whole
		// decision to the host. So without a baseline every send failed
		// closed, including for an admin. Baseline grants are evaluated from
		// RoleOf at resolution time and never written to the store, so a role
		// change takes effect within one cache window with no backfill.
		Entitlements: core.NewEntitlements(core.EntitlementsConfig{
			Store: core.NewMemEntitlementStore(),
			RoleOf: func(ctx context.Context, userID int64) (core.Role, bool, error) {
				u, err := st.users.ByID(ctx, userID)
				if err != nil || u == nil {
					// (0, false, nil) = "no such user, cacheable". Reserve the
					// error return for transient failures, which are NOT cached.
					return 0, false, nil
				}
				return u.ToCore().Role, true, nil
			},
			Baseline: map[core.Role][]core.EntitlementGrant{
				core.RoleMod: {{Key: "dm.initiate", Val: 1, Source: "role"}},
				// Every signed-in member may use the tracker on this demo.
				//
				// A real private site would grant this by rank, by invite or by
				// hand — the plugin deliberately treats it as an entitlement
				// rather than a column so the host decides. The gate fails
				// CLOSED without a grant, so leaving this out would leave the
				// tracker mounted and refusing everyone, which looks like a
				// bug rather than a policy.
				core.RoleUser: {{Key: tracker.EntitlementKey, Val: 1, Source: "role"}},
			},
		}),
		HTTPClient: core.NewHTTPClient(),
		Errors:     core.NewErrorReporter(core.ErrorAdapter{}), // stderr fallback
		// Optional: only wired when REDIS_ADDR is set; otherwise Core.Redis stays
		// nil and Redis-capable plugins fall back to their durable mode.
		Redis: func() core.RedisService {
			if redisClient == nil {
				return nil
			}
			return core.NewRedis(redisClient)
		}(),
	})
	if err != nil {
		logger.Error("core.New", "err", err)
		os.Exit(1)
	}

	wirePluginSeams(c, wsrv, engine, logger)

	// --- loon-plugins wiring (all worker plugins; they boot under Process
	// "all"). The scraper needs the shared catalog.Registry on the extension
	// registry — empty here until a source module lands — plus a write sink;
	// backups needs a place to put entries; stats needs a cache. The demo
	// impls just log (or write to a temp dir), the way a real host would swap
	// in its catalog_entry table / archive store / Redis cache.
	wireMetadataSources(c, logger)
	// Invites: the host capability the store's invite items need. Invites live
	// on users, so no sibling plugin can own this — see invites_web.go.
	if err := wireInvites(c, data); err != nil {
		logger.Error("invites wiring", "err", err)
		os.Exit(1)
	}

	// Publish the Turnstile verifier as a cross-cutting capability so plugins
	// (e.g. the dailyreward claim button) can require a captcha without importing
	// loon-baseline. Registered before Boot so plugin Provision can Lookup it; a
	// disabled verifier means plugins gate nothing (graceful).
	if err := c.Register("captcha", wsrv.captcha); err != nil {
		logger.Error("register captcha capability", "err", err)
	}
	// Publish the notification fan-out so a plugin can Add its own delivery
	// channel (Lookup "notify.fanout" + Add a sink) during Provision.
	if err := c.Register("notify.fanout", notifications); err != nil {
		logger.Error("register notify.fanout capability", "err", err)
	}
	// Publish the event bus so plugins can EmitEvent through it (usenet emits
	// EventIngested after a build). Registered before Boot, so it's present when
	// jobs first run.
	if err := pluginapi.RegisterEvents(c, bus); err != nil {
		logger.Error("register events capability", "err", err)
	}
	// Achievement metric counters (rewardsmetrics_web.go). BEFORE Boot is not a
	// preference: the rewards plugin collects these during its own Provision by
	// scanning the registry, so one registered afterwards is never seen and its
	// achievements sit at zero forever with nothing logged.
	if err := registerAchievementMetrics(c, conn); err != nil {
		logger.Error("register achievement metrics", "err", err)
	}
	// Scraper enrichment: persist entries + link covers via the catalog plugin
	// (resolved lazily after Boot), fed release candidates from the usenet index.
	scraper.SetDeps(scraper.Deps{
		Sink:       lazySink{w: wsrv},
		Candidates: wsrv.catalogCandidates,
		Link:       wsrv.linkCover,
	})
	stats.SetDeps(stats.Deps{Cache: func(_ context.Context, s []pluginapi.Stat) error {
		logger.Info("stats snapshot cached", "metrics", len(s))
		return nil
	}})
	backups.SetDeps(backups.Deps{OpenEntry: demoBackupOpener(logger)})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Re-read the maintenance flag periodically so a toggle on another web
	// replica reaches this one (single process here, but the pattern is the same
	// as loon-api's settings refresh — coordinate through shared state).
	go st.maint.StartRefresh(ctx, 5*time.Second)

	// Drain the cross-process run-now queue: claim requests another process
	// enqueued and run the job locally via schedule.TriggerJob.
	//
	// WORKER SIDE ONLY, and this is the pairing that makes "Run now" work at
	// all once the processes are split. The button lives on an admin page in
	// the web process, where the job does not exist to be triggered; it enqueues
	// instead, and the process that owns the job picks it up here. A web process
	// draining this queue would claim the request and then find nothing to run,
	// so the job would be marked handled and never execute.
	if !config.RunsJobs() {
		// This process serves the admin page but owns no jobs, so "Run now"
		// hands the request to the one that does.
		wsrv.jobQueue = st.jobTriggers
	}
	if config.RunsJobs() {
		go jobtrigger.StartPoller(ctx, st.jobTriggers, 3*time.Second, func(name string) bool {
			ran := schedule.TriggerJob(name)
			logger.Info("job trigger drained", "job", name, "ran", ran)
			return ran
		})
	}

	// Beat this instance's presence every 15s, under its own role, so the
	// service-heartbeat table distinguishes a web replica from the worker. With
	// every process reporting "all" they overwrite each other and the table
	// answers "is something alive" when the question is "is the WORKER alive".
	go heartbeat.StartReporter(ctx, st.heartbeat,
		heartbeat.HostID(role), role, "loon-demo", 15*time.Second)

	// user_display is the plugin-facing identity contract, and the baseline
	// builds it with avatar_path and reputation_tier stubbed to constants until
	// a host fills them in (see migrateUserDisplay). Replaced HERE, after every
	// migration that adds those columns has run, and before Boot, so the first
	// plugin query already sees the real view.
	if err := data.MigrateAvatarMod(); err != nil {
		logger.Error("avatar moderation migrate", "err", err)
		os.Exit(1)
	}
	if err := data.MigrateSecurity(); err != nil {
		logger.Error("security migrate", "err", err)
		os.Exit(1)
	}
	if err := data.MigrateWishlist(); err != nil {
		logger.Error("wishlist migrate", "err", err)
		os.Exit(1)
	}
	if err := data.MigrateGifts(); err != nil {
		logger.Error("gifts migrate", "err", err)
		os.Exit(1)
	}
	if err := data.MigrateUndo(); err != nil {
		logger.Error("undo migrate", "err", err)
		os.Exit(1)
	}
	if err := data.MigrateCommunityMod(); err != nil {
		logger.Error("community moderation migrate", "err", err)
		os.Exit(1)
	}
	if err := data.MigrateUserDisplay(); err != nil {
		logger.Error("user_display migrate", "err", err)
		os.Exit(1)
	}

	contractsDB, contractsCore = db, c

	// The host's own placeable widgets, registered BEFORE Boot so they sit in
	// the same registry as the plugins' and the editor lists them together —
	// an operator arranging a page should not be able to tell which came from
	// where. See widgetsbuiltin_web.go.
	wsrv.registerBuiltinWidgets(c)

	rt, err := core.Boot(ctx, c)
	if err != nil {
		logger.Error("core.Boot", "err", err)
		os.Exit(1)
	}

	// The contract audit (contracts_web.go), AFTER Boot — extensions are
	// registered during each plugin's Provision, so before Boot every one of
	// them is legitimately absent and the audit would report the whole site as
	// broken.
	//
	// It reports and does not stop the site. Most of these seams are optional
	// by design, and an operator who has deliberately not wired one should not
	// have to argue with the binary about it.
	reportContracts(ctx, c, conn, logger)

	// The avatar file sweep (avatarsweep_web.go) -- the only thing that deletes
	// an avatar file, now that undo needs replaced and cleared ones to survive.
	wsrv.startAvatarSweep(ctx, conn, logger)

	// --- Admin dashboard. core.AdminHandler renders the plugin manifest;
	// schedule.JobsAdminHandler renders the jobs/services table with manual
	// run/pause controls. Both sit behind the same admin role gate the plugins
	// use — log in as an admin (alice) in the browser to reach them.
	// The demo renders its admin pages (plugins/jobs/usenet) in its own layout
	// for a consistent look, using loon's data (rt.Plugins, schedule snapshots).
	wsrv.rt = rt
	// Moderation sits at RoleMod, NOT under /admin — that group is
	// RoleAdmin-only, so a moderator could not reach a queue mounted there.
	// The distinction is the point of having the role: reviewing an avatar is
	// exactly the judgement a moderator is for, and needing an admin for it is
	// how a queue goes unworked (see the reports plugin, whose oldest open
	// item was 98 days).
	// Two queues at one prefix, gated separately.
	//
	//   /moderation          RoleAdmin — the curated queue (communitymod_web.go)
	//   /moderation/avatars  RoleMod   — has staff looked at it (avatarmod_web.go)
	//
	// /moderation is ADMIN-ONLY for now, and the voting machinery behind it is
	// deliberately left intact. The plan is admin-curated first and
	// community-driven second, and those are the same queue with the gate in a
	// different place — opening it up later is this one line, not a rewrite.
	//
	// REPORTING stays open to every member (POST /u/:name/report-avatar, on the
	// profile). A curated queue with no way for members to put anything in it
	// is an empty queue.
	//
	// The group takes the LOOSER gate and each route carries its own, rather
	// than the other way round: the stricter gate is then written next to the
	// stricter page, where it is read.
	wireAdminAndViews(c, wsrv, engine, data, conn, st, rt, inbox, ctx, stop, logger, redisClient)

}

// seedDemoUsers creates the two demo accounts (password == username) directly
// via the store — bypassing the register flow's password-strength rule, since
// seeding is a privileged setup step, not a user signup. Login still exercises
// the real store; new signups still get the 8-char minimum.
func seedDemoUsers(store users.Store, log *slog.Logger) {
	hasher := password.Hasher{}
	for _, s := range []struct {
		name string
		role core.Role
	}{{"alice", core.RoleAdmin}, {"bob", core.RoleUser}} {
		if _, err := store.ByUsername(context.Background(), s.name); err == nil {
			continue // already seeded
		} else if !errors.Is(err, users.ErrNotFound) {
			log.Error("seed lookup", "user", s.name, "err", err)
			continue
		}
		hash, err := hasher.Hash(s.name) // password == username
		if err != nil {
			log.Error("seed hash", "user", s.name, "err", err)
			continue
		}
		if _, err := store.Create(context.Background(), &users.User{Username: s.name, PasswordHash: hash, Role: s.role}); err != nil {
			log.Error("seed create", "user", s.name, "err", err)
		}
	}
}

func connect(dsn string) (*sqlx.DB, error) {
	var err error
	for i := 0; i < 10; i++ {
		var db *sqlx.DB
		if db, err = sqlx.Connect("postgres", dsn); err == nil {
			return db, nil
		}
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("after 10 attempts: %w", err)
}

// chooseCache picks the page-cache backend, and returns the Redis client to
// share with the core.Redis seam when there is one.
//
// PING before adopting it. Without that check the site technically stays up
// when Redis is gone and is unusable anyway: every cache read dials, retries
// five times and fails, so a page reading four keys spent TEN SECONDS before
// rendering — long enough that the browser had already hung up. A degraded
// cache has to be fast about being degraded.
//
// One short timeout, once, at boot. Redis dying LATER is a different problem
// and not one a boot probe can answer; this only stops the site adopting a
// backend that is already unreachable.
//
// A nil client is returned alongside the memory cache, and callers must treat
// it as "no Redis seam" — the fallback has to be total. Handing back a live
// client for a Redis that failed its ping would leave plugins talking to a
// server the cache had already given up on.
//
// Extracted from Main so it can be tested: this is a decision with three
// outcomes, two of which only happen when something is wrong.
func chooseCache(addr string, logger *slog.Logger) (cache.Cache, *goredis.Client) {
	if addr == "" {
		logger.Info("cache backend", "kind", "memory")
		return cachememory.New(), nil
	}
	client := goredis.NewClient(&goredis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := client.Ping(ctx).Err()
	cancel()
	if err != nil {
		// Loud, and naming the address: an operator who set REDIS_ADDR expects
		// Redis, and silently running on an in-process cache is how a
		// two-replica deployment starts serving two different versions of
		// every cached page.
		logger.Error("redis unreachable — falling back to the in-memory cache; "+
			"pages are still cached, but per-process and not shared",
			"addr", addr, "err", err)
		_ = client.Close()
		return cachememory.New(), nil
	}
	logger.Info("cache backend", "kind", "redis", "addr", addr)
	return cacheredis.New(client), client
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// trustedProxies reads LOON_TRUSTED_PROXIES into the list gin will believe
// about X-Forwarded-For.
//
// nil, not an empty slice, when nothing is configured: gin reads nil as "trust
// no proxy" and an empty non-nil slice the same way, but nil is the one the
// documentation names, and this is not a place to be clever about a difference
// that could change under us.
//
// Each entry is an IP or a CIDR, comma-separated. Whitespace is tolerated
// because this is written by hand in a compose file and "10.0.0.0/8, 172.16.
// 0.0/12" should not fail silently — SetTrustedProxies rejects a bad entry and
// the caller logs it.
func trustedProxies() []string {
	raw := strings.TrimSpace(os.Getenv("LOON_TRUSTED_PROXIES"))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// demoBackupOpener returns the backups plugin's OpenEntry seam, writing each
// backup entry under the mounted data directory. A real host would stream into
// a tar/dated dir or an object store.
//
// Under uploadRoot, not os.TempDir(): only /data is a volume, so a backup
// written anywhere else lives in the container layer and is discarded by the
// next `up --build` — silently, and precisely when someone reaches for it. The
// demo is a demonstration, and one that quietly throws its backups away
// demonstrates the wrong thing.
func demoBackupOpener(log *slog.Logger) func(context.Context, string) (io.WriteCloser, error) {
	dir := filepath.Join(uploadRoot, "backups")
	return func(_ context.Context, name string) (io.WriteCloser, error) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		log.Info("backup entry", "path", filepath.Join(dir, name))
		return os.Create(filepath.Join(dir, name))
	}
}
