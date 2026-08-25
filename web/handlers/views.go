package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/i18n"

	"github.com/the-loon-clan/loon-site/internal/storage"

	goredis "github.com/redis/go-redis/v9"

	"github.com/the-loon-clan/loon-site/internal/middleware"

	site "github.com/the-loon-clan/loon-site"

	"context"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-baseline/apikey"
	"github.com/the-loon-clan/loon-baseline/authflow"
	"github.com/the-loon-clan/loon-baseline/authtoken"
	"github.com/the-loon-clan/loon-baseline/cache"
	"github.com/the-loon-clan/loon-baseline/captcha"
	"github.com/the-loon-clan/loon-baseline/loginlog"
	"github.com/the-loon-clan/loon-baseline/notify"
	"github.com/the-loon-clan/loon-baseline/password"
	"github.com/the-loon-clan/loon-baseline/session"
	"github.com/the-loon-clan/loon-baseline/users"
	"github.com/the-loon-clan/loon-baseline/webauth"

	"github.com/the-loon-clan/loon-plugins/achievements"
	"github.com/the-loon-clan/loon-plugins/dailyreward"
	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// sessionCookieName is loon-baseline's own default, named here because the
// wiring installs the session middleware by hand. It must not change casually:
// a different name is a different cookie, and every signed-in visitor is
// signed out the moment it ships.
const sessionCookieName = "mysession"

// web is the demo's host-side HTTP surface: templates, static assets,
// username+password login + registration, and the public pages. The whole auth
// stack — user store, session cookie, bcrypt verify, register/login flow,
// current-user middleware — comes from loon-baseline (the host baseline loon
// omits by design), so the demo exercises the exact code a real site would.
// Users live in a real Postgres table (loon-baseline users.PGStore), seeded
// with alice/bob (password == username).
type web struct {
	// jobQueue enqueues a "run now" for the process that owns the job.
	//
	// nil in a process that runs its own jobs, where triggering is a local
	// call and no queue is needed — see adminJobsControl.
	jobQueue interface {
		Request(ctx context.Context, jobName string) error
	}

	store     users.Store    // loon-baseline user store (Postgres reference impl)
	flow      authflow.Flow  // register / authenticate / change-password
	resetFlow authtoken.Flow // password reset + email verification (token flows)
	auth      webauth.Auth
	loginLog  loginlog.Store    // login-attempt audit (recorded here, viewed via its views)
	captcha   *captcha.Verifier // Turnstile hook (disabled when no keys configured)
	// pluginCSS holds the stylesheets plugins hand over at Provision, and
	// serves them from a URL so they are cached instead of re-sent inside
	// every fragment. See pluginstyles_web.go.
	pluginCSS *pluginStyles
	points    core.PointsService // for the navbar balance readout
	inbox     notify.InboxStore  // for the navbar unread-count bell
	cache     cache.Cache        // page cache (in-memory by default, redis if configured)
	ipSalt    string             // salt for hashing client IPs before storing them
	log       *slog.Logger
	// mail sends one message. The SAME function the reset/verify flow uses —
	// held here as well so anything else that needs to email a member goes
	// through one seam, and a host that configures SMTP once has configured it
	// for everything. nil is a real state: a host with no mailer, where the
	// callers log rather than pretending the message went.
	mail func(to, subject, body string) error

	// data is the site's own SQL, behind one handle. It replaced eight
	// package-level *sqlx.DB globals and the 44 nil guards that defended them.
	data *storage.Store
	// tmpls is keyed by LOCALE then page. One parsed set per supported
	// language, because timeAgo and shortDate are bound to a locale in the
	// FuncMap — see tmplFuncs. Built once at boot.
	tmpls map[string]map[string]*template.Template

	// usenet plugin read capability, looked up on the extension registry after
	// Boot (the plugin's ADMIN surface is no longer consumed here — the plugin
	// renders its own views through loon's view system).
	usenet pluginapi.UsenetIndex
	// series answers about shows — the /series pages. Nil on a host whose
	// indexer publishes no series index; every surface that uses it is gated.
	series pluginapi.SeriesIndex
	// trackers asks the outside world (trackers.search); nil when unwired.
	trackers pluginapi.TrackerSearcher
	// mirrors answers which releases the TRACKER also carries, so a listing can
	// offer both ways of getting the same content on one row. Nil on a pure
	// indexer, and on a tracker that is configured off — see mirrors_web.go,
	// which falls back to the site's own read when the seam is absent.
	mirrors pluginapi.TorrentMirrors
	// mirrorMaker is the write side of the same relationship: making a torrent
	// for a release that has none. Nil wherever mirrors is, and the release
	// page then offers no button rather than one that cannot work.
	mirrorMaker pluginapi.TorrentMirrorMaker
	usenetAPI   pluginapi.UsenetNewznab // Newznab /api + /rss
	catalog     pluginapi.Catalog       // taxonomy + names for /browse (filled after Boot)
	// agentToken is the MASTER bearer that gates /api/agent/register (where a
	// client self-provisions a per-agent token); empty disables registration.
	// The protocol verbs themselves authenticate by per-agent token, not this.
	// See agentapi_web.go.
	agentToken string
	// ents answers "may this member do X" (core.Entitlements). Filled straight
	// after core.New, because SetDeps and the view mounts run before it and
	// only ever CALL this at render time. Reads fail closed: a nil service
	// denies, which is the safe direction for a gate.
	ents          core.EntitlementsService
	catalogSink   pluginapi.CatalogSink   // scraper write side (filled after Boot)
	catalogCovers pluginapi.CatalogCovers // release↔cover store (filled after Boot)
	// covers downloads scraped art to local storage so the site serves it
	// instead of hotlinking a provider CDN — see covercache_web.go. Built at
	// construction, not after Boot: it depends on nothing but the upload volume.
	covers *coverCache
	rt     *core.Runtime // plugin runtime, for the /admin/plugins page
	// achievements answers where a member stands on every earnable badge.
	// nil when the achievements plugin is absent, which renders the page's
	// unavailable state rather than a 404 on a link the nav always shows.
	// The type moved plugins when achievements left rewards (9a0e52b).
	achievements achievements.ListFunc

	// calSources contribute dated events to /calendar. A slice rather than a
	// field per source: the page's whole point is that adding a kind of event
	// does not touch the page.
	calSources []calSource

	// tv is the broadcast schedule for the shows this site carries
	// (tvschedule_web.go), filled by a job and read by the calendar. nil when
	// no TVmaze source is registered, which is a calendar without television
	// rather than a broken one.
	tv *tvSchedule

	// dailyStatus answers "may this member claim today?" for the stat strip's
	// compact button. nil when the plugin is absent, which renders no button.
	dailyStatus dailyreward.StatusFunc

	// View-system lookup tables, filled by wireViews after Boot.
	// apiKeys resolves ?apikey= on /api and /rss (apiquota_web.go). Set in
	// main() beside the other baseline stores.
	apiKeys *apikey.PGStore

	adminNav       []navItem            // admin subnav: Settings + plugin pages + host pages
	settingsViews  []core.View          // sections on /admin/settings
	sitePages      []core.View          // public-facing pages at /p/<slug>
	siteWidgets    []core.View          // cards on the home page
	userWidgets    []core.View          // cards on the /u/<name> profile page (user.* slot)
	userTabs       []core.View          // panels on the /u/<name> profile page (user.tab slot)
	jobsWidgets    map[string]core.View // job-group name -> override widget
	siteNavEntries []siteNavEntry       // site pages, pre-sorted for the nav (built once at boot)

	// redis is the shared-cache client when REDIS_ADDR is set, nil otherwise.
	// Held only for the rate limiter, which is the one thing here that has to
	// agree across replicas; everything else that wants Redis goes through
	// Core.Redis after Boot.
	redis *goredis.Client

	// Rate limiting, one instance per tier (internal/middleware/throttle.go).
	// Held here rather than built at the route so a tier is ONE table: a
	// limiter constructed per call site would give every route its own
	// allowance, which is the same as having none.
	tBrowse, tWork, tAuth *middleware.Throttle

	// engine is kept only so the widget-rule preview can ask what routes this
	// site actually serves (widgetpreview.go). Read at REQUEST time rather
	// than snapshotted at mount, because a snapshot would have to be taken
	// after the last plugin registered its routes and nothing enforces that
	// ordering -- an early snapshot would quietly under-report, which is the
	// failure the preview exists to prevent.
	engine *gin.Engine
}

