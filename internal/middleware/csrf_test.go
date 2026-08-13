package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

// The CSRF gate had no tests at all — this package was at 0% coverage while
// being the thing that stands between a member's session and a form submitted
// by somebody else's page.
//
// Each test below is a property the middleware is supposed to have, written so
// that removing the corresponding line of csrf.go fails it. A test that cannot
// fail is decoration.

func newEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(sessions.Sessions("mysession", cookie.NewStore([]byte("test-secret-test-secret-abcd"))))
	e.Use(CSRF())
	e.GET("/form", func(c *gin.Context) { c.String(http.StatusOK, Token(c)) })
	e.POST("/save", func(c *gin.Context) { c.String(http.StatusOK, "saved") })
	e.PUT("/put", func(c *gin.Context) { c.String(http.StatusOK, "put") })
	e.DELETE("/del", func(c *gin.Context) { c.String(http.StatusOK, "deleted") })
	e.POST("/api", func(c *gin.Context) { c.String(http.StatusOK, "api") })
	e.POST("/dev/focus", func(c *gin.Context) { c.String(http.StatusOK, "focus") })
	return e
}

// mint performs the GET a browser would, returning the token and the cookies
// that go with it. The pair matters: a token without its session is not a
// token, which is the property the whole scheme rests on.
func mint(t *testing.T, e *gin.Engine) (string, []*http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/form", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /form = %d", rec.Code)
	}
	tok := strings.TrimSpace(rec.Body.String())
	if tok == "" {
		t.Fatal("no token was minted for a plain GET")
	}
	return tok, rec.Result().Cookies()
}

func post(e *gin.Engine, path, token string, cookies []*http.Cookie, header bool) *httptest.ResponseRecorder {
	var req *http.Request
	if header {
		req = httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("X-CSRF-Token", token)
	} else {
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader("_csrf="+token))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestGetIsNeverBlocked(t *testing.T) {
	// A read must never need a token: requiring one would make every link into
	// a state-changing request in the eyes of the middleware.
	e := newEngine(t)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/form", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET = %d, want 200", rec.Code)
	}
}

func TestPostWithTheMintedTokenPasses(t *testing.T) {
	e := newEngine(t)
	tok, cookies := mint(t, e)
	if rec := post(e, "/save", tok, cookies, false); rec.Code != http.StatusOK {
		t.Errorf("POST with a valid form token = %d, want 200 (%s)", rec.Code, rec.Body)
	}
}

func TestPostViaTheHeaderPasses(t *testing.T) {
	// fetch() cannot send a form field, so the header is how a JS caller
	// participates at all. Dropping it would silently break every such call.
	e := newEngine(t)
	tok, cookies := mint(t, e)
	if rec := post(e, "/save", tok, cookies, true); rec.Code != http.StatusOK {
		t.Errorf("POST with X-CSRF-Token = %d, want 200", rec.Code)
	}
}

func TestPostWithoutATokenIsRefused(t *testing.T) {
	e := newEngine(t)
	_, cookies := mint(t, e)
	if rec := post(e, "/save", "", cookies, false); rec.Code != http.StatusForbidden {
		t.Errorf("POST with no token = %d, want 403", rec.Code)
	}
}

func TestPostWithAForgedTokenIsRefused(t *testing.T) {
	// The attack this exists for: the other page can guess the field name, but
	// not the value.
	e := newEngine(t)
	_, cookies := mint(t, e)
	if rec := post(e, "/save", "not-the-real-token", cookies, false); rec.Code != http.StatusForbidden {
		t.Errorf("POST with a forged token = %d, want 403", rec.Code)
	}
}

func TestATokenFromAnotherSessionIsRefused(t *testing.T) {
	// The heart of double-submit: a token is only valid WITH the session it was
	// minted for. Comparing the field against the cookie alone would pass here,
	// and would be forgeable by anyone who can set a cookie.
	e := newEngine(t)
	stolen, _ := mint(t, e) // token from session A
	_, mine := mint(t, e)   // cookies from session B
	if rec := post(e, "/save", stolen, mine, false); rec.Code != http.StatusForbidden {
		t.Errorf("another session's token = %d, want 403", rec.Code)
	}
}

func TestEveryStateChangingMethodIsGated(t *testing.T) {
	// POST is the one people remember. A route that accepts PUT or DELETE is
	// just as state-changing and just as reachable from another origin.
	e := newEngine(t)
	_, cookies := mint(t, e)
	for _, m := range []struct{ method, path string }{
		{http.MethodPut, "/put"},
		{http.MethodDelete, "/del"},
	} {
		req := httptest.NewRequest(m.method, m.path, nil)
		for _, ck := range cookies {
			req.AddCookie(ck)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with no token = %d, want 403", m.method, m.path, rec.Code)
		}
	}
}

func TestTheKeyedAPIIsExempt(t *testing.T) {
	// /api and /rss authenticate by api_key, not by the session cookie, so a
	// CSRF token is both irrelevant and impossible for Sonarr to supply. If
	// this ever starts returning 403, every downstream client breaks at once
	// and the cause will not be obvious from their end.
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(sessions.Sessions("mysession", cookie.NewStore([]byte("test-secret-test-secret-abcd"))))
	e.Use(CSRF())
	e.POST("/api", func(c *gin.Context) { c.String(http.StatusOK, "api") })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("POST /api with no token = %d, want 200 — the keyed API must stay exempt", rec.Code)
	}
}

func TestTheTokenIsStableWithinASession(t *testing.T) {
	// A token that changed per request would invalidate any form already open
	// in a tab, which reads to a member as "the site randomly logs me out".
	e := newEngine(t)
	first, cookies := mint(t, e)

	req := httptest.NewRequest(http.MethodGet, "/form", nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if second := strings.TrimSpace(rec.Body.String()); second != first {
		t.Errorf("token changed within one session: %q then %q", first, second)
	}
}

func TestTheTokenIsAlsoReadableAsACookie(t *testing.T) {
	// Deliberately NOT HttpOnly: a fetch() caller has to read it to echo it in
	// the header. The session cookie stays HttpOnly, which is the one that
	// matters — this value is useless without it.
	e := newEngine(t)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/form", nil))
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == csrfCookieName {
			if ck.HttpOnly {
				t.Error("the CSRF cookie is HttpOnly, so a fetch() caller cannot read it")
			}
			return
		}
	}
	t.Error("no CSRF cookie was set")
}
