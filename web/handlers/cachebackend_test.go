package handlers

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// Which cache the site adopts at boot.
//
// Three outcomes, two of which only occur when something is wrong — which is
// exactly the shape that goes untested and then behaves unexpectedly on the day
// it matters. The consequence of getting it wrong is not a crash: the site
// comes up, serves pages, and is quietly slower or quietly inconsistent
// between replicas.

// captureLogs returns a logger writing into a buffer, and the buffer.
func captureLogs() (*slog.Logger, *strings.Builder) {
	var sb strings.Builder
	return slog.New(slog.NewTextHandler(&sb, &slog.HandlerOptions{Level: slog.LevelDebug})), &sb
}

func TestNoRedisAddressMeansTheMemoryCache(t *testing.T) {
	log, out := captureLogs()

	backend, client := chooseCache("", log)

	if backend == nil {
		t.Fatal("no cache backend at all; every cached read would panic")
	}
	if client != nil {
		t.Error("a Redis client was returned for an empty address")
	}
	if !strings.Contains(out.String(), "kind=memory") {
		t.Errorf("the backend choice was not logged as memory:\n%s", out)
	}
}

func TestAnUnreachableRedisFallsBackInsteadOfTakingTheSiteDown(t *testing.T) {
	// The failure this exists for. Adopting a dead Redis leaves the site up and
	// unusable: each cache read dials, retries and fails, so a page reading
	// four keys spends ten seconds before rendering — by which time the browser
	// has hung up. A degraded cache has to be fast about being degraded.
	//
	// Port 1 on loopback: reserved, never listening, and refused immediately
	// rather than left to time out.
	log, out := captureLogs()

	backend, client := chooseCache("127.0.0.1:1", log)

	if backend == nil {
		t.Fatal("no cache backend after the fallback")
	}
	if client != nil {
		t.Error("a client for an unreachable Redis was handed back; the core.Redis " +
			"seam would then point at a server the cache itself gave up on")
	}
	logged := out.String()
	if !strings.Contains(logged, "level=ERROR") {
		t.Errorf("falling back to memory was not logged as an error:\n%s", logged)
	}
	if !strings.Contains(logged, "127.0.0.1:1") {
		t.Errorf("the log does not name the address that failed:\n%s", logged)
	}
}

func TestTheFallbackCacheStillWorks(t *testing.T) {
	// Falling back must produce a WORKING cache, not merely a non-nil one.
	log, _ := captureLogs()
	backend, _ := chooseCache("127.0.0.1:1", log)

	ctx := context.Background()
	if err := backend.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("the fallback cache refused a write: %v", err)
	}
	got, ok, err := backend.Get(ctx, "k")
	if err != nil || !ok || string(got) != "v" {
		t.Errorf("the fallback cache did not store a value: got %q ok=%v err=%v", got, ok, err)
	}
}

// TestARealRedisIsAdopted needs an actual server.
//
// It reads REDIS_TEST_ADDR rather than REDIS_ADDR, deliberately: REDIS_ADDR is
// the operator's switch and is set in every compose file here, so reusing it
// would make this test silently exercise whatever Redis happened to be running
// on a developer's machine.
func TestARealRedisIsAdopted(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		if os.Getenv("CI") != "" {
			// In CI a missing address is a broken workflow, not a laptop
			// without Redis. Skipping there would mean the service container
			// could be removed and nothing would say so.
			t.Fatal("REDIS_TEST_ADDR is unset in CI: the redis service container " +
				"is missing from .github/workflows/ci.yml, and this test has " +
				"been silently skipping")
		}
		t.Skip("set REDIS_TEST_ADDR to test against a real Redis")
	}

	log, out := captureLogs()
	backend, client := chooseCache(addr, log)
	if client == nil {
		t.Fatalf("a reachable Redis at %s was not adopted:\n%s", addr, out)
	}
	t.Cleanup(func() { _ = client.Close() })

	if !strings.Contains(out.String(), "kind=redis") {
		t.Errorf("the backend choice was not logged as redis:\n%s", out)
	}

	// It is the REDIS cache, not memory wearing its name: write through the
	// cache and read the key back off the client itself.
	ctx := context.Background()
	if err := backend.Set(ctx, "loon-test-key", []byte("through the cache"), time.Minute); err != nil {
		t.Fatalf("writing through the redis cache failed: %v", err)
	}

	found := false
	for iter := client.Scan(ctx, 0, "*loon-test-key*", 100).Iterator(); iter.Next(ctx); {
		found = true
		_ = client.Del(ctx, iter.Val())
	}
	if !found {
		t.Error("a value written through the cache never reached Redis — the " +
			"backend is reporting itself as redis while storing in process memory")
	}
}
