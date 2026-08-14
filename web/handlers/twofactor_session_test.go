package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

// The half-authenticated state between a correct password and a second factor.
//
// security_web.go was at 0 of 180 statements — the whole file, including this.
// The parts needing a database are covered by the storage integration tests;
// what is here is the SESSION state machine, which needs nothing but a session
// and decides whether somebody who knows a password but not a code gets in.
//
// The dangerous property is not "does the code check work" — totp_web_test.go
// covers that against the RFC vectors. It is that the pending state cannot be
// inherited, cannot outlive its window, and cannot be mistaken for a session.

// session2fa runs handlers against one shared cookie jar, so a sequence of
// requests behaves like one browser.
type session2fa struct {
	e       *gin.Engine
	cookies []*http.Cookie
	t       *testing.T
}

func new2FASession(t *testing.T) *session2fa {
	t.Helper()
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(sessions.Sessions("mysession", cookie.NewStore([]byte("test-secret-test-secret-abcd"))))
	return &session2fa{e: e, t: t}
}

// do issues a request, carrying and collecting cookies.
func (s *session2fa) do(method, path string) *httptest.ResponseRecorder {
	s.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for _, ck := range s.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	if got := rec.Result().Cookies(); len(got) > 0 {
		s.cookies = got
	}
	return rec
}

func TestBeginningAChallengeStampsThePendingUser(t *testing.T) {
	s := new2FASession(t)
	var seen int64
	s.e.GET("/begin", func(c *gin.Context) { beginTOTPChallenge(c, 42) })
	s.e.GET("/who", func(c *gin.Context) { seen = pendingTOTPUser(c) })

	rec := s.do(http.MethodGet, "/begin")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login/2fa" {
		t.Fatalf("begin gave %d -> %q, want 303 to /login/2fa", rec.Code, rec.Header().Get("Location"))
	}

	s.do(http.MethodGet, "/who")
	if seen != 42 {
		t.Errorf("pending user = %d, want 42", seen)
	}
}

func TestAChallengeInheritsNothingFromTheSessionItReplaces(t *testing.T) {
	// The reason beginTOTPChallenge clears first. Somebody arriving at the
	// second step has proved a password and nothing more; if the session they
	// arrive with still carries what was in it before — another account's
	// values, a flash, anything set by an earlier visit — then a state that is
	// deliberately half-authenticated is carrying authenticated leftovers.
	s := new2FASession(t)
	var leftover any
	s.e.GET("/seed", func(c *gin.Context) {
		sess := sessions.Default(c)
		sess.Set("something_from_before", "must not survive")
		_ = sess.Save()
	})
	s.e.GET("/begin", func(c *gin.Context) { beginTOTPChallenge(c, 7) })
	s.e.GET("/peek", func(c *gin.Context) { leftover = sessions.Default(c).Get("something_from_before") })

	s.do(http.MethodGet, "/seed")
	s.do(http.MethodGet, "/begin")
	s.do(http.MethodGet, "/peek")

	if leftover != nil {
		t.Errorf("the pending-2FA session still holds %v from before the challenge", leftover)
	}
}

func TestNoChallengeMeansNobodyIsPending(t *testing.T) {
	s := new2FASession(t)
	var seen int64 = -1
	s.e.GET("/who", func(c *gin.Context) { seen = pendingTOTPUser(c) })

	s.do(http.MethodGet, "/who")
	if seen != 0 {
		t.Errorf("pending user = %d on a fresh session, want 0", seen)
	}
}

func TestAnExpiredChallengeIsGoneAndTakesTheSessionWithIt(t *testing.T) {
	// The window exists so a half-finished login cannot be resumed from an
	// unattended browser an hour later. Expiry must also CLEAR the state: a
	// pending id left behind after the deadline is a login waiting to be
	// completed by whoever sits down next.
	s := new2FASession(t)
	var seen int64 = -1
	var stillStamped any

	s.e.GET("/begin-stale", func(c *gin.Context) {
		sess := sessions.Default(c)
		sess.Clear()
		sess.Set(pendingTOTPKey, int64(9))
		// Stamped one second beyond the window.
		sess.Set(pendingTOTPAtKey, time.Now().Add(-pendingTOTPTTL-time.Second).Unix())
		_ = sess.Save()
	})
	s.e.GET("/who", func(c *gin.Context) { seen = pendingTOTPUser(c) })
	s.e.GET("/peek", func(c *gin.Context) { stillStamped = sessions.Default(c).Get(pendingTOTPKey) })

	s.do(http.MethodGet, "/begin-stale")
	s.do(http.MethodGet, "/who")
	s.do(http.MethodGet, "/peek")

	if seen != 0 {
		t.Errorf("an expired challenge still reports user %d as pending", seen)
	}
	if stillStamped != nil {
		t.Errorf("the expired pending id is still in the session: %v", stillStamped)
	}
}

