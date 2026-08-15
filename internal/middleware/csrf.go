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
		// /api, /rss authenticate by api_key (no session cookie). And
		// /admin/jobs/config is rendered by loon/schedule's built-in handler,
		// which doesn't embed the host CSRF token — a production host (see the
		// prod indexer's AdminHandler.JobConfig) uses its own config handler
		// that carries it. Exempt it rather than fork the framework's template.
		if p := c.FullPath(); p == "/api" || p == "/rss" || p == "/admin/jobs/config" {
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