// pageTemplates is every page under web/templates that newWeb parses into its
// own set. base.html and site_chrome.html are NOT here: they are the shell,
// parsed alongside every page. Adding a page file without adding it here leaves
// it unreachable — templates_test.go fails on that mismatch in either direction.
var pageTemplates = []string{
	"home.html", "groups.html", "search.html", "browse.html", "release.html",
	// Releases grouped by show (series_web.go).
	"series_index.html", "series.html",
	// The staff view of who vouched for whom (invitesadmin_web.go).
	"admin_invites.html",
	"trending.html", "bookmarks.html", "cart.html", "follows.html", "calendar.html",
	"achievements.html", "forum_activity.html", "rewards.html", "subscriptions.html",
	"invites.html", "gifts.html", "wishlist.html",
	// The bare fragment shell, for a plugin whose fragment is a whole PAGE
	// rather than page content — see site_fragment.html on why the forum
	// cannot use site_page.html.
	"site_fragment.html",
	"login.html", "register.html", "forgot.html", "reset.html", "profile.html",
	"site_page.html", "admin_view.html", "admin_settings.html",
	"admin_jobs.html", "admin_plugins.html", "admin_features.html", "admin_dashboard.html",
	"admin_access.html", "admin_contracts.html", "admin_covers.html",
	// What aired without arriving (tvgapsadmin_web.go).
	"admin_tvgaps.html",
	// The external tracker directory (trackersadmin_web.go).
	"admin_trackers.html",
	// Per-tracker API keys (trackerskeys_web.go).
	"admin_tracker_keys.html",
	// The fleet agent roster (agentadmin_web.go).
	"admin_agents.html",
	// The message catalogue (i18nadmin_web.go).
	"admin_i18n.html",
	// Editable site pages (pagesadmin_web.go).
	"admin_pages.html",
	// The menu editor (navadmin_web.go).
	"admin_nav.html",
	// The widget page editor (widgetsadmin_web.go).
	"admin_widgets.html",
	// Data-source attribution (credits_web.go).
	"credits.html",
	// Moderation is not under /admin (it gates at RoleMod) but is the same
	// kind of page — see avatarmod_web.go.
	"moderation_avatars.html", "moderation_community.html", "cheat_queue.html",
	// Fixed host pages — UNIT3D's page/* and stats/index (pages_web.go).
	"staff.html", "stats.html", "rules.html", "faq.html", "about.html",
	"sitemap.html",
	// Viewer settings (settings_web.go) — UNIT3D's privacy/notification pages.
	"settings_privacy.html", "settings_notifications.html", "settings_profile.html",
	"settings_security.html", "login_2fa.html",
}

// sharedPartials maps a page to the partials it needs beyond the shell. Each
// page gets its OWN template set, so a {{define}} in browse.html is invisible
// to search.html; a block two pages must render identically has to live in a
// file both sets name. listing.html holds the release-row and cat-icon blocks
// that /, /browse and /search render as one table.
var sharedPartials = map[string][]string{
	"home.html":      {"listing.html"},
	"browse.html":    {"listing.html", "facets.html"},
	"search.html":    {"listing.html", "facets.html"},
	"trending.html":  {"listing.html"},
	"bookmarks.html": {"listing.html"},
	// The cart renders the shared release-row, tick box and all — see
	// cart.html's own note on why the tick means "remove" there.
	"cart.html":                 {"listing.html"},
	"release.html":              {"bookmark_button.html"},
	"profile.html":              {"follow_button.html"},
	"wishlist.html":             {"wishlist_item.html"},
	"moderation_community.html": {"mod_item.html"},
}

func newWeb(store users.Store, secret []byte, log *slog.Logger, data *storage.Store) *web {
	w := &web{
		store:  store,
		data:   data,
		flow:   authflow.Flow{Users: store, Hasher: password.Hasher{}, DefaultRole: core.RoleUser, MinPasswordLen: minPasswordLen},
		log:    log,
		tmpls:  map[string]map[string]*template.Template{},
		covers: newCoverCache(),
	}
	// Session + current-user middleware from the baseline — the exact prod
	// scheme (gin-contrib/sessions "mysession" cookie, login_at expiry). Resolve
	// reads the user store; a richer host returns password_changed_at + IPHash
	// for session invalidation + admin IP pinning.
	w.auth = webauth.Auth{
		// "mysession", 7-day default; Secure off (plain-HTTP demo). When
		// REDIS_ADDR is set (the same env the page cache uses), sessions move
		// server-side into Redis — the cookie carries only a signed id, so
		// sessions survive a secret rotation and are revocable.
		// Secure defaults off (this demo serves plain HTTP), but a copied
		// reference deployed behind TLS should set SECURE_COOKIES=1 so the
		// session cookie isn't sent over an unencrypted request.
		// Name is stated rather than left to the package default, because
		// main.go builds the store itself (to fall back when Redis is down)
		// and needs the cookie name to install the middleware. The value is
		// the baseline's own default — changing it would sign every existing
		// session out.
		Session: session.Config{
			Name: sessionCookieName, Secret: secret,
			RedisAddr: os.Getenv("REDIS_ADDR"), Secure: os.Getenv("SECURE_COOKIES") == "1",
		},
		Resolve: func(ctx context.Context, id int64) (*core.User, webauth.Meta, bool) {
			u, err := store.ByID(ctx, id)
			if err != nil {
				return nil, webauth.Meta{}, false
			}
			// Last seen rides here because this is the one hook that runs for
			// every authenticated request. It is throttled to one write per
			// user per interval (presence_web.go) — unthrottled it would put
			// an UPDATE in front of every page load and sub-resource.
			touchLastSeen(ctx, w.data.DB(), id)
			return u.ToCore(), webauth.Meta{}, true
		},
	}
	// One template set per page: base.html (the document), site_chrome.html
	// (the header/footer/sprite blocks, shared with the forum plugin's own
	// parse set — see wireForumPlugin), any partial the page shares with
	// another page, then the page itself. The page is parsed LAST on purpose:
	// a {{define}} there overrides the same name in base.html, which is how a
	// page can replace a shell block ("stat-strip" on the home page).
	//
	// Once per supported language: the FuncMap carries locale-bound date
	// helpers, so the set a request renders from decides what language its
	// dates come out in without any template naming a locale.
	for _, loc := range i18n.Locales {
		set := make(map[string]*template.Template, len(pageTemplates))
		for _, page := range pageTemplates {
			set[page] = template.Must(template.New(page).Funcs(w.tmplFuncs(loc)).ParseFS(site.FS, pageFiles(page)...))
		}
		w.tmpls[loc.Key()] = set
	}
	return w
}

// logger is the nil-safe host logger — tests build a bare &web{} with no
// logger, and a degraded home-page panel must not panic on the way to being
// logged.
func (w *web) logger() *slog.Logger {
	if w.log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return w.log
}

// cacheGet / cacheSet wrap the page cache so every shared home-page block
// reads the same way. Errors are deliberately swallowed: a cache problem must
// degrade to a live read, never to a failed page. A host built without a cache
// (tests) simply always misses.
func (w *web) cacheGet(ctx context.Context, key string, dst any) bool {
	if w.cache == nil {
		return false
	}
	hit, err := cache.GetJSON(ctx, w.cache, key, dst)
	if err != nil {
		w.logger().Warn("page cache read", "key", key, "err", err)
		return false
	}
	return hit
}

func (w *web) cacheSet(ctx context.Context, key string, v any, ttl time.Duration) {
	if w.cache == nil {
		return
	}
	if err := cache.SetJSON(ctx, w.cache, key, v, ttl); err != nil {
		w.logger().Warn("page cache write", "key", key, "err", err)
	}
}

// currentUser resolves the request's user via the baseline session middleware.
// db is the site's database handle, or nil when there is no store.
//
// The storage package refuses a nil handle — that is where a mis-wiring should
// be caught. Here the answer is different on purpose: a page must still RENDER
// when a data source is absent, showing "—" rather than a 500, which is the
// admin dashboard's stated contract and what its test constructs a web with no
// sources to prove. So handlers tolerate a missing store; queries do not.
func (w *web) db() storage.Conn {
	if w.data == nil {
		return storage.Conn{}
	}
	// w.data.DB(), NOT w.db(): the rewrite that introduced this accessor
	// replaced every w.data.DB() in the package, including the one inside the
	// accessor itself, which recursed until the stack died. The build was
	// clean and every test passed — a nil store short-circuits above this
	// line, so the tests never reached it. Only running the site found it.
	return w.data.DB()
}

func (w *web) currentUser(c *gin.Context) (*core.User, bool) {
	return w.auth.Current(c)
}

// authed mounts a handler behind the shared sign-in gate.
//
// The gate belongs on the ROUTE, not at the top of the handler, and the
// difference is not tidiness. Twenty-nine handlers each opened by resolving
// the viewer and, failing that, issuing `c.Redirect(http.StatusFound,
// "/login")` — unconditionally, to every caller. A fetch() or an API client
// hitting a gated route therefore received a 302 and an HTML login page
// instead of a status it could act on. The hand-rolled copies did not even
// agree with each other: 29 used StatusFound (302), four StatusSeeOther (303).
//
// baseline's Require does the content negotiation properly — 303 to the login
// page for a browser, a status code for anything else — and it is already in
// the dependency. This is one line per route instead of five per handler, and
// it is the version that answers an API client correctly.
//
// RoleUser is the floor: the default role of a new account, so this means
// "any signed-in user". Anything needing more passes its own minimum.
func (w *web) authed(h gin.HandlerFunc) gin.HandlersChain {
	return append(w.auth.Require(core.RoleUser), h)
}

// limited mounts one handler behind a rate-limit tier, in the same shape as
// authed() above so a route reads the same either way.
//
// The AUTH tier is the one that matters and the one that is felt: login,
// register, password reset and the second factor are the four places where
// repeating the request IS the attack, and they are the only routes where a
// person could notice the limit at all.
func limited(t *middleware.Throttle, h gin.HandlerFunc) gin.HandlersChain {
	if t == nil {
		return gin.HandlersChain{h} // unconfigured: mount it bare rather than refuse
	}
	return gin.HandlersChain{t.Guard(), h}
}

