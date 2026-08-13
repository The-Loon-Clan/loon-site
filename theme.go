package site

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// Theme selection.
//
// UNIT3D ships seventeen themes over IDENTICAL markup: the theme is a per-user
// setting, not a build-time choice, and only design tokens change between them.
// The demo does the same with a cookie — one extra stylesheet, one switcher, no
// markup differences at all.
//
// The split is structure vs. skin, NOT base vs. override: tokens.css declares
// only the values every theme shares (spacing, type, z-layers, motion,
// breakpoints) and declares no colour, radius, border or shadow at all. Each
// theme file defines the COMPLETE visual token set on its own; no theme leans
// on another to fill a gap, and a test pins that the three declare identical
// name sets.
//
// The one safety net is in tokens.css, which @imports the DEFAULT theme ahead
// of its own rules. That exists for pages this file never sees: loon's
// framework admin pages (core/admin.go, schedule/admin.go,
// schedule/config_admin.go) hand-roll a <head> that links bootstrap, tokens.css
// and theme.css and nothing else — they cannot read the viewer's cookie or know
// this allowlist, and without the import theme.css's Bootstrap-subset
// utilities resolved to nothing and those pages rendered unstyled. A page that
// DOES link a theme still gets it: the import sits earlier in the cascade than
// any <link>. Href is still a constant off the allowlist below and can never be
// a name the visitor chose.
//
// SECURITY, the whole reason this file exists as its own unit:
// the cookie value is attacker-controlled and ends up inside a <link href> and
// a data-theme attribute. It is therefore NEVER interpolated into a path. An
// incoming name is matched against the allowlist below and what gets emitted is
// the CONSTANT Href of the matching entry; anything else — "../../etc/passwd",
// `x" onload=alert(1)`, a 4 KB blob, "cosmic-void/../nord" — falls through to
// the default. There is no code path in this program that builds a stylesheet
// URL out of request input, which is what makes traversal and attribute-escape
// impossible rather than merely filtered.
const (
	// themeCookieName is deliberately explicit: it shares a domain with the
	// session ("mysession") and the CSRF cookie ("csrf_tok").
	themeCookieName = "loon_theme"
	// themeCookieMaxAge is a year. A theme is a preference, not a session:
	// losing it on browser restart would be the bug.
	themeCookieMaxAge = 365 * 24 * 60 * 60
	// themeRoute is the switcher's form target. Namespaced under /settings
	// because it is a per-VIEWER preference; /admin/settings is site config.
	themeRoute = "/settings/theme"
	// themeFormField / themeNextField are the POST inputs. The form also
	// carries the usual hidden _csrf (csrfMiddleware gates every POST).
	themeFormField = "theme"
	themeNextField = "next"
)

// themeOption is one selectable theme. Href is a CONSTANT here — the switcher
// echoes Key back to the POST route, and the only thing that ever reaches the
// page is whichever of these literals matched.
type themeOption struct {
	Key   string // stable slug: the cookie value and the <option value>
	Label string // human label for the switcher UI
	Href  string // stylesheet path — a literal, never assembled from input
}

// siteThemes is the allowlist AND the menu order. The FIRST entry is the
// default: it is what an absent, unknown or hostile cookie resolves to.
//
// Every theme, including the default, loads a file, so switching is symmetrical
// (there is no "unset" state that behaves differently from a chosen one). Each
// file is nothing but a :root block — it must stay that way, since layout.css,
// components.css and theme.css all load after it and would win any rule it
// tried to carry. Those three are theme-independent by construction, exactly as
// UNIT3D's sass/themes/ are. All three files declare an identical token set:
// a token present in only some of them is a silent fallback-to-nothing in the
// rest, so a new token goes in every theme (or in tokens.css if the value is
// the same everywhere).
// Href is spelled out per entry rather than derived from Key so that grepping
// for the emitted URL finds it, and so no path-building code exists at all.
var siteThemes = []themeOption{
	{Key: "cosmic-void", Label: "Cosmic Void", Href: "/static/css/themes/cosmic-void.css"},
	{Key: "nord", Label: "Nord", Href: "/static/css/themes/nord.css"},
	{Key: "midnight", Label: "Midnight", Href: "/static/css/themes/midnight.css"},
}

