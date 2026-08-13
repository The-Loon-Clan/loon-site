package handlers

import (
	"context"
	"database/sql"
	"math"
	"strconv"
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

// Upsert stores a catalogue entry, caching its cover art locally on the way.
//
// Lazy because the real sink is not known when the scraper is wired: a nil
// sink here means the host has not registered one, and dropping the entry is
// correct rather than an error.
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

// candidateCursorKey remembers how far down the index the match sweep has got.
const candidateCursorKey = "catalog_match_cursor"

// candidateBatch is how many releases one Catalog Match run considers.
//
// Timed rather than guessed, and the timing overturned the obvious answer. A
// run of 1,000 took over 55 minutes against an hourly interval, which looks
// like an argument for a SMALLER batch — until you read ServiceLoop: it runs
// the tick to completion and THEN sleeps the interval. Runs cannot overlap,
// and the 60 minutes is idle time between them.
//
// So the sleep is a fixed cost amortised over whatever the batch is:
//
//	1,000 →  ~55m work + 60m idle = 1,000 per 115m  ≈  8.7/min
//	3,000 → ~165m work + 60m idle = 3,000 per 225m  ≈ 13.3/min
//
// The REQUEST rate is identical either way — the sources' own throttles set
// that — so a larger batch is not less polite, just less idle.
//
// Not larger still: the cursor advances when candidates are fetched, so a
// process restart skips a batch until the sweep wraps, and a very long run
// makes that skip proportionally worse. 3,000 takes most of the gain and
// keeps that window survivable.
const candidateBatch = 3000

// catalogCandidates yields releases for the scraper's Catalog Match job.
//
// This used to be Browse(ctx, "", 200) — the newest 200 releases, every run,
// forever. It re-asked the same questions each hour and never looked below
// that window, which is why 739 of 116,493 releases had cover art: 0.63%.
// Everything built on catalog data (posters, backdrops, the external-database
// buttons) inherited that ceiling.
//
// So: releases with no cover yet, walking DOWN a cursor through the whole
// index and wrapping at the bottom.
//
// The cursor is the part that matters. "Releases with no cover" alone looks
// sufficient and is not: a release that CANNOT be matched never gets a cover,
// so it qualifies again on the next run, and the sweep jams against the first
// batch of unmatchable titles and never reaches the rest of the index. The
// cursor steps past them; the wrap brings them round again later, which is
// what retries a title that failed only because a source was down.
func (w *web) catalogCandidates(ctx context.Context) ([]scraper.Candidate, error) {
	cursor, err := w.sweepCursor(ctx, candidateCursorKey)
	if err != nil {
		return nil, err
	}
	rows, err := w.data.DB().QueryContext(ctx,
		`SELECT n.id, n.title, n.category_id
		   FROM usenet.nzbs n
		   LEFT JOIN catalog.release_cover rc ON rc.release_id = n.id
		  -- ::bigint because usenet.nzbs.id is an INTEGER: without the cast
		  -- Postgres types the parameter from the column and the "start at the
		  -- top" sentinel overflows with 22003, which failed this whole sweep
		  -- silently — the job logged its CPU time and matched nothing.
		  WHERE rc.release_id IS NULL AND n.id < $1::bigint
		  ORDER BY n.id DESC
		  LIMIT $2`, cursor, candidateBatch)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]scraper.Candidate, 0, candidateBatch)
	var lowest int64
	for rows.Next() {
		var c scraper.Candidate
		if err := rows.Scan(&c.ID, &c.Title, &c.Category); err != nil {
			return nil, err
		}
		out = append(out, c)
		lowest = c.ID
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	_ = w.setSweepCursor(ctx, candidateCursorKey, nextCandidateCursor(len(out), candidateBatch, lowest))
	return out, nil
}

// nextCandidateCursor decides where the next sweep starts.
//
// A full page means there is more below: carry on from the lowest id seen. A
// short page means the bottom of the index, so wrap to 0 — the next run starts
// at the newest again and picks up whatever arrived meanwhile, and brings the
// unmatchable titles round for another try.
func nextCandidateCursor(returned, batch int, lowest int64) int64 {
	if returned < batch {
		return 0
	}
	return lowest
}

// sweepCursor reads a named sweep's position. Zero — unset, or just wrapped —
// means "start at the top", expressed as an id above any real one.
//
// Shared by every descending sweep over nzbs (the match candidates here, the
// local catalog links in locallink_web.go). One implementation because they
// need identical wrap behaviour, and because a second copy is where the two
// would quietly stop agreeing.
func (w *web) sweepCursor(ctx context.Context, key string) (int64, error) {
	var v sql.NullString
	err := w.data.DB().QueryRowContext(ctx,
		`SELECT value FROM site_settings WHERE key = $1`, key).Scan(&v)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	n, _ := strconv.ParseInt(v.String, 10, 64)
	if n <= 0 {
		return math.MaxInt64, nil
	}
	return n, nil
}

func (w *web) setSweepCursor(ctx context.Context, key string, id int64) error {
	_, err := w.data.DB().ExecContext(ctx,
		`INSERT INTO site_settings (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		key, strconv.FormatInt(id, 10))
	return err
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

// releaseArt returns the wide art for a release: the banner and the background
// a source stored alongside the poster (TVmaze carries both).
//
// Joined on the COVER URL, which needs explaining. release_cover holds
// (release_id, cover_url) and catalog_entry holds the entry; nothing links
// them, because the scraper writes both from the same match and never needed
// to. The cover URL is that shared value — identical in both rows by
// construction, including after the download rewrites it — so it is the join.
//
// It behaves correctly where a naive id link would not: a season of episodes
// shares one poster, so every episode resolves to its series' banner, which is
// the answer a release page wants. The cost is that a release whose poster
// happens to equal another entry's would borrow its art; providers serve
// per-title images, so that means "the same title", which is the same answer
// again.
//
// A missing row, an unmatched release or an entry with no wide art are all the
// same empty result — the page renders without a backdrop, which is the normal
// case for most of the catalogue.
func (w *web) releaseArt(ctx context.Context, coverURL string) (banner, background string) {
	if coverURL == "" || !w.data.DB().Valid() {
		return "", ""
	}
	var b, bg sql.NullString
	err := w.data.DB().QueryRowContext(ctx,
		`SELECT fields->>'banner_url', fields->>'background_url'
		   FROM catalog.catalog_entry
		  WHERE cover_url = $1 AND fields IS NOT NULL
		  ORDER BY updated_at DESC LIMIT 1`, coverURL).Scan(&b, &bg)
	if err != nil {
		return "", ""
	}
	return b.String, bg.String
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
