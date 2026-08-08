package main

import (
	"context"
	"time"

	"github.com/the-loon-clan/loon/catalog"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
	"github.com/the-loon-clan/loon-plugins/scraper"
)

// Host side of the scraper enrichment flow. scraper.SetDeps runs BEFORE Boot,
// but the catalog capability (the sink + cover store) only exists AFTER Boot —
// so these resolve lazily off the web struct, whose catalog fields main.go
// fills once Boot has run. The scraper's jobs run post-Boot, so by call time
// the fields are set.

// lazySink is a pluginapi.CatalogSink that forwards to the catalog plugin once
// it's resolved.
type lazySink struct{ w *web }

func (l lazySink) Upsert(ctx context.Context, e catalog.CatalogEntry) error {
	if l.w.catalogSink == nil {
		return nil
	}
	// Pull the art down before storing the entry, so the catalogue holds a URL
	// on this site rather than one on the provider's CDN. See covercache_web.go
	// — on failure this returns the remote URL unchanged, and a hotlinked cover
	// beats no cover.
	e.CoverURL = l.w.covers.localize(ctx, e.CoverURL)
	// The wide art too. A source that carries a banner and a background (TVmaze
	// does) would otherwise have those hotlinked while its poster was local —
	// half a privacy guarantee, and half a defence against link rot, which is
	// neither.
	for _, key := range artFields {
		if u, ok := e.Fields[key].(string); ok && u != "" {
			if got := l.w.covers.localize(ctx, u); got != "" {
				e.Fields[key] = got
			}
		}
	}
	return l.w.catalogSink.Upsert(ctx, e)
}

// catalogCandidates yields recent releases for the scraper's Catalog Match job.
func (w *web) catalogCandidates(ctx context.Context) ([]scraper.Candidate, error) {
	if w.usenet == nil {
		return nil, nil
	}
	rs, err := w.usenet.Browse(ctx, "", 200)
	if err != nil {
		return nil, err
	}
	out := make([]scraper.Candidate, 0, len(rs))
	for _, r := range rs {
		out = append(out, scraper.Candidate{ID: r.ID, Title: r.Title, Category: r.CategoryID})
	}
	return out, nil
}

// linkCover records a matched cover for a release (read back on the release page).
//
// The image is downloaded and re-pointed at this site first — see
// covercache_web.go for why hotlinking was not good enough. Same call as the
// sink above, and it is cheap to repeat: the second caller for a URL waits on
// the first rather than fetching again, and a URL already stored locally is
// returned untouched.
func (w *web) linkCover(ctx context.Context, releaseID int64, coverURL string) error {
	if w.catalogCovers == nil {
		return nil
	}
	resolved := w.covers.localize(ctx, coverURL)
	if resolved == "" {
		// Strict-local mode with a failed download. Storing "" would replace a
		// cover the release may already have with a blank one, so leave the row
		// alone and let a later match try again.
		return nil
	}
	return w.catalogCovers.SetReleaseCover(ctx, releaseID, resolved)
}

// ── cover art for a whole page of releases ──────────────────────────

// releaseCovers looks up cover art for many release ids at once.
//
// The capability is resolved LAZILY here rather than stored on the web struct:
// main.go type-asserts pluginapi.CatalogCovers off the catalog plugin at boot
// (views.go's catalogCovers field), and pluginapi.CatalogCoverBatch is an
// OPTIONAL second interface on the same object — so we feature-detect it at the
// call site, the same convention as pluginapi.Fillable / scraper.Searcher /
// catalog.CrossIDResolver. A type assertion on a concrete value is nanoseconds;
// the fallback is what matters.
//
// FALLBACK: when the catalog plugin in this build predates the batch interface,
// this loops ReleaseCover per id — correct, just N round trips instead of 1. A
// caller must therefore keep its id slice page-sized (a poster strip + a
// listing + a sidebar, ~30), which is exactly what the home page passes.
//
// Ids with no cover are absent from the returned map (a blank stored cover
// counts as absent, matching ReleaseCover's ok=false). A failed lookup returns
// an empty map, never an error: a page missing its posters still renders.
func (w *web) releaseCovers(ctx context.Context, ids []int64) map[int64]string {
	if w.catalogCovers == nil || len(ids) == 0 {
		return nil
	}
	if b, ok := w.catalogCovers.(pluginapi.CatalogCoverBatch); ok {
		covers, err := b.ReleaseCovers(ctx, ids)
		if err != nil {
			w.logger().Error("batch cover lookup", "ids", len(ids), "err", err)
			return nil
		}
		return covers
	}
	// Per-id fallback. Dedup first — the batch path dedups internally, so
	// callers are allowed to concatenate their id slices without filtering.
	out := make(map[int64]string, len(ids))
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		coverURL, has, err := w.catalogCovers.ReleaseCover(ctx, id)
		if err != nil {
			w.logger().Error("cover lookup", "release", id, "err", err)
			continue
		}
		if has && coverURL != "" {
			out[id] = coverURL
		}
	}
	return out
}

// attachCovers fills searchRow.Cover for a whole listing in ONE lookup. Rows
// with no cover keep an empty string — the template renders its gradient
// fallback tile for those, never a broken <img>.
func (w *web) attachCovers(ctx context.Context, rows []searchRow) {
	if w.catalogCovers == nil || len(rows) == 0 {
		return
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	covers := w.releaseCovers(ctx, ids)
	for i := range rows {
		rows[i].Cover = covers[rows[i].ID]
	}
}

// ── home page: catalog-derived blocks ───────────────────────────────

// homeCatsKey caches the enabled taxonomy for the home page. The set only
// changes when an admin toggles a category, so the TTL is generous compared to
// the release blocks.
const homeCatsKey = "home:cats:v1"

// homeCategories returns the admin-enabled top-level categories (each with its
// subcats). ok is false when the read failed or nothing is enabled.
//
// The home page now uses only the COUNT of these, for the stat strip's
// "Categories" tile — the genre-pill row this used to fill was mockup furniture
// and is gone with the UNIT3D block stack. /browse reads the taxonomy through
// its own path (browse() → catalog.Enabled), so category browsing does not
// depend on this call and the list is still returned in full for any caller
// that wants it.
func (w *web) homeCategories(ctx context.Context) ([]pluginapi.Category, bool) {
	var cats []pluginapi.Category
	if w.cacheGet(ctx, homeCatsKey, &cats) {
		return cats, len(cats) > 0
	}
	cats, err := w.catalog.Enabled(ctx)
	if err != nil {
		w.logger().Error("home categories", "err", err)
		return nil, false
	}
	w.cacheSet(ctx, homeCatsKey, cats, 5*time.Minute)
	return cats, len(cats) > 0
}
