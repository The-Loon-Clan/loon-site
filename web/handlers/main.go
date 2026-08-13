// loon-demo-site is the smallest useful host for the loon
// framework: it wires every core.Deps seam with an in-memory or
// logging implementation, boots the plugin runtime against a real
// Postgres, and serves one demo plugin (guestbook).
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

	"github.com/the-loon-clan/loon/catalog"
	"github.com/the-loon-clan/loon/core"
	"github.com/the-loon-clan/loon/schedule"

	goredis "github.com/redis/go-redis/v9"

	"github.com/the-loon-clan/loon-baseline/account"
	"github.com/the-loon-clan/loon-baseline/adminusers"
	"github.com/the-loon-clan/loon-baseline/apikey"
	"github.com/the-loon-clan/loon-baseline/authtoken"
	"github.com/the-loon-clan/loon-baseline/cache"
	cachememory "github.com/the-loon-clan/loon-baseline/cache/memory"
	cacheredis "github.com/the-loon-clan/loon-baseline/cache/redis"
	"github.com/the-loon-clan/loon-baseline/captcha"
	"github.com/the-loon-clan/loon-baseline/events"
	"github.com/the-loon-clan/loon-baseline/heartbeat"
	"github.com/the-loon-clan/loon-baseline/jobtrigger"
	"github.com/the-loon-clan/loon-baseline/loginlog"
	"github.com/the-loon-clan/loon-baseline/notify"
	"github.com/the-loon-clan/loon-baseline/password"
	"github.com/the-loon-clan/loon-baseline/profile"
	"github.com/the-loon-clan/loon-baseline/users"

	// Plugins register themselves Caddy-style at init time. The loon-plugins
	// ones are named imports because the host injects their deps via SetDeps.
	"github.com/the-loon-clan/loon-plugins/backups"
	_ "github.com/the-loon-clan/loon-plugins/catalog"
	"github.com/the-loon-clan/loon-plugins/dailyreward"
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
	"github.com/the-loon-clan/loon-plugins/rewards"
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

	dsn := os.Getenv("LOON_DSN")
	if dsn == "" {
		dsn = "postgres://demo:demo@localhost:5544/loon_demo?sslmode=disable"
	}
	db, err := connect(dsn)
	if err != nil {
		logger.Error("database unreachable — run `docker compose up -d db` first", "err", err)
		os.Exit(1)
	}

	engine := gin.Default()

	// Liveness endpoint for a reverse proxy / load balancer health check.
	// Registered before any middleware so it's always cheap and always answers —
	// even while the site is in maintenance mode (the proxy needs a true "is the
	// process up?" signal, independent of the app's maintenance flag).
	engine.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	// --- Demo users + username/password login. A real host wires its session
	// store + users table here; the demo keeps two in-memory users whose
	// password (bcrypt-verified) equals their username, and signs an HMAC
	// session cookie on login. The web struct (views.go) owns the templates,
	// static assets, session cookie, and the public/login pages.
	st := wireBaselineStores(db, logger)
	migrateSiteTables(db, logger, st.users)
	wsrv := newWeb(st.users, st.sessionSecret, logger, storage.New(db))
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
	var redisClient *goredis.Client
	if addr := os.Getenv("REDIS_ADDR"); addr != "" {
		client := goredis.NewClient(&goredis.Options{Addr: addr})
		// PING before adopting it. Without this check the site technically
		// stays up when Redis is gone and is unusable anyway: every cache read
		// dials, retries five times and fails, so a page that reads four keys
		// spent TEN SECONDS before rendering — long enough that the browser
		// had already hung up. A degraded cache has to be fast about being
		// degraded.
		//
		// One short timeout, once, at boot. Redis dying LATER is a different
		// problem and not one a boot probe can answer; this only stops the
		// site adopting a backend that is already unreachable.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := client.Ping(ctx).Err()
		cancel()
		switch {
		case err != nil:
			// Loud, and naming the address: an operator who set REDIS_ADDR
			// expects Redis, and silently running on an in-process cache is
			// how a two-replica deployment starts serving two different
			// versions of every cached page.
			logger.Error("redis unreachable — falling back to the in-memory cache; "+
				"pages are still cached, but per-process and not shared",
				"addr", addr, "err", err)
			_ = client.Close()
			wsrv.cache = cachememory.New()
		default:
			redisClient = client
			wsrv.cache = cacheredis.New(client)
			logger.Info("cache backend", "kind", "redis", "addr", addr)
		}
	} else {
		wsrv.cache = cachememory.New()
		logger.Info("cache backend", "kind", "memory")
	}
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
	if err := pointsMigrate(db); err != nil {
		logger.Error("points migrate", "err", err)
		os.Exit(1)
	}
	// Privacy + notification preferences (settings_web.go).
	if err := settingsMigrate(db); err != nil {
		logger.Error("settings migrate", "err", err)
		os.Exit(1)
	}

	// Grab counting (grabs_web.go) — the source trending, "N downloads" and the
	// economy plugin's uploader bonus were all waiting on.
	if err := grabsMigrate(db); err != nil {
		logger.Error("grabs migrate", "err", err)
		os.Exit(1)
	}

	// Bookmarks (bookmarks_web.go) — saved releases, retiring MOCKS M4.
	if err := bookmarksMigrate(db); err != nil {
		logger.Error("bookmarks migrate", "err", err)
		os.Exit(1)
	}

	// Widget placements (widgets_web.go) — WHERE an operator has put each
	// registered widget. The widgets themselves come from plugins at boot and
	// live in memory; this table only remembers the arrangement.
	if err := widgetsMigrate(db); err != nil {
		logger.Error("widgets migrate", "err", err)
		os.Exit(1)
	}

	// Last seen (presence_web.go) and follows (follows_web.go) — MOCKS M1 and
	// M3, the last two placeholders on the profile.
	if err := lastSeenMigrate(db); err != nil {
		logger.Error("last-seen migrate", "err", err)
		os.Exit(1)
	}
	if err := followsMigrate(db); err != nil {
		logger.Error("follows migrate", "err", err)
		os.Exit(1)
	}
	// Topics/Posts read forum_threads and forum_posts, which live in
	// `public` and are the host's — no migration of its own to run.

	points := pgPoints{db: db}
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

	c, err := core.New(core.Deps{
		Process:   "all",
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
			NotifyFn: prefFiltered(db, notifications.Deliver),
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
	if err := wireInvites(c, db); err != nil {
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
	if err := registerAchievementMetrics(c, db); err != nil {
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
	go jobtrigger.StartPoller(ctx, st.jobTriggers, 3*time.Second, func(name string) bool {
		ran := schedule.TriggerJob(name)
		logger.Info("job trigger drained", "job", name, "ran", ran)
		return ran
	})

	// Beat this instance's presence every 15s (kind "all" — single-process demo).
	go heartbeat.StartReporter(ctx, st.heartbeat, heartbeat.HostID("all"), "all", "loon-demo", 15*time.Second)

	// user_display is the plugin-facing identity contract, and the baseline
	// builds it with avatar_path and reputation_tier stubbed to constants until
	// a host fills them in (see migrateUserDisplay). Replaced HERE, after every
	// migration that adds those columns has run, and before Boot, so the first
	// plugin query already sees the real view.
	if err := avatarModMigrate(db); err != nil {
		logger.Error("avatar moderation migrate", "err", err)
		os.Exit(1)
	}
	if err := securityMigrate(db); err != nil {
		logger.Error("security migrate", "err", err)
		os.Exit(1)
	}
	if err := wishlistMigrate(db); err != nil {
		logger.Error("wishlist migrate", "err", err)
		os.Exit(1)
	}
	if err := giftsMigrate(db); err != nil {
		logger.Error("gifts migrate", "err", err)
		os.Exit(1)
	}
	if err := undoMigrate(db); err != nil {
		logger.Error("undo migrate", "err", err)
		os.Exit(1)
	}
	if err := communityModMigrate(db); err != nil {
		logger.Error("community moderation migrate", "err", err)
		os.Exit(1)
	}
	if err := migrateUserDisplay(db); err != nil {
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
	reportContracts(ctx, c, db, logger)

	// The avatar file sweep (avatarsweep_web.go) -- the only thing that deletes
	// an avatar file, now that undo needs replaced and cleared ones to survive.
	wsrv.startAvatarSweep(ctx, db, logger)

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
	moderation := engine.Group("/moderation", wsrv.auth.Require(core.RoleUser)...)
	adminOnly := wsrv.auth.Require(core.RoleAdmin)
	moderation.GET("", append(adminOnly, wsrv.communityModPage)...)
	moderation.POST("/vote", append(adminOnly, wsrv.communityModVote)...)
	staffOnly := wsrv.auth.Require(core.RoleMod)
	moderation.GET("/avatars", append(staffOnly, wsrv.avatarModPage)...)
	moderation.POST("/avatars", append(staffOnly, wsrv.avatarModAction)...)

	admin := engine.Group("/admin", wsrv.auth.Require(core.RoleAdmin)...)
	// Access modes + the page map (accessadmin_web.go).
	admin.GET("/contracts", wsrv.adminContracts)
	admin.GET("/access", wsrv.adminAccess)
	admin.POST("/access", wsrv.adminAccessSave)
	// Where cover art comes from (coversadmin_web.go + covermode_web.go).
	admin.GET("/covers", wsrv.adminCovers)
	admin.POST("/covers", wsrv.adminCoversSave)
	// The page editor: where an operator puts registered widgets
	// (widgetsadmin_web.go). One region at a time, chosen by ?region=.
	admin.GET("/widgets", wsrv.widgetsAdminPage)
	admin.POST("/widgets/apply", wsrv.widgetsAdminAction)
	admin.GET("/plugins", wsrv.adminPlugins)
	admin.GET("/jobs", wsrv.adminJobs)
	admin.POST("/jobs/control", wsrv.adminJobsControl)
	// Per-job/-service settings — loon's bundled config form (self-contained
	// page). The demo's jobs table links here via a Config button for any job
	// that declares settings (HasConfig).
	admin.GET("/jobs/config", schedule.JobConfigHandler(nil))
	admin.POST("/jobs/config", schedule.JobConfigSaveHandler(nil))

	// Wire the usenet plugin's READ capability into the public pages — the
	// plugin publishes it on the extension registry during Provision.
	if v, ok := c.Lookup(pluginapi.UsenetIndexName); ok {
		wsrv.usenet, _ = v.(pluginapi.UsenetIndex)
	}
	if v, ok := c.Lookup(pluginapi.UsenetNewznabName); ok {
		wsrv.usenetAPI, _ = v.(pluginapi.UsenetNewznab)
	}
	// Daily reward: the plugin owns the once-per-day rule and the streak; the
	// host only asks whether a claim is available, so the stat strip can offer
	// a compact button and hide it once taken. Absent extension = no button,
	// which is what a host without the plugin should get.
	if v, ok := c.Lookup(dailyreward.StatusExtension); ok {
		wsrv.dailyStatus, _ = v.(dailyreward.StatusFunc)
	}

	// Catalog plugin: its service also implements the sink + cover store the
	// scraper writes to and the release page reads.
	if v, ok := c.Lookup(pluginapi.CatalogName); ok {
		if cat, ok := v.(pluginapi.Catalog); ok {
			wsrv.catalog = cat // taxonomy + names for the /browse page
			wsrv.catalogSink, _ = cat.(pluginapi.CatalogSink)
			wsrv.catalogCovers, _ = cat.(pluginapi.CatalogCovers)
		}
	}
	// Rewards: the plugin owns what is earnable and who has earned it; the
	// host owns the page that shows it. Absent extension = the page says
	// so, rather than 404ing a link the account nav always renders.
	if v, ok := c.Lookup(rewards.AchievementsExtension); ok {
		wsrv.achievements, _ = v.(rewards.AchievementsFunc)
		// Seed the catalogue here rather than beside the other seeds up top:
		// rewards.achievements is the PLUGIN's table, created by its own
		// migration during Boot, so nothing could be inserted into it before
		// this point. Guarded on the table being empty, so it runs once.
		achievementsSeed(db, logger)
	}

	// The rest of the demo seed (demoseed_web.go). Here for the same reason
	// achievementsSeed is: ranks.groups and store.items are PLUGIN tables,
	// created by their own migrations during Boot, so nothing could be
	// inserted into them any earlier. Each seeder checks its own table is
	// empty, so this runs once and never fights an operator's own data.
	demoSeed(db, logger)

	// Calendar sources. Registered AFTER the capability lookups above because
	// each source closes over one of them; a source whose dependency is absent
	// contributes nothing and the grid simply has fewer chips on it, which is
	// the whole reason the page reads a slice instead of naming its sources.
	wsrv.calSources = []calSource{
		wsrv.calAttendance(),
		wsrv.calBookmarks(),
	}

	// Newznab / Torznab API (Sonarr/Radarr/Prowlarr consume these).
	engine.GET("/api", wsrv.newznabAPI)
	engine.GET("/rss", wsrv.newznabAPI)

	// sitemap.xml, from loon-baseline/sitemap. Wired AFTER the usenet lookup
	// above: its releases Source reads through that capability, and a demo
	// without the plugin configured simply publishes a static-only sitemap.
	// See sitemap_web.go — the host supplies the Sources, the schedule and the
	// routes; the package does the XML, the paging and the index.
	wsrv.wireSitemap(engine, getenvDefault("LOON_BASE_URL", "http://localhost:8090"))

	// loon-baseline's batteries-included admin views (user management) plug
	// into the SAME view system the plugins use — the host just registers
	// them on the Core after Boot and wireViews mounts them at /admin/p/users.
	// This is the reusable admin chrome a real host adopts instead of
	// hand-rolling a users page.
	if bviews, err := adminusers.Views(st.users, password.Hasher{}); err != nil {
		logger.Error("adminusers.Views", "err", err)
	} else {
		for _, v := range bviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register admin view", "slug", v.Slug, "err", err)
			}
		}
	}
	// loon-baseline self-service account page (profile + change password) —
	// same view-system path, mounted at /p/account for any logged-in user.
	// Closes the loop on authflow.ChangePassword (logic existed; this is its UI).
	if aviews, err := account.Views(wsrv.flow, wsrv.currentUser); err != nil {
		logger.Error("account.Views", "err", err)
	} else {
		for _, v := range aviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register account view", "slug", v.Slug, "err", err)
			}
		}
	}
	// loon-baseline "Services online" view: /admin/p/services lists process
	// instances that have beaten a heartbeat recently (kind, uptime, last seen).
	if hviews, err := heartbeat.Views(st.heartbeat); err != nil {
		logger.Error("heartbeat.Views", "err", err)
	} else {
		for _, v := range hviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register services view", "slug", v.Slug, "err", err)
			}
		}
	}
	// loon-baseline maintenance toggle: /admin/p/maintenance (begin/end). Turning
	// it on shows the 503 page to visitors; /admin + /api stay reachable.
	if mviews, err := st.maint.Views(); err != nil {
		logger.Error("maintenance.Views", "err", err)
	} else {
		for _, v := range mviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register maintenance view", "slug", v.Slug, "err", err)
			}
		}
	}
	// loon-baseline self-service API key page: /p/api-key shows the user's
	// Newznab key (created on first visit) + a Regenerate button. loon-api
	// validates the key against the same table.
	if kviews, err := apikey.Views(st.apiKeys, wsrv.currentUser); err != nil {
		logger.Error("apikey.Views", "err", err)
	} else {
		for _, v := range kviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register apikey view", "slug", v.Slug, "err", err)
			}
		}
	}
	// loon-baseline login audit views. Two are offered: /admin/p/login-log
	// (every attempt, with a user column) and /p/sign-ins (your own).
	//
	// Only the ADMIN one is registered. The member page is the same table minus
	// the user column, and its one distinguishing column is "IP fingerprint" —
	// a hash, because the raw address is deliberately never stored. A row
	// reading "fc73e44eeb75…" cannot tell a member whether a sign-in was
	// theirs, which is the only question the page exists to answer; it closed
	// by asking them to change their password if they saw one they did not
	// recognise, which they had no way to determine.
	//
	// Staff keep the whole log, where the hash does earn its place: it groups
	// attempts by origin without the site holding anyone's address.
	if lviews, err := loginlog.Views(st.loginLog, wsrv.currentUser); err != nil {
		logger.Error("loginlog.Views", "err", err)
	} else {
		for _, v := range lviews {
			if v.Slot == core.SlotSitePage {
				continue // the member-facing "Sign-ins" page — see above
			}
			if err := c.RegisterView(v); err != nil {
				logger.Error("register loginlog view", "slug", v.Slug, "err", err)
			}
		}
	}
	// loon-baseline profile summary (SlotUserWidget on /u/<name>). Resolves the
	// profile subject by id off the user store.
	if pviews, err := profile.Views(func(ctx context.Context, id int64) (*core.User, bool) {
		u, err := st.users.ByID(ctx, id)
		if err != nil {
			return nil, false
		}
		return u.ToCore(), true
	}); err != nil {
		logger.Error("profile.Views", "err", err)
	} else {
		for _, v := range pviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register profile view", "slug", v.Slug, "err", err)
			}
		}
	}
	// Notification inbox page (/p/inbox). The navbar bell reads UnreadCount.
	if nviews, err := notify.InboxViews(inbox, wsrv.currentUser); err != nil {
		logger.Error("notify.InboxViews", "err", err)
	} else {
		for _, v := range nviews {
			if err := c.RegisterView(v); err != nil {
				logger.Error("register inbox view", "slug", v.Slug, "err", err)
			}
		}
	}

	// Plugin views (loon's view system): plugins render their settings
	// sections, admin/status pages, public pages, and widgets as fragments;
	// the demo mounts every slot generically and wraps the fragments in its
	// layout. Zero plugin-specific UI code host-side.
	wsrv.wireViews(c, engine, admin)

	serve(engine, wsrv, rt, ctx, stop, logger, redisClient)
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

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// catalogLogSink is the demo's pluginapi.CatalogSink: a real host writes each
// scraped entry into its unified catalog_entry table; the demo just logs. It's
// never called until a MetadataSource is registered (Phase 3), but the scraper
// plugin still boots and appears on the jobs page with it wired.
type catalogLogSink struct{ log *slog.Logger }

func (s catalogLogSink) Upsert(_ context.Context, e catalog.CatalogEntry) error {
	s.log.Info("catalog upsert", "kind", e.Ref.Kind, "id", e.Ref.ID, "title", e.Title)
	return nil
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
