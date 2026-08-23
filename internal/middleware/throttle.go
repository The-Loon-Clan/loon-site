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

// store is where a bucket's state lives. Two implementations: in this process
// (memStore, below) and in Redis (throttle_redis.go).
//
// An interface rather than a flag, because the two differ in more than where
// the number is kept -- the Redis one has a failure mode and a fallback, and
// folding that into an if-statement inside allow() would put a network call's
// error handling in the middle of an arithmetic function.
type store interface {
	take(key string, l Limit) (allowed bool, retry time.Duration)
	give(key string, l Limit)
}

// Throttle is a limiter for one tier. Separate instances rather than one map
// with a tier in the key, so the auth tier's table cannot be filled up by
// browsing traffic.
type Throttle struct {
	limit Limit
	store store
	mem   *memStore // the local table: the store itself, or the Redis fallback

	// Skip decides, BEFORE a token is spent, that this request is not this
	// tier's business at all. Different from Exempt, which is about who is
	// asking; this is about what they asked for.
	//
	// It exists because the browse tier covered /login. Browse heavily enough
	// to empty the bucket -- a shared office address, a VPN exit, a crawler --
	// and the login PAGE is refused too, so the one action that would identify
	// you and lift the limit is the one action you cannot take. A member
	// behind a busy NAT would see a site that had locked them out with no way
	// back in. The auth tier governs those routes and governs them harder;
	// they should not also be charged for reading.
	Skip func(*gin.Context) bool

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
}

// NewThrottle builds a limiter whose buckets live in THIS process.
//
// Correct for one instance and wrong for several: two replicas each keep their
// own table, so the effective limit is the tier multiplied by the number of
// replicas. NewRedisThrottle is the shared version.
func NewThrottle(l Limit) *Throttle {
	m := newMemStore()
	return &Throttle{limit: l, store: m, mem: m}
}

// allow spends a token for key, and reports how long until one is free when
// there is none.
func (t *Throttle) allow(key string) (bool, time.Duration) {
	if t.limit.Burst <= 0 || t.limit.Every <= 0 {
		return true, 0 // an unconfigured tier lets everything through
	}
	return t.store.take(key, t.limit)
}

// Refund returns one token to this caller's bucket.
//
// THE AUTH TIER IS A BRUTE-FORCE BUDGET, and a brute-force budget should be
// spent by FAILURES. Charging a correct password the same as a wrong one means
// eight successful logins in a row lock the ninth out -- which is not an attack
// pattern, it is a shared address, an operator's tooling, or a household. It
// broke the site's own audit scripts within an hour of the limiter landing:
// each signs in once, so running the suite twice exhausted the tier and the
// next script reported "could not sign in".
//
// So the middleware still spends on every attempt -- it has to, it runs before
// anyone knows the outcome -- and the handler hands the token back when the
// password was right. Anything that fails keeps the charge.
func (t *Throttle) Refund(c *gin.Context) {
	if t == nil || t.limit.Burst <= 0 || t.limit.Every <= 0 {
		return
	}
	t.store.give(throttleKey(c), t.limit)
}

// memStore keeps buckets in a map in this process.
type memStore struct {
	now func() time.Time // injectable so the tests do not sleep

	mu      sync.Mutex
	buckets map[string]*bucket

	// max bounds the table. An attacker rotating source addresses would
	// otherwise turn a rate limiter into a memory exhaustion primitive, which
	// is a poor trade for the thing it was defending. At the cap the table is
	// dropped whole: the alternative is an LRU, and an LRU under exactly this
	// attack evicts the honest entries and keeps the attacker's.
	max int
}

func newMemStore() *memStore {
	return &memStore{now: time.Now, buckets: map[string]*bucket{}, max: 50000}
}

func (m *memStore) take(key string, l Limit) (bool, time.Duration) {
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.buckets) >= m.max {
		m.buckets = map[string]*bucket{}
	}
	b, ok := m.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(l.Burst), last: now}
		m.buckets[key] = b
	}
	// Refill for the time since the last look, capped at full.
	if d := now.Sub(b.last); d > 0 {
		b.tokens += d.Seconds() / l.Every.Seconds()
		if b.tokens > float64(l.Burst) {
			b.tokens = float64(l.Burst)
		}
		b.last = now
	}
	if b.tokens < 1 {
		// Time until the bucket holds one whole token.
		need := 1 - b.tokens
		return false, time.Duration(need * float64(l.Every))
	}
	b.tokens--
	return true, 0
}

func (m *memStore) give(key string, l Limit) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.buckets[key]
	if !ok {
		return // nothing spent here; nothing to give back
	}
	if b.tokens += 1; b.tokens > float64(l.Burst) {
		b.tokens = float64(l.Burst)
	}
}

// sweep drops buckets that have been full and untouched, so an address seen
// once does not occupy memory forever. Full means the holder has spent
// nothing recently, so forgetting them changes no decision.
func (m *memStore) sweep(l Limit) {
	now := m.now()
	idle := time.Duration(l.Burst) * l.Every * 2
	if idle < time.Minute {
		idle = time.Minute
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, b := range m.buckets {
		if now.Sub(b.last) > idle {
			delete(m.buckets, k)
		}
	}
}

// sweep is on Throttle too, because the local table needs sweeping whether it
// is the store or only the fallback.
func (t *Throttle) sweep() { t.mem.sweep(t.limit) }

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
		if t.Skip != nil && t.Skip(c) {
			c.Next()
			return
		}
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
