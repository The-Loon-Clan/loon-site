package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-baseline/notify"
	"github.com/the-loon-clan/loon-baseline/session"
	"github.com/the-loon-clan/loon-baseline/webauth"
	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-site/internal/middleware"
	"github.com/the-loon-clan/loon-site/internal/storage"
)

// The shell every page is rendered through.
//
// chromeData decides what the header, the account menu and the nav can show —
// which means a mistake here is a mistake on every page at once, and the two
// kinds it makes are both quiet. Showing an admin link to somebody who cannot
// use it sends them to a 403 JSON blob. Hiding a tile because a value is zero
// makes a working feature look unwired.

// stubPoints is core.PointsService. Only Balance matters here; the rest exist
// to satisfy the interface, and they return zero values rather than panicking
// so that a chromeData change which starts calling one fails on the assertion
// rather than on a stub.
type stubPoints struct {
	bal int
	err error
}

func (s stubPoints) Balance(context.Context, int64) (int, error) { return s.bal, s.err }
func (s stubPoints) Award(context.Context, int64, int, string, string, int64) (int, error) {
	return s.bal, nil
}
func (s stubPoints) Deduct(context.Context, int64, int, string, string, int64) (int, error) {
	return s.bal, nil
}
func (s stubPoints) Refund(context.Context, int64, int, string, string, int64) (int, error) {
	return s.bal, nil
}
func (s stubPoints) History(context.Context, int64, int, int) ([]core.LedgerEntry, int, error) {
	return nil, 0, nil
}

// stubInbox is notify.InboxStore, same arrangement.
type stubInbox struct {
	unread int
	err    error
}

func (s stubInbox) Add(context.Context, int64, core.Notification) error { return nil }
func (s stubInbox) List(context.Context, int64, int) ([]notify.Item, error) {
	return nil, nil
}
func (s stubInbox) UnreadCount(context.Context, int64) (int, error) { return s.unread, s.err }
func (s stubInbox) MarkAllRead(context.Context, int64) error        { return nil }
func (s stubInbox) DeleteAll(context.Context, int64) error          { return nil }

