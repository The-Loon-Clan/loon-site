package middleware

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// A token bucket that lives in Redis, so every replica spends from the same
// allowance.
//
// WHY THIS EXISTS. The in-process version is correct for one instance and
// quietly wrong for several: two replicas keep two tables, so a member gets
// the tier twice and an attacker behind a round-robin gets it once per
// backend. The limit an operator configured is not the limit they got, and
// nothing anywhere says so — which is the failure mode this whole file family
// keeps being written against.
//
// WHY A LUA SCRIPT. Read-modify-write on a bucket has to be atomic or two
// concurrent requests both read "1 token left" and both spend it. Redis runs a
// script to completion with nothing interleaved, so the read, the refill, the
// spend and the write are one step. Doing it with WATCH/MULTI instead would be
// three round trips and a retry loop on every page view.
//
// WHY REDIS'S OWN CLOCK. The script asks Redis for the time rather than taking
// it from the caller, and that is the whole reason the shared version is worth
// having. Passing each instance's clock in would mean an instance whose clock
// runs sixty seconds fast computes a sixty-second refill on every request and
// hands out a full bucket every time — the limiter would be disabled for
// whichever backend happened to be skewed, and it would look like it was
// working. One authoritative clock, and skew stops mattering.
//
// Needs Redis 5 or newer, where scripts replicate by effects and a
// non-deterministic command like TIME is allowed before a write.
const throttleLua = `
local burst = tonumber(ARGV[1])
local every = tonumber(ARGV[2])
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local d = redis.call('HMGET', KEYS[1], 'n', 's')
local tokens = tonumber(d[1])
local seen = tonumber(d[2])
if tokens == nil or seen == nil then
  tokens = burst
  seen = now
end
if now > seen then
  tokens = math.min(burst, tokens + (now - seen) / every)
  seen = now
end
local allowed = 0
local retry = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  retry = math.ceil((1 - tokens) * every)
end
redis.call('HSET', KEYS[1], 'n', tokens, 's', seen)
-- Live only as long as a full refill could take. A key nobody touches again
-- is a key whose bucket would be full anyway, so expiring it decides nothing
-- differently and keeps the keyspace proportional to ACTIVE callers rather
-- than to every address ever seen.
redis.call('PEXPIRE', KEYS[1], math.ceil(burst * every) + 1000)
return {allowed, retry}
`

var throttleScript = redis.NewScript(throttleLua)

// refundLua hands one token back, capped at full. Used when an attempt the
// middleware already charged for turns out not to have been an attack -- see
// Throttle.Refund. A missing key means the bucket has already expired, which
// means it was full, which means there is nothing to give back.
const refundLua = `
local burst = tonumber(ARGV[1])
local n = tonumber(redis.call('HGET', KEYS[1], 'n'))
if n == nil then return 0 end
n = math.min(burst, n + 1)
redis.call('HSET', KEYS[1], 'n', n)
return 1
`

var refundScript = redis.NewScript(refundLua)

// redisStore spends tokens in Redis, and falls back to a local table when it
// cannot.
//
// FALLING BACK RATHER THAN FAILING OPEN, and rather than failing closed. Open
// would mean a Redis outage removes the login rate limit — the one tier that
// exists because repeating the request is the attack — at exactly the moment
// the site is already unwell. Closed would mean a Redis outage takes the site
// down harder than any attack this guards. The local table is neither: the
// limit still applies, it just applies per replica until Redis is back, which
// is precisely the in-process version everyone ran before this file existed.
type redisStore struct {
	client redis.UniversalClient
	fallen *memStore
	prefix string

	// down tracks Redis health so the report happens on the TRANSITION rather
	// than per request. A limiter that logs every failure logs once per page
	// view during an outage, which buries the line saying it started.
	down atomic.Bool
	// OnState is called when Redis starts or stops answering. The host wires a
	// logger; nil is silent, which is the right default for a package that
	// must not decide how a host logs.
	OnState func(up bool, err error)
}

// NewRedisThrottle builds a limiter whose buckets are shared through Redis.
//
// A nil client returns the in-process limiter instead of a broken one: a host
// without Redis (Core.Redis is nil by design) gets the behaviour it had, not a
// nil dereference on its first request.
func NewRedisThrottle(l Limit, client redis.UniversalClient, name string) *Throttle {
	m := newMemStore()
	if client == nil {
		return &Throttle{limit: l, store: m, mem: m}
	}
	return &Throttle{
		limit: l,
		store: &redisStore{client: client, fallen: m, prefix: "throttle:" + name + ":"},
		mem:   m,
	}
}

// SetOnState wires the host's logger to the Redis up/down transition. No-op on
// an in-process limiter, so a caller does not have to know which it built.
func (t *Throttle) SetOnState(f func(up bool, err error)) {
	if rs, ok := t.store.(*redisStore); ok {
		rs.OnState = f
	}
}

func (r *redisStore) take(key string, l Limit) (bool, time.Duration) {
	// Short: this runs on every request, and a limiter that makes a slow page
	// slower is one an operator turns off. A Redis that cannot answer in
	// 200ms is a Redis this should stop waiting for.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	res, err := throttleScript.Run(ctx, r.client,
		[]string{r.prefix + key},
		l.Burst, l.Every.Milliseconds(),
	).Result()
	if err != nil {
		r.note(false, err)
		return r.fallen.take(key, l)
	}
	vals, ok := res.([]any)
	if !ok || len(vals) != 2 {
		// A well-formed reply is part of the contract; a malformed one means
		// the script is not the script we think it is.
		r.note(false, errUnexpectedReply)
		return r.fallen.take(key, l)
	}
	allowed, _ := vals[0].(int64)
	retryMS, _ := vals[1].(int64)
	r.note(true, nil)
	return allowed == 1, time.Duration(retryMS) * time.Millisecond
}

func (r *redisStore) give(key string, l Limit) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := refundScript.Run(ctx, r.client,
		[]string{r.prefix + key}, l.Burst).Err(); err != nil {
		// A refund that does not happen costs the caller one token out of a
		// burst, so this is reported like any other Redis failure and then
		// dropped rather than retried on the login path.
		r.note(false, err)
		r.fallen.give(key, l)
	}
}

func (r *redisStore) note(up bool, err error) {
	if r.down.Load() == !up {
		return // no change
	}
	r.down.Store(!up)
	if r.OnState != nil {
		r.OnState(up, err)
	}
}

type throttleError string

func (e throttleError) Error() string { return string(e) }

const errUnexpectedReply = throttleError(
	"rate-limit script returned something other than {allowed, retry}")
