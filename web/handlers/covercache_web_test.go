package handlers

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/the-loon-clan/loon/blob"
)

// pngBytes is a real, sniffable PNG — blob.SniffImage reads the CONTENT, so a
// fixture of arbitrary bytes would be rejected exactly like a bad download.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// cacheInto builds a coverCache writing to a temp dir, so tests never touch the
// real upload volume.
func cacheInto(t *testing.T, dir string) *coverCache {
	t.Helper()
	return &coverCache{
		files:    blob.NewLocal(dir, "/uploads"),
		http:     http.DefaultClient,
		prefix:   "/uploads",
		inflight: map[string]*coverFetch{},
	}
}

func TestLocalizeDownloadsAndRewrites(t *testing.T) {
	body := pngBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("no User-Agent — a free CDN blocks anonymous floods")
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	got := cacheInto(t, dir).localize(context.Background(), srv.URL+"/poster.jpg")

	if !strings.HasPrefix(got, "/uploads/"+coverDir+"/") {
		t.Fatalf("localize = %q, want a URL on this site", got)
	}
	// The file is really there, and it is the bytes we served.
	onDisk := filepath.Join(dir, coverDir, filepath.Base(got))
	saved, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("nothing written to %s: %v", onDisk, err)
	}
	if !bytes.Equal(saved, body) {
		t.Error("stored bytes differ from what was served")
	}
	// Sniffed, not taken from the URL: the source said .jpg and the content is
	// a PNG, so the stored name must say png.
	if !strings.HasSuffix(got, ".png") {
		t.Errorf("stored as %q — extension came from the URL, not the content", got)
	}
}

// Every failure has the same contract: return the remote URL untouched. A
// hotlinked cover beats no cover, and a release must never lose its art because
// a CDN hiccuped.
func TestLocalizeFallsBackToRemoteOnFailure(t *testing.T) {
	png := pngBytes(t)
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"404", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }},
		{"500", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }},
		{"empty body", func(w http.ResponseWriter, r *http.Request) {}},
		{"not an image", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html>error page served with a .jpg path</html>"))
		}},
		{"oversized", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(append(png, make([]byte, maxCoverBytes+1)...))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(c.handler)
			defer srv.Close()
			remote := srv.URL + "/poster.jpg"
			if got := cacheInto(t, t.TempDir()).localize(context.Background(), remote); got != remote {
				t.Errorf("localize = %q, want the remote URL back", got)
			}
		})
	}
}

// A URL is fetched ONCE however many callers ask at once. The match job runs a
// page of releases at a time and a season of episodes shares one poster, so
// without collapsing this is a burst against a free service.
func TestConcurrentLocalizeFetchesOnce(t *testing.T) {
	var hits int32
	body := pngBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := cacheInto(t, t.TempDir())
	remote := srv.URL + "/shared-poster.jpg"

	var wg sync.WaitGroup
	results := make([]string, 12)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = c.localize(context.Background(), remote)
		}(i)
	}
	wg.Wait()

	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("fetched %d times concurrently, want 1", n)
	}
	for i, r := range results {
		if r != results[0] {
			t.Errorf("caller %d got %q, caller 0 got %q — same URL must give one answer", i, r, results[0])
		}
	}
}

// Re-running a match must not re-download. A URL already pointing at this site
// is returned untouched — the seam that makes linkCover and Upsert both calling
// localize harmless.
func TestAlreadyLocalIsLeftAlone(t *testing.T) {
	c := cacheInto(t, t.TempDir())
	local := "/uploads/covers/abc123.png"
	if got := c.localize(context.Background(), local); got != local {
		t.Errorf("localize(%q) = %q, want it untouched", local, got)
	}
}

// Non-fetchable inputs pass straight through rather than erroring or blanking.
func TestLocalizeIgnoresWhatItCannotFetch(t *testing.T) {
	c := cacheInto(t, t.TempDir())
	for _, in := range []string{"", "not a url", "ftp://example.com/x.jpg", "data:image/png;base64,AAAA", "/relative/path.jpg"} {
		if got := c.localize(context.Background(), in); got != in {
			t.Errorf("localize(%q) = %q, want it unchanged", in, got)
		}
	}
}

// A nil cache must be safe: tests and any host path that builds a bare &web{}
// would otherwise panic on the first scraped cover.
func TestNilCacheIsSafe(t *testing.T) {
	var c *coverCache
	const u = "https://example.com/x.jpg"
	if got := c.localize(context.Background(), u); got != u {
		t.Errorf("nil cache returned %q", got)
	}
}

// The same URL always names the same file, and different URLs do not collide —
// which is what makes re-running the match job idempotent.
func TestCoverNameIsStableAndDistinct(t *testing.T) {
	a := coverName("https://static.tvmaze.com/uploads/images/original_untouched/501/1253519.jpg")
	if a != coverName("https://static.tvmaze.com/uploads/images/original_untouched/501/1253519.jpg") {
		t.Error("coverName is not stable for one URL")
	}
	if b := coverName("https://image.tmdb.org/t/p/w500/501/1253519.jpg"); a == b {
		t.Error("two providers' URLs produced the same name")
	}
}
