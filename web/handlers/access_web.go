package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/storage"

	"context"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// Who may reach this site, and who may join it — the two questions every
// tracker answers differently and most answer badly.
//
// They are SEPARATE settings on purpose. "Can a stranger read the site" and
// "can a stranger get an account" are different decisions, and collapsing them
// into one "private" switch is what forces a site to choose between being
// findable and being closed. A public catalogue with closed registration is a
// perfectly ordinary configuration; so is a dark site that anyone may apply to.
//
//	registration   open | invite | closed
//	browsing       public | members
//
// Both live in site_settings (the shared key/value table) and are mirrored into
// atomics, because they are read on EVERY request and a settings lookup per
// request would put a database round trip in front of the whole site. The
// mirror is written on save, so a flip takes effect immediately without a
// restart — the same reason donateToggle exists.

// Registration modes.
const (
	// RegOpen — anyone may create an account.
	RegOpen = "open"
	// RegInvite — an account needs an invite code from an existing member.
	RegInvite = "invite"
	// RegClosed — nobody may register. Staff create accounts directly.
	//
	// Distinct from invite: invite delegates the decision to the membership,
	// closed keeps it with staff. A site that means "no new members for now"
	// and sets invite-only is still growing, just more slowly than it thinks.
	RegClosed = "closed"
)

// Browsing modes.
const (
	// BrowsePublic — anonymous visitors may read the site, and crawlers may
	// index it.
	BrowsePublic = "public"
	// BrowseMembers — everything except the doors requires an account, and
	// robots.txt disallows everything.
	BrowseMembers = "members"
)

const (
	settingRegistration = "access_registration"
	settingBrowsing     = "access_browsing"
)

// The live mirrors. Read per request, written on save.
var (
	regMode     atomic.Value // string
	browseMode  atomic.Value // string
	accessStore siteSettings
)

func init() {
	regMode.Store(RegOpen)
	browseMode.Store(BrowsePublic)
}

// registrationMode returns the current mode, defaulting to open.
func registrationMode() string {
	s, _ := regMode.Load().(string)
	if s == "" {
		return RegOpen
	}
	return s
}

// browsingMode returns the current mode, defaulting to public.
func browsingMode() string {
	s, _ := browseMode.Load().(string)
	if s == "" {
		return BrowsePublic
	}
	return s
}

// loadAccessSettings restores both from the database at boot.
//
// A restart must not silently reopen a closed site — the whole point of
// persisting these is that the answer survives the process.
func loadAccessSettings(ctx context.Context, db storage.Conn) error {
	accessStore = siteSettings{db: db}
	if v, err := accessStore.GetSetting(ctx, settingRegistration); err != nil {
		return err
	} else if v != "" {
		regMode.Store(v)
	}
	if v, err := accessStore.GetSetting(ctx, settingBrowsing); err != nil {
		return err
	} else if v != "" {
		browseMode.Store(v)
	}
	return nil
}

// saveAccessSettings persists and mirrors both.
func saveAccessSettings(ctx context.Context, reg, browse string) error {
	if !validReg(reg) || !validBrowse(browse) {
		// An unknown mode is a bug in a form, not a state to adopt: adopting it
		// would leave the site in a mode nothing enforces, which reads as
		// "open" and may be the opposite of what was asked for.
		return http.ErrNotSupported
	}
	if err := accessStore.SetSetting(ctx, settingRegistration, reg); err != nil {
		return err
	}
	if err := accessStore.SetSetting(ctx, settingBrowsing, browse); err != nil {
		return err
	}
	regMode.Store(reg)
	browseMode.Store(browse)
	return nil
}

func validReg(s string) bool {
	return s == RegOpen || s == RegInvite || s == RegClosed
}

func validBrowse(s string) bool {
	return s == BrowsePublic || s == BrowseMembers
}

