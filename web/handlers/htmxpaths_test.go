package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// The async layer against a real database.
//
// htmx_test.go checks what can be checked from the source: that an hx-post
// agrees with its form's action, that it sits on a real form. This file checks
// the half that only a request can answer — what the handler actually SENDS.
//
// That distinction matters here more than usual, because every conversion so
// far was verified by hand with curl and none of it survived the terminal it
// was typed into. A property nobody can re-run is not a property the project
// has.

// postAs issues a POST carrying a session and a valid CSRF token.
//
// The harness had no POST helper at all, which is why the htmx work was
// verified by hand. The token comes from the csrf_tok cookie and travels as
// the X-CSRF-Token header — the same route htmx itself uses, so this exercises
// the middleware path the site depends on rather than a test-only shortcut.
func (ts *testSite) postAs(t *testing.T, userID int64, path string, form url.Values, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	jar := map[string]*http.Cookie{}
	collect := func(rec *httptest.ResponseRecorder) {
		// Last value per name, as a browser stores them: the CSRF middleware
		// saves before the handler and session.Issue saves after, so one
		// response can carry two Set-Cookie headers for the same name.
		for _, ck := range rec.Result().Cookies() {
			jar[ck.Name] = ck
		}
	}
	send := func(req *http.Request) *httptest.ResponseRecorder {
		for _, ck := range jar {
			req.AddCookie(ck)
		}
		rec := httptest.NewRecorder()
		ts.engine.ServeHTTP(rec, req)
		collect(rec)
		return rec
	}

	send(httptest.NewRequest(http.MethodGet,
		"/__test_signin?id="+strconv.FormatInt(userID, 10), nil))

	// A second GET, carrying the session just issued. The csrf_tok minted
	// BEFORE sign-in belongs to the pre-login session and the middleware
	// compares against the session's own copy, so posting it lands a 403 — and
	// a 403 is not a redirect, which is how a test asserting "never redirects"
	// passes while never reaching the handler at all.
	send(httptest.NewRequest(http.MethodGet, "/", nil))

	body := strings.NewReader(form.Encode())
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if tok, ok := jar["csrf_tok"]; ok {
		req.Header.Set("X-CSRF-Token", tok.Value)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := send(req)

	// The guard that keeps every assertion below honest. A CSRF rejection never
	// reaches the handler, and it satisfies "not a redirect" and "no page
	// chrome" perfectly — so without this, a broken helper turns this entire
	// file green while testing nothing. It has already happened once.
	if rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "CSRF") {
		t.Fatalf("POST %s was rejected by CSRF, so the handler never ran: %s",
			path, first(rec.Body.String(), 120))
	}
	return rec
}

var hxHeader = map[string]string{"HX-Request": "true"}

// looksLikeAWholePage reports whether a body carries the site chrome.
//
// A fragment that accidentally contains it is the signature failure of this
// whole design: renderStatus instead of renderFragment, or a redirect htmx
// followed. Either way the swap pastes a second navbar into the middle of the
// page, and nothing but an assertion like this notices.
func looksLikeAWholePage(body string) bool {
	b := strings.ToLower(body)
	return strings.Contains(b, "<html") || strings.Contains(b, "<nav")
}

// ── the invariant the whole design rests on ─────────────────────────────

// An htmx request must never be answered with a redirect.
//
// htmx follows redirects on its own request and swaps whatever comes back, so
// a converted handler that still redirects on ONE branch pastes an entire
// rendered page inside whatever it was targeting — a table row, a button.
//
// This is the test that would have caught a missed branch in communityModVote,
// where seven separate paths each had to be converted by hand. It exercises the
// refusal branches specifically, because those are the ones no one clicks and
// therefore the ones left behind.
func TestAnHTMXRequestIsNeverAnsweredWithARedirect(t *testing.T) {
	ts := newTestSite(t)

	for _, tc := range []struct {
		name string
		path string
		form url.Values
	}{
		// Deliberately invalid input: these take the error branches, which are
		// exactly the paths a manual click never reaches.
		// NOTE: /moderation/* is deliberately absent. Those routes are not
		// mounted by w.mount — they need the plugin runtime, as harness_test.go
		// says — so a request here 404s and would assert nothing. Their branch
		// coverage is the source-level count in htmx_test.go instead.
		{"wishlist, bad id", "/wishlist/0", url.Values{"action": {"remove"}}},
		{"undo, junk token", "/undo", url.Values{"token": {"not-a-token"}, "next": {"/"}}},
		{"bookmark, unknown release", "/release/999999999/bookmark", url.Values{}},
		{"follow, unknown user", "/u/nobody-by-that-name/follow", url.Values{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := ts.postAs(t, ts.users.ID, tc.path, tc.form, hxHeader)
			if rec.Code >= 300 && rec.Code < 400 {
				t.Errorf("POST %s with HX-Request = %d %q\n"+
					"htmx follows this and swaps the whole page into the target",
					tc.path, rec.Code, rec.Header().Get("Location"))
			}
			if looksLikeAWholePage(rec.Body.String()) {
				t.Errorf("POST %s with HX-Request returned page chrome:\n%s",
					tc.path, first(rec.Body.String(), 200))
			}
		})
	}
}

