package handlers

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// The gate in front of every plugin-supplied page.
//
// Plugins register views and the host mounts them; siteGate is the single
// decision about who gets one. It was at 0%, which means the refusal path —
// the half that only runs when somebody is not allowed in — had never been
// executed by anything.
//
// The interesting behaviour is not "does it refuse" but HOW it refuses. A
// browser needs the door; a script needs a status it can act on. Getting that
// backwards does not look like a security bug, it looks like a broken API
// client, and it gets reported as one.

// runGate drives siteGate through a real engine.
//
// A bare gin.CreateTestContext is not enough: w.auth.Current reads the session,
// and the session middleware has to have run or MustGet panics. Building the
// request the way the site does is also the more honest test — it exercises
// the Accept header exactly as a client sends it.
func runGate(t *testing.T, v core.View, accept string) (allowed bool, rec *httptest.ResponseRecorder, aborted bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(sessions.Sessions("mysession", cookie.NewStore([]byte("test-secret-test-secret-abcd"))))

	w := &web{}
	e.GET("/p/example", func(c *gin.Context) {
		allowed = w.siteGate(v, c)
		aborted = c.IsAborted()
	})

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/p/example", nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	e.ServeHTTP(rec, req)
	return allowed, rec, aborted
}

// runCanView answers the visibility question alone, with a session available.
func runCanView(t *testing.T, v core.View) bool {
	t.Helper()
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(sessions.Sessions("mysession", cookie.NewStore([]byte("test-secret-test-secret-abcd"))))

	w := &web{}
	var out bool
	e.GET("/p/example", func(c *gin.Context) { out = w.canView(v, c) })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/p/example", nil))
	return out
}

func TestAPublicViewIsOpenToAnonymousVisitors(t *testing.T) {
	// Public means public. A plugin that says so is taking responsibility for
	// the page being readable by anyone, and the host must not second-guess it.
	allowed, rec, aborted := runGate(t, core.View{Slug: "about", Public: true}, "text/html")

	if !allowed {
		t.Fatal("a public view was refused to an anonymous visitor")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("the gate wrote a body for an allowed request: %q", rec.Body)
	}
	if aborted {
		t.Error("the gate aborted an allowed request")
	}
}

func TestABrowserIsSentToTheDoor(t *testing.T) {
	// A person gets somewhere they can act: the login page. Answering a browser
	// with 403 JSON shows them a page of braces.
	allowed, rec, aborted := runGate(t, core.View{Slug: "members", Public: false},
		"text/html,application/xhtml+xml")

	if allowed {
		t.Fatal("a members-only view was allowed to an anonymous visitor")
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 to the login page", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
	if !aborted {
		t.Error("the request was refused but not aborted, so the handler still runs")
	}
}

func TestAScriptGetsAStatusItCanActOn(t *testing.T) {
	// The other half, and the one that gets misreported. A fetch() or a
	// downloader following a 303 to /login receives 200 and an HTML login page
	// — a success, containing markup, where JSON was expected. The bug then
	// looks like it is in the client.
	for _, accept := range []string{"application/json", "*/*", ""} {
		allowed, rec, _ := runGate(t, core.View{Slug: "members", Public: false}, accept)

		if allowed {
			t.Fatalf("Accept=%q: a members-only view was allowed", accept)
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("Accept=%q: status = %d, want 403", accept, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "" {
			t.Errorf("Accept=%q: a non-browser client was redirected to %q", accept, loc)
		}
		if body := rec.Body.String(); !strings.Contains(body, `"ok":false`) {
			t.Errorf("Accept=%q: body = %q, want a JSON refusal", accept, body)
		}
	}
}

func TestTheRefusalNamesNoInternals(t *testing.T) {
	// The body reaches whoever asked, including somebody who should not have.
	// "insufficient role" says enough to debug a real client and nothing about
	// what role would have worked, which slug exists, or what the plugin is.
	_, rec, _ := runGate(t, core.View{
		Slug: "secret-admin-thing", Public: false, MinRole: core.RoleAdmin,
	}, "application/json")

	body := rec.Body.String()
	for _, leak := range []string{"secret-admin-thing", "admin", "MinRole"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(leak)) {
			t.Errorf("the refusal quotes %q back: %s", leak, body)
		}
	}
}

func TestCanViewFollowsTheViewsOwnRules(t *testing.T) {
	// canView is a thin pass-through to View.AllowsUser, and thin is the point:
	// the host does not get to have an opinion about a plugin's visibility. The
	// test exists so it stays thin — a host-side special case added here would
	// mean a plugin's declared visibility is no longer what happens.
	if !runCanView(t, core.View{Public: true}) {
		t.Error("a public view was refused for an anonymous viewer")
	}
	if runCanView(t, core.View{Public: false}) {
		t.Error("a non-public view was allowed for an anonymous viewer")
	}
	if runCanView(t, core.View{Public: false, MinRole: core.RoleAdmin}) {
		t.Error("an admin-only view was allowed for an anonymous viewer")
	}
}

func TestSitePageRefusesBeforeItRenders(t *testing.T) {
	// Order matters: a view that is not allowed must never have Render called.
	// Rendering first and discarding the output would run the plugin's code —
	// its queries, its side effects — on behalf of somebody being refused.
	rendered := false
	v := core.View{
		Slug: "members", Title: "Members", Public: false,
		Render: func(*gin.Context) (template.HTML, error) {
			rendered = true
			return "", nil
		},
	}

	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(sessions.Sessions("mysession", cookie.NewStore([]byte("test-secret-test-secret-abcd"))))
	w := &web{}
	e.GET("/p/members", w.sitePage(v))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/p/members", nil)
	req.Header.Set("Accept", "application/json")
	e.ServeHTTP(rec, req)

	if rendered {
		t.Error("the view rendered for a viewer who was refused")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
