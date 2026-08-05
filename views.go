package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"hash/fnv"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

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

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

//go:embed web/templates web/static
var embeddedFS embed.FS

// siteFS is where templates and static assets are read from. Normally that is
// the embedded copy, so the runtime image needs nothing but the binary (the
// Dockerfile ships distroless — there IS no web/ directory in the container).
// LOON_DEMO_DEV=1 swaps in the working tree instead and makes render() re-parse
// per request, so a template or stylesheet edit shows on refresh with no
// rebuild. os.DirFS(".") means the process must run from the repo root, which
// both `go run .` and the compose dev mount already satisfy.
var siteFS fs.FS = embeddedFS

// devReload reports whether templates are re-read from disk on every render.
// Off by default: the cost is a full parse per request, and a parse error
// becomes a page instead of a boot panic — right for a dev loop, wrong for prod.
var devReload = os.Getenv("LOON_DEMO_DEV") == "1"

func init() {
	if devReload {
		siteFS = os.DirFS(".")
	}
}

// web is the demo's host-side HTTP surface: templates, static assets,
// username+password login + registration, and the public pages. The whole auth
// stack — user store, session cookie, bcrypt verify, register/login flow,
// current-user middleware — comes from loon-baseline (the host baseline loon
// omits by design), so the demo exercises the exact code a real site would.
// Users live in a real Postgres table (loon-baseline users.PGStore), seeded
// with alice/bob (password == username).
type web struct {
	store     users.Store    // loon-baseline user store (Postgres reference impl)
	flow      authflow.Flow  // register / authenticate / change-password
	resetFlow authtoken.Flow // password reset + email verification (token flows)
	auth      webauth.Auth
	loginLog  loginlog.Store     // login-attempt audit (recorded here, viewed via its views)
	captcha   *captcha.Verifier  // Turnstile hook (disabled when no keys configured)
	points    core.PointsService // for the navbar balance readout
	inbox     notify.InboxStore  // for the navbar unread-count bell
	cache     cache.Cache        // page cache (in-memory by default, redis if configured)
	ipSalt    string             // salt for hashing client IPs before storing them
	log       *slog.Logger
	tmpls     map[string]*template.Template // page name -> parsed (base + page)

	// usenet plugin read capability, looked up on the extension registry after
	// Boot (the plugin's ADMIN surface is no longer consumed here — the plugin
	// renders its own views through loon's view system).
	usenet        pluginapi.UsenetIndex
	usenetAPI     pluginapi.UsenetNewznab // Newznab /api + /rss
	catalog       pluginapi.Catalog       // taxonomy + names for /browse (filled after Boot)
	catalogSink   pluginapi.CatalogSink   // scraper write side (filled after Boot)
	catalogCovers pluginapi.CatalogCovers // release↔cover store (filled after Boot)
	rt            *core.Runtime           // plugin runtime, for the /admin/plugins page

	// View-system lookup tables, filled by wireViews after Boot.
	adminNav       []navItem            // admin subnav: Settings + plugin pages + host pages
	settingsViews  []core.View          // sections on /admin/settings
	sitePages      []core.View          // public-facing pages at /p/<slug>
	siteWidgets    []core.View          // cards on the home page
	userWidgets    []core.View          // cards on the /u/<name> profile page (user.* slot)
	jobsWidgets    map[string]core.View // job-group name -> override widget
	siteNavEntries []siteNavEntry       // site pages, pre-sorted for the nav (built once at boot)
}

// pageTemplates is every page under web/templates that newWeb parses into its
// own set. base.html and site_chrome.html are NOT here: they are the shell,
// parsed alongside every page. Adding a page file without adding it here leaves
// it unreachable — templates_test.go fails on that mismatch in either direction.
var pageTemplates = []string{
	"home.html", "groups.html", "search.html", "browse.html", "release.html",
	"login.html", "register.html", "forgot.html", "reset.html", "profile.html",
	"site_page.html", "admin_view.html", "admin_settings.html",
	"admin_jobs.html", "admin_plugins.html",
}

