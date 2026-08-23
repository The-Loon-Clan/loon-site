package middleware

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The security headers, and the directives whose absence would be silent.
//
// A Content-Security-Policy is uniquely bad at failing loudly. A malformed one
// is treated by browsers as no policy at all, a missing directive falls back to
// default-src without saying so, and in all three cases the response still
// carries a header that looks like protection. Nothing in a test suite or a
// browser will mention it. So these assert the specific directives that were
// chosen for specific reasons, by name.

func serveWithHeaders(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(SecurityHeaders())
	e.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec
}

func TestTheSecurityHeadersAreSet(t *testing.T) {
	rec := serveWithHeaders(t)
	for _, h := range []string{
		"Content-Security-Policy",
		"X-Content-Type-Options",
		"Referrer-Policy",
	} {
		if rec.Header().Get(h) == "" {
			t.Errorf("%s is not set", h)
		}
	}
}

// The directives that do real work without blocking scripts. Each one is named
// here because each one stops an attack that does not require executing a
// script — see csp.go.
func TestTheDirectivesThatWorkWithoutBlockingScripts(t *testing.T) {
	policy := serveWithHeaders(t).Header().Get("Content-Security-Policy")

	for _, want := range []struct {
		directive string
		stops     string
	}{
		{"frame-ancestors 'none'", "clickjacking — the site being framed"},
		{"form-action 'self'", "a rewritten form action posting a password off-site"},
		{"base-uri 'self'", "an injected <base> re-pointing every relative URL"},
		{"object-src 'none'", "<object>/<embed> execution paths nothing here uses"},
		{"default-src 'self'", "external script, font, frame and connect origins"},
		{"connect-src 'self'", "htmx and fetch reaching a third-party host"},
	} {
		if !strings.Contains(policy, want.directive) {
			t.Errorf("policy is missing %q\n  which is what stops: %s\n  got: %s",
				want.directive, want.stops, policy)
		}
	}
}

// 'unsafe-inline' must stay OFF everything but script-src and style-src.
//
// The concession is specific and documented (39 inline blocks across the host
// and plugins). Its danger is that it reads as a general-purpose fix for a
// blocked resource, so the next person to hit a CSP error is one paste away
// from putting it in default-src, where it would silence the whole policy.
func TestUnsafeInlineIsConfinedToStyleSrc(t *testing.T) {
	policy := serveWithHeaders(t).Header().Get("Content-Security-Policy")
	for _, directive := range strings.Split(policy, ";") {
		directive = strings.TrimSpace(directive)
		if !strings.Contains(directive, "'unsafe-inline'") {
			continue
		}
		name, _, _ := strings.Cut(directive, " ")
		if name != "style-src" {
			t.Errorf("'unsafe-inline' has spread to %q — see csp.go. It is confined "+
				"to style-src, which covers inline style ATTRIBUTES; script-src "+
				"carries a nonce instead and style-src-elem carries neither", name)
		}
	}
	// 'unsafe-eval' was never needed: htmx runs with allowEval false.
	if strings.Contains(policy, "'unsafe-eval'") {
		t.Error("policy allows 'unsafe-eval'; htmx is configured with allowEval:false " +
			"and nothing else here needs it")
	}
}

// Every directive must have at least one source. "script-src;" is valid syntax
// that means "block everything", and an accidentally empty one would take the
// site down in a way that looks like a caching problem.
func TestNoDirectiveIsEmpty(t *testing.T) {
	policy := serveWithHeaders(t).Header().Get("Content-Security-Policy")
	for _, directive := range strings.Split(policy, ";") {
		directive = strings.TrimSpace(directive)
		if directive == "" {
			t.Errorf("policy has an empty directive (a stray ';'): %q", policy)
			continue
		}
		if len(strings.Fields(directive)) < 2 {
			t.Errorf("directive %q has no sources, which means 'block everything'", directive)
		}
	}
}

// The headers must be on every response, not only on the ones that render a
// page. An error path is exactly where a browser is most likely to be shown
// content somebody else influenced.
func TestHeadersAreSetOnErrorsToo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(SecurityHeaders())
	e.GET("/boom", func(c *gin.Context) { c.String(http.StatusInternalServerError, "boom") })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("no CSP on a 500 response")
	}
	// A 404 never reaches a handler at all — it is gin's own, so the middleware
	// has to have run before routing for this to hold.
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nothing-here", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("no CSP on a 404 response")
	}
}

// The nonce is the whole reason script-src can stop carrying 'unsafe-inline',
// so these check the two properties it depends on.
func TestScriptSrcCarriesANonceAndNotUnsafeInline(t *testing.T) {
	policy := serveWithHeaders(t).Header().Get("Content-Security-Policy")
	var script string
	for _, d := range strings.Split(policy, ";") {
		if strings.HasPrefix(strings.TrimSpace(d), "script-src ") {
			script = strings.TrimSpace(d)
		}
	}
	if script == "" {
		t.Fatal("no script-src directive at all")
	}
	if strings.Contains(script, "'unsafe-inline'") {
		t.Errorf("script-src still carries 'unsafe-inline': %s", script)
	}
	if !strings.Contains(script, "'nonce-") {
		t.Errorf("script-src carries no nonce, so every inline script is blocked: %s", script)
	}
}

// A nonce reused across responses is a value an attacker reads from one page
// and replays into an injection on the next. That is the attack it exists to
// prevent, so the test is not ceremony.
func TestTheNonceIsDifferentEveryRequest(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		policy := serveWithHeaders(t).Header().Get("Content-Security-Policy")
		m := regexp.MustCompile(`'nonce-([^']+)'`).FindStringSubmatch(policy)
		if m == nil {
			t.Fatalf("request %d: no nonce in %q", i, policy)
		}
		if seen[m[1]] {
			t.Fatalf("nonce %q was reused", m[1])
		}
		seen[m[1]] = true
	}
}