// authPaths are the routes that let somebody in, or back in. Prefixes rather
// than exact matches so /login/2fa and /verify/resend are covered with their
// parents; each has an auth-tier guard of its own on the POST.
var authPaths = []string{"/login", "/register", "/forgot", "/reset", "/verify", "/logout"}

func onAuthPath(c *gin.Context) bool {
	p := c.Request.URL.Path
	for _, a := range authPaths {
		if p == a || strings.HasPrefix(p, a+"/") {
			return true
		}
	}
	return false
}

// throttleTier builds one tier, shared through Redis if the host has one, and
// reports a Redis outage exactly once in each direction.
//
// The log line matters more than it looks: with Redis down the limiter still
// works, per replica, so nothing visibly breaks and the effective limit
// silently multiplies. That is the state an operator needs told about, and it
// is the state nobody would otherwise notice.
func (w *web) throttleTier(l middleware.Limit, name string) *middleware.Throttle {
	var client goredis.UniversalClient
	if w.redis != nil {
		client = w.redis
	}
	t := middleware.NewRedisThrottle(l, client, name)
	t.SetOnState(func(up bool, err error) {
		if up {
			w.log.Info("rate limit: buckets shared through Redis again", "tier", name)
			return
		}
		w.log.Error("rate limit: Redis unavailable, falling back to per-instance "+
			"buckets — the effective limit is now multiplied by the number of "+
			"running replicas", "tier", name, "err", err)
	})
	return t
}

// throttleExempt reports whether this viewer is staff, and is called only on a
// request the limiter is about to refuse.
func (w *web) throttleExempt(c *gin.Context) bool {
	u, ok := w.auth.Current(c)
	return ok && u != nil && u.AtLeast(core.RoleMod)
}

// viewer returns the signed-in user on a route mounted behind authed.
//
// The gate guarantees one, so a nil here is a MOUNTING mistake — a handler
// wired without its gate — rather than a signed-out visitor. It fails with a
// status rather than rendering a signed-out page, because a page that quietly
// renders as though nobody is logged in is how such a mistake survives review.
func (w *web) viewer(c *gin.Context) (*core.User, bool) {
	u, ok := w.auth.Current(c)
	if !ok || u == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return nil, false
	}
	return u, true
}

// ── routes + rendering ──────────────────────────────────────────────

func (w *web) mount(e *gin.Engine) {
	w.engine = e
	// Shared through Redis when there is one. A limiter whose buckets live in
	// one process is right for one process and quietly wrong for two: each
	// replica keeps its own table, so the tier an operator configured is
	// multiplied by however many backends are running. NewRedisThrottle with a
	// nil client returns the in-process limiter, so a single-instance host
	// needs to know none of this.
	w.tBrowse = w.throttleTier(middleware.LimitBrowse, "browse")
	w.tWork = w.throttleTier(middleware.LimitWork, "work")
	w.tAuth = w.throttleTier(middleware.LimitAuth, "auth")
	// Idle buckets are forgotten every minute; see Throttle.sweep for why a
	// full bucket is safe to drop. No stop func is kept: these live as long as
	// the process, and a limiter that stopped sweeping would only leak.
	// Staff are exempt from the two READING tiers, and from neither of the
	// writing ones. An operator crawling their own site with the audit scripts
	// is the case this exists for; the auth tier stays universal because
	// nobody is staff yet at the moment they are trying to log in.
	// The way IN is never charged to the reading tier. See Throttle.Skip: an
	// empty browse bucket must not refuse the login page, or heavy browsing
	// from a shared address locks a member out of the one action that would
	// lift the limit. These paths have their own, tighter tier.
	w.tBrowse.Skip = onAuthPath
	w.tBrowse.Exempt = w.throttleExempt
	w.tWork.Exempt = w.throttleExempt
	w.tBrowse.Sweeper(time.Minute)
	w.tWork.Sweeper(time.Minute)
	w.tAuth.Sweeper(time.Minute)
	sub, _ := fs.Sub(site.FS, "web/static")
	// CSP and friends first, so they cover /static and every error path too —
	// a policy that covers only the HTML is a map of where it is not.
	e.Use(middleware.SecurityHeaders())
	// Headers BEFORE the file handler: the embedded FS has no modtime, so
	// without this nothing tells a browser when to look again.
	e.Use(staticCacheHeaders())
	e.StaticFS("/static", http.FS(sub))
	// The browse tier covers everything mounted BELOW this line, which is why
	// it sits here: gin applies Use() to later registrations only, so /static
	// and /healthz are outside it by construction rather than by an exemption
	// list. A page costs a token; the eighty images on it do not.
	e.Use(w.tBrowse.Guard())
	// Plugin stylesheets, handed over at Provision and served from a URL so
	// they are cached rather than re-sent inside every fragment.
	if w.pluginCSS != nil {
		e.GET(pluginCSSPrefix+":name", w.pluginCSS.serve)
	}
	e.GET("/", w.home)
	e.GET("/groups", w.groups)
	// Trending — most-grabbed releases (trending_web.go). Public: it exposes no
	// more than /browse already does, ordered differently.
	e.GET("/trending", w.trending)
	// Bookmarks (bookmarks_web.go). The list is per-viewer and the toggle
	// WRITES, so it is POST — a GET that mutates is one prefetching browser
	// away from bookmarking somebody's whole history for them.
	e.GET("/bookmarks", w.authed(w.bookmarksPage)...)
	// The cart (cart_web.go) — a selection accumulated across listings, and the
	// four things it can empty into. Every one is a POST because every one
	// writes; /cart/zip writes a grab per NZB it packs.
	e.GET("/cart", w.authed(w.cartPage)...)
	e.POST("/cart/add", w.authed(w.cartAdd)...)
	e.POST("/cart/remove", w.authed(w.cartRemove)...)
	e.POST("/cart/clear", w.authed(w.cartClear)...)
	e.POST("/cart/bookmark", w.authed(w.cartBookmark)...)
	e.POST("/cart/collect", w.authed(w.cartCollect)...)
	e.POST("/cart/zip", w.authed(w.cartZip)...)
	// Calendar (calendar_web.go) — the member's own dated things, so it is
	// login-gated inside the handler like /bookmarks rather than by role.
	e.GET("/calendar", w.authed(w.calendarPage)...)
	// Achievements (achievements_web.go) and the two forum-activity
	// listings (forumactivity_web.go). All account-scoped, so each gates
	// on the viewer inside the handler rather than on a role.
	e.GET("/achievements", w.authed(w.achievementsPage)...)
	e.GET("/p/topics", w.authed(w.forumActivityPage(false))...)
	e.GET("/p/posts", w.authed(w.forumActivityPage(true))...)
	// Rewards (rewards_web.go) — the points area's third tab. Registered here
	// rather than under /store/*, which the store plugin owns.
	e.GET(storeRewardsPath, w.rewardsPage)
	// Subscriptions (subscriptions_web.go) — one list of everything the viewer
	// follows, read from the tables that already hold it.
	e.GET("/subscriptions", w.authed(w.subscriptionsPage)...)
	// Invite codes (invitecodes_web.go).
	e.GET("/login/2fa", w.twoFactorPage)
	e.POST("/login/2fa", limited(w.tAuth, w.twoFactorPost)...)
	e.GET("/wishlist", w.authed(w.wishlistPage)...)
	e.POST("/wishlist", w.authed(w.wishlistAdd)...)
	e.POST("/wishlist/:id", w.authed(w.wishlistUpdate)...)
	e.GET("/gifts", w.authed(w.giftsPage)...)
	e.POST("/gifts", w.authed(w.giftsSend)...)
	e.GET("/invites", w.authed(w.invitesPage)...)
	e.POST("/invites", w.authed(w.invitesCreate)...)
	// Cancel a pending invite, and hide a dead one. Both POST because both
	// write; revoke is the one that can hand an invite back (invitecodes_web.go).
	e.POST("/invites/revoke", w.authed(w.invitesRevoke)...)
	e.POST("/invites/delete", w.authed(w.invitesDelete)...)
	e.POST("/u/:name/follow", w.authed(w.followToggle)...)
	// Reporting an avatar opens (or votes on) a community moderation item —
	// see communitymod_web.go.
	e.POST("/u/:name/report-avatar", w.authed(w.reportAvatarPost)...)
	// Reverse the last destructive thing you did (undo_web.go).
	e.POST("/undo", w.authed(w.undoPost)...)
	e.GET("/u/:name/followers", w.followPage(followKindFollowers))
	e.GET("/u/:name/following", w.followPage(followKindFollowing))
	// Mutual follows (follows_web.go). Derived from the same table, so this is
	// a third READING of user_follow rather than a third feature.
	e.GET("/u/:name/friends", w.followPage(followKindFriends))
	e.POST("/release/:id/bookmark", w.authed(w.bookmarkToggle)...)
	// Make this release a torrent on the site's own tracker (mirrors_web.go).
	// POST because it creates one, and behind auth because the tracker's pages
	// need an account.
	e.POST("/release/:id/torrent", w.authed(w.mirrorRelease)...)
	e.GET("/search", limited(w.tWork, w.search)...)
	// The quick-search dropdown. A fragment, so it is never a page — see
	// suggest_web.go.
	e.GET("/search/suggest", limited(w.tWork, w.suggestPage)...)
	e.GET("/browse", w.browse)
	// Releases grouped by the show they belong to (series_web.go). Mounted
	// unconditionally and gated INSIDE the handler: the capability is looked up
	// after Boot, and routes are registered before it, so a nil check here would
	// read a field nothing had filled yet.
	e.GET("/series", w.seriesIndexPage)
	e.GET("/series/:key", w.seriesPage)
	e.GET("/release/:id", w.releasePage)
	e.GET("/nzb/:id", w.nzbDownload)
	// Fixed host pages: /staff /stats /rules /faq /about (pages_web.go).
	w.mountSitePages(e)
	// The dev-only UI inspector (uiinspect_web.go). Registers nothing unless
	// LOON_UI_INSPECT is set — it serves files off disk and injects script
	// into a frame of the site, neither of which belongs on a reachable build.
	w.mountUIInspect(e)
	// Viewer settings: /settings/privacy, /settings/notifications.
	w.mountSettings(e)
	e.GET("/login", w.loginPage)
	e.POST("/login", limited(w.tAuth, w.loginPost)...)
	e.GET("/register", w.registerPage)
	e.POST("/register", limited(w.tAuth, w.registerPost)...)
	e.GET("/forgot", w.forgotPage)
	e.POST("/forgot", limited(w.tAuth, w.forgotPost)...)
	e.GET("/reset", w.resetPage)
	e.POST("/reset", limited(w.tAuth, w.resetPost)...)
	e.GET("/verify", w.verifyEmail)
	// State-changing: POST + CSRF. A GET logout/resend is forgeable via a
	// cross-site top-level navigation (SameSite=Lax still sends the cookie).
	e.POST("/verify/resend", w.authed(w.resendVerify)...)
	e.GET("/u/:name", w.profilePage)
	e.POST("/logout", w.logout)
	// Theme switcher (theme.go). POST + CSRF like every other state-changing
	// form here; the preference is a cookie, so it works for anonymous
	// visitors too and sits outside any auth group.
	e.POST(themeRoute, w.setTheme)
}

