package handlers

import (
	"github.com/the-loon-clan/loon-demo-site/internal/middleware"

	site "github.com/the-loon-clan/loon-demo-site"

	"context"
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

	"github.com/the-loon-clan/loon-plugins/dailyreward"
	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon-plugins/rewards"
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
	// covers downloads scraped art to local storage so the site serves it
	// instead of hotlinking a provider CDN — see covercache_web.go. Built at
	// construction, not after Boot: it depends on nothing but the upload volume.
	covers *coverCache
	rt     *core.Runtime // plugin runtime, for the /admin/plugins page
	// achievements answers where a member stands on every earnable badge.
	// nil when the rewards plugin is absent, which renders the page's
	// unavailable state rather than a 404 on a link the nav always shows.
	achievements rewards.AchievementsFunc

	// calSources contribute dated events to /calendar. A slice rather than a
	// field per source: the page's whole point is that adding a kind of event
	// does not touch the page.
	calSources []calSource

	// dailyStatus answers "may this member claim today?" for the stat strip's
	// compact button. nil when the plugin is absent, which renders no button.
	dailyStatus dailyreward.StatusFunc

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
	"trending.html", "bookmarks.html", "follows.html", "calendar.html",
	"achievements.html", "forum_activity.html", "rewards.html", "subscriptions.html",
	"invites.html", "gifts.html", "wishlist.html",
	"login.html", "register.html", "forgot.html", "reset.html", "profile.html",
	"site_page.html", "admin_view.html", "admin_settings.html",
	"admin_jobs.html", "admin_plugins.html", "admin_dashboard.html",
	"admin_access.html", "admin_contracts.html", "admin_covers.html",
	// The widget page editor (widgetsadmin_web.go).
	"admin_widgets.html",
	// Data-source attribution (credits_web.go).
	"credits.html",
	// Moderation is not under /admin (it gates at RoleMod) but is the same
	// kind of page — see avatarmod_web.go.
	"moderation_avatars.html", "moderation_community.html",
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
}

func newWeb(store users.Store, secret []byte, log *slog.Logger) *web {
	w := &web{
		store:  store,
		flow:   authflow.Flow{Users: store, Hasher: password.Hasher{}, DefaultRole: core.RoleUser, MinPasswordLen: minPasswordLen},
		log:    log,
		tmpls:  map[string]*template.Template{},
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
			touchLastSeen(ctx, usersDB, id)
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
		w.tmpls[page] = template.Must(template.New(page).Funcs(w.tmplFuncs()).ParseFS(site.FS, pageFiles(page)...))
	}
	return w
}

// pluginTemplates parses the set gin renders for every plugin that draws its UI
// through c.HTML rather than the demo's own per-page map: the shared site chrome
// plus each plugin's full documents. One set, not one per plugin, because gin
// holds exactly one HTMLRender.
//
// Reads through site.FS (the embedded copy by default) rather than the
// filesystem. The runtime image is distroless and carries ONLY the binary, so a
// disk-relative ParseGlob here matches no files and takes the whole process down
// at boot via main.go's os.Exit(1) — which is exactly what it used to do.
//
// Template names are base filenames, so the dirs below share one flat namespace:
// two plugins must not both ship an "index.html".
func pluginTemplates() (*template.Template, error) {
	return template.New("plugins").Funcs(tmplHelpers()).ParseFS(site.FS,
		"web/templates/site_chrome.html",
		"web/templates/forum/*.html",
		"web/templates/editor.html",
		"web/templates/plugin/*.html",
	)
}

