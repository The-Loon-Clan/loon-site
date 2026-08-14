package handlers

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon-baseline/session"
	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-site/internal/middleware"
	"github.com/the-loon-clan/loon-site/internal/storage"
)

// A real web, against a real database.
//
// Most of what is left uncovered in this package is HTTP handlers that read
// rows — the listing pages, the profile, the moderation queues. They cannot be
// reached by a unit test with a stub, because what they mostly DO is ask the
// database questions and turn the answers into a page; a fake store would only
// be asserting that the fake was called.
//
// This became possible when the boot helpers stopped calling os.Exit. A library
// function that exits cannot be called by a test: the process dies and takes
// the whole run with it. They return errors now, and Main does the exiting.
//
// SKIPPED without LOON_TEST_DSN, and FAILING in CI without one — the same rule
// as the storage integration tests, for the same reason: a suite that silently
// skips its most substantial half looks identical to one that passes.
//
// SCOPE: the routes w.mount registers, which is the site's own pages. The
// /admin and /moderation trees are NOT here, and deliberately: they are wired
// by wireAdminAndViews, which takes a booted *core.Core and *core.Runtime, so
// reaching them from a test would mean provisioning twenty-four plugins and
// starting their job loops. That is a different kind of test — closer to what
// CI's out-of-the-box job already does by running the real thing and asking it
// for pages.

// testSite is a booted site: real storage, real templates, real routes.
type testSite struct {
	w      *web
	engine *gin.Engine
	db     *sqlx.DB
	users  *core.User // alice, seeded by migrateSiteTables
}

func newTestSite(t *testing.T) *testSite {
	t.Helper()

	dsn := os.Getenv("LOON_TEST_DSN")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("LOON_TEST_DSN is unset in CI: the postgres service container " +
				"is missing, and these handler tests have been silently skipping")
		}
		t.Skip("set LOON_TEST_DSN to run the handler tests against a real database")
	}

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Quiet: a boot logs a couple of dozen lines and they are not what a
	// failing assertion needs above it.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	data := storage.New(db)
	st, err := wireBaselineStores(data.DB(), logger)
	if err != nil {
		t.Fatalf("wire stores: %v", err)
	}
	if err := migrateSiteTables(data, logger, st.users); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	w := newWeb(st.users, st.sessionSecret, logger, data)

	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(sessions.Sessions(sessionCookieName, mustSessionStore(t, st.sessionSecret)))
	e.Use(middleware.CSRF())
	w.mount(e)
	// The moderation surface, which w.mount does not register: it lived inside
	// wireAdminAndViews until this test needed it, and that function wants a
	// plugin runtime this harness has no business building. mountModeration
	// needs neither — see moderation_wiring.go.
	mountModeration(e, w)

	// Registered ONCE, here. It was in getAs, which gin panics on the second
	// call — a duplicate route is a programming error and gin says so loudly.
	e.GET("/__test_signin", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Query("id"), 10, 64)
		_ = session.Issue(c, id, "", 0)
		c.Status(http.StatusOK)
	})

	alice, err := st.users.ByUsername(t.Context(), "alice")
	if err != nil {
		t.Fatalf("the demo seed did not produce alice: %v", err)
	}

	return &testSite{w: w, engine: e, db: db, users: alice.ToCore()}
}

func mustSessionStore(t *testing.T, secret []byte) sessions.Store {
	t.Helper()
	store, err := session.Config{Name: sessionCookieName, Secret: secret}.Store()
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	return store
}

// get issues a GET as an anonymous visitor.
func (ts *testSite) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ts.engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// getAs issues a GET carrying a session for the given user id.
func (ts *testSite) getAs(t *testing.T, userID int64, path string) *httptest.ResponseRecorder {
	t.Helper()

	// Mint a session the way login does, then replay its cookies.
	rec := httptest.NewRecorder()
	ts.engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/__test_signin?id="+strconv.FormatInt(userID, 10), nil))

	// Last value per name, as a browser stores them: the CSRF middleware saves
	// before the handler and session.Issue saves after, so the response carries
	// two Set-Cookie headers for the session and the earlier one is pre-login.
	jar := map[string]*http.Cookie{}
	for _, ck := range rec.Result().Cookies() {
		jar[ck.Name] = ck
	}

	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, ck := range jar {
		req.AddCookie(ck)
	}
	out := httptest.NewRecorder()
	ts.engine.ServeHTTP(out, req)
	return out
}

// ── the pages an anonymous visitor sees ─────────────────────────────────

func TestThePublicPagesRenderAgainstARealDatabase(t *testing.T) {
	ts := newTestSite(t)

	for _, path := range []string{
		"/", "/browse", "/search", "/search?q=blade", "/groups", "/trending",
		"/login", "/register", "/forgot", "/rules", "/faq", "/about",
		"/staff", "/stats", "/sitemap", "/robots.txt",
	} {
		rec := ts.get(t, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200\n%s", path, rec.Code, first(rec.Body.String(), 300))
			continue
		}
		if rec.Body.Len() == 0 {
			t.Errorf("GET %s returned an empty body", path)
		}
	}
}

func TestAProfileRendersAndAMissingOneIs404(t *testing.T) {
	ts := newTestSite(t)

	if rec := ts.get(t, "/u/alice"); rec.Code != http.StatusOK {
		t.Errorf("GET /u/alice = %d, want 200", rec.Code)
	}
	// A name nobody has must be 404 rather than 200 with an empty page: a
	// crawler, a cache and a scripted client all read the status, and this
	// project has had exactly that bug — three pages answering 200 while
	// showing "not found".
	if rec := ts.get(t, "/u/nobody-by-that-name"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /u/nobody-by-that-name = %d, want 404", rec.Code)
	}
}

func TestTheMembersOnlyPagesRefuseAnAnonymousVisitor(t *testing.T) {
	ts := newTestSite(t)

	for _, path := range []string{
		"/bookmarks", "/wishlist", "/gifts", "/invites", "/subscriptions",
		"/settings/privacy", "/settings/security", "/settings/notifications",
	} {
		rec := ts.get(t, path)
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s = 200 for an anonymous visitor", path)
		}
	}
}

func TestTheSamePagesRenderForAMember(t *testing.T) {
	ts := newTestSite(t)

	for _, path := range []string{
		"/bookmarks", "/wishlist", "/gifts", "/invites", "/subscriptions",
		"/settings/privacy", "/settings/security", "/settings/notifications",
		"/achievements", "/calendar",
	} {
		rec := ts.getAs(t, ts.users.ID, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s as a member = %d, want 200\n%s",
				path, rec.Code, first(rec.Body.String(), 300))
		}
	}
}

func TestTheNewznabAPIAnswers(t *testing.T) {
	ts := newTestSite(t)

	for _, path := range []string{"/api?t=caps", "/api?t=search&q=x", "/rss"} {
		rec := ts.get(t, path)
		// 200 with the plugin wired, 503 without — either is a real answer.
		// What must not happen is a panic or a 500.
		if rec.Code >= http.StatusInternalServerError && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s = %d\n%s", path, rec.Code, first(rec.Body.String(), 200))
		}
	}
}

func first(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