// sharedPartials maps a page to the partials it needs beyond the shell. Each
// page gets its OWN template set, so a {{define}} in browse.html is invisible
// to search.html; a block two pages must render identically has to live in a
// file both sets name. listing.html holds the release-row and cat-icon blocks
// that /, /browse and /search render as one table.
var sharedPartials = map[string][]string{
	"home.html":   {"listing.html"},
	"browse.html": {"listing.html"},
	"search.html": {"listing.html"},
}

func newWeb(store users.Store, secret []byte, log *slog.Logger) *web {
	w := &web{
		store: store,
		flow:  authflow.Flow{Users: store, Hasher: password.Hasher{}, DefaultRole: core.RoleUser},
		log:   log,
		tmpls: map[string]*template.Template{},
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
		Session: session.Config{Secret: secret, RedisAddr: os.Getenv("REDIS_ADDR"), Secure: os.Getenv("SECURE_COOKIES") == "1"},
		Resolve: func(ctx context.Context, id int64) (*core.User, webauth.Meta, bool) {
			u, err := store.ByID(ctx, id)
			if err != nil {
				return nil, webauth.Meta{}, false
			}
			return u.ToCore(), webauth.Meta{}, true
		},
	}
	// One template set per page: base.html (the document), site_chrome.html
	// (the header/footer/sprite blocks, shared with the forum plugin's own
	// parse set — see wireForumPlugin), any partial the page shares with
	// another page, then the page itself. The page is parsed LAST on purpose:
	// a {{define}} there overrides the same name in base.html, which is how a
	// page can replace a shell block ("stat-strip" on the home page).
	for _, page := range pageTemplates {
		w.tmpls[page] = template.Must(template.New(page).Funcs(w.tmplFuncs()).ParseFS(siteFS, pageFiles(page)...))
	}
	return w
}

// pageFiles is the parse list for one page, in the order described above. Split
// out of newWeb so render() can rebuild the same set when devReload is on.
func pageFiles(page string) []string {
	files := []string{"web/templates/base.html", "web/templates/site_chrome.html"}
	for _, p := range sharedPartials[page] {
		files = append(files, "web/templates/"+p)
	}
	return append(files, "web/templates/"+page)
}

// tmplFuncs exposes host helpers to templates. {{captcha}} renders the
// Turnstile widget (empty when captcha is disabled), so any form can drop it
// in; everything else comes from tmplHelpers (pure, host-independent).
func (w *web) tmplFuncs() template.FuncMap {
	fns := tmplHelpers()
	fns["captcha"] = func() template.HTML { return w.captcha.Widget() }
	return fns
}

// tmplHelpers are the pure template helpers — no host state, no I/O, so the
// SAME map can be registered on the forum plugin's separate template set
// (forum_web.go parses full documents through gin's HTML set, not the demo's
// per-page map, and its chrome hand-duplicates base.html's header). Anything
// needing the web struct belongs in tmplFuncs instead.
//
//	bytes t     4831838208            -> "4.5 GB"     (release sizes)
//	timeAgo t   2026-08-04T09:00:00Z  -> "3 hours ago" ("" when zero)
//	shortDate t 2026-08-04T09:00:00Z  -> "4 Aug 2026"  ("" when zero)
//	hue s       "Some.Release.1080p"  -> 0..7         (poster fallback bucket)
//	initials s  "[Grp] Some.Release"  -> "GS"         (poster fallback text)
//	roleName r  core.RoleMod          -> "Moderator"
//	ordinal n   3                     -> "3rd"
//	add a b     1 1                   -> 2            (loop indexes)
//	dict k v …  "Row" . "Size" "lg"   -> map          (multi-arg templates)
func tmplHelpers() template.FuncMap {
	return template.FuncMap{
		"bytes":     humanBytes,
		"timeAgo":   timeAgo,
		"shortDate": shortDate,
		"hue":       hueBucket,
		"initials":  initials,
		"roleName":  roleName,
		"ordinal":   ordinal,
		"add":       func(a, b int) int { return a + b },
		"dict":      dict,
	}
}

