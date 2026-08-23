package middleware

// Content-Security-Policy, plus the two other headers that belong with it.
//
// The site sent none of these. htmx is what made it worth writing now — an
// injected attribute is an instruction once htmx is on the page, where before
// it was inert markup — but every directive here would have been worth having
// beforehand, and none of them is htmx-specific.
//
// # What this policy can and cannot be
//
// script-src carries 'unsafe-inline', and that is a measured concession rather
// than an oversight. Nonces are the correct answer and they are not available
// here: there are 4 inline <script> blocks in the host's own templates and
// 35 more across the plugin templates, and plugins are a published contract
// rendered into these pages by hosts this repository does not control. A
// nonce-based policy would silently break every one of them.
//
// So the honest framing is that this policy does not stop XSS — the sanitizer
// does that (internal/sanitize, allowlisted attributes, proven by its tests),
// and html/template's contextual escaping does the rest. What it stops is the
// class of attack that works even when no script executes:
//
//	frame-ancestors 'none'   clickjacking: the site cannot be framed
//	form-action 'self'       a form whose action was rewritten to an
//	                         attacker's host cannot post the viewer's
//	                         session or their password to it
//	base-uri 'self'          an injected <base href> cannot silently
//	                         re-point every relative URL on the page,
//	                         including the src of every script
//	object-src 'none'        no <object>/<embed>, which is a plugin-era
//	                         execution path nothing here uses
//	default-src 'self'       no external script, font, frame or connect
//	                         origin — the same-origin restriction htmx
//	                         applies to its own requests, applied to
//	                         everything else on the page
//
// # The nonce, and why the objection above no longer holds
//
// Everything above was written when a plugin's inline <script> was beyond the
// host's reach: a fragment arrived as finished HTML and went into the page
// untouched, so a nonce policy really would have blanked every plugin page.
//
// The host rewrites fragments now. It already lifts their <style> blocks into
// the head (handlers/fragmentstyles.go); adding a nonce to their inline
// <script> tags is the same seam, and the plugin never learns the nonce exists
// — which is the property that makes this safe for a published contract.
//
// So script-src carries a per-request nonce and no longer carries
// 'unsafe-inline'. A browser that supports nonces ignores 'unsafe-inline'
// anyway when one is present, so keeping it would buy nothing but the
// appearance of the concession still being here.
//
// What this does NOT do is make the sanitizer redundant. An injected script
// still has no nonce and will not run, which is new; but the sanitizer is
// still what stops injected markup existing in the first place, and CSP is the
// second line, not the first.
//
// img-src stays permissive. User prose may legitimately embed a remote image —
// internal/sanitize allows an https src through safeURL — and an image cannot
// execute. Every image the host itself renders is already local (covers are
// proxied), so this exists for user content alone.

import (
	"crypto/rand"
	"encoding/base64"
	"strings"

	"github.com/gin-gonic/gin"
)

// contentSecurityPolicy is assembled from named parts so each one can carry the
// reason it is there. Joined once at init rather than per request.
var contentSecurityPolicy = strings.Join([]string{
	"default-src 'self'",
	// See the file comment: 'unsafe-inline' is required by 39 inline blocks
	// across the host and the plugin ecosystem.
	// script-src is completed per request with the nonce; see nonceKey and
	// SecurityHeaders. It is NOT in this list, because this list is joined
	// once at init and a nonce that never changes is not a nonce.
	// (script-src goes here, per request)
	// The FALLBACK, and it is now doing one job only: covering inline style=""
	// ATTRIBUTES, of which this tree has 1,647 and 98% are static. Removing
	// those is a large sweep for another day; an inline style cannot reach the
	// network or execute, which is why it was the smaller concession all along.
	"style-src 'self' 'unsafe-inline'",
	// ELEMENTS are locked down, and this is the half that just became
	// possible. Every plugin stylesheet moved out of its fragment and is served
	// from /pluginstyle/<name>.css (BACKLOG #13), and the last runtime
	// document.write('<style>…') went with it, so nothing on this site creates
	// a style element any more.
	//
	// CSP3 lets the two be split: style-src-elem governs <style> and
	// <link rel=stylesheet>, while attributes keep falling back to style-src
	// above. So this closes the door the migration emptied WITHOUT waiting on
	// the 1,647 attributes.
	//
	// It also makes the migration self-enforcing. A plugin that ships CSS in a
	// fragment again gets it hoisted to the head by fragmentstyles.go and then
	// BLOCKED here — an unstyled page, which somebody notices, rather than a
	// silent return to inline styling.
	"style-src-elem 'self'",
	"img-src 'self' data: https:",
	"font-src 'self' data:",
	// XHR/fetch/htmx targets. Same-origin, matching htmx's own
	// selfRequestsOnly default rather than relying on it alone.
	"connect-src 'self'",
	"object-src 'none'",
	"base-uri 'self'",
	"form-action 'self'",
	"frame-ancestors 'none'",
}, "; ")

// SecurityHeaders sets CSP and the two headers that answer questions CSP does
// not.
//
// Applied to every response including /static: a policy that covers only the
// HTML is a policy an attacker reads as a map of where it is not.
// nonceKey is where the request's script nonce lives on the gin context.
//
// Exported through Nonce() rather than as a raw key: a template needs the
// value and nothing else needs to know where it is kept.
const nonceKey = "csp_script_nonce"

// Nonce returns this request's script nonce, or "" outside a request that has
// one. A template writing nonce="" is a script that will not run, which is the
// safe direction to fail.
func Nonce(c *gin.Context) string {
	if c == nil {
		return ""
	}
	v, _ := c.Get(nonceKey)
	s, _ := v.(string)
	return s
}

// newNonce is 16 bytes of crypto/rand, base64. Per REQUEST, never per process:
// a nonce reused across responses is a value an attacker can read from one page
// and replay into an injection on the next, which is the whole attack it
// exists to prevent.
func newNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// No nonce rather than a predictable one. Every inline script on the
		// page then fails to run, which is loud, and the alternative is a
		// policy that looks enforced and is not.
		return ""
	}
	// URL-safe alphabet, and that is not cosmetic. Standard base64 yields
	// '+' and '/', and html/template escapes '+' to &#43; inside an
	// attribute — so the tag carried a nonce that did not match the header
	// byte for byte. Browsers decode the entity before comparing, so it
	// worked; it also made every check of the two look like a mismatch.
	// [A-Za-z0-9_-] survives escaping untouched.
	return base64.RawURLEncoding.EncodeToString(b)
}

func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		n := newNonce()
		c.Set(nonceKey, n)
		script := "script-src 'self'"
		if n != "" {
			script += " 'nonce-" + n + "'"
		}
		h.Set("Content-Security-Policy", script+"; "+contentSecurityPolicy)
		// Content sniffing turns a user-uploaded file the server labelled
		// text/plain into whatever the browser decides it looks like. This site
		// takes avatar uploads, so that path exists.
		h.Set("X-Content-Type-Options", "nosniff")
		// Referrer: send the origin cross-site, the full URL same-site. A
		// members-only page's full path can name a release, a profile or a
		// moderation queue item, and none of that belongs in a third party's
		// logs because somebody followed a link in a forum post.
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Next()
	}
}