// profilePage renders a user's public profile: it resolves the subject by name,
// sets it via core.SetViewSubject, and renders every SlotUserWidget the viewer
// may see (the baseline summary + any plugin contributions like the daily
// streak) — the host owns zero profile content.
// wornMedals reads the medals a member chose to display, through the
// medals plugin's published answer (medals.worn). Empty on any failure —
// a profile must not 500 over decoration.
func (w *web) wornMedals(c *gin.Context, userID int64) []pluginapi.WornMedal {
	if w.rt == nil || w.rt.Core() == nil {
		return nil
	}
	v, ok := w.rt.Core().Lookup(pluginapi.WornMedalsName)
	if !ok {
		return nil
	}
	fn, ok := v.(pluginapi.WornMedalsFunc)
	if !ok {
		return nil
	}
	worn, err := fn(c.Request.Context(), userID)
	if err != nil {
		return nil
	}
	return worn
}

func (w *web) profilePage(c *gin.Context) {
	subject, err := w.store.ByUsername(c.Request.Context(), c.Param("name"))
	if err != nil {
		c.Status(http.StatusNotFound)
		w.render(c, "profile.html", map[string]any{"Title": "Not found", "Missing": true})
		return
	}
	core.SetViewSubject(c, subject.ID)
	// This IS the public profile — the page answering "what do other members
	// see". A SlotUserWidget that carries a CONTROL rather than a fact (the agent
	// fleet roster, an IRC verification token) is correct on the owner's own
	// settings surface and wrong here, even for the owner looking at their own
	// page. The plugin's viewer==subject check cannot tell the two apart — the
	// owner passes it on both — so the host marks which page this is and the
	// widget suppresses itself. Set here and nowhere else, per core.SetPublicProfile.
	core.SetPublicProfile(c)

	viewer, _ := w.currentUser(c)
	subj := subject.ToCore()
	// Privacy is ENFORCED here, not just rendered: a private profile shows its
	// identity card and nothing else. Staff and the owner still see everything —
	// a setting that hid a user from moderation would be a moderation hole, not
	// a privacy feature.
	isSelf := viewer != nil && viewer.ID == subject.ID
	isStaff := viewer != nil && viewer.AtLeast(core.RoleMod)

	// ?preview=1 — "view public profile". The owner sees their own page as
	// another member sees it: owner-only controls drop, and the privacy setting
	// applies to them the way it applies to everyone else.
	//
	// Worth having because the privacy setting is otherwise unverifiable by the
	// person it protects: they are the one account it never hides anything
	// from, so "is my profile actually private?" has no answer without either
	// a second account or trust. EMP ships the same thing on its user page,
	// labelled Preview.
	//
	// Applied to isSelf and isStaff BOTH: a moderator previewing their own page
	// would otherwise keep seeing everything through the staff exemption and
	// conclude the setting does nothing.
	preview := isSelf && c.Query("preview") == "1"
	if preview {
		isSelf, isStaff = false, false
	}

	private := !isSelf && !isStaff && w.data.IsPrivateProfile(c.Request.Context(), subject.ID)
	data := map[string]any{
		"Title":   subject.Username,
		"Subject": subj,
		// The subject's avatar, which is not the viewer's — chromeData's
		// UserAvatar is whoever is logged in, and on someone else's profile
		// those are different people.
		"SubjectAvatar": readAvatarPath(c.Request.Context(), w.data.DB(), subject.ID),
		// The subject's worn medals (medals.worn — the plugin owns the
		// cabinet, this page only draws the icons). Empty without the plugin.
		"SubjectMedals": w.wornMedals(c, subject.ID),
		// Outcome of a report POST, round-tripped through the redirect.
		"Report":  c.Query("report"),
		"IsSelf":  isSelf,
		"Role":    roleLabel(subj.Role),
		"Private": private,
		// Preview is NOT IsSelf: the page must render as a stranger sees it,
		// and this is only for the banner saying so and the way back out.
		"Preview": preview,
		// The owner, for the "view public profile" link — set even while
		// previewing, since that is when the way back matters.
		"CanPreview": viewer != nil && viewer.ID == subject.ID,
	}
	if private {
		// Stop before every read below. Skipping the render is not enough —
		// the figures must never be FETCHED, or a timing difference still
		// answers the question the setting exists to refuse.
		w.render(c, "profile.html", data)
		return
	}
	// Every plugin card goes to the SIDEBAR now, and two are suppressed
	// outright. Placement is the host's call — the page is its layout — and
	// the profile had drifted into a collage: four plugin cards stacked in the
	// main column, two of them repeating what the page already said.
	//
	//   summary       (loon-baseline) said Role and Joined — the identity card
	//                 at the top of the sidebar already carries both, so this
	//                 was the same two facts a second time, four cards down.
	//   achievements  (rewards) duplicated the host's own Achievements panel
	//                 one section below it: the same earned badges, twice on
	//                 one screen. The widget's extra — in-progress bars — lives
	//                 on /achievements, which the panel links to.
	//
	// What remains (flair, daily streak, linked accounts, agent fleet) is a
	// fact ABOUT the member, and facts live in the sidebar with the identity
	// card and the stats; the main column is content. Suppression is by slug,
	// which couples the host to two plugin names — accepted and stated, the
	// same way navPlacedByHost pins nav entries.
	//
	// AFTER the private gate on purpose — the sidebar renders on private
	// profiles, so a fragment built here would leak there. Below the gate the
	// fragments are never rendered at all for a private profile, which also
	// honours the gate's stronger rule: the figures must never be FETCHED.
	var sidebarWidgets []widgetVM
	for _, v := range w.userWidgets {
		if v.Slug == "summary" || v.Slug == "achievements" {
			continue
		}
		if !w.canView(v, c) {
			continue
		}
		frag, err := v.Render(c)
		if err != nil {
			w.log.Error("user widget", "slug", v.Slug, "err", err)
			continue
		}
		if frag == "" {
			continue
		}
		sidebarWidgets = append(sidebarWidgets, widgetVM{Title: v.Title, Fragment: frag})
	}

	data["SidebarWidgets"] = sidebarWidgets

	// SlotUserTab: the profile's main-column panels. The core declares this
	// slot beside SlotUserWidget, but nothing rendered it -- a plugin
	// registering a user.tab view found it vanish, the seam-with-a-missing-half
	// this codebase treats as a bug. Mounted here the same way widgets are:
	// subject already set via SetViewSubject above, so a tab reads whose
	// profile it is; guarded by canView; below the private gate so it never
	// leaks on a private profile. None ship today, so this renders nothing --
	// but the mount point exists, which is the point.
	var profileTabs []widgetVM
	for _, v := range w.userTabs {
		if !w.canView(v, c) {
			continue
		}
		frag, err := v.Render(c)
		if err != nil {
			w.log.Error("user tab", "slug", v.Slug, "err", err)
			continue
		}
		if frag == "" {
			continue
		}
		profileTabs = append(profileTabs, widgetVM{Title: v.Title, Fragment: frag})
	}
	data["ProfileTabs"] = profileTabs

	// Real profile figures only. Each is guarded: a missing capability drops
	// the tile rather than showing a zero, because "0 points" and "points are
	// unavailable" are different claims and a profile should not conflate them.
	//
	// Subject* prefixes are NOT decoration. render() -> chromeData sets Points
	// and Unread to the VIEWER's values on every page, so a subject figure
	// stored under those names is silently overwritten on the way out — the
	// profile would show your own balance on someone else's page.
	ctx := c.Request.Context()
	// The subject's own words, rendered from markdown at READ time — see
	// profilebio_web.go. AFTER the privacy gate above on purpose: a private
	// profile shows its identity card and nothing else, and the text a member
	// wrote about themselves is exactly what that setting is for.
	if bio := renderBio(w.data.ReadBio(ctx, subject.ID)); bio != "" {
		data["Bio"] = bio
	}
	if w.points != nil {
		if bal, err := w.points.Balance(ctx, subject.ID); err == nil {
			data["SubjectPoints"], data["HasSubjectPoints"] = bal, true
		}
	}
	if forumReads != nil {
		if n, err := w.forumPostCount(ctx, subject.ID); err == nil {
			data["SubjectPosts"], data["HasSubjectPosts"] = n, true
		}
	}
	// Who this member brought in — OFF unless an operator turned it on.
	//
	// Behind a setting rather than shown by default because it is a social
	// graph: "alice recruited 30 people" is a fact about thirty other accounts
	// as much as about alice, and publishing it should be a decision somebody
	// made rather than one they inherited. The setting is invite.public_stats
	// and it defaults to off (inviteoptions_web.go).
	//
	// Not exempted for self or staff. They have /invites and /admin/invites,
	// which show the whole chain rather than a count — so an exemption here
	// would add nothing except a second answer to maintain.
	if currentInviteOptions().PublicStats {
		if direct, chain, okR := w.data.RecruitCounts(ctx, subject.ID); okR && chain > 0 {
			data["SubjectRecruits"], data["HasSubjectRecruits"] = direct, true
			// Only when the chain is bigger than the direct count, because
			// otherwise the second figure repeats the first and a tile that
			// says "3 · 3 in chain" reads as though one of them is wrong.
			if chain > direct {
				data["SubjectRecruitChain"] = chain
			}
		}
	}
	// Bookmarks are PUBLIC on a profile the way UNIT3D shows them — a count,
	// not the list. Has* rather than a bare zero, so an unreachable table
	// leaves the tile an em dash instead of claiming nobody saved anything.
	if n, ok := w.data.BookmarkCount(ctx, subject.ID); ok {
		data["SubjectBookmarks"], data["HasSubjectBookmarks"] = n, true
	}
	// Achievements — MOCKS M2, retired. Public like the bookmark count: what
	// someone has earned is a display, and UNIT3D shows it on the profile too.
	// The "see all" link is self-only, because /achievements is the viewer's
	// own page and includes what they have NOT earned.
	if sum, ok := w.recentAchievements(c, subject.ID, profileAchievements); ok && sum.Total > 0 {
		data["Achievements"], data["HasAchievements"] = sum, true
	}
	// Followers/following (M3) and last seen (M1) — the last two placeholders
	// on this page. Has* on each, so an unreachable table leaves an em dash
	// rather than asserting a zero nobody measured.
	if followers, following, ok := w.data.FollowCounts(ctx, subject.ID); ok {
		data["SubjectFollowers"], data["SubjectFollowing"] = followers, following
		data["HasSubjectFollows"] = true
	}
	if t, ok := lastSeenAt(ctx, w.data.DB(), subject.ID); ok {
		data["SubjectLastSeen"], data["HasSubjectLastSeen"] = t, true
	}
	// Operator-placed widgets for the profile region. After the private gate,
	// so a hidden profile hides these too; SetViewSubject has already run, so a
	// widget here can read whose profile it is exactly as a SlotUserWidget can.
	if ws := w.renderRegion(c, "profile"); len(ws) > 0 {
		data["RegionWidgets"] = ws
	}
	// The subject's tracker standing, which is public on a private tracker for
	// the reason ratio exists at all: it is the thing members are accountable
	// to each other for.
	//
	// SubjectTracker*, not TrackerUp — chromeData sets the VIEWER's figures
	// under those names on every page, so an unprefixed key here would be
	// overwritten on the way out and every profile would show your OWN ratio.
	// The same trap the Subject prefixes above exist for.
	//
	// Reached only after the private gate, so a member who has hidden their
	// profile hides this with it.
	if tt, ok := w.data.ReadTrackerTotals(ctx, subject.ID); ok {
		data["HasSubjectTracker"] = true
		data["SubjectTrackerUp"] = humanBytes(tt.Uploaded)
		data["SubjectTrackerDown"] = humanBytes(tt.Downloaded)
		data["SubjectTrackerRatio"] = tt.RatioLabel()
		data["SubjectTrackerSeeding"] = tt.Seeding
		data["SubjectTrackerLeeching"] = tt.Leeching
		data["SubjectTrackerSnatched"] = tt.Snatched
	}
	// The follow button is for a signed-in viewer looking at SOMEONE ELSE.
	if viewer != nil && viewer.ID != subject.ID {
		data["CanFollow"] = true
		data["Following"] = w.data.IsFollowing(ctx, viewer.ID, subject.ID)
	}
	// Invites are the viewer's own spendable balance, so they only show on
	// your own profile — someone else's invite count is not your business.
	if viewer != nil && viewer.ID == subject.ID {
		if n, ok := w.inviteBalance(ctx, subject.ID); ok {
			data["Invites"], data["HasInvites"] = n, true
		}
	}
	w.render(c, "profile.html", data)
}