// pageFiles is the parse list for one page, in the order described above. Split
// out of newWeb so render() can rebuild the same set when site.DevReload is on.
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
		// asset appends the build's content hash to a static URL, so a
		// stylesheet change is a new URL rather than a cached old one. See
		// assetversion_web.go.
		"asset":     assetURL,
		"bytes":     humanBytes,
		"timeAgo":   timeAgo,
		"shortDate": shortDate,
		"hue":       hueBucket,
		"initials":  initials,
		"roleName":  roleName,
		// pwmin is the minimum password length, so a form's minlength attribute
		// and its help text quote the number the server enforces rather than a
		// number someone typed once. See password_web.go.
		"pwmin":     func() int { return minPasswordLen },
		"roleSlug":  roleSlug,
		"roleLabel": roleLabel,
		"eqID":      eqID,
		"hasPrefix": strings.HasPrefix,
		"navActive": navActive,
		"inGroup":   inGroup,
		// prose renders a person-authored body through the site's one
		// sanitizing markdown pipeline (markdown_web.go). It exists because the
		// plugin-rendered pages hand their bodies over as plain strings — no
		// Deps.Markdown seam to route them through — and printing those with
		// {{.Body}} means HTML collapses every newline, so a multi-paragraph
		// support ticket arrives as one run-on block.
		"prose":    siteMarkdown,
		"ordinal":  ordinal,
		"ellipsis": ellipsis,
		"excerpt":  excerpt,
		"str":      str_,
		"add":      func(a, b int) int { return a + b },
		"dict":     dict,
		// cond is the ternary the template language does not have. It earns its
		// place in ARGUMENT position: {{if}} is a statement and cannot appear
		// inside a dict literal, so passing a component an optional label meant
		// a $var and a three-line {{if}} above every call site.
		//
		// Takes `any`, not bool, and applies the SAME truthiness {{if}} does.
		// As a bool it rejected `cond .Since "…" ""` with "wrong type for
		// value" at EXECUTE time — which truncates the page mid-document and
		// still returns 200, so it looks like a blank section rather than an
		// error. A ternary that only accepts one type is a trap in a language
		// where every {{if}} accepts all of them.
		"cond": func(c, yes, no any) any {
			if templateTruth(c) {
				return yes
			}
			return no
		},
	}
}

// str_ renders a value as a plain string, dereferencing a *string first.
//
// It exists because {{print "/u/" .Name}} on a *string emits the POINTER —
// "/u/0x1129d1d30910" — while {{.Name}} beside it prints "bob", because
// html/template auto-indirects when printing a value on its own but not inside
// a fmt.Sprint of several. Plugin row structs use *string for nullable columns
// (the forum's LastPostUsername), so the result is a correct-looking name whose
// link is garbage: nothing errors, nothing logs, and the page looks right.
func str_(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case *string:
		if x == nil {
			return ""
		}
		return *x
	}
	return fmt.Sprint(v)
}

// ellipsis shortens a string to n runes, ending in a single-character ellipsis
// when it had to cut. Runes, not bytes: a release title is arbitrary bytes off
// a Usenet header, and slicing one mid-rune produces the replacement character.
//
// Titles here are not names — obfuscated posts run to ninety characters of
// punctuation — so any slot that shows one inline (a breadcrumb, a poster
// field) needs a bound or the layout is at the mercy of whatever was posted.
// The full value stays available in a title attribute at the call site.
func ellipsis(n int, s string) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	// Prefer to cut at a space so the result ends on a word where one is near
	// the limit, rather than mid-token.
	cut := n
	for i := n; i > n*3/4 && i > 0; i-- {
		if r[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(r[:cut]), " ") + "…"
}

// navActive reports whether a nav entry covers the current path — an exact
// match, or a parent of it.
//
// An equality test loses the highlight the moment a nav target grows child
// pages: /admin/settings/usenet is the Settings page, and the subnav has to
// keep saying so. The child test is segment-aware for the same reason
// matchesSection's is — a bare strings.HasPrefix would make /admin/plugins a
// child of /admin/plug.
func navActive(path, href string) bool {
	href = strings.TrimSuffix(href, "/")
	return path == href || strings.HasPrefix(path, href+"/")
}

// inGroup reports whether the current path is one of the plugin pages merged
// into a host dropdown (see hostNavGroups). Without it a page that opted into
// Community would appear in that menu and still leave it unlit, which reads as
// the nav having lost track of where you are.
func inGroup(m map[string][]navItem, group, path string) bool {
	for _, it := range m[group] {
		if navActive(path, it.Href) {
			return true
		}
	}
	return false
}

// timeAgo renders a coarse "3 hours ago" for a past instant. A zero time (the
// crawler never learned a post date) renders empty rather than "56 years ago",
// and a clock-skewed future stamp reads "just now".
// relativeTime adapts timeAgo to the any-taking seam plugins ask for. They
// take `any` because a plugin's row type may carry a timestamp as time.Time, a
// pointer, or an interface field — and a seam that demanded one concrete shape
// would push that conversion into every caller.
//
// Anything unrecognised renders empty rather than a Go dump: a malformed
// timestamp should cost a line its "2 hours ago", not print a struct at a user.
func relativeTime(v any) string {
	switch t := v.(type) {
	case time.Time:
		return timeAgo(t)
	case *time.Time:
		if t == nil {
			return ""
		}
		return timeAgo(*t)
	}
	return ""
}

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