// The same endpoints must still redirect for a client that is not htmx.
// This is rule 1 — the site works with JavaScript off — made checkable.
func TestWithoutTheHeaderTheSameEndpointsStillRedirect(t *testing.T) {
	ts := newTestSite(t)

	for _, tc := range []struct{ name, path string }{
		{"undo", "/undo"},
		{"wishlist", "/wishlist/0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := ts.postAs(t, ts.users.ID, tc.path, url.Values{"id": {"0"}, "token": {"x"}}, nil)
			if rec.Code < 300 || rec.Code >= 400 {
				t.Errorf("POST %s without HX-Request = %d, want a redirect — "+
					"the no-JavaScript path must be unchanged", tc.path, rec.Code)
			}
		})
	}
}

// A settings save is notice-only: nothing on the page needs replacing, so the
// entire response is the out-of-band confirmation. If that notice were missing
// the member would press Save and see absolutely nothing happen — the worst
// outcome of the three, because the setting DID save and they will press it
// again.
func TestASettingsSaveAnswersWithNothingButTheNotice(t *testing.T) {
	ts := newTestSite(t)

	for _, tc := range []struct {
		path string
		form url.Values
	}{
		{"/settings/privacy", url.Values{"private_profile": {"1"}}},
		{"/settings/notifications", url.Values{}},
	} {
		rec := ts.postAs(t, ts.users.ID, tc.path, tc.form, hxHeader)

		if rec.Code != http.StatusOK {
			t.Errorf("POST %s = %d, want 200", tc.path, rec.Code)
			continue
		}
		body := rec.Body.String()
		for _, want := range []string{`hx-swap-oob="true"`, `id="notices"`, "notice--success"} {
			if !strings.Contains(body, want) {
				t.Errorf("POST %s is missing %q — the member presses Save and "+
					"sees nothing, having in fact saved:\n%s", tc.path, want, first(body, 200))
			}
		}
		if looksLikeAWholePage(body) {
			t.Errorf("POST %s returned the whole page", tc.path)
		}
	}
}

// ── the notice convention ───────────────────────────────────────────────

// A refusal answers 422 and explains itself out-of-band.
//
// 200 would tell every cache and scripted client that a rejected action was
// accepted. An empty body would leave a dead button with no reason.
func TestARefusalAnswers422WithAnOutOfBandNotice(t *testing.T) {
	ts := newTestSite(t)

	rec := ts.postAs(t, ts.users.ID, "/wishlist/0",
		url.Values{"action": {"remove"}}, hxHeader)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`hx-swap-oob="true"`, `id="notices"`, "notice--danger"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal body is missing %q — the member is told nothing:\n%s",
				want, first(body, 300))
		}
	}
}

// ── fragments carry no chrome, and the back button does ─────────────────

func TestBrowseAndSearchAnswerHTMXWithJustTheResults(t *testing.T) {
	ts := newTestSite(t)

	for _, path := range []string{"/browse?cat=2000", "/search?q=x"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("HX-Request", "true")
		frag := httptest.NewRecorder()
		ts.engine.ServeHTTP(frag, req)

		if frag.Code != http.StatusOK {
			t.Errorf("GET %s with HX-Request = %d", path, frag.Code)
			continue
		}
		// /browse only has a results panel when a category resolved. A bare
		// test database has no catalog plugin, so it renders "not set up yet"
		// and answering THAT with a full page is correct, not a miss. Assert
		// against what the page actually is.
		full := ts.get(t, path)
		if !strings.Contains(full.Body.String(), `id="results"`) {
			t.Logf("%s has no #results in this database (catalog unconfigured); "+
				"nothing to swap, so nothing to assert", path)
			continue
		}
		if looksLikeAWholePage(frag.Body.String()) {
			t.Errorf("GET %s with HX-Request returned the whole page", path)
		}
		if !strings.Contains(frag.Body.String(), `id="results"`) {
			t.Errorf("GET %s fragment has no #results, so the NEXT filter click "+
				"has nothing to target:\n%s", path, first(frag.Body.String(), 200))
		}

		// And the full page must still be a full page.
		if !looksLikeAWholePage(full.Body.String()) {
			t.Errorf("GET %s without the header lost its chrome", path)
		}
	}
}

// The back button must get a whole page. htmx marks history-restore requests
// HX-Request: true as well, so a handler checking only that header answers a
// back-navigation with a fragment and the browser paints a bare table where the
// site used to be — a failure that needs a real back button to reproduce.
func TestAHistoryRestoreGetsTheWholePage(t *testing.T) {
	ts := newTestSite(t)

	req := httptest.NewRequest(http.MethodGet, "/browse?cat=2000", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-History-Restore-Request", "true")
	rec := httptest.NewRecorder()
	ts.engine.ServeHTTP(rec, req)

	if !looksLikeAWholePage(rec.Body.String()) {
		t.Errorf("a history restore was answered with a fragment; the back "+
			"button would paint a bare results table as the whole site:\n%s",
			first(rec.Body.String(), 200))
	}
}

// ── every page carries the region a notice needs ────────────────────────

// Without #notices on the page, an out-of-band notice has nothing to swap into
// and htmx discards it silently — the action happens and the member is told
// nothing at all.
func TestEveryPageCarriesTheNoticeRegion(t *testing.T) {
	ts := newTestSite(t)

	for _, path := range []string{"/", "/browse", "/search", "/wishlist", "/moderation/avatars"} {
		rec := ts.getAs(t, ts.users.ID, path)
		if rec.Code != http.StatusOK {
			continue // covered by the page tests; not this test's business
		}
		if !strings.Contains(rec.Body.String(), `id="notices"`) {
			t.Errorf("%s has no #notices region: an out-of-band notice sent to "+
				"it would be discarded and the member told nothing", path)
		}
	}
}
