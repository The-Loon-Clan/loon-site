package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"

	"github.com/the-loon-clan/loon/blob"
)

// Cover art, downloaded and served from here rather than hotlinked.
//
// The scraper matches a release to a metadata entry and gets back a cover URL
// on the provider's CDN — image.tmdb.org, static.tvmaze.com,
// covers.openlibrary.org. Storing that URL is one line and costs nothing, and
// it was what the site did. Three things are wrong with it:
//
//   - LINK ROT. A provider that re-paths or removes an image breaks it
//     permanently and silently. Nothing re-checks a stored URL, so the failure
//     surfaces as a broken image on a page nobody is looking at.
//   - VISITOR PRIVACY. Every browse page told three third-party CDNs which
//     releases each visitor was reading, from the visitor's own IP.
//   - THEIR BANDWIDTH, OUR TRAFFIC. A popular listing page turns into a burst
//     of requests against a free service that never agreed to serve them.
//
// So covers are fetched ONCE, stored under the mounted upload volume, and
// served from this site. The provider is asked for each image a single time no
// matter how many visitors see it.
//
// The remote URL is kept as the fallback: if a download fails the release still
// gets its cover, hotlinked, rather than no cover at all. A partial local cache
// is strictly better than an empty one.

// artFields are the CatalogEntry.Fields keys that hold an image URL.
//
// CoverURL is the poster and has its own field on the entry; a source may also
// return the wide shapes a release page wants and those arrive in Fields —
// TVmaze carries a banner and a background alongside its poster. Listed here so
// the download path covers them all: a deployment that stored its posters
// locally while hotlinking its banners would have half a privacy guarantee and
// half a defence against link rot, which is neither.
var artFields = []string{"banner_url", "background_url"}

const (
	// coverDir is the subdirectory under uploadRoot. Mounted (uploads:/data)
	// and already served under uploadURL — see wiki_web.go for the pairing.
	coverDir = "covers"
	// maxCoverBytes bounds one download. Posters run 50-400 KB; 8 MB is far
	// above anything legitimate and stops a hostile or broken endpoint from
	// filling the volume. io.LimitReader enforces it during the read, so the
	// bound holds whatever Content-Length claimed.
	maxCoverBytes = 8 << 20
	coverTimeout  = 30 * time.Second
)

// coverCache downloads cover art to local storage.
type coverCache struct {
	files  blob.Store
	http   *http.Client
	prefix string // public URL prefix the blob store reports under

	// inflight collapses concurrent requests for the SAME url. The match job
	// runs over a page of releases at a time and a season's worth of episodes
	// share one poster, so without this a single image is fetched a dozen times
	// in parallel — the exact burst this cache exists to prevent.
	mu       sync.Mutex
	inflight map[string]*coverFetch
}

type coverFetch struct {
	done sync.WaitGroup
	url  string
	err  error
}

func newCoverCache() *coverCache {
	return &coverCache{
		files:    blob.NewLocal(uploadRoot, uploadURL),
		http:     &http.Client{Timeout: coverTimeout},
		prefix:   uploadURL,
		inflight: map[string]*coverFetch{},
	}
}

// localize resolves a provider's cover URL to whatever the operator's cover
// mode says should be stored — see covermode_web.go. It returns "" only when
// the mode is strictly local and the download failed; every other path yields
// a usable URL.
func (c *coverCache) localize(ctx context.Context, remote string) string {
	if c == nil || remote == "" {
		return remote
	}
	// Already ours (a re-run over a release cached earlier). Returned in every
	// mode, including remote-only: re-hotlinking art we already hold would
	// discard a local copy and start asking the provider for it again.
	if strings.HasPrefix(remote, c.prefix) {
		return remote
	}
	u, err := url.Parse(remote)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		// Not something this can fetch, so there is nothing to decide.
		return remote
	}

	switch coverMode() {
	case CoverRemote:
		return remote
	case CoverRemoteLocal:
		// Hotlink, but only a URL that actually answers. Link rot is the one
		// failure hotlinking cannot see on its own — nothing re-checks a stored
		// URL, and the break surfaces as a missing image later.
		if c.reachable(ctx, remote) {
			return remote
		}
		// Unreachable: fall through and try to store a copy. Usually futile
		// (a 404 does not become a download) but free to attempt, and it
		// rescues the case where HEAD is refused while GET works.
	}

	name := coverName(remote)

	// One fetch per URL, however many callers ask at once.
	c.mu.Lock()
	f, running := c.inflight[remote]
	if !running {
		f = &coverFetch{}
		f.done.Add(1)
		c.inflight[remote] = f
	}
	c.mu.Unlock()

	if running {
		f.done.Wait()
		if f.err != nil {
			return onCoverFailure(remote)
		}
		return f.url
	}

	local, err := c.fetch(ctx, remote, name)
	f.url, f.err = local, err
	f.done.Done()

	c.mu.Lock()
	delete(c.inflight, remote)
	c.mu.Unlock()

	if err != nil {
		return onCoverFailure(remote)
	}
	return local
}