// chrome renders the shell data for one viewer.
//
// viewer nil means anonymous. Otherwise a real session is issued and resolved
// through webauth, rather than reaching past it — the point is to exercise the
// path a request actually takes.
func chrome(t *testing.T, w *web, viewer *core.User) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := session.Config{Name: sessionCookieName, Secret: []byte("test-secret-test-secret-abcd")}
	w.auth = webauth.Auth{
		Session: cfg,
		Resolve: func(_ context.Context, id int64) (*core.User, webauth.Meta, bool) {
			if viewer == nil || viewer.ID != id {
				return nil, webauth.Meta{}, false
			}
			return viewer, webauth.Meta{}, true
		},
	}
	if w.data == nil {
		// A zero Store: DB().Valid() is false, so the queries chromeData makes
		// short-circuit instead of dialling anything.
		w.data = &storage.Store{}
	}

	e := gin.New()
	e.Use(sessions.Sessions(sessionCookieName, cookie.NewStore(cfg.Secret)))
	e.Use(middleware.CSRF())

	var out map[string]any
	e.GET("/sign-in", func(c *gin.Context) {
		if viewer != nil {
			_ = session.Issue(c, viewer.ID, "", 0)
		}
		c.Status(http.StatusOK)
	})
	e.GET("/page", func(c *gin.Context) {
		out = w.chromeData(c, nil)
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sign-in", nil))

	// LAST value per name, the way a browser stores cookies.
	//
	// The response carries TWO Set-Cookie headers for the session: the CSRF
	// middleware mints a token and saves before the handler runs, then
	// session.Issue saves again with the login. Replaying both left the server
	// reading the earlier, pre-login copy — so every signed-in assertion here
	// failed with a nil viewer, and it looked like the session was not being
	// issued at all.
	jar := map[string]*http.Cookie{}
	for _, ck := range rec.Result().Cookies() {
		jar[ck.Name] = ck
	}

	req := httptest.NewRequest(http.MethodGet, "/page", nil)
	for _, ck := range jar {
		req.AddCookie(ck)
	}
	e.ServeHTTP(httptest.NewRecorder(), req)
	return out
}

func member(id int64, role core.Role) *core.User {
	return &core.User{ID: id, Username: "someone", Role: role}
}

func TestAnAnonymousVisitorGetsNoStaffAffordances(t *testing.T) {
	d := chrome(t, &web{}, nil)

	// A typed nil: data["User"] holds (*core.User)(nil), which is NOT equal to
	// a nil interface. The templates are unaffected — text/template's truth
	// test treats a nil pointer as false — but a test comparing against nil
	// passes for the wrong reason, so the assertion is on the pointer.
	if u, _ := d["User"].(*core.User); u != nil {
		t.Errorf("User = %v on an anonymous request", u)
	}
	if d["IsAdmin"] != false || d["IsMod"] != false {
		t.Errorf("IsAdmin=%v IsMod=%v for an anonymous visitor", d["IsAdmin"], d["IsMod"])
	}
	for _, k := range []string{"RoleLabel", "MemberSince", "Points", "HasPoints", "Unread", "HasUnread"} {
		if _, present := d[k]; present {
			t.Errorf("%s is set for an anonymous visitor", k)
		}
	}
}

func TestTheAdminNavIsGatedOnRoleNotOnBeingLoggedIn(t *testing.T) {
	// Stated in the code and worth holding: /admin/* sits behind
	// Require(RoleAdmin), so showing the link to a plain member sends them to a
	// 403 JSON blob rather than a page. A menu entry that cannot work is worse
	// than no entry.
	for _, tc := range []struct {
		role               core.Role
		wantAdmin, wantMod bool
		name               string
	}{
		{core.RoleUser, false, false, "member"},
		{core.RoleMod, false, true, "moderator"},
		{core.RoleAdmin, true, true, "admin"},
	} {
		d := chrome(t, &web{}, member(1, tc.role))
		if d["IsAdmin"] != tc.wantAdmin {
			t.Errorf("%s: IsAdmin = %v, want %v", tc.name, d["IsAdmin"], tc.wantAdmin)
		}
		if d["IsMod"] != tc.wantMod {
			t.Errorf("%s: IsMod = %v, want %v", tc.name, d["IsMod"], tc.wantMod)
		}
	}
}

func TestAnAdminIsAlsoAModerator(t *testing.T) {
	// AtLeast, not equality. An admin who cannot see the moderation queue is
	// the kind of gap nobody notices until the only admin is the only person
	// left to work it.
	d := chrome(t, &web{}, member(1, core.RoleAdmin))
	if d["IsMod"] != true {
		t.Error("an admin does not get the moderator affordances")
	}
}

func TestEveryPageCarriesACSRFToken(t *testing.T) {
	// Every POST form on the site renders this into a hidden field. Absent, and
	// every form submission on every page is refused by the CSRF gate — the
	// site appears to be up and nothing can be submitted.
	for _, viewer := range []*core.User{nil, member(1, core.RoleUser)} {
		d := chrome(t, &web{}, viewer)
		if tok, _ := d["CSRFToken"].(string); tok == "" {
			t.Errorf("no CSRF token in the chrome (viewer=%v)", viewer != nil)
		}
	}
}

func TestAZeroBalanceStillShowsTheTile(t *testing.T) {
	// The bug the Has* keys exist for. A template cannot tell an ABSENT map key
	// from a zero one, so {{if .Points}} hid the tile both when the points
	// service was unwired AND for a member whose balance is genuinely 0 — and
	// a new member's balance is genuinely 0.
	d := chrome(t, &web{points: stubPoints{bal: 0}}, member(1, core.RoleUser))

	if d["HasPoints"] != true {
		t.Error("HasPoints is not set for a real balance of 0, so the tile hides")
	}
	if d["Points"] != 0 {
		t.Errorf("Points = %v, want 0", d["Points"])
	}
}

func TestAnUnwiredPointsServiceHidesTheTile(t *testing.T) {
	// The other half of the same distinction: nothing wired means the key is
	// absent, so the template hides the tile rather than showing a confident 0.
	d := chrome(t, &web{}, member(1, core.RoleUser))

	if _, present := d["HasPoints"]; present {
		t.Error("HasPoints is set with no points service wired")
	}
	if _, present := d["Points"]; present {
		t.Error("Points is set with no points service wired")
	}
}

func TestAFailingPointsServiceShowsNothingRatherThanZero(t *testing.T) {
	// A read that errored is not a balance of zero. Rendering "0 points" from a
	// failed query tells a member something false about their own account.
	d := chrome(t, &web{points: stubPoints{bal: 500, err: errors.New("down")}}, member(1, core.RoleUser))

	if _, present := d["HasPoints"]; present {
		t.Error("a failed balance read still set HasPoints")
	}
}

func TestAZeroUnreadCountIsStillAKnownValue(t *testing.T) {
	d := chrome(t, &web{inbox: stubInbox{unread: 0}}, member(1, core.RoleUser))

	if d["HasUnread"] != true {
		t.Error("HasUnread is not set for a real count of 0")
	}
	if d["Unread"] != 0 {
		t.Errorf("Unread = %v, want 0", d["Unread"])
	}
}

func TestAFailingInboxLeavesTheBellAlone(t *testing.T) {
	d := chrome(t, &web{inbox: stubInbox{unread: 3, err: errors.New("down")}}, member(1, core.RoleUser))

	if _, present := d["HasUnread"]; present {
		t.Error("a failed unread read still set HasUnread")
	}
	if _, present := d["Unread"]; present {
		t.Error("a failed unread read still set Unread")
	}
}

func TestTheViewerIdentityIsCarried(t *testing.T) {
	u := member(7, core.RoleMod)
	d := chrome(t, &web{}, u)

	if d["User"] == nil {
		t.Fatal("User is nil for a signed-in viewer")
	}
	if d["RoleLabel"] == nil || d["RoleLabel"] == "" {
		t.Errorf("RoleLabel = %v, want the rank the role maps to", d["RoleLabel"])
	}
}

func TestChromeDataBuildsAMapWhenGivenNone(t *testing.T) {
	// Called with nil from render(), so returning nil would panic on the first
	// assignment rather than at a place naming the cause.
	w := &web{data: &storage.Store{}}
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(sessions.Sessions(sessionCookieName, cookie.NewStore([]byte("test-secret-test-secret-abcd"))))
	e.Use(middleware.CSRF())

	var got map[string]any
	e.GET("/page", func(c *gin.Context) { got = w.chromeData(c, nil) })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/page", nil))

	if got == nil {
		t.Fatal("chromeData(nil) returned nil")
	}
}