// roleSlug maps a role onto the kebab token the .user-tag--<slug> classes and
// the --user-tag-<slug>-fg theme tokens key off.
//
// It takes `any` because usernames reach templates in two different shapes and
// both must render identically: host pages carry a typed core.Role, while the
// forum plugin hands back a free-text role string from its user_display view
// ("admin", "mod", "user", …). Anything unrecognised — including the empty
// string a plugin row with no role yields — falls back to "member", so an
// unknown role renders as a plain member rather than an unstyled tag.
func roleSlug(v any) string {
	// Plugin row structs carry a nullable role as *string (the forum's
	// LastPostRole). Without this, such a value matches no case below and every
	// last poster silently renders as "member" — the exact half-right output
	// eqID exists to prevent, and the reason normalising belongs in the helper
	// rather than at each call site.
	if p, ok := v.(*string); ok {
		if p == nil {
			return "member"
		}
		v = *p
	}
	switch r := v.(type) {
	case core.Role:
		switch {
		case r <= core.RoleBanned:
			return "banned"
		case r == core.RoleDisabled:
			return "disabled"
		case r == core.RoleContributor:
			return "contributor"
		case r == core.RoleMod:
			return "mod"
		case r >= core.RoleAdmin:
			return "admin"
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(r)) {
		case "admin", "administrator", "owner":
			return "admin"
		case "mod", "moderator", "staff":
			return "mod"
		case "contributor", "uploader":
			return "contributor"
		case "banned":
			return "banned"
		case "disabled":
			return "disabled"
		}
	}
	return "member"
}

// eqID compares two user ids that arrive as different integer types.
//
// {{eq}} refuses this: core.User.ID is int64 while several plugins carry their
// user ids as plain int, and html/template reports "incompatible types for
// comparison" at EXECUTE time — which means the page half-renders in
// production rather than failing a build. Normalising both sides here is the
// only comparison an ownership check should use.
func eqID(a, b any) bool {
	toI64 := func(v any) (int64, bool) {
		switch n := v.(type) {
		case int:
			return int64(n), true
		case int32:
			return int64(n), true
		case int64:
			return n, true
		case uint:
			return int64(n), true
		case uint32:
			return int64(n), true
		case uint64:
			return int64(n), true
		}
		return 0, false
	}
	x, okA := toI64(a)
	y, okB := toI64(b)
	// Two non-numbers are not "equal" — a false here fails an ownership check
	// closed, which is the right direction for the thing this guards.
	return okA && okB && x == y
}