// chromeData fills everything the SHARED site chrome (site_chrome.html) reads,
// for any page in either template set.
//
// This exists because there are TWO render paths into the same chrome — this
// file's render() for host pages, and forum.Deps.BaseData (forum_web.go) for
// the plugin's own documents — and they had drifted: BaseData supplied five
// keys where render() supplied ten, so for the SAME signed-in user the forum
// rendered 13 nav links, 3 stat tiles and no bell badge where the home page
// rendered 25, 8 and a badge. Nothing errored; the chrome just degraded, and
// half the site quietly became a different site. Both callers now go through
// here, so the two CANNOT diverge again without editing this function.
//
// Every key is either always-set (User, IsAdmin, IsMod, CSRFToken, Path,
// AdminNav, SiteNav, Theme, Themes) or explicitly optional with a Has*
// sentinel. Optional keys stay ABSENT rather than zero — see the note on
// HasPoints/HasUnread below.
func (w *web) chromeData(c *gin.Context, data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	u, _ := w.currentUser(c)
	data["User"] = u
	// The request's CSP script nonce. Every inline <script> in these templates
	// must carry it or it will not run — see internal/middleware/csp.go.
	data["Nonce"] = middleware.Nonce(c)
	// The stylesheets plugins handed over at Provision. One <link> each, with
	// the content hash in the URL, so they are fetched once and then immutable
	// — instead of the same bytes riding inside every fragment that needs them.
	// See pluginstyles_web.go.
	if w.pluginCSS != nil {
		data["PluginCSS"] = w.pluginCSS.links()
	}
	// Gate the admin nav on actual role, not mere logged-in-ness — the
	// /admin/* routes sit behind Require(RoleAdmin), so a plain user
	// clicking them lands on a 403 JSON blob instead of a page.
	data["IsAdmin"] = u != nil && u.AtLeast(core.RoleAdmin)
	// ShowIndexer gates the FOOTER's Newznab/RSS links; the menus' own
	// flavour gating lives in the nav assembly (navadmin_web.go), which runs
	// below once the plugin site pages are known.
	data["ShowIndexer"] = flavourIndexer()
	// The forum's moderation routes (pin/lock, category admin) gate at RoleMod
	// — templates must show those buttons to the role that can use them.
	data["IsMod"] = u != nil && u.AtLeast(core.RoleMod)
	// The pending-avatar count on the account menu's moderation entry. Only
	// read for staff — an unread badge is what makes a queue get worked, and
	// the reports plugin's 98-day-old open item is what the absence of one
	// looks like. Costs one indexed count per staff page view, nothing at all
	// for everybody else.
	if u != nil && u.AtLeast(core.RoleMod) {
		if n := countPendingAvatars(c.Request.Context(), w.data.DB()); n > 0 {
			data["PendingAvatars"] = n
		}
	}
	// A page that set no title gets one from the site's own page map.
	//
	// The plugin pages rendered through fhead mostly set none, so every one of
	// them fell back to the site name — the donate page, a playlist and a
	// community all sharing one tab label. The map already exists and is
	// already reviewed (accessadmin_web.go); this reads it rather than adding a
	// second list of what pages are called, which would drift from the first.
	//
	// Only when ABSENT. A page that set its own title knows better than a table
	// does — a thread is called after the thread, not after "Forums".
	if t, ok := data["Title"].(string); !ok || t == "" {
		if name := pageTitleFor(c.Request.URL.Path); name != "" {
			data["Title"] = name
		}
	}

	// The request's language, for <html lang> and the picker. Resolved here so
	// every page gets it without each handler remembering to — a page that
	// forgot would render lang="" and tell a screen reader nothing.
	loc := w.locale(c)
	data["Lang"] = loc.Key()
	data["Locales"] = localeOptions(loc)
	data["CSRFToken"] = middleware.Token(c) // hidden _csrf field for every POST form
	if u != nil {
		// Viewer identity bits the user panel + top bar show. Both come off the
		// session-resolved user (no extra query): the rank label the role maps
		// to, and the account's creation date for "member since".
		data["RoleLabel"] = roleName(u.Role)
		data["MemberSince"] = u.CreatedAt
		// The account menu's avatar, on every page. One indexed lookup by id
		// — core.User carries no image field, and threading it through every
		// handler instead would mean every new page remembering to.
		data["UserAvatar"] = readAvatarPath(c.Request.Context(), w.data.DB(), u.ID)
		// HasPoints/HasUnread exist because a template cannot tell an ABSENT
		// map key from a zero one: {{if .Points}} hid the tile both when the
		// points service was unwired AND for a user whose balance is genuinely
		// 0. Guard the tiles on Has*, and read the value from Points/Unread.
		// (The top-nav bell BADGE still guards on .Unread — a "0" badge is
		// noise, unlike a "0" stat tile, which is real information.)
		if w.points != nil {
			if bal, err := w.points.Balance(c.Request.Context(), u.ID); err == nil {
				data["Points"] = bal
				data["HasPoints"] = true
			}
		}
		// The cart badge. Set only when there is something in it, so the
		// template's {{if .CartCount}} hides an empty cart — which is the
		// bell-badge rule rather than the stat-tile one: a cart with nothing in
		// it is not information, it is a control asking to be pressed for
		// nothing, and every listing links to it anyway.
		if n, ok := w.data.CartCount(c.Request.Context(), u.ID); ok && n > 0 {
			data["CartCount"] = n
		}
		// No tracker figures in the chrome. They were rendered twice — these
		// keys AND the tracker-standing widget, which an operator can place in
		// the same header bar — and two sources for one number is a thing to
		// remove rather than style around. The widget won because an operator
		// controls where it goes, or whether it appears at all.
		//
		// A member's standing is still on /stats and on their profile, and
		// storage.ReadTrackerTotals is still what those read.
		// unverified-email banner: look up the full record (core.User omits the flag)
		if w.store != nil {
			if full, err := w.store.ByID(c.Request.Context(), u.ID); err == nil && full != nil {
				data["EmailUnverified"] = full.Email != "" && !full.EmailVerified
			}
		}
		if w.inbox != nil {
			if n, err := w.inbox.UnreadCount(c.Request.Context(), u.ID); err == nil {
				data["Unread"] = n
				data["HasUnread"] = true
			}
		}
	}
	// Operator-placed widgets in the four regions the CHROME owns. One read of
	// the placement table for all of them — see renderRegions — and nil when a
	// site has placed nothing, which is the default and costs the templates
	// nothing.
	//
	// The page-specific regions (profile, release, listing) are rendered by
	// their own handlers instead, because those need to say WHAT the page is
	// about first (core.SetWidgetItem / SetViewSubject) and the chrome does not
	// know.
	// WidgetRegions, NOT "Widgets" — the profile page already publishes its own
	// .Widgets ([]widgetVM, the SlotUserWidget cards), and chromeData runs after
	// a handler's data on every page. Sharing the name meant the chrome tried to
	// index a slice with a string and took the whole profile down. The same
	// collision the Subject* prefixes exist for, one map up.
	if ws := w.renderRegions(c, "header-bar", "sidebar-left", "sidebar-right", "footer"); ws != nil {
		data["WidgetRegions"] = ws
	}
	// The nav shows Donate only where donations are actually accepted, so the
	// gate has to be readable by the SHARED chrome, not just the donate pages.
	// donationsEnabled is the env flag; donateToggle is the admin switch. Both,
	// same as IsDonateEnabled — see donations_web.go.
	data["DonateEnabled"] = donationsEnabled && donateToggle.Load()
	// Metadata-source attribution. A licence condition for TVmaze and TMDB, not
	// decoration — see credits_web.go. Empty until a source registers, so a
	// deployment with none credits nobody rather than claiming a provenance it
	// does not have.
	data["SourceCredits"] = sourceCredits()
	data["Path"] = c.Request.URL.Path
	// The account area as a BAR, beside the breadcrumb — see sectionnav_web.go.
	// Nil off the account area, which is the template's guard. The avatar menu
	// no longer lists these; landing on the profile is what brings them up,
	// the way UNIT3D's user area works.
	// Gated on the VIEWER, not just the path: every entry is the viewer's own
	// page, and a profile is a public page sitting at an account-area path. See
	// accountBar — judging by path alone showed anonymous visitors a personal
	// account menu (Security, API key) on every member's profile.
	{
		viewer, _ := w.currentUser(c)
		own := false
		if viewer != nil {
			if name := profileNameFromPath(c.Request.URL.Path); name != "" {
				own = strings.EqualFold(name, viewer.Username)
			}
		}
		// The fleet entry is per-VIEWER: it is gated on agentEntitlementKey, so
		// a member without the grant never sees the door, and the page behind it
		// refuses them anyway (canView).
		canAgents := false
		if viewer != nil && w.ents != nil {
			canAgents = w.ents.Has(c.Request.Context(), viewer.ID, agentEntitlementKey)
		}
		data["AccountBar"] = accountBar(c.Request.URL.Path, viewer != nil, own, canAgents)
	}
	// PathQuery is Path PLUS the query string: the "send me back exactly here"
	// target for the theme switcher's hidden next field. It has to be a SECOND
	// key rather than a richer Path, because every nav active-state comparison
	// matches Path against a literal href ({{if eq .Path "/browse"}}, the
	// /community/forums and /admin/ prefix slices) — a query on Path would
	// silently unhighlight the current tab on /browse?cat=5000.
	//
	// Without it the switcher posted the bare path, and theme.go backLink()
	// prefers next over the Referer, so applying a theme on /search?q=foo landed
	// on /search: the hidden field actively replaced a recoverable target with a
	// lossy one. RequestURI() is EscapedPath + "?" + RawQuery, i.e. already the
	// rooted same-origin form backLink() accepts.
	data["PathQuery"] = c.Request.URL.RequestURI()
	// The admin bar, minus anything whose feature is switched off. Filtered
	// HERE and not at build time: the bar is assembled once at boot, and a
	// toggle has to move it on the next request.
	data["AdminNav"] = w.liveAdminNav()
	// Plugin site pages the viewer may open: SiteNav is the top-level nodes,
	// SiteNavGroup the ones that asked to sit inside a dropdown the host writes
	// itself (see hostNavGroups), SiteNavAccount the per-viewer pages that
	// belong on the account menu. All three ALWAYS set — the nav indexes
	// SiteNavGroup by name on every render.
	siteNodes, siteGrouped, siteAccount := w.siteNav(c)
	data["SiteNav"], data["SiteNavGroup"], data["SiteNavAccount"] = siteNodes, siteGrouped, siteAccount
	// Both storeys of the editable navigation (navadmin_web.go): the top
	// bar's tabs and the footer's link columns, assembled for THIS request —
	// operator rows filtered and ordered, labels localised through the
	// message catalogue when a group names a slug, plugin pages merged into
	// the tab that asked for them, actives computed against the path.
	var ungrouped []navItem
	for _, n := range siteNodes {
		if len(n.Children) == 0 && n.Href != "" {
			ungrouped = append(ungrouped, navItem{Href: n.Href, Label: n.Label})
		}
	}
	tabs, footerCols := assembleNav(currentNav(), c.Request.URL.Path,
		func(slug string) (string, bool) { return w.resolveI18n(c, slug) },
		siteGrouped, ungrouped)
	data["NavTabs"] = tabs
	data["FooterNav"] = footerCols
	// Theme is resolved from an allowlist (theme.go): the cookie value never
	// reaches the page, only the matching entry does — the head prints
	// .Theme.Href, which is a constant. Both keys are ALWAYS set: the
	// stylesheet link is unconditional and the switcher compares each
	// .Themes entry's Key against .Theme.Key, and a field lookup (or `eq`)
	// against an ABSENT key is an execute error, not a false. A forum page
	// without them would render unthemed — exactly the drift this function
	// exists to prevent.
	// Daily reward: only ever set when a claim is actually available, so the
	// chrome shows the button by its PRESENCE rather than having to reason
	// about state. A nil seam (plugin absent) leaves all three keys unset.
	if w.dailyStatus != nil {
		if u, ok := w.auth.Current(c); ok && u != nil {
			if st, err := w.dailyStatus(c.Request.Context(), u.ID); err == nil && !st.Claimed {
				data["CanClaimDaily"] = true
				data["DailyStreak"] = st.Streak
				data["DailyReward"] = st.Reward
			}
		}
	}
	data["Theme"] = currentTheme(c) // themeOption: .Key .Label .Href
	data["Themes"] = siteThemes     // the allowlist itself, in menu order
	return data
}