// timeAgo renders a coarse "3 hours ago" for a past instant. A zero time (the
// crawler never learned a post date) renders empty rather than "56 years ago",
// and a clock-skewed future stamp reads "just now".
func timeAgo(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit + " ago"
		}
		return strconv.Itoa(n) + " " + unit + "s ago"
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour")
	case d < 7*24*time.Hour:
		return plural(int(d/(24*time.Hour)), "day")
	case d < 30*24*time.Hour:
		return plural(int(d/(7*24*time.Hour)), "week")
	case d < 365*24*time.Hour:
		return plural(int(d/(30*24*time.Hour)), "month")
	default:
		return plural(int(d/(365*24*time.Hour)), "year")
	}
}

// shortDate is the human date form used in captions ("4 Aug 2026"). Empty for
// a zero time, so a template can {{if}} on it.
func shortDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2 Jan 2006")
}

// hueBucket maps any string (a release title, a username) onto a stable 0-7
// bucket, so a release with no cover art always gets the SAME gradient tile
// across page loads and processes. FNV-1a: cheap and deterministic.
//
// The modulus is 8 because that is exactly how many hue stops the stylesheet
// defines (.poster--h0 … .poster--h7, components.css). Emitting a bucket with
// no matching class is silent: --poster-hue just keeps its default and every
// such tile renders the same colour. If more stops are ever added to the CSS,
// raise this to match — templates index the class directly off this number.
func hueBucket(s string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32() % 8)
}

// initials takes up to two leading alphanumerics from the first words of a
// title, for the text on a cover-less poster tile. Scene punctuation is skipped,
// so "[SubGrp] Some.Show.S01E02" reads "SS".
func initials(s string) string {
	var out []rune
	inWord := false
	for _, r := range s {
		alnum := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !alnum {
			inWord = false
			continue
		}
		if !inWord {
			out = append(out, unicode.ToUpper(r))
			if len(out) == 2 {
				break
			}
		}
		inWord = true
	}
	return string(out)
}

// roleName is the display label for a role level — the same names the
// user_display view exposes to plugin SQL, title-cased for the UI.
func roleName(r core.Role) string {
	switch {
	case r <= core.RoleBanned:
		return "Banned"
	case r == core.RoleDisabled:
		return "Disabled"
	case r == core.RoleContributor:
		return "Contributor"
	case r == core.RoleMod:
		return "Moderator"
	case r >= core.RoleAdmin:
		return "Admin"
	default:
		return "Member"
	}
}

// ordinal renders a 1-based rank as "1st"/"2nd"/"3rd"/"4th" for rank chips.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

// dict builds a map from alternating key/value pairs, so a shared {{define}}
// can take more than one value: {{template "poster" dict "Row" . "Size" "lg"}}.
// An odd argument count or a non-string key fails the render loudly rather
// than silently dropping a value.
func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("dict: got %d arguments, want an even number of key/value pairs", len(kv))
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: argument %d is %T, want a string key", i, kv[i])
		}
		m[k] = kv[i+1]
	}
	return m, nil
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
func (w *web) currentUser(c *gin.Context) (*core.User, bool) {
	return w.auth.Current(c)
}

// ── routes + rendering ──────────────────────────────────────────────

func (w *web) mount(e *gin.Engine) {
	sub, _ := fs.Sub(siteFS, "web/static")
	e.StaticFS("/static", http.FS(sub))
	e.GET("/", w.home)
	e.GET("/groups", w.groups)
	e.GET("/search", w.search)
	e.GET("/browse", w.browse)
	e.GET("/release/:id", w.releasePage)
	e.GET("/nzb/:id", w.nzbDownload)
	e.GET("/login", w.loginPage)
	e.POST("/login", w.loginPost)
	e.GET("/register", w.registerPage)
	e.POST("/register", w.registerPost)
	e.GET("/forgot", w.forgotPage)
	e.POST("/forgot", w.forgotPost)
	e.GET("/reset", w.resetPage)
	e.POST("/reset", w.resetPost)
	e.GET("/verify", w.verifyEmail)
	// State-changing: POST + CSRF. A GET logout/resend is forgeable via a
	// cross-site top-level navigation (SameSite=Lax still sends the cookie).
	e.POST("/verify/resend", w.resendVerify)
	e.GET("/u/:name", w.profilePage)
	e.POST("/logout", w.logout)
}

