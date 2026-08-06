package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-baseline/session"
	"github.com/the-loon-clan/loon-baseline/webauth"
)

// The theme cookie is attacker-controlled and its resolution decides a URL the
// browser then fetches, so these tests are the security contract, not polish:
// nothing outside siteThemes may ever reach a page, and the switcher must not
// become an open redirect.

// TestThemeAllowlistShape pins the properties the rest of the file relies on.
func TestThemeAllowlistShape(t *testing.T) {
	if len(siteThemes) < 2 {
		t.Fatal("a switcher needs at least two themes")
	}
	if defaultTheme() != siteThemes[0] {
		t.Error("defaultTheme must be the first allowlist entry")
	}
	if defaultTheme().Key != "cosmic-void" {
		t.Errorf("default theme is %q, want cosmic-void", defaultTheme().Key)
	}
	seen := map[string]bool{}
	for _, th := range siteThemes {
		if seen[th.Key] {
			t.Errorf("duplicate theme key %q — themeByName would be ambiguous", th.Key)
		}
		seen[th.Key] = true
		if th.Label == "" {
			t.Errorf("%s: no label for the switcher", th.Key)
		}
		// Href is emitted into a <link href> as a whole attribute value. It is
		// a literal in theme.go, and these assertions are what keeps it one.
		if !strings.HasPrefix(th.Href, "/static/css/themes/") || !strings.HasSuffix(th.Href, ".css") {
			t.Errorf("%s: Href %q is not a rooted /static/css/themes/*.css path", th.Key, th.Href)
		}
		if strings.ContainsAny(th.Href, "\"'<>` \\") || strings.Contains(th.Href, "..") {
			t.Errorf("%s: Href %q contains a character that must never reach an attribute", th.Key, th.Href)
		}
	}
}

// TestThemeByNameRejectsEverythingElse is the core guard: any value that is not
// an exact allowlist key resolves to the default, so no traversal, no scheme,
// no quote and no near-miss can ever select a stylesheet.
func TestThemeByNameRejectsEverythingElse(t *testing.T) {
	for _, th := range siteThemes {
		if got := themeByName(th.Key); got != th {
			t.Errorf("themeByName(%q) = %+v, want the matching entry", th.Key, got)
		}
	}
	hostile := []string{
		"", " ", "COSMIC-VOID", "cosmic-void ", " nord", "nordic",
		"../../etc/passwd", "../tokens", "cosmic-void/../nord",
		"nord.css", "/static/css/themes/nord.css",
		`x" onload="alert(1)`, `nord"><script>alert(1)</script>`,
		"https://evil.example/x.css", "//evil.example/x.css",
		"javascript:alert(1)", "nord\n", "nord\x00", strings.Repeat("a", 5000),
	}
	for _, in := range hostile {
		got := themeByName(in)
		if got != defaultTheme() {
			t.Errorf("themeByName(%q) = %+v, want the default", in, got)
		}
		// Belt and braces: whatever came back is an entry OF THE TABLE, so the
		// emitted URL is a literal no matter what was sent.
		known := false
		for _, th := range siteThemes {
			if got == th {
				known = true
			}
		}
		if !known {
			t.Errorf("themeByName(%q) = %+v, which is not an allowlist entry", in, got)
		}
	}
}

// TestSameOriginPath is the open-redirect table. The switcher posts a "next"
// field and falls back to the Referer — both are visitor-controlled.
func TestSameOriginPath(t *testing.T) {
	const host = "demo.example:8090"
	ok := map[string]string{
		"/":                                   "/",
		"/browse":                             "/browse",
		"/browse?cat=5000":                    "/browse?cat=5000",
		"/u/alice":                            "/u/alice",
		"/community/forums#x":                 "/community/forums", // fragment dropped
		"http://demo.example:8090/search?q=a": "/search?q=a",
	}
	for in, want := range ok {
		got, allowed := sameOriginPath(in, host)
		if !allowed || got != want {
			t.Errorf("sameOriginPath(%q) = %q,%v — want %q,true", in, got, allowed, want)
		}
	}
	bad := []string{
		"", "   ", "browse", "browse/x", // not rooted: would resolve off the referer
		"//evil.example/x",       // protocol-relative
		"/\\evil.example",        // backslash: some browsers read it as //
		"/%2f%2fevil.example",    // encoded protocol-relative
		"https://evil.example/x", // other host
		"http://demo.example/x",  // same name, different port == different origin
		"javascript:alert(1)",    // scheme, no host
		"mailto:a@example.invalid",
		"http://user:pw@evil.example/", // credentials-in-URL confusion
		"\n/browse",
	}
	for _, in := range bad {
		if got, allowed := sameOriginPath(in, host); allowed {
			t.Errorf("sameOriginPath(%q) allowed %q — that is an open redirect", in, got)
		}
	}
}

// themeEngine mounts just the switcher route. setTheme touches no session, so
// this needs no auth middleware; CSRF is the global middleware's job and is
// deliberately not in the way of asserting cookie + redirect behaviour.
func themeEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	w := &web{}
	e.POST(themeRoute, w.setTheme)
	return e
}

func postTheme(t *testing.T, form url.Values, referer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, themeRoute, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "demo.example:8090"
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	rec := httptest.NewRecorder()
	themeEngine().ServeHTTP(rec, req)
	return rec
}

func themeCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		if c.Name == themeCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", themeCookieName, rec.Header())
	return nil
}

