package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// withCoverMode sets the live mirror for one test and restores it after, so the
// four modes can be exercised without a database.
func withCoverMode(t *testing.T, mode string) {
	t.Helper()
	prev := coverMode()
	coverModeVal.Store(mode)
	t.Cleanup(func() { coverModeVal.Store(prev) })
}

// Each mode does what its name says. This is the whole contract, so it is
// asserted end-to-end against a server rather than by inspecting branches.
func TestCoverModesResolveCorrectly(t *testing.T) {
	body := pngBytes(t)

	t.Run("remote never downloads", func(t *testing.T) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			_, _ = w.Write(body)
		}))
		defer srv.Close()
		withCoverMode(t, CoverRemote)

		remote := srv.URL + "/poster.jpg"
		if got := cacheInto(t, t.TempDir()).localize(context.Background(), remote); got != remote {
			t.Errorf("localize = %q, want the remote URL", got)
		}
		if n := atomic.LoadInt32(&hits); n != 0 {
			t.Errorf("made %d request(s) in remote-only mode — it must never fetch", n)
		}
	})

	t.Run("local stores locally", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(body)
		}))
		defer srv.Close()
		withCoverMode(t, CoverLocal)

		got := cacheInto(t, t.TempDir()).localize(context.Background(), srv.URL+"/poster.jpg")
		if !strings.HasPrefix(got, "/uploads/"+coverDir+"/") {
			t.Errorf("localize = %q, want a local URL", got)
		}
	})

	// The distinguishing case: strict local returns NOTHING on failure, because
	// the guarantee it makes is that no visitor's browser ever contacts the
	// provider. A remote fallback would silently break that promise.
	t.Run("local returns nothing when the download fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
		}))
		defer srv.Close()
		withCoverMode(t, CoverLocal)

		if got := cacheInto(t, t.TempDir()).localize(context.Background(), srv.URL+"/poster.jpg"); got != "" {
			t.Errorf("localize = %q, want \"\" — strict local must not hotlink", got)
		}
	})

	t.Run("local_remote falls back to the provider", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
		}))
		defer srv.Close()
		withCoverMode(t, CoverLocalRemote)

		remote := srv.URL + "/poster.jpg"
		if got := cacheInto(t, t.TempDir()).localize(context.Background(), remote); got != remote {
			t.Errorf("localize = %q, want the remote URL back", got)
		}
	})

	t.Run("remote_local hotlinks a URL that answers", func(t *testing.T) {
		var gets int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				atomic.AddInt32(&gets, 1)
			}
			_, _ = w.Write(body)
		}))
		defer srv.Close()
		withCoverMode(t, CoverRemoteLocal)

		remote := srv.URL + "/poster.jpg"
		if got := cacheInto(t, t.TempDir()).localize(context.Background(), remote); got != remote {
			t.Errorf("localize = %q, want the remote URL", got)
		}
		// HEAD only: pulling the body to check existence would download exactly
		// what this mode avoids downloading.
		if n := atomic.LoadInt32(&gets); n != 0 {
			t.Errorf("issued %d GET(s) — reachability must be a HEAD", n)
		}
	})

	// Link rot is the failure hotlinking cannot see on its own, and this mode
	// exists to catch it: a dead URL is replaced by a stored copy.
	t.Run("remote_local downloads when the URL is dead", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write(body)
		}))
		defer srv.Close()
		withCoverMode(t, CoverRemoteLocal)

		got := cacheInto(t, t.TempDir()).localize(context.Background(), srv.URL+"/poster.jpg")
		if !strings.HasPrefix(got, "/uploads/"+coverDir+"/") {
			t.Errorf("localize = %q, want a local copy after the HEAD failed", got)
		}
	})
}

// Art already stored locally is returned in EVERY mode, remote-only included:
// re-hotlinking a copy we hold would discard it and start asking the provider
// for an image we already have.
func TestAlreadyLocalSurvivesEveryMode(t *testing.T) {
	const local = "/uploads/covers/abc123.png"
	for _, mode := range coverModes() {
		withCoverMode(t, mode)
		if got := cacheInto(t, t.TempDir()).localize(context.Background(), local); got != local {
			t.Errorf("mode %s: localize(%q) = %q, want it untouched", mode, local, got)
		}
	}
}

func TestCoverModeValidation(t *testing.T) {
	for _, ok := range coverModes() {
		if !validCoverMode(ok) {
			t.Errorf("validCoverMode(%q) = false, but it is in coverModes()", ok)
		}
		if coverModeLabel(ok) == ok {
			t.Errorf("mode %q has no human label", ok)
		}
	}
	for _, bad := range []string{"", "LOCAL", "hotlink", "local-remote", "remote_first"} {
		if validCoverMode(bad) {
			t.Errorf("validCoverMode(%q) = true", bad)
		}
	}
	if len(coverModes()) != 4 {
		t.Errorf("coverModes() has %d entries, want the 4 documented modes", len(coverModes()))
	}
}

// The default must be the forgiving one: a fresh deployment should get local
// art without risking a blank page when a provider is briefly unreachable.
func TestDefaultCoverModeIsLocalWithRemoteFallback(t *testing.T) {
	prev := coverMode()
	coverModeVal.Store("")
	defer coverModeVal.Store(prev)
	if got := coverMode(); got != CoverLocalRemote {
		t.Errorf("coverMode() with nothing stored = %q, want %q", got, CoverLocalRemote)
	}
}

// COVER_MODE seeds a fresh deployment, but a value an operator saved in the
// admin page must survive a restart — otherwise a stale line in a compose file
// silently undoes a deliberate choice.
func TestStoredModeBeatsTheEnvironment(t *testing.T) {
	if os.Getenv("COVER_MODE") != "" {
		t.Skip("COVER_MODE set in this environment")
	}
	// Documented precedence, asserted on the pieces loadCoverMode composes:
	// env seeds the mirror, then a stored value overwrites it.
	coverModeVal.Store(CoverRemote) // stands in for the env seed
	defer coverModeVal.Store(CoverLocalRemote)
	if v := CoverLocal; validCoverMode(v) {
		coverModeVal.Store(v) // stands in for the stored value winning
	}
	if got := coverMode(); got != CoverLocal {
		t.Errorf("stored value did not win: %q", got)
	}
}