func (w *web) render(c *gin.Context, page string, data map[string]any) {
	// c.Writer.Status(), NOT http.StatusOK. Several handlers call c.Status(404)
	// and THEN render a "not found" body — profile, release and the follow
	// lists all do. Hard-coding 200 here overrode every one of them, so three
	// pages answered 200 while showing "Not found": invisible in a browser, and
	// wrong for every crawler, cache and scripted client. gin's writer already
	// defaults to 200, so this preserves the handler's choice without needing
	// one at each call site.
	w.renderStatus(c, c.Writer.Status(), page, data)
}

// renderStatus is render with an explicit HTTP status.
//
// It exists for the plugins that re-render a form on a validation failure: a
// page whose body says the submission was rejected must not tell every client
// 200, or a cache, a crawler or a scripted client records the failure as a
// success. The status is written BEFORE the body, because once html/template
// starts streaming the header is already gone.
func (w *web) renderStatus(c *gin.Context, status int, page string, data map[string]any) {
	// A plugin fragment ships its CSS in a <style> block because it has no
	// other way to; inserted into the body that is invalid HTML. Hoisted
	// here, at the one seam where a fragment becomes a page, rather than in
	// twenty plugin templates that cannot reach the head. See
	// fragmentstyles.go.
	if frag, ok := data["Fragment"].(template.HTML); ok {
		if body, styles := hoistFragmentStyles(frag); styles != "" {
			frag = body
			data["FragmentStyles"] = styles
		}
		// And the nonce, for the same reason and through the same seam: a
		// plugin cannot know a per-request value, so the host stamps it on.
		data["Fragment"] = nonceFragmentScripts(frag, middleware.Nonce(c))
	}
	data = w.chromeData(c, data)
	// The set for this request's language. chromeData resolved it already and
	// cached it on the context, so this is a map lookup rather than a second
	// pass over the headers.
	set := w.tmpls[w.locale(c).Key()]
	if set == nil {
		set = w.tmpls[i18n.Default().Key()]
	}
	t := set[page]
	if t == nil {
		c.String(http.StatusInternalServerError, "unknown page %q", page)
		return
	}
	if site.DevReload {
		// Re-read from disk so a template edit shows on refresh. Unlike the boot
		// path this must NOT template.Must — a half-saved file would kill the
		// server mid-edit; show the parse error in the browser and stay up.
		fresh, err := template.New(page).Funcs(w.tmplFuncs(w.locale(c))).ParseFS(site.FS, pageFiles(page)...)
		if err != nil {
			c.String(http.StatusInternalServerError, "template %s: %v", page, err)
			return
		}
		t = fresh
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(status)
	if err := t.ExecuteTemplate(c.Writer, "base.html", data); err != nil {
		w.log.Error("render", "page", page, "err", err)
	}
}

// ── home page: the block stack ──────────────────────────────────────
//
// UNIT3D's home/index.blade.php is nothing but a foreach over a configurable
// $blocks list (HomeController: an ordered slice, filtered by a per-user
// visible flag) with one @include per block. This is the same shape in Go: an
// ordered slice of names, filtered down to the blocks whose data actually
// resolved, handed to the template as .Blocks.
//
// Go templates cannot take a dynamic template name ({{template .Name}} is a
// compile error), so the page template switches on .Name the way the blade
// switches on $block. Adding a block is four edits: a const, an entry in
// homeBlockOrder, a line in home() that fills its data, and an arm in the
// template's range.

const (
	blockWidgets        = "widgets"         // SlotSiteWidget fragments (plugin-injected)
	blockFeatured       = "featured"        // poster carousel — UNIT3D's featured
	blockLatestReleases = "latest_releases" // the listing table — UNIT3D's top_torrents
	blockNoReleases     = "no_releases"     // that table's EMPTY state; Data is Configured
	blockTopGroups      = "top_groups"      // busiest newsgroups (no UNIT3D analogue)
	blockPopular        = "popular"         // most-grabbed this week — UNIT3D's trending
	blockLatestTopics   = "latest_topics"   // forum threads — UNIT3D's latest_topics
	blockTopPosters     = "top_posters"     // forum posters — UNIT3D's top_users
)

// homeBlockOrder is the demo's $blocks: the render order of the single-column
// stack. Widgets lead (the slot a plugin uses for announcements, so it stands
// where UNIT3D puts news), then the indexer's own content, then the community
// panels — UNIT3D's own ordering of torrents-before-forum.
var homeBlockOrder = []string{
	blockWidgets,
	blockFeatured,
	blockLatestReleases,
	blockNoReleases, // mutually exclusive with the one above; same slot either way
	blockPopular,
	blockTopGroups,
	blockLatestTopics,
	blockTopPosters,
}

// communityBlocks are the three panels that read as a row rather than as a
// stack: the busiest groups, the latest threads and who posts most.
//
// They are grouped because they are the same KIND of thing — small, ranked,
// glanceable summaries of activity — and stacking three of them full-width
// pushes everything below them off the screen for no gain. Each is a short
// list; none needs the width.
//
// A set rather than "the last three", because homeBlockOrder is meant to be
// reorderable and a positional rule would silently regroup whatever ended up
// at the bottom.
var communityBlocks = map[string]bool{
	blockTopGroups:    true,
	blockLatestTopics: true,
	blockTopPosters:   true,
}

// homeBlock is one section of the stack. Data is the block's own view model —
// []widgetVM, []searchRow, []groupRowVM, []forumThreadVM, []forumPosterVM, or
// (for blockNoReleases) the Configured bool — depending on Name.
type homeBlock struct {
	Name string
	Data any
}

// orderedBlocks filters the fixed order down to the blocks that actually have
// content, so the template never has to guard a section: everything in the
// returned slice is renderable. A name with no entry in content is simply
// dropped — that is how a missing usenet/catalog/forum capability degrades.
func orderedBlocks(content map[string]any) []homeBlock {
	out := make([]homeBlock, 0, len(homeBlockOrder))
	for _, name := range homeBlockOrder {
		if d, ok := content[name]; ok {
			out = append(out, homeBlock{Name: name, Data: d})
		}
	}
	return out
}

// home renders the front page as that stack. Every block is OPTIONAL: each read
// below is guarded so a missing capability (no usenet plugin, no catalog, no
// forum) or a failed read just omits the block — nothing here can turn a slow
// or unconfigured dependency into a 500. The releases slot is the exception:
// when it resolves nothing it emits blockNoReleases instead, so the setup
// guidance is never crowded out by whatever else happens to have rows.
//
// Caching: everything shared between viewers (release rows + their covers, the
// category count, the group figures, the forum panels) goes through the
// short-TTL page cache under its own key. Per-viewer values — points, unread,
// role, member-since — are injected by chromeData() and are NEVER written to a
// shared key. Widgets are per-viewer too (each view is role-gated), so they are
// rendered live, not cached. The X-Cache header reports the release block, the
// one read that dominates the page.
func (w *web) home(c *gin.Context) {
	ctx := c.Request.Context()
	// No page-level "Configured": the flag that separates "no indexer plugin in
	// this build" from "the indexer is up but has nothing yet" now rides with
	// blockNoReleases as its Data, like every other block's view model.
	data := map[string]any{"Title": "Home"}

	// block name -> its view model. A block with no entry never renders.
	content := map[string]any{}

	if ws := w.homeWidgets(c); len(ws) > 0 {
		content[blockWidgets] = ws
	}

	var stats siteStatsVM
	var haveStats bool

	if w.usenet != nil {
		rows, hit := w.homeReleases(ctx)
		c.Header("X-Cache", map[bool]string{true: "hit", false: "miss"}[hit])
		if len(rows) > 0 {
			content[blockLatestReleases] = w.attachSwarm(ctx, w.attachGrabs(ctx, capRows(rows, homeTableRows)))
			content[blockFeatured] = featuredRows(rows, homeFeatured)
		}
		// Most-grabbed this week — UNIT3D's trending, now that grabs are
		// recorded. Resolved from the rows already fetched rather than a second
		// index read; an id that has aged out of the recent window simply does
		// not appear, which is why the table stores no titles of its own.
		if pop := w.popularRows(ctx, rows, homePopularRows); len(pop) > 0 {
			content[blockPopular] = pop
		}
		if gs, ok := w.homeGroups(ctx); ok {
			stats, haveStats = gs.Stats, true
			if len(gs.Top) > 0 {
				content[blockTopGroups] = gs.Top
			}
		}
	}

	// The catalog read survives ONLY as a stat-strip figure. The genre pills it
	// used to fill were mockup furniture and are gone; /browse still reads the
	// taxonomy through its own path (browse() → catalog.Enabled), so nothing
	// about category browsing depends on this call.
	if w.catalog != nil {
		if cats, ok := w.homeCategories(ctx); ok {
			stats.Categories, haveStats = len(cats), true
		}
	}
	if haveStats {
		data["Stats"] = stats
	}

	if fv, ok := w.homeForum(ctx); ok {
		if len(fv.Threads) > 0 {
			content[blockLatestTopics] = fv.Threads
		}
		if len(fv.Posters) > 0 {
			content[blockTopPosters] = fv.Posters
		}
	}

	// The releases panel is this page's reason to exist, so when nothing
	// resolved for it, it renders as its OWN empty state rather than vanishing.
	// That guidance used to hang off {{range}}'s else arm — reachable only when
	// the ENTIRE stack was empty — so a build with no indexer but a seeded forum
	// (which always has rows) silently dropped the one line telling an admin
	// where to add one. Its Data is the bool the two messages differ on: an
	// indexer that is wired but empty is not the same case as no indexer at all.
	if _, ok := content[blockLatestReleases]; !ok {
		content[blockNoReleases] = w.usenet != nil
	}

	// Always set: the template ranges over it unguarded, and {{range}} over an
	// ABSENT map key is an execute error that truncates the page mid-document.
	// It can no longer be EMPTY — the releases slot always fills one way or the
	// other — but nothing here relies on that.
	data["Blocks"] = orderedBlocks(content)
	w.render(c, "home.html", data)
}

func (w *web) groups(c *gin.Context) {
	data := map[string]any{"Title": "Groups", "Configured": w.usenet != nil}
	if w.usenet != nil {
		if gs, err := w.usenet.Groups(c.Request.Context()); err == nil {
			data["Groups"] = gs
		}
	}
	w.render(c, "groups.html", data)
}

// browse renders the category grid (no cat) or the release list for one
// category (?cat=N), reusing the usenet Feed capability + the catalog taxonomy.
func (w *web) browse(c *gin.Context) {
	data := map[string]any{"Title": "Browse"}
	if w.catalog == nil {
		data["Unconfigured"] = true
		w.render(c, "browse.html", data)
		return
	}
	ctx := c.Request.Context()
	catParam := strings.TrimSpace(c.Query("cat"))
	if catParam == "" {
		if cats, err := w.catalog.Enabled(ctx); err == nil {
			data["Categories"] = cats
		}
		w.render(c, "browse.html", data)
		return
	}
	catID, err := strconv.Atoi(catParam)
	if err != nil || catID <= 0 {
		// A malformed ?cat= would fall through to an empty categories grid
		// ("No categories enabled") — send it back to the real grid instead.
		c.Redirect(http.StatusSeeOther, "/browse")
		return
	}
	data["CatID"] = catID
	data["CatName"] = w.catalog.Name(catID)
	if w.usenet != nil {
		if res, total, err := w.usenet.Feed(ctx, w.expandCats(ctx, catID), listingLimit, 0); err == nil {
			f := parseFilter(c)
			rows := toSearchRows(res)
			w.attachCovers(ctx, rows) // one lookup for the page, not one per row
			rows = w.attachSwarm(ctx, w.attachGrabs(ctx, rows))
			// Operator-placed widgets above the results.
			if ws := w.renderRegion(c, "listing"); len(ws) > 0 {
				data["RegionWidgets"] = ws
			}
			// Facets from the UNFILTERED set; ?cat= rides along on every facet
			// link so filtering never drops the category you are browsing.
			data["Facets"] = buildFacets(rows, f, "/browse", keepParams(c, "cat"))
			// The table's headers ARE the sort control — see sortColumns.
			data["SortCols"] = sortColumns(f, "/browse", keepParams(c, "cat"))
			shown := f.apply(rows)
			data["Results"] = shown
			w.cartData(c, data, shown)
			data["Filter"] = f
			// Total is the category's real size from the index. The filtered
			// count is len(Results) and the template shows both — conflating
			// them would claim a filter shrank the category itself.
			data["Total"] = total
		}
	}
	// A facet click or a column sort changes only the results panel, so htmx
	// gets that panel and nothing else — see docs/ASYNC.md. Only the CatID
	// branch has one: /browse with no category is the grid of categories, and
	// there is no #results on that page to swap.
	if isHTMX(c) && data["CatID"] != nil {
		w.renderFragment(c, "browse.html", "browse-results", data)
		return
	}
	w.render(c, "browse.html", data)
}

// expandCats maps a browse click to the category ids to query: a top-level
// category expands to itself + all its subcats (Newznab parent semantics); a
// subcategory queries only itself.
func (w *web) expandCats(ctx context.Context, catID int) []int {
	cats, _ := w.catalog.All(ctx)
	for _, cat := range cats {
		if cat.ID == catID {
			ids := []int{cat.ID}
			for _, s := range cat.Subcats {
				ids = append(ids, s.ID)
			}
			return ids
		}
		for _, s := range cat.Subcats {
			if s.ID == catID {
				return []int{catID}
			}
		}
	}
	return []int{catID}
}

func (w *web) search(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	f := parseFilter(c)
	// ?group= is BOTH a search mode (browse that group) and a facet. Treating
	// it only as a facet would break the existing group links; treating it only
	// as a mode would stop it composing with the others. So the read below uses
	// it to choose the source, and the filter applies it again — a no-op on
	// that path, and a real filter on a ?q= search.
	data := map[string]any{"Title": "Search", "Query": q, "Group": f.Group, "Configured": w.usenet != nil}
	if w.usenet != nil {
		var res []pluginapi.Release
		var err error
		switch {
		case f.Group != "":
			res, err = w.usenet.Browse(c.Request.Context(), f.Group, listingLimit)
		case q != "":
			res, err = w.usenet.Search(c.Request.Context(), q, listingLimit)
		default:
			// Nothing asked for yet: show the newest releases rather than an
			// empty page.
			//
			// A search box above a "type something" notice is a dead end — the
			// one page a reader lands on with no idea what is in the index, and
			// it shows them nothing that is in the index. The latest feed
			// answers that for free: it is the same Feed the home page reads,
			// and the facet bar built below then turns it into something
			// browsable rather than something to be typed at.
			//
			// A nil category set means every category — the Feed contract says
			// so, which is why there is no list to keep in step here.
			res, _, err = w.usenet.Feed(c.Request.Context(), nil, listingLimit, 0)
			data["Latest"] = true
		}
		if err == nil {
			ctx := c.Request.Context()
			rows := toSearchRows(res)
			w.attachCovers(ctx, rows) // one lookup for the page
			rows = w.attachSwarm(ctx, w.attachGrabs(ctx, rows))
			// Operator-placed widgets above the results.
			if ws := w.renderRegion(c, "listing"); len(ws) > 0 {
				data["RegionWidgets"] = ws
			}
			// Facets come from the UNFILTERED set so every value offered
			// matches something; the results are what is left after applying.
			data["Facets"] = buildFacets(rows, f, "/search", keepParams(c, "q"))
			data["SortCols"] = sortColumns(f, "/search", keepParams(c, "q"))
			shown := f.apply(rows)
			data["Results"] = shown
			w.cartData(c, data, shown)
			data["Filter"] = f
		}
	}
	// Same as browse. search-results wraps all three shapes of this region
	// (results, no results, nothing searched yet) so the swap target survives
	// a filter that matches nothing.
	if isHTMX(c) {
		w.renderFragment(c, "search.html", "search-results", data)
		return
	}
	w.render(c, "search.html", data)
}

// usersAdapter builds the core.UsersService backing from the user store.
func (w *web) usersAdapter() core.UsersAdapter {
	coreByID := func(ctx context.Context, id int64) (*core.User, error) {
		u, err := w.store.ByID(ctx, id)
		if errors.Is(err, users.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return u.ToCore(), nil
	}
	return core.UsersAdapter{
		GetByIDFn: coreByID,
		GetByUsernameFn: func(ctx context.Context, name string) (*core.User, error) {
			u, err := w.store.ByUsername(ctx, name)
			if errors.Is(err, users.ErrNotFound) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			return u.ToCore(), nil
		},
		DisplayNameFn: func(ctx context.Context, id int64) (string, error) {
			if u, err := coreByID(ctx, id); err == nil && u != nil {
				return u.Username, nil
			}
			return "", nil
		},
		BulkDisplayNamesFn: func(ctx context.Context, ids []int64) (map[int64]string, error) {
			out := make(map[int64]string, len(ids))
			for _, id := range ids {
				if u, err := coreByID(ctx, id); err == nil && u != nil {
					out[id] = u.Username
				}
			}
			return out, nil
		},
	}
}

// homePopularRows is how many rows the "most grabbed" block shows.
const homePopularRows = 5

// popularRows ranks the rows already on hand by their grab count over the last
// week. Deliberately NOT a second index query: this block is a view of what the
// page already fetched, so a release that has aged out of the recent window is
// absent rather than resurrected — the grab table stores ids, not titles, for
// exactly that reason.
//
// Returns nothing when no row has a grab, so a site nobody has downloaded from
// shows no block at all instead of a ranking of zeroes.
func (w *web) popularRows(ctx context.Context, rows []searchRow, limit int) []searchRow {
	ids, counts := w.data.PopularGrabs(ctx, 7, limit*4)
	if len(ids) == 0 {
		return nil
	}
	byID := make(map[int64]searchRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	out := make([]searchRow, 0, limit)
	for _, id := range ids {
		r, ok := byID[id]
		if !ok {
			continue // aged out of the index, or not on this page
		}
		r.Grabs = counts[id]
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}
	return out
}

// templateTruth mirrors text/template's own truth test: the zero value of any
// kind is false, everything else true. Kept next to cond because that is the
// only caller and the two must not drift.
func templateTruth(v any) bool {
	if v == nil {
		return false
	}
	t, ok := template.IsTrue(v)
	return ok && t
}