// profilePage renders a user's public profile: it resolves the subject by name,
// sets it via core.SetViewSubject, and renders every SlotUserWidget the viewer
// may see (the baseline summary + any plugin contributions like the daily
// streak) — the host owns zero profile content.
func (w *web) profilePage(c *gin.Context) {
	subject, err := w.store.ByUsername(c.Request.Context(), c.Param("name"))
	if err != nil {
		c.Status(http.StatusNotFound)
		w.render(c, "profile.html", map[string]any{"Title": "Not found", "Missing": true})
		return
	}
	core.SetViewSubject(c, subject.ID)

	var widgets []widgetVM
	for _, v := range w.userWidgets {
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
		widgets = append(widgets, widgetVM{Title: v.Title, Fragment: frag})
	}

	viewer, _ := w.currentUser(c)
	w.render(c, "profile.html", map[string]any{
		"Title":   subject.Username,
		"Subject": subject.ToCore(),
		"IsSelf":  viewer != nil && viewer.ID == subject.ID,
		"Widgets": widgets,
	})
}

func (w *web) render(c *gin.Context, page string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	u, _ := w.currentUser(c)
	data["User"] = u
	// Gate the admin nav on actual role, not mere logged-in-ness — the
	// /admin/* routes sit behind Require(RoleAdmin), so a plain user
	// clicking them lands on a 403 JSON blob instead of a page.
	data["IsAdmin"] = u != nil && u.AtLeast(core.RoleAdmin)
	data["CSRFToken"] = csrfToken(c) // hidden _csrf field for every POST form
	if u != nil {
		// Viewer identity bits the user panel + top bar show. Both come off the
		// session-resolved user (no extra query): the rank label the role maps
		// to, and the account's creation date for "member since".
		data["RoleLabel"] = roleName(u.Role)
		data["MemberSince"] = u.CreatedAt
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
		// unverified-email banner: look up the full record (core.User omits the flag)
		if full, err := w.store.ByID(c.Request.Context(), u.ID); err == nil && full != nil {
			data["EmailUnverified"] = full.Email != "" && !full.EmailVerified
		}
		if w.inbox != nil {
			if n, err := w.inbox.UnreadCount(c.Request.Context(), u.ID); err == nil {
				data["Unread"] = n
				data["HasUnread"] = true
			}
		}
	}
	data["Path"] = c.Request.URL.Path
	data["AdminNav"] = w.adminNav
	data["SiteNav"] = w.siteNav(c) // plugin site pages the viewer may open
	t := w.tmpls[page]
	if t == nil {
		c.String(http.StatusInternalServerError, "unknown page %q", page)
		return
	}
	if devReload {
		// Re-read from disk so a template edit shows on refresh. Unlike the boot
		// path this must NOT template.Must — a half-saved file would kill the
		// server mid-edit; show the parse error in the browser and stay up.
		fresh, err := template.New(page).Funcs(w.tmplFuncs()).ParseFS(siteFS, pageFiles(page)...)
		if err != nil {
			c.String(http.StatusInternalServerError, "template %s: %v", page, err)
			return
		}
		t = fresh
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "base.html", data); err != nil {
		w.log.Error("render", "page", page, "err", err)
	}
}

