package middleware

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// The Lua script is the part of the shared limiter that cannot be unit tested:
// its correctness is Redis's atomicity and Redis's clock, and a fake has
// neither. So this runs against a real server.
//
// It reads REDIS_TEST_ADDR rather than REDIS_ADDR for the reason
// cachebackend_test.go gives: REDIS_ADDR is the operator's switch and is set in
// every compose file here, so reusing it would point this at whatever Redis
// happened to be running on somebody's machine — and this test writes keys.
func redisForTest(t *testing.T) redis.UniversalClient {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("REDIS_TEST_ADDR is unset in CI: the redis service container " +
				"is missing from the workflow, and this test has been silently skipping")
		}
		t.Skip("set REDIS_TEST_ADDR to test the shared limiter against a real Redis")
	}
	c := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		t.Fatalf("REDIS_TEST_ADDR=%s is set but not answering: %v", addr, err)
	}
	return c
}

func TestRedisThrottleSpendsAndRefuses(t *testing.T) {
	c := redisForTest(t)
	key := "spend-" + t.Name()
	defer c.Del(context.Background(), "throttle:test:"+key)

	th := NewRedisThrottle(Limit{Burst: 5, Every: time.Second}, c, "test")
	for i := 0; i < 5; i++ {
		if ok, _ := th.allow(key); !ok {
			t.Fatalf("refused request %d of a burst of 5", i+1)
		}
	}
	ok, retry := th.allow(key)
	if ok {
		t.Fatal("a sixth request passed an empty bucket")
	}
	if retry <= 0 || retry > time.Second {
		t.Fatalf("retry %v, want (0, 1s] — the script's retry arithmetic is wrong", retry)
	}
}

// THE POINT OF THE WHOLE FILE: two limiters, as two replicas would be, sharing
// one allowance. The in-process version passes ten here, which is the bug.
func TestTwoInstancesShareOneAllowance(t *testing.T) {
	c := redisForTest(t)
	key := "shared-" + t.Name()
	defer c.Del(context.Background(), "throttle:test:"+key)

	l := Limit{Burst: 5, Every: time.Minute}
	a := NewRedisThrottle(l, c, "test")
	b := NewRedisThrottle(l, c, "test")

	allowed := 0
	for i := 0; i < 5; i++ {
		if ok, _ := a.allow(key); ok {
			allowed++
		}
		if ok, _ := b.allow(key); ok {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("two instances allowed %d requests against a burst of 5; a shared "+
			"limiter that is not shared is the bug this exists to fix", allowed)
	}
}

// Atomicity. Concurrent callers must not both spend the last token, which is
// exactly what a read-then-write without a script would do.
func TestConcurrentSpendingIsAtomic(t *testing.T) {
	c := redisForTest(t)
	key := "atomic-" + t.Name()
	defer c.Del(context.Background(), "throttle:test:"+key)

	const burst = 20
	th := NewRedisThrottle(Limit{Burst: burst, Every: time.Minute}, c, "test")

	var mu sync.Mutex
	allowed := 0
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := th.allow(key); ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != burst {
		t.Fatalf("100 concurrent requests spent %d tokens from a bucket of %d; "+
			"the read-modify-write is not atomic", allowed, burst)
	}
}

// Tokens come back on Redis's clock, not the caller's.
func TestRedisBucketRefills(t *testing.T) {
	c := redisForTest(t)
	key := "refill-" + t.Name()
	defer c.Del(context.Background(), "throttle:test:"+key)

	th := NewRedisThrottle(Limit{Burst: 2, Every: 300 * time.Millisecond}, c, "test")
	th.allow(key)
	th.allow(key)
	if ok, _ := th.allow(key); ok {
		t.Fatal("a third request passed a bucket of 2")
	}
	time.Sleep(400 * time.Millisecond)
	if ok, _ := th.allow(key); !ok {
		t.Fatal("no token came back after more than one refill interval")
	}
}

// A dead Redis must degrade to the local table, not to letting everything
// through. Failing open here would remove the LOGIN limit at exactly the
// moment the site is already unwell.
func TestRedisFailureFallsBackRatherThanOpening(t *testing.T) {
	// A port nothing answers on. No REDIS_TEST_ADDR needed: the point is the
	// failure path, so this runs everywhere.
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	th := NewRedisThrottle(Limit{Burst: 3, Every: time.Minute}, dead, "test")

	var states []bool
	th.SetOnState(func(up bool, err error) { states = append(states, up) })

	allowed := 0
	for i := 0; i < 10; i++ {
		if ok, _ := th.allow("k"); ok {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("with Redis down, %d of 10 requests were allowed against a burst "+
			"of 3; the fallback must still limit, not fail open", allowed)
	}
	// And it must say so, ONCE, rather than per request.
	if len(states) != 1 || states[0] {
		t.Fatalf("state transitions %v, want exactly one report of 'down' — a "+
			"limiter that logs every failure buries the line saying it started", states)
	}
}