// alwaysPublic are the paths that must answer even on a members-only site.
//
// Every one is either a door (you cannot log in through a gate that requires
// you to be logged in), a machine endpoint that carries its own credential, or
// an operational probe that must not depend on the site's mood.
//
// /api and /rss authenticate with an api key rather than a session, so gating
// them on a session would break every downloader the moment the site went
// private — which is the opposite of what "members only" means for a tool a
// member already authorised.
func alwaysPublic(p string) bool {
	switch p {
	case "/login", "/logout", "/register", "/forgot", "/reset",
		"/healthz", "/robots.txt", "/favicon.ico":
		return true
	}
	// Exact match or a path SEGMENT, never a bare prefix.
	//
	// strings.HasPrefix(p, "/api") also matches /apikeys and /api-docs, which
	// nothing here is called today — but it means the exemption is granted by
	// spelling rather than by anybody deciding, and the day a route lands on
	// the wrong side of that line it is readable on a members-only site with
	// no symptom. The endpoints that exist (/api, /rss, /api/chat/…,
	// /api/btcpay/webhook) all match this form.
	return strings.HasPrefix(p, "/static/") ||
		p == "/api" || strings.HasPrefix(p, "/api/") ||
		p == "/rss" || strings.HasPrefix(p, "/rss/") ||
		strings.HasPrefix(p, "/verify/")
}

// requireLoginMiddleware sends anonymous visitors to the door when browsing is
// members-only.
//
// Installed unconditionally and checked per request rather than wired at boot,
// so flipping the mode takes effect on the next request instead of the next
// deploy.
func (w *web) requireLoginMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		u, ok := w.currentUser(c)
		if allow, to := browsingGate(browsingMode(), c.Request.URL.Path,
			c.Request.URL.RequestURI(), ok && u != nil); !allow {
			c.Redirect(http.StatusFound, to)
			c.Abort()
			return
		}
		c.Next()
	}
}

// browsingGate is the whole decision: may this request proceed, and if not,
// where does it go instead.
//
// Pulled out of the middleware so the truth table can be tested without a
// session, a store or a request — this decides whether a private site is
// actually private, and "we believe it works" is a poor guarantee for that.
// The middleware above is now only the plumbing that carries the answer.
//
// requestURI is passed separately rather than derived, because the redirect has
// to preserve the QUERY and path alone would silently drop it.
func browsingGate(mode, path, requestURI string, signedIn bool) (allow bool, redirectTo string) {
	if mode != BrowseMembers || alwaysPublic(path) || signedIn {
		return true, ""
	}
	// ?next= so the door returns you where you were going. Query included: a
	// private site turning away a deep link and then dumping you on the home
	// page has lost the thing you came for.
	return false, "/login?next=" + url.QueryEscape(requestURI)
}

// robotsTxt answers /robots.txt from the browsing mode.
//
// A members-only site that still invites crawlers is not members-only: search
// engines and AI scrapers index whatever they can read, and a stale allow rule
// is how a private catalogue ends up in a public index. So the file is
// GENERATED, never a static asset — one source of truth with the gate itself.
func (w *web) robotsTxt(c *gin.Context) {
	c.Header("Content-Type", "text/plain; charset=utf-8")
	if browsingMode() == BrowseMembers {
		c.String(http.StatusOK, "# members-only: nothing here is readable without an account\nUser-agent: *\nDisallow: /\n")
		return
	}
	// Public, but the account area and the API are still nobody's business to
	// crawl: they are per-viewer or credentialed, so indexing them wastes a
	// crawl budget on pages that answer differently for every reader.
	c.String(http.StatusOK, strings.Join([]string{
		"User-agent: *",
		"Disallow: /admin/",
		"Disallow: /settings/",
		"Disallow: /subscriptions",
		"Disallow: /bookmarks",
		"Disallow: /inbox",
		"Disallow: /api",
		"Disallow: /rss",
		"Allow: /",
		"",
		"Sitemap: /sitemap.xml",
		"",
	}, "\n"))
}