func TestAChallengeInsideTheWindowSurvives(t *testing.T) {
	// The other side of the boundary — a window that expired too eagerly would
	// send people back to the password form mid-login for no reason.
	s := new2FASession(t)
	var seen int64
	s.e.GET("/begin-recent", func(c *gin.Context) {
		sess := sessions.Default(c)
		sess.Clear()
		sess.Set(pendingTOTPKey, int64(11))
		sess.Set(pendingTOTPAtKey, time.Now().Add(-pendingTOTPTTL+time.Minute).Unix())
		_ = sess.Save()
	})
	s.e.GET("/who", func(c *gin.Context) { seen = pendingTOTPUser(c) })

	s.do(http.MethodGet, "/begin-recent")
	s.do(http.MethodGet, "/who")

	if seen != 11 {
		t.Errorf("a challenge inside the window reports %d, want 11", seen)
	}
}

func TestAPendingIdWithNoTimestampIsRefused(t *testing.T) {
	// Fail closed on a session that has an id and no stamp. That combination is
	// not something the code writes, so it means something else wrote it — an
	// older format, or a tampered cookie — and treating it as a live challenge
	// would honour a state nothing here created.
	s := new2FASession(t)
	var seen int64 = -1
	s.e.GET("/half", func(c *gin.Context) {
		sess := sessions.Default(c)
		sess.Clear()
		sess.Set(pendingTOTPKey, int64(5)) // no pendingTOTPAtKey
		_ = sess.Save()
	})
	s.e.GET("/who", func(c *gin.Context) { seen = pendingTOTPUser(c) })

	s.do(http.MethodGet, "/half")
	s.do(http.MethodGet, "/who")

	if seen != 0 {
		t.Errorf("a pending id with no timestamp was honoured as user %d", seen)
	}
}

func TestAnIdStoredAsIntIsStillRead(t *testing.T) {
	// The switch on int64/int is not defensive noise: session backends round
	// trip through their own encoding, and a value written as int64 can come
	// back as int. Reading only one type would silently drop the challenge and
	// bounce somebody back to the password form with no error.
	s := new2FASession(t)
	var seen int64
	s.e.GET("/as-int", func(c *gin.Context) {
		sess := sessions.Default(c)
		sess.Clear()
		sess.Set(pendingTOTPKey, 13) // plain int
		sess.Set(pendingTOTPAtKey, time.Now().Unix())
		_ = sess.Save()
	})
	s.e.GET("/who", func(c *gin.Context) { seen = pendingTOTPUser(c) })

	s.do(http.MethodGet, "/as-int")
	s.do(http.MethodGet, "/who")

	if seen != 13 {
		t.Errorf("an id stored as int read back as %d, want 13", seen)
	}
}

// ── recovery codes shown once ───────────────────────────────────────────

func TestRecoveryCodesAreShownOnceAndThenGone(t *testing.T) {
	// These are the credentials that get somebody back in without their phone,
	// and they are shown in the clear exactly once. Leaving them in the session
	// would keep a full set of usable second factors sitting in a cookie for
	// the rest of the session's life.
	s := new2FASession(t)
	var first, second []string

	s.e.GET("/flash", func(c *gin.Context) { flashCodes(c, []string{"aaaa-bbbb", "cccc-dddd"}) })
	s.e.GET("/take1", func(c *gin.Context) { first = takeFlashCodes(c) })
	s.e.GET("/take2", func(c *gin.Context) { second = takeFlashCodes(c) })

	s.do(http.MethodGet, "/flash")
	s.do(http.MethodGet, "/take1")
	s.do(http.MethodGet, "/take2")

	if len(first) != 2 || first[0] != "aaaa-bbbb" || first[1] != "cccc-dddd" {
		t.Errorf("first read gave %v, want both codes intact", first)
	}
	if len(second) != 0 {
		t.Errorf("the codes survived being read: %v", second)
	}
}

func TestNoFlashedCodesReadsAsNoneRatherThanOneEmptyCode(t *testing.T) {
	// strings.Split("", ",") returns a slice of one empty string, so the naive
	// version hands the template a list of length 1 and the page draws an empty
	// code box on every visit.
	s := new2FASession(t)
	var got []string
	s.e.GET("/take", func(c *gin.Context) { got = takeFlashCodes(c) })

	s.do(http.MethodGet, "/take")
	if len(got) != 0 {
		t.Errorf("got %d codes on a session that flashed none: %q", len(got), got)
	}
}
