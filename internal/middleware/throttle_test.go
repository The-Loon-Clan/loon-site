package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// clock lets the tests exercise refill without sleeping. A limiter tested with
// real sleeps is a limiter tested at one speed, and the interesting cases are
// all about elapsed time.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func testThrottle(l Limit) (*Throttle, *clock) {
	c := &clock{t: time.Unix(1700000000, 0)}
	t := NewThrottle(l)
	t.now = c.now
	return t, c
}

// A burst is what ordinary browsing looks like. Ten tabs must not be refused,
// which is the whole reason this is a bucket and not a window.
func TestBurstIsNotThrottled(t *testing.T) {
	th, _ := testThrottle(LimitBrowse)
	for i := 0; i < LimitBrowse.Burst; i++ {
		if ok, _ := th.allow("ip:1.2.3.4"); !ok {
			t.Fatalf("refused request %d of a %d burst; opening tabs would be "+
				"refused, which is the failure this exists to avoid",
				i+1, LimitBrowse.Burst)
		}
	}
}

func TestBucketEmptiesAndRefills(t *testing.T) {
	th, clk := testThrottle(Limit{Burst: 3, Every: time.Second})
	for i := 0; i < 3; i++ {
		if ok, _ := th.allow("k"); !ok {
			t.Fatalf("refused request %d of 3", i+1)
		}
	}
	ok, retry := th.allow("k")
	if ok {
		t.Fatal("a fourth request passed an empty bucket")
	}
	if retry <= 0 || retry > time.Second {
		t.Fatalf("retry-after %v, want (0, 1s]", retry)
	}
	// Nothing has changed for the caller until time passes.
	if ok, _ := th.allow("k"); ok {
		t.Fatal("an empty bucket refilled without time passing")
	}
	clk.add(time.Second)
	if ok, _ := th.allow("k"); !ok {
		t.Fatal("one token did not come back after one interval")
	}
	// ...and only one.
	if ok, _ := th.allow("k"); ok {
		t.Fatal("one interval returned more than one token")
	}
}

// A bucket must not refill past full, or an address idle overnight arrives
// with an unbounded allowance and the burst cap means nothing.
func TestRefillStopsAtFull(t *testing.T) {
	th, clk := testThrottle(Limit{Burst: 3, Every: time.Second})
	clk.add(24 * time.Hour)
	for i := 0; i < 3; i++ {
		if ok, _ := th.allow("k"); !ok {
			t.Fatalf("refused request %d after a long idle", i+1)
		}
	}
	if ok, _ := th.allow("k"); ok {
		t.Fatal("a day of idling banked more than the burst")
	}
}

// Two keys are two allowances. Without this, one member exhausts the site.
func TestKeysAreIndependent(t *testing.T) {
	th, _ := testThrottle(Limit{Burst: 1, Every: time.Minute})
	if ok, _ := th.allow("a"); !ok {
		t.Fatal("first key refused immediately")
	}
	if ok, _ := th.allow("b"); !ok {
		t.Fatal("a second key was refused by the first key's spending")
	}
}

// The auth tier is the one that has to actually bite, because repeating the
// request IS the attack there.
func TestAuthTierStopsAPasswordList(t *testing.T) {
	th, clk := testThrottle(LimitAuth)
	n := 0
	for i := 0; i < 1000; i++ {
		if ok, _ := th.allow("ip:9.9.9.9"); ok {
			n++
		}
		clk.add(time.Second)
	}
	// 1000 seconds at one per twelve, plus the initial burst.
	if n > 100 {
		t.Fatalf("%d attempts allowed in 1000 seconds; the auth tier is not "+
			"tight enough to make a password list pointless", n)
	}
	if n < 10 {
		t.Fatalf("only %d attempts in 1000 seconds — that is tight enough to "+
			"lock out a member who mistypes", n)
	}
}

// An unconfigured tier must fail OPEN. A zero Limit is what a caller gets from
// a struct they forgot to fill, and refusing everything on a config mistake
// takes the site down harder than any attack this is guarding.
func TestZeroLimitAllowsEverything(t *testing.T) {
	th, _ := testThrottle(Limit{})
	for i := 0; i < 100; i++ {
		if ok, _ := th.allow("k"); !ok {
			t.Fatal("a zero Limit refused a request; it must fail open")
		}
	}
}

// The table is bounded. Left unbounded, an attacker rotating source addresses
// turns the rate limiter into a way to exhaust memory -- a poor trade for the
// thing it was defending.
func TestBucketTableIsBounded(t *testing.T) {
	th, _ := testThrottle(Limit{Burst: 1, Every: time.Minute})
	th.max = 100
	for i := 0; i < 1000; i++ {
		th.allow("ip:" + strconv.Itoa(i))
	}
	th.mu.Lock()
	n := len(th.buckets)
	th.mu.Unlock()
	if n > th.max {
		t.Fatalf("%d buckets held with a cap of %d", n, th.max)
	}
}

// Idle buckets are forgotten. A full bucket has spent nothing, so dropping it
// changes no decision -- and keeping it forever means every address ever seen
// is remembered for the life of the process.
func TestSweepForgetsIdleBuckets(t *testing.T) {
	th, clk := testThrottle(Limit{Burst: 2, Every: time.Second})
	th.allow("stale")
	clk.add(time.Hour)
	th.allow("fresh")
	th.sweep()
	th.mu.Lock()
	_, staleKept := th.buckets["stale"]
	_, freshKept := th.buckets["fresh"]
	th.mu.Unlock()
	if staleKept {
		t.Error("an hour-idle bucket survived the sweep")
	}
	if !freshKept {
		t.Error("the sweep dropped a bucket that was just used")
	}
}

// The response has to be usable by a client that wants to behave: a status a
// library recognises, and a Retry-After it can wait for.
func TestRefusalAnswers429WithRetryAfter(t *testing.T) {
	th, _ := testThrottle(Limit{Burst: 1, Every: 10 * time.Second})
	guard := th.Guard()

	run := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/browse", nil)
		guard(c)
		return w
	}
	if w := run(); w.Code == http.StatusTooManyRequests {
		t.Fatal("the first request was throttled")
	}
	w := run()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second request answered %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After on a 429; a well-behaved client cannot tell how long to wait")
	}
	if body := w.Body.String(); len(body) < 20 {
		t.Errorf("refusal body %q says nothing a member could act on", body)
	}
}