// home renders the front page. Every panel is OPTIONAL: each block below is
// guarded so a missing capability (no usenet plugin, no catalog, no forum) or a
// failed read just leaves its key out and the template drops that section —
// nothing here can turn a slow or unconfigured dependency into a 500.
//
// Caching: everything shared between viewers (release rows + their covers, the
// category list, the group figures, the forum panels) goes through the
// short-TTL page cache under its own key. Per-viewer values — points, unread,
// role, member-since — are injected by render() and are NEVER written to a
// shared key. The X-Cache header reports the release block, the one read that
// dominates the page.
func (w *web) home(c *gin.Context) {
	ctx := c.Request.Context()
	data := map[string]any{
		"Title":   "Home",
		"Widgets": w.homeWidgets(c),
		// Configured separates "no indexer plugin in this build" from "the
		// indexer is up but has nothing yet", which are different empty states.
		"Configured": w.usenet != nil,
	}

	var stats siteStatsVM
	var haveStats bool

	if w.usenet != nil {
		rows, hit := w.homeReleases(ctx)
		c.Header("X-Cache", map[bool]string{true: "hit", false: "miss"}[hit])
		if len(rows) > 0 {
			data["Recent"] = capRows(rows, homeTableRows)       // the main listing table
			data["Featured"] = featuredRows(rows, homeFeatured) // the poster strip
		}
		if gs, ok := w.homeGroups(ctx); ok {
			stats, haveStats = gs.Stats, true
			data["TopGroups"] = gs.Top
		}
	}

	if w.catalog != nil {
		if cats, ok := w.homeCategories(ctx); ok {
			data["Categories"] = cats // tab row + genre pills
			stats.Categories, haveStats = len(cats), true
		}
	}
	if haveStats {
		data["Stats"] = stats
	}

	if fv, ok := w.homeForum(ctx); ok {
		if len(fv.Threads) > 0 {
			data["ForumThreads"] = fv.Threads
		}
		if len(fv.Posters) > 0 {
			data["ForumPosters"] = fv.Posters
		}
	}

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
		if res, total, err := w.usenet.Feed(ctx, w.expandCats(ctx, catID), 50, 0); err == nil {
			rows := toSearchRows(res)
			w.attachCovers(ctx, rows) // one lookup for the page, not one per row
			data["Results"] = rows
			data["Total"] = total
		}
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
	group := strings.TrimSpace(c.Query("group"))
	data := map[string]any{"Title": "Search", "Query": q, "Group": group, "Configured": w.usenet != nil}
	if w.usenet != nil {
		var res []pluginapi.Release
		var err error
		switch {
		case group != "":
			res, err = w.usenet.Browse(c.Request.Context(), group, 100)
		case q != "":
			res, err = w.usenet.Search(c.Request.Context(), q, 50)
		}
		if err == nil {
			rows := toSearchRows(res)
			w.attachCovers(c.Request.Context(), rows) // one lookup for the page
			data["Results"] = rows
		}
	}
	w.render(c, "search.html", data)
}

func (w *web) loginPage(c *gin.Context) {
	w.render(c, "login.html", map[string]any{"Title": "Log in"})
}

func (w *web) loginPost(c *gin.Context) {
	// Captcha first — a bot shouldn't get to probe credentials. No-op when the
	// Turnstile hook is unconfigured (demo default).
	if err := w.captcha.Verify(c.Request.Context(), c.PostForm(captcha.FormField), c.ClientIP()); err != nil {
		c.Status(http.StatusBadRequest)
		w.render(c, "login.html", map[string]any{"Title": "Log in", "Error": "Please complete the captcha and try again."})
		return
	}
	name := c.PostForm("username")
	u, err := w.flow.Authenticate(c.Request.Context(), name, c.PostForm("password"))
	// Audit the attempt via loon-baseline's standard policy (hash the IP,
	// attribute a failed attempt to the targeted account). One call — the
	// policy lives in loginlog, not here.
	if w.loginLog != nil {
		var uid int64
		if u != nil {
			uid = u.ID
		}
		if e := loginlog.Attempt(c.Request.Context(), w.loginLog, w.store.IDByName,
			w.ipSalt, c.ClientIP(), name, uid, err == nil); e != nil {
			w.log.Error("login log", "err", e)
		}
	}
	if err != nil {
		c.Status(http.StatusUnauthorized)
		w.render(c, "login.html", map[string]any{"Title": "Log in", "Error": "Invalid username or password."})
		return
	}
	if err := w.flow.Issue(c, u); err != nil {
		w.log.Error("session issue", "err", err)
	}
	c.Redirect(http.StatusSeeOther, "/")
}