// roleLabel is roleName for the mixed-shape case. roleName takes a typed
// core.Role and is kept that way for the host's own call sites; the user-tag
// block also renders forum rows whose role is a plain string, so it needs a
// label helper that accepts both. Derived from roleSlug so the label a tag
// shows can never disagree with the colour it is painted.
func roleLabel(v any) string {
	switch roleSlug(v) {
	case "admin":
		return "Admin"
	case "mod":
		return "Moderator"
	case "contributor":
		return "Contributor"
	case "banned":
		return "Banned"
	case "disabled":
		return "Disabled"
	}
	return "Member"
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
	sub, _ := fs.Sub(site.FS, "web/static")
	// Headers BEFORE the file handler: the embedded FS has no modtime, so
	// without this nothing tells a browser when to look again.
	e.Use(staticCacheHeaders())
	e.StaticFS("/static", http.FS(sub))
	e.GET("/", w.home)
	e.GET("/groups", w.groups)
	// Trending — most-grabbed releases (trending_web.go). Public: it exposes no
	// more than /browse already does, ordered differently.
	e.GET("/trending", w.trending)
	// Bookmarks (bookmarks_web.go). The list is per-viewer and the toggle
	// WRITES, so it is POST — a GET that mutates is one prefetching browser
	// away from bookmarking somebody's whole history for them.
	e.GET("/bookmarks", w.bookmarksPage)
	// Calendar (calendar_web.go) — the member's own dated things, so it is
	// login-gated inside the handler like /bookmarks rather than by role.
	e.GET("/calendar", w.calendarPage)
	// Achievements (achievements_web.go) and the two forum-activity
	// listings (forumactivity_web.go). All account-scoped, so each gates
	// on the viewer inside the handler rather than on a role.
	e.GET("/achievements", w.achievementsPage)
	e.GET("/p/topics", w.forumActivityPage(false))
	e.GET("/p/posts", w.forumActivityPage(true))
	// Rewards (rewards_web.go) — the points area's third tab. Registered here
	// rather than under /store/*, which the store plugin owns.
	e.GET(storeRewardsPath, w.rewardsPage)
	// Subscriptions (subscriptions_web.go) — one list of everything the viewer
	// follows, read from the tables that already hold it.
	e.GET("/subscriptions", w.subscriptionsPage)
	// Invite codes (invitecodes_web.go).
	e.GET("/login/2fa", w.twoFactorPage)
	e.POST("/login/2fa", w.twoFactorPost)
	e.GET("/wishlist", w.wishlistPage)
	e.POST("/wishlist", w.wishlistAdd)
	e.POST("/wishlist/:id", w.wishlistUpdate)
	e.GET("/gifts", w.giftsPage)
	e.POST("/gifts", w.giftsSend)
	e.GET("/invites", w.invitesPage)
	e.POST("/invites", w.invitesCreate)
	e.POST("/u/:name/follow", w.followToggle)
	// Reporting an avatar opens (or votes on) a community moderation item —
	// see communitymod_web.go.
	e.POST("/u/:name/report-avatar", w.reportAvatarPost)
	// Reverse the last destructive thing you did (undo_web.go).
	e.POST("/undo", w.undoPost)
	e.GET("/u/:name/followers", w.followPage(followKindFollowers))
	e.GET("/u/:name/following", w.followPage(followKindFollowing))
	// Mutual follows (follows_web.go). Derived from the same table, so this is
	// a third READING of user_follow rather than a third feature.
	e.GET("/u/:name/friends", w.followPage(followKindFriends))
	e.POST("/release/:id/bookmark", w.bookmarkToggle)
	e.GET("/search", w.search)
	e.GET("/browse", w.browse)
	e.GET("/release/:id", w.releasePage)
	e.GET("/nzb/:id", w.nzbDownload)
	// Fixed host pages: /staff /stats /rules /faq /about (pages_web.go).
	w.mountSitePages(e)
	// The dev-only UI inspector (uiinspect_web.go). Registers nothing unless
	// LOON_DEMO_UI_INSPECT is set — it serves files off disk and injects script
	// into a frame of the site, neither of which belongs on a reachable build.
	w.mountUIInspect(e)
	// Viewer settings: /settings/privacy, /settings/notifications.
	w.mountSettings(e)
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
	// Theme switcher (theme.go). POST + CSRF like every other state-changing
	// form here; the preference is a cookie, so it works for anonymous
	// visitors too and sits outside any auth group.
	e.POST(themeRoute, w.setTheme)
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

	private := !isSelf && !isStaff && isPrivateProfile(c.Request.Context(), subject.ID)
	data := map[string]any{
		"Title":   subject.Username,
		"Subject": subj,
		// The subject's avatar, which is not the viewer's — chromeData's
		// UserAvatar is whoever is logged in, and on someone else's profile
		// those are different people.
		"SubjectAvatar": readAvatarPath(c.Request.Context(), usersDB, subject.ID),
		// Outcome of a report POST, round-tripped through the redirect.
		"Report":  c.Query("report"),
		"IsSelf":  isSelf,
		"Widgets": widgets,
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
	if bio := renderBio(readBio(ctx, subject.ID)); bio != "" {
		data["Bio"] = bio
	}
	if w.points != nil {
		if bal, err := w.points.Balance(ctx, subject.ID); err == nil {
			data["SubjectPoints"], data["HasSubjectPoints"] = bal, true
		}
	}
	if forumReads != nil {
		if n, err := forumPostCount(ctx, subject.ID); err == nil {
			data["SubjectPosts"], data["HasSubjectPosts"] = n, true
		}
	}
	// Bookmarks are PUBLIC on a profile the way UNIT3D shows them — a count,
	// not the list. Has* rather than a bare zero, so an unreachable table
	// leaves the tile an em dash instead of claiming nobody saved anything.
	if n, ok := bookmarkCount(ctx, subject.ID); ok {
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
	if followers, following, ok := followCounts(ctx, subject.ID); ok {
		data["SubjectFollowers"], data["SubjectFollowing"] = followers, following
		data["HasSubjectFollows"] = true
	}
	if t, ok := lastSeenAt(ctx, usersDB, subject.ID); ok {
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
	if tt, ok := readTrackerTotals(ctx, usersDB, subject.ID); ok {
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
		data["Following"] = isFollowing(ctx, viewer.ID, subject.ID)
	}
	// Invites are the viewer's own spendable balance, so they only show on
	// your own profile — someone else's invite count is not your business.
	if viewer != nil && viewer.ID == subject.ID {
		if n, ok := inviteBalance(ctx, subject.ID); ok {
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
	// Gate the admin nav on actual role, not mere logged-in-ness — the
	// /admin/* routes sit behind Require(RoleAdmin), so a plain user
	// clicking them lands on a 403 JSON blob instead of a page.
	data["IsAdmin"] = u != nil && u.AtLeast(core.RoleAdmin)
	// The forum's moderation routes (pin/lock, category admin) gate at RoleMod
	// — templates must show those buttons to the role that can use them.
	data["IsMod"] = u != nil && u.AtLeast(core.RoleMod)
	// The pending-avatar count on the account menu's moderation entry. Only
	// read for staff — an unread badge is what makes a queue get worked, and
	// the reports plugin's 98-day-old open item is what the absence of one
	// looks like. Costs one indexed count per staff page view, nothing at all
	// for everybody else.
	if u != nil && u.AtLeast(core.RoleMod) {
		if n := countPendingAvatars(c.Request.Context(), usersDB); n > 0 {
			data["PendingAvatars"] = n
		}
	}
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
		data["UserAvatar"] = readAvatarPath(c.Request.Context(), usersDB, u.ID)
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
		// No tracker figures in the chrome. They were rendered twice — these
		// keys AND the tracker-standing widget, which an operator can place in
		// the same header bar — and two sources for one number is a thing to
		// remove rather than style around. The widget won because an operator
		// controls where it goes, or whether it appears at all.
		//
		// A member's standing is still on /stats and on their profile, and
		// readTrackerTotals is still what those read.
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
		data["AccountBar"] = accountBar(c.Request.URL.Path, viewer != nil, own)
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
	data["AdminNav"] = w.adminNav
	// Plugin site pages the viewer may open: SiteNav is the top-level nodes,
	// SiteNavGroup the ones that asked to sit inside a dropdown the host writes
	// itself (see hostNavGroups), SiteNavAccount the per-viewer pages that
	// belong on the account menu. All three ALWAYS set — the nav indexes
	// SiteNavGroup by name on every render.
	data["SiteNav"], data["SiteNavGroup"], data["SiteNavAccount"] = w.siteNav(c)
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
	data = w.chromeData(c, data)
	t := w.tmpls[page]
	if t == nil {
		c.String(http.StatusInternalServerError, "unknown page %q", page)
		return
	}
	if site.DevReload {
		// Re-read from disk so a template edit shows on refresh. Unlike the boot
		// path this must NOT template.Must — a half-saved file would kill the
		// server mid-edit; show the parse error in the browser and stay up.
		fresh, err := template.New(page).Funcs(w.tmplFuncs()).ParseFS(site.FS, pageFiles(page)...)
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
			content[blockLatestReleases] = attachSwarm(ctx, attachGrabs(ctx, capRows(rows, homeTableRows)))
			content[blockFeatured] = featuredRows(rows, homeFeatured)
		}
		// Most-grabbed this week — UNIT3D's trending, now that grabs are
		// recorded. Resolved from the rows already fetched rather than a second
		// index read; an id that has aged out of the recent window simply does
		// not appear, which is why the table stores no titles of its own.
		if pop := popularRows(ctx, rows, homePopularRows); len(pop) > 0 {
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
			rows = attachSwarm(ctx, attachGrabs(ctx, rows))
			// Operator-placed widgets above the results.
			if ws := w.renderRegion(c, "listing"); len(ws) > 0 {
				data["RegionWidgets"] = ws
			}
			// Facets from the UNFILTERED set; ?cat= rides along on every facet
			// link so filtering never drops the category you are browsing.
			data["Facets"] = buildFacets(rows, f, "/browse", keepParams(c, "cat"))
			// The table's headers ARE the sort control — see sortColumns.
			data["SortCols"] = sortColumns(f, "/browse", keepParams(c, "cat"))
			data["Results"] = f.apply(rows)
			data["Filter"] = f
			// Total is the category's real size from the index. The filtered
			// count is len(Results) and the template shows both — conflating
			// them would claim a filter shrank the category itself.
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
		}
		if err == nil {
			ctx := c.Request.Context()
			rows := toSearchRows(res)
			w.attachCovers(ctx, rows) // one lookup for the page
			rows = attachSwarm(ctx, attachGrabs(ctx, rows))
			// Operator-placed widgets above the results.
			if ws := w.renderRegion(c, "listing"); len(ws) > 0 {
				data["RegionWidgets"] = ws
			}
			// Facets come from the UNFILTERED set so every value offered
			// matches something; the results are what is left after applying.
			data["Facets"] = buildFacets(rows, f, "/search", keepParams(c, "q"))
			data["SortCols"] = sortColumns(f, "/search", keepParams(c, "q"))
			data["Results"] = f.apply(rows)
			data["Filter"] = f
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
	// SECOND FACTOR, between a correct password and a session. Deliberately
	// after the login-attempt audit above: an attempt that got the password
	// right is worth recording as a success whether or not the second step
	// follows, because "the password is known" is the fact a reader of that log
	// needs. See security_web.go.
	if secretOf(c.Request.Context(), u.ID) != "" {
		beginTOTPChallenge(c, u.ID)
		return
	}
	if err := w.flow.Issue(c, u); err != nil {
		w.log.Error("session issue", "err", err)
	}
	c.Redirect(http.StatusSeeOther, "/")
}

func (w *web) registerPage(c *gin.Context) {
	// The mode reaches the template rather than the handler refusing outright:
	// a closed site should SAY it is closed, not 404 the page a visitor was
	// invited to by a link. "Registration is closed" is information; a dead
	// link is a puzzle.
	w.render(c, "register.html", map[string]any{
		"Title":   "Register",
		"RegMode": registrationMode(),
	})
}

func (w *web) registerPost(c *gin.Context) {
	name := strings.TrimSpace(c.PostForm("username"))
	email := strings.TrimSpace(c.PostForm("email"))
	// Enforced HERE as well as in the template, because the template only
	// hides the form. A closed site whose POST still works is open to anyone
	// who kept the page open, or who has ever used curl.
	switch registrationMode() {
	case RegClosed:
		c.Status(http.StatusForbidden)
		w.render(c, "register.html", map[string]any{
			"Title": "Register", "RegMode": RegClosed,
		})
		return
	case RegInvite:
		if !inviteCodeValid(c.Request.Context(), strings.TrimSpace(c.PostForm("invite"))) {
			c.Status(http.StatusForbidden)
			w.render(c, "register.html", map[string]any{
				"Title": "Register", "RegMode": RegInvite,
				"Error": "That invite code is not valid.", "Username": name, "Email": email,
				"Invite": strings.TrimSpace(c.PostForm("invite")),
			})
			return
		}
	}
	if err := w.captcha.Verify(c.Request.Context(), c.PostForm(captcha.FormField), c.ClientIP()); err != nil {
		c.Status(http.StatusBadRequest)
		w.render(c, "register.html", map[string]any{"Title": "Register", "Error": "Please complete the captcha and try again.", "Username": name, "Email": email, "RegMode": registrationMode(), "Invite": strings.TrimSpace(c.PostForm("invite"))})
		return
	}
	invite := strings.TrimSpace(c.PostForm("invite"))
	u, err := w.flow.Register(c.Request.Context(), name, email, c.PostForm("password"))
	if err != nil {
		c.Status(http.StatusBadRequest)
		w.render(c, "register.html", map[string]any{"Title": "Register", "Error": err.Error(), "Username": name, "Email": email, "RegMode": registrationMode(), "Invite": invite})
		return
	}
	// Consume the code now the account exists. Redeem, not just validate: a
	// gate that checks without consuming lets one code make any number of
	// accounts, which is the whole thing invite-only is trying to stop.
	//
	// After Register on purpose. Redeeming first would burn the code when
	// registration then fails on a taken username — the visitor loses an invite
	// they were given and has nothing to show for it.
	if registrationMode() == RegInvite && !redeemInviteCode(c.Request.Context(), invite, u.ID) {
		// The account exists and the code did not stick — a race with another
		// registration on the same code. Say so rather than leaving them signed
		// in via a gate that did not open.
		w.log.Warn("invite not redeemed after register", "user", u.ID)
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
func popularRows(ctx context.Context, rows []searchRow, limit int) []searchRow {
	ids, counts := popularGrabs(ctx, 7, limit*4)
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