func TestSetThemeWritesAnAllowlistedCookie(t *testing.T) {
	rec := postTheme(t, url.Values{themeFormField: {"nord"}, themeNextField: {"/browse?cat=5000"}}, "")
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/browse?cat=5000" {
		t.Errorf("Location %q, want the posted same-origin path", loc)
	}
	ck := themeCookie(t, rec)
	if ck.Value != "nord" {
		t.Errorf("cookie value %q, want nord", ck.Value)
	}
	if ck.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", ck.SameSite)
	}
	if ck.Path != "/" {
		t.Errorf("cookie path %q, want /", ck.Path)
	}
	if ck.MaxAge < 30*24*60*60 {
		t.Errorf("MaxAge %d is not long-lived — a preference must survive a restart", ck.MaxAge)
	}
	if ck.HttpOnly {
		t.Error("HttpOnly is set; the theme is a public preference a client-side switcher may read")
	}
}

// A hostile theme name must still produce a VALID cookie — the default — not an
// echo of the input and not a 400 that leaves the visitor stuck.
func TestSetThemeFallsBackForHostileInput(t *testing.T) {
	for _, in := range []string{"../../etc/passwd", `x" onload=alert(1)`, "https://evil.example/x.css", ""} {
		rec := postTheme(t, url.Values{themeFormField: {in}}, "")
		if ck := themeCookie(t, rec); ck.Value != defaultTheme().Key {
			t.Errorf("theme=%q wrote cookie %q, want %q", in, ck.Value, defaultTheme().Key)
		}
	}
}

// The redirect target is the other attacker-controlled input on this route.
func TestSetThemeRedirectIsSameOriginOnly(t *testing.T) {
	cases := []struct {
		name, next, referer, want string
	}{
		{"next wins", "/groups", "http://demo.example:8090/browse", "/groups"},
		{"referer fallback", "", "http://demo.example:8090/browse", "/browse"},
		{"offsite next falls back to referer", "https://evil.example/x", "http://demo.example:8090/browse", "/browse"},
		{"offsite both", "https://evil.example/x", "https://evil.example/y", "/"},
		{"protocol relative", "//evil.example/x", "", "/"},
		{"nothing at all", "", "", "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{themeFormField: {"nord"}}
			if tc.next != "" {
				form.Set(themeNextField, tc.next)
			}
			rec := postTheme(t, form, tc.referer)
			if loc := rec.Header().Get("Location"); loc != tc.want {
				t.Errorf("Location %q, want %q", loc, tc.want)
			}
		})
	}
}

// ── chrome parity ───────────────────────────────────────────────────

// TestChromeDataKeys is the anti-drift guard the forum bug earned: the shared
// chrome reads these keys unguarded, so every one of them must be present on
// EVERY render. Both render() and forum.Deps.BaseData go through chromeData, so
// pinning it here pins both — the failure it replaces was a forum page quietly
// rendering 13 nav links where the home page rendered 25.
func TestChromeDataKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := &web{}
	w.auth = webauth.Auth{Session: session.Config{Secret: []byte("test-secret-test-secret-abc")}}

	var got map[string]any
	e := gin.New()
	e.Use(w.auth.Session.Middleware())
	e.GET("/probe", func(c *gin.Context) {
		got = w.chromeData(c, map[string]any{"Title": "Probe"})
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/probe?cat=5000", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	for _, k := range []string{
		"User", "IsAdmin", "IsMod", "CSRFToken", "Path", "PathQuery",
		"AdminNav", "SiteNav", "SiteNavGroup", "SiteNavAccount", "Theme", "Themes",
	} {
		if _, ok := got[k]; !ok {
			t.Errorf("chromeData did not set %q — the shared chrome reads it unguarded", k)
		}
	}
	if got["Title"] != "Probe" {
		t.Error("chromeData clobbered a caller's own key")
	}
	// Path stays query-FREE: every nav active-state comparison matches it
	// against a literal href, so a query would unhighlight the current tab.
	if got["Path"] != "/probe" {
		t.Errorf("Path = %v, want the request path with no query", got["Path"])
	}
	// PathQuery keeps it: it is the theme switcher's next field, and backLink()
	// prefers next over the Referer, so a lossy value here is what strands a
	// viewer who switched themes on /search?q=foo at /search.
	if got["PathQuery"] != "/probe?cat=5000" {
		t.Errorf("PathQuery = %v, want path+query", got["PathQuery"])
	}
	// The optional per-viewer keys must stay ABSENT rather than zero for a
	// logged-out viewer: a "0 points" tile for nobody is fabricated data.
	for _, k := range []string{"Points", "HasPoints", "Unread", "HasUnread", "RoleLabel", "MemberSince", "EmailUnverified"} {
		if _, ok := got[k]; ok {
			t.Errorf("chromeData set %q for an anonymous viewer", k)
		}
	}
	if got["Theme"] != any(defaultTheme()) {
		t.Errorf("Theme = %v, want the default for a request with no cookie", got["Theme"])
	}
}

// A hostile theme cookie must resolve to the default before it reaches a page.
func TestChromeDataThemeFromCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := &web{}
	w.auth = webauth.Auth{Session: session.Config{Secret: []byte("test-secret-test-secret-abc")}}

	probe := func(cookie string) themeOption {
		var out themeOption
		e := gin.New()
		e.Use(w.auth.Session.Middleware())
		e.GET("/probe", func(c *gin.Context) {
			out, _ = w.chromeData(c, nil)["Theme"].(themeOption)
			c.Status(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: themeCookieName, Value: cookie})
		}
		e.ServeHTTP(httptest.NewRecorder(), req)
		return out
	}

	if got := probe("nord"); got.Key != "nord" {
		t.Errorf("a valid cookie resolved to %q", got.Key)
	}
	for _, hostile := range []string{"../../etc/passwd", `x" onload=alert(1)`, "unknown-theme"} {
		if got := probe(hostile); got != defaultTheme() {
			t.Errorf("cookie %q resolved to %+v, want the default", hostile, got)
		}
	}
}
