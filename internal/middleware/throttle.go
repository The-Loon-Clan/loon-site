package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

// Request rate limiting.
//
// THE FAILURE MODE THIS IS BUILT AGAINST is not the absence of a limiter, it
// is a limiter that fires on ordinary use. Open ten tabs on a UNIT3D site and
// it tells you that you "loaded too many pages too fast" — because a fixed
// window of N-per-minute cannot tell ten tabs from a scraper, and a member who
// is throttled for browsing learns that the site is broken, not that they did
// something wrong.
//
// So: a TOKEN BUCKET, not a window. A bucket holds `Burst` tokens and refills
// one every `Every`. Ten tabs spend ten tokens out of a full bucket and nobody
// notices; a scraper at ten a second drains it in three and then runs at
// exactly the refill rate forever. Bursts are what real browsing looks like,
// and sustained rate is the thing worth capping.
//
// KEYED PER MEMBER WHERE THERE IS ONE, per IP otherwise. A university, an
// office or a VPN exit is one address with many people behind it, and keying
// only on IP makes them share a bucket and blame each other.
//
// THE IP HAS TO BE THE REAL ONE, and that is not this file's doing: gin trusts
// every proxy by default and would take X-Forwarded-For from anybody, so
// main.go sets trusted proxies explicitly and defaults to trusting NOBODY.
// Both halves of getting that wrong land here. Trust everyone and an attacker
// rotates a header to get an unlimited number of buckets; trust nobody while
// actually running behind a proxy and every request in the building shares the
// proxy's address, which is exactly the "throttled after ten pages" report
// that started this. `LOON_TRUSTED_PROXIES` is the one knob.

// Limit is a bucket's shape: how much can arrive at once, and how fast the
// allowance comes back.
type Limit struct {
	Burst int           // tokens the bucket holds when full
	Every time.Duration // one token returns this often
}

// The three tiers. Deliberately few: a limiter with a knob per route is one
// nobody can reason about, and the interesting distinction is not which page
// it is but what an attacker gets out of repeating it.
var (
	// LimitBrowse — reading pages. Generous, and MORE generous than the first
	// guess, which was 60 at two a second and was measured rather than argued
	// about: it refused 18 of 60 signed-in page fetches, so the site's own
	// accessibility crawler reached 0 of 140 pages and reported a clean run.
	// A page-rate limit catches every crawler, including friendly ones, and a
	// number a person cannot hit is not automatically a number a tool cannot.
	//
	// 240 at once and twenty a second sustained. A person never approaches
	// either; a naive scraper of the whole 160k-release catalogue is held to
	// about two hours, which is the honest limit of what this tier buys. It is
	// not the defence against determined scraping -- that rotates addresses --
	// it is the one against a runaway loop and a lazy bot.
	LimitBrowse = Limit{Burst: 240, Every: 50 * time.Millisecond}

	// LimitWork — search, exports, anything that costs the database real work.
	// A burst still covers a member refining a query several times in a row.
	LimitWork = Limit{Burst: 15, Every: 2 * time.Second}

	// LimitAuth — login, register, password reset, second factor. The only
	// tier tight enough to be felt, because it is the only one where repeating
	// the request IS the attack. Eight tries then one every twelve seconds:
	// nothing a person doing their best to remember a password will hit, and
	// 300 an hour against a password list, which is not an attack that finishes.
	LimitAuth = Limit{Burst: 8, Every: 12 * time.Second}
)

// bucket is one key's allowance. Tokens are computed lazily from the clock
// rather than refilled by a ticker — a ticker per key would be thousands of
// goroutines to save one subtraction.
type bucket struct {
	tokens float64
	last   time.Time
}

// Throttle is a limiter for one tier. Separate instances rather than one map
// with a tier in the key, so the auth tier's table cannot be filled up by
// browsing traffic.
type Throttle struct {
	limit Limit
	now   func() time.Time // injectable so the tests do not sleep

	mu      sync.Mutex
	buckets map[string]*bucket

	// Exempt is consulted ONLY when the bucket is empty, which is the whole
	// design: answering "is this staff?" costs a session read and a store
	// lookup, and paying that on every page to skip a subtraction would be a
	// worse deal than the limit it implements. A refusal is rare, and no token
	// has been spent at that point, so letting one through there is clean.
	//
	// It exists because the first version of this tier refused 18 of 60
	// signed-in fetches and the site's own crawler reached 0 of 140 pages. A
	// browse-rate limit catches every crawler, friendly ones included, and an
	// operator running audits against their own site is the friendliest there
	// is. nil means nobody is exempt.
	Exempt func(*gin.Context) bool

	// max bounds the table. An attacker rotating source addresses would
	// otherwise turn a rate limiter into a memory exhaustion primitive, which
	// is a poor trade for the thing it was defending. At the cap the table is
	// dropped whole: the alternative is an LRU, and an LRU under exactly this
	// attack evicts the honest entries and keeps the attacker's.
	max int
}