// defaultTheme is siteThemes[0]. Kept as a function rather than a var so the
// allowlist stays the single source of truth.
func defaultTheme() themeOption { return siteThemes[0] }

// themeByName resolves an untrusted name to an allowlisted theme, falling back
// to the default on ANY mismatch. Exact match only: no trimming, no case
// folding, no prefix logic — every one of those is a place a bypass hides, and
// the switcher only ever submits values this program printed.
func themeByName(name string) themeOption {
	for _, t := range siteThemes {
		if t.Key == name {
			return t
		}
	}
	return defaultTheme()
}

// currentTheme is the theme for this request: the cookie when it names an
// allowlisted theme, the default otherwise. A malformed cookie header, an
// absent cookie and a hostile value are all the same case on purpose.
func currentTheme(c *gin.Context) themeOption {
	if c == nil || c.Request == nil {
		return defaultTheme()
	}
	name, err := c.Cookie(themeCookieName)
	if err != nil {
		return defaultTheme()
	}
	return themeByName(name)
}

// setTheme is POST /settings/theme: the switcher's form target. CSRF is
// enforced by the global csrfMiddleware (the form carries the hidden _csrf
// every other POST in this demo carries), so this handler only has to decide
// the cookie and where to send the browser back to.
func (w *web) setTheme(c *gin.Context) {
	// The cookie is written from the ALLOWLIST entry, not from the form value.
	theme := themeByName(c.PostForm(themeFormField))
	// Lax: the cookie must survive a normal top-level navigation back to the
	// site (that is the entire point of a persisted preference), and it carries
	// nothing worth protecting cross-site beyond that. HttpOnly is off
	// deliberately — the value is a public preference with no security meaning,
	// and leaving it readable lets a progressive-enhancement switcher preview a
	// theme client-side without a round trip. Secure follows the session's own
	// SECURE_COOKIES switch so a TLS deployment marks all three cookies alike.
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(themeCookieName, theme.Key, themeCookieMaxAge, "/", "",
		os.Getenv("SECURE_COOKIES") == "1", false)
	c.Redirect(http.StatusSeeOther, backLink(c, c.PostForm(themeNextField)))
}

// backLink resolves "send the user back where they came from" to a path on THIS
// site. It tries the form's explicit next field first, then the Referer, then
// gives up and returns "/". Anything that is not an unambiguous same-origin
// path is refused rather than sanitised — an open redirect out of a
// preference form is a phishing primitive, and there is no value in salvaging a
// hostile target.
func backLink(c *gin.Context, want string) string {
	if p, ok := sameOriginPath(want, c.Request.Host); ok {
		return p
	}
	if p, ok := sameOriginPath(c.Request.Referer(), c.Request.Host); ok {
		return p
	}
	return "/"
}

// sameOriginPath accepts either a root-relative path ("/browse?cat=5") or an
// absolute URL whose host matches this request's, and returns the path+query to
// redirect to. Everything else is rejected:
//
//	https://evil.example/x   → different host
//	//evil.example/x         → protocol-relative; url.Parse hands it a Host
//	/\evil.example           → browsers may read the backslash as a second slash
//	javascript:alert(1)      → scheme with no host
//	browse                   → not rooted; would resolve relative to the referer
//
// The fragment is dropped (it never reaches the server anyway).
func sameOriginPath(raw, host string) (string, bool) {
	// Control characters are refused BEFORE the trim: trimming a leading "\n"
	// and then accepting what is left would be repairing hostile input, and a
	// redirect target is the last place to be clever. (Go's header writer
	// would neutralise a CRLF anyway; this keeps the decision here.)
	if strings.ContainsFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if u.Scheme != "" || u.Host != "" || u.Opaque != "" {
		if u.Host == "" || u.Host != host {
			return "", false
		}
		if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
			return "", false
		}
	}
	// Test the DECODED path for the structural traps ("/%2f%2fevil.example"
	// decodes to "//evil.example"), but emit the escaped form.
	if !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") || strings.HasPrefix(u.Path, `/\`) {
		return "", false
	}
	p := u.EscapedPath()
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p, true
}
