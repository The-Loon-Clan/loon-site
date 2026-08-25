// Package middleware holds the gin middleware this site adds of its own.
//
// One thing so far: the CSRF gate. It sits here rather than in web/handlers
// because it is decided entirely by the request — no view models, no store, no
// knowledge of what any route does — and because a package with no reason to
// import the handlers cannot accidentally acquire one.
package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

// A minimal double-submit CSRF guard — the pattern the production indexer runs
// (pkg/middleware/csrf.go), shown here so a site built on this reference gets
// it too. SameSite=Lax already strips the session cookie from cross-site POSTs;
// this covers what Lax does not (a same-site content-injection foothold, or a
// deployment that downgrades SameSite): mint a per-session token, expose it to
// templates (gin context) + a JS-readable cookie, and require it in the "_csrf"
// form field or "X-CSRF-Token" header on every state-changing method.
const (
	csrfSessionKey = "csrf_token"
	csrfCookieName = "csrf_tok"
	csrfContextKey = "csrf_token"
)

// CSRF must run AFTER the session middleware (it reads/writes the
// session) and before the routes. The keyed Newznab API (/api, /rss)
// authenticates by api_key, not the session cookie, so the CSRF gate is both
// irrelevant and harmful there and is skipped.
func CSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		// /api, /rss authenticate by api_key (no session cookie).
		//
		// /admin/jobs/config USED TO BE HERE, and it was the one exemption that
		// was not an API route: a browser-rendered admin form, exempted because
		// loon/schedule's bundled handler could not embed a host token and
		// forking the framework's template was the alternative. schedule now
		// reads the token off the gin context (schedule.CSRFContextKey, which
		// is the key this middleware already sets below), so the form carries
		// one and the route is gated like every other.
		//
		// /api/downloads/report is the same case one step further: it is a POST
		// made by a script running inside a member's SABnzbd or NZBGet, which
		// has no browser, no session and no page to have been issued a token
		// on. It carries the member's API key like the two above, and the
		// plugin refuses outright when no resolver is registered — so the
		// credential is real, it is simply not a cookie.
		//
		// Listed by exact path rather than a /api prefix: a prefix would exempt
		// every future route under it, including one somebody adds for a
		// browser form, and that is the kind of blanket rule this file exists
		// to avoid.
		if p := c.FullPath(); p == "/api" || p == "/rss" ||
			p == "/api/downloads/report" ||
			// /api/agent/report is the same shape: a fleet agent POSTs its
			// state authenticated by a bearer token, no session cookie, so a
			// CSRF token it could not have is irrelevant. See agentapi_web.go.
			p == "/api/agent/report" {
			c.Next()
			return
		}
		// The dev UI inspector's focus save (uiinspect_web.go). Exempt for the
		// same reason as the routes above — its page is standalone and carries
		// no session-issued token — and safe for a reason none of them have:
		// the whole /dev tree is only REGISTERED when LOON_UI_INSPECT is
		// set, so on any build that does not ask for it this path resolves to
		// nothing and there is no handler to reach.
		if c.FullPath() == "/dev/focus" {
			c.Next()
			return
		}
		sess := sessions.Default(c)
		token, _ := sess.Get(csrfSessionKey).(string)
		if token == "" {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			token = base64.RawURLEncoding.EncodeToString(b)
			sess.Set(csrfSessionKey, token)
			//nolint:errcheck // a token that was not stored fails the next POST visibly
			_ = sess.Save()
		}
		c.Set(csrfContextKey, token)
		// JS-readable (HttpOnly false) so a fetch/XHR caller can echo it in the
		// X-CSRF-Token header; the session cookie itself stays HttpOnly.
		c.SetCookie(csrfCookieName, token, 0, "/", "", false, false)

		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			got := strings.TrimSpace(c.PostForm("_csrf"))
			if got == "" {
				got = c.GetHeader("X-CSRF-Token")
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				c.String(http.StatusForbidden, "invalid or missing CSRF token — reload the page and try again")
				c.Abort()
				return
			}
		}
		c.Next()
	}
}

// Token returns the per-session token the middleware stashed in context, for
// templates to embed as the hidden _csrf field.
func Token(c *gin.Context) string {
	t, _ := c.Get(csrfContextKey)
	s, _ := t.(string)
	return s
}