// NewThrottle builds a limiter for one tier.
func NewThrottle(l Limit) *Throttle {
	return &Throttle{limit: l, now: time.Now, buckets: map[string]*bucket{}, max: 50000}
}

// allow spends a token for key, and reports how long until one is free when
// there is none.
func (t *Throttle) allow(key string) (bool, time.Duration) {
	if t.limit.Burst <= 0 || t.limit.Every <= 0 {
		return true, 0 // an unconfigured tier lets everything through
	}
	now := t.now()

	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.buckets) >= t.max {
		t.buckets = map[string]*bucket{}
	}
	b, ok := t.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(t.limit.Burst), last: now}
		t.buckets[key] = b
	}
	// Refill for the time since the last look, capped at full.
	if d := now.Sub(b.last); d > 0 {
		b.tokens += d.Seconds() / t.limit.Every.Seconds()
		if b.tokens > float64(t.limit.Burst) {
			b.tokens = float64(t.limit.Burst)
		}
		b.last = now
	}
	if b.tokens < 1 {
		// Time until the bucket holds one whole token.
		need := 1 - b.tokens
		return false, time.Duration(need * float64(t.limit.Every))
	}
	b.tokens--
	return true, 0
}

// sweep drops buckets that have been full and untouched, so an address seen
// once does not occupy memory forever. Full means the holder has spent
// nothing recently, so forgetting them changes no decision.
func (t *Throttle) sweep() {
	now := t.now()
	idle := time.Duration(t.limit.Burst) * t.limit.Every * 2
	if idle < time.Minute {
		idle = time.Minute
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, b := range t.buckets {
		if now.Sub(b.last) > idle {
			delete(t.buckets, k)
		}
	}
}

// Sweeper runs sweep on a ticker until ctx-less stop is called. Returned as a
// stop func rather than taking a context, because the caller here is main()
// and the only thing it ever does is run for the life of the process.
func (t *Throttle) Sweeper(every time.Duration) (stop func()) {
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				t.sweep()
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// throttleKey identifies who is asking: the member if there is one, the
// address otherwise.
//
// A member's own key survives them changing address, and — more to the point —
// keeps one member's behaviour off everyone else behind the same NAT.
func throttleKey(c *gin.Context) string {
	// c.Get, not sessions.Default: Default does a MustGet and PANICS when the
	// session middleware has not run. That is a live risk rather than a
	// theoretical one -- this guard is mounted per route group, and a group
	// registered before the session store (or a test that builds a bare
	// context) would take the whole request down instead of rate limiting it.
	if v, ok := c.Get(sessions.DefaultKey); ok {
		if sess, ok := v.(sessions.Session); ok && sess != nil {
			if v := sess.Get("user_id"); v != nil {
				switch id := v.(type) {
				case int64:
					if id > 0 {
						return "u:" + strconv.FormatInt(id, 10)
					}
				case int:
					if id > 0 {
						return "u:" + strconv.Itoa(id)
					}
				}
			}
		}
	}
	return "ip:" + c.ClientIP()
}

// Guard is the middleware for one tier.
//
// Refused requests answer 429 with Retry-After, which is the only part of this
// a well-behaved client reads. The headers go out on ALLOWED requests too: a
// scraper that honours X-RateLimit-Remaining slows itself down before being
// refused, and the ones that do not were never going to be stopped politely.
func (t *Throttle) Guard() gin.HandlerFunc {
	return func(c *gin.Context) {
		ok, retry := t.allow(throttleKey(c))
		if !ok && t.Exempt != nil && t.Exempt(c) {
			ok = true
		}
		if ok {
			c.Next()
			return
		}
		secs := int(retry.Seconds())
		if secs < 1 {
			secs = 1
		}
		c.Header("Retry-After", strconv.Itoa(secs))
		c.Header("X-RateLimit-Limit", strconv.Itoa(t.limit.Burst))
		c.Header("X-RateLimit-Remaining", "0")
		// Plain text and a real sentence. This is the one response a member
		// sees when the site refuses them for something they did not know was
		// a rule, so it says what happened, that it is temporary, and how long
		// — "Too Many Requests" alone reads like a fault.
		c.String(http.StatusTooManyRequests,
			"Too many requests. This is a rate limit, not an error: "+
				"wait %d second(s) and it will work again.", secs)
		c.Abort()
	}
}