func (w *web) registerPage(c *gin.Context) {
	w.render(c, "register.html", map[string]any{"Title": "Register"})
}

func (w *web) registerPost(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("username"))
	email := strings.TrimSpace(c.PostForm("email"))
	if err := w.captcha.Verify(c.Request.Context(), c.PostForm(captcha.FormField), c.ClientIP()); err != nil {
		c.Status(http.StatusBadRequest)
		w.render(c, "register.html", map[string]any{"Title": "Register", "Error": "Please complete the captcha and try again.", "Username": name, "Email": email})
		return
	}
	u, err := w.flow.Register(c.Request.Context(), name, email, c.PostForm("password"))
	if err != nil {
		c.Status(http.StatusBadRequest)
		w.render(c, "register.html", map[string]any{"Title": "Register", "Error": err.Error(), "Username": name, "Email": email})
		return
	}
	if err := w.flow.Issue(c, u); err != nil {
		w.log.Error("session issue", "err", err)
	}
	// Send the email-verification link (no-op if they left email blank).
	if err := w.resetFlow.SendVerify(c.Request.Context(), u.ID, u.Email); err != nil {
		w.log.Error("send verify", "err", err)
	}
	c.Redirect(http.StatusSeeOther, "/")
}

func (w *web) logout(c *gin.Context) {
	_ = session.Clear(c)
	c.Redirect(http.StatusSeeOther, "/")
}

// ── password reset + email verification (loon-baseline authtoken) ────

func (w *web) forgotPage(c *gin.Context) {
	w.render(c, "forgot.html", map[string]any{"Title": "Reset password"})
}

func (w *web) forgotPost(c *gin.Context) {
	if err := w.captcha.Verify(c.Request.Context(), c.PostForm(captcha.FormField), c.ClientIP()); err != nil {
		c.Status(http.StatusBadRequest)
		w.render(c, "forgot.html", map[string]any{"Title": "Reset password", "Error": "Please complete the captcha."})
		return
	}
	// RequestReset is deliberately silent about whether the email exists, so we
	// always show the same confirmation.
	if err := w.resetFlow.RequestReset(c.Request.Context(), strings.TrimSpace(c.PostForm("email"))); err != nil {
		w.log.Error("request reset", "err", err)
	}
	w.render(c, "forgot.html", map[string]any{"Title": "Reset password", "Sent": true})
}

func (w *web) resetPage(c *gin.Context) {
	w.render(c, "reset.html", map[string]any{"Title": "Set a new password", "Token": c.Query("token")})
}

func (w *web) resetPost(c *gin.Context) {
	token := c.PostForm("token")
	err := w.resetFlow.PerformReset(c.Request.Context(), token, c.PostForm("password"))
	if err != nil {
		msg := "Could not reset your password."
		switch {
		case errors.Is(err, authtoken.ErrWeakPassword):
			msg = "Password must be at least 8 characters."
		case errors.Is(err, authtoken.ErrInvalidToken):
			msg = "This reset link is invalid or has expired."
		}
		c.Status(http.StatusBadRequest)
		w.render(c, "reset.html", map[string]any{"Title": "Set a new password", "Token": token, "Error": msg})
		return
	}
	w.render(c, "login.html", map[string]any{"Title": "Log in", "Notice": "Password updated. Please log in."})
}

func (w *web) verifyEmail(c *gin.Context) {
	data := map[string]any{"Title": "Log in"}
	if _, err := w.resetFlow.ConfirmVerify(c.Request.Context(), c.Query("token")); err != nil {
		data["Error"] = "This verification link is invalid or has expired."
	} else {
		data["Notice"] = "Your email is verified. Thanks!"
	}
	w.render(c, "login.html", data)
}

func (w *web) resendVerify(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	if full, err := w.store.ByID(c.Request.Context(), u.ID); err == nil && full != nil {
		if err := w.resetFlow.SendVerify(c.Request.Context(), full.ID, full.Email); err != nil {
			w.log.Error("resend verify", "err", err)
		}
	}
	c.Redirect(http.StatusSeeOther, "/")
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