// onCoverFailure answers what to store when a download fails.
//
// Strict-local means it: no cover rather than a third-party request from a
// visitor's browser, which is the entire guarantee that mode exists to make.
// Every other mode keeps the remote URL, because a hotlinked cover beats a
// blank one.
func onCoverFailure(remote string) string {
	if coverMode() == CoverLocal {
		return ""
	}
	return remote
}

// reachable reports whether a remote URL answers, for CoverRemoteLocal.
//
// HEAD, not GET: the question is only "does this still exist", and pulling the
// image body to answer it would download exactly what this mode is trying to
// avoid downloading. A server that refuses HEAD reads as unreachable, which is
// the safe direction — the caller then tries a real fetch and stores a local
// copy instead of a URL it could not confirm.
func (c *coverCache) reachable(ctx context.Context, remote string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, remote, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "loon-demo-site/1.0 (+cover check)")
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	return resp.StatusCode == http.StatusOK
}

// fetch downloads one image and stores it, returning the local URL.
func (c *coverCache) fetch(ctx context.Context, remote, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remote, nil)
	if err != nil {
		return "", err
	}
	// Identify ourselves for the same reason the sources do: a free service
	// blocks anonymous floods, and being identifiable is what lets them ask us
	// to stop instead.
	req.Header.Set("User-Agent", "loon-demo-site/1.0 (+cover cache)")
	req.Header.Set("Accept", "image/*")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("cover fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cover fetch: status %d", resp.StatusCode)
	}
	// +1 so a file exactly at the cap is detected as over it rather than
	// silently truncated into a corrupt image.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverBytes+1))
	if err != nil {
		return "", fmt.Errorf("cover read: %w", err)
	}
	if len(data) > maxCoverBytes {
		return "", fmt.Errorf("cover too large (> %d bytes)", maxCoverBytes)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("cover empty")
	}
	// Sniff the CONTENT, never trust the URL's extension or the declared
	// Content-Type: this writes a file the site then serves, and a provider
	// answering an HTML error page with a .jpg path would otherwise be stored
	// and served as an image.
	mime, ext, err := blob.SniffImage(data)
	if err != nil {
		return "", fmt.Errorf("cover is not a usable image (%s): %w", mime, err)
	}
	return c.files.Save(ctx, path.Join(coverDir, name+ext), data)
}

// backfillCovers pulls down the art of releases matched BEFORE local caching
// existed, whose stored URL still points at a provider.
//
// It is needed because nothing else will reach them. Covers are localised on
// WRITE, and the scraper's match job only walks recent releases — so a release
// matched last week keeps its remote URL forever, and "download everything
// locally" would have meant "download whatever happens to be matched next".
//
// Deliberately slow. There is no deadline here: the images have survived as
// hotlinks this long, and a burst against a free CDN to fix a cosmetic
// inconsistency would be a worse citizen than the hotlinking it replaces. One
// image per tick, and the run simply ends when the context does.
func (w *web) backfillCovers(ctx context.Context, every time.Duration, log *slog.Logger) {
	if w.catalogCovers == nil || w.covers == nil {
		return
	}
	tick := time.NewTicker(every)
	defer tick.Stop()

	// skip holds ids this run could not convert, so one dead URL cannot stall
	// the walk on the same row forever. Per-run and local: a fresh boot retries
	// them, which is right — the usual reason is a transient provider error.
	skip := []int64{}
	var done, failed int
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		id, remote, ok := nextRemoteCover(ctx, skip)
		if !ok {
			if done > 0 || failed > 0 {
				log.Info("cover backfill complete", "localised", done, "left_remote", failed)
			}
			return // nothing left that can be converted
		}
		local := w.covers.localize(ctx, remote)
		if local == remote {
			failed++
			skip = append(skip, id)
			continue
		}
		if err := w.catalogCovers.SetReleaseCover(ctx, id, local); err != nil {
			log.Warn("cover backfill store", "release", id, "err", err)
			failed++
			skip = append(skip, id)
			continue
		}
		done++
	}
}

// nextRemoteCover finds one release whose cover is still remote, ignoring ids
// this run has already failed on.
//
// The catalog plugin owns this table and the host reads it directly, because
// the capability exposes no "list by shape" call — and adding one to a shared
// plugin for what is a one-time migration is the larger change.
func nextRemoteCover(ctx context.Context, skip []int64) (int64, string, bool) {
	var (
		id     int64
		remote string
	)
	err := usersDB.QueryRowContext(ctx,
		`SELECT release_id, cover_url FROM catalog.release_cover
		  WHERE cover_url NOT LIKE '/uploads/%' AND NOT (release_id = ANY($1))
		  ORDER BY release_id LIMIT 1`, pq.Array(skip)).Scan(&id, &remote)
	if err != nil {
		return 0, "", false
	}
	return id, remote, true
}

// coverName is the stable on-disk name for a remote URL: the hash of the URL,
// so the same image downloads once and re-running a match is idempotent.
//
// Hashed rather than derived from the remote path because provider paths
// collide by design — TVmaze serves every poster from
// "…/original_untouched/<n>/<n>.jpg" and TMDB's are bare ids, so two providers
// meet in the same filename sooner than is comfortable. The URL is the
// identity.
func coverName(remote string) string {
	sum := sha256.Sum256([]byte(remote))
	return hex.EncodeToString(sum[:12])
}
