package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/the-loon-clan/loon-plugins/scraper/sources/tvmaze"
	"github.com/the-loon-clan/loon/catalog"
)

// Linking releases to catalog entries we ALREADY have, without asking anyone.
//
// The match job asks a source per release. But TVmaze answers per SERIES, and
// an index is mostly siblings: 87,717 uncovered TV releases here reduce to a
// few thousand shows, and 32,144 of them (36.6%) belong to a show already
// sitting in catalog_entry from some earlier episode. Every one of those was
// queued behind a network call for an answer already in the database.
//
// Gullak is the shape of it: 67 releases on this index, three with cover art,
// one catalog entry, and 64 episodes waiting their turn through a 600ms
// throttle for a poster the site already had.
//
// So this runs first and locally. It costs one indexed query per release and
// no API call at all, it makes the network job smaller by exactly the number it
// solves, and it compounds — every series the match job learns hands this pass
// all of that series' other episodes for free.

// localLinkBatch is how many releases one pass considers. Larger than the
// match job's batch because the work is a keyed lookup rather than a throttled
// request; the limit here is politeness to the database, not to a provider.
const localLinkBatch = 5000

// linkFromCatalog gives cover art to releases whose SERIES is already known,
// and returns how many it linked.
//
// The join is on the normalised series name, which is what catalog_entry
// already indexes (norm_title, written by DefaultNormalize). The release side
// has to be reduced to the same thing — a release name is a series name plus
// episode identity plus packaging — and that reduction is the source's own
// ParseReleaseName, not a second guess at it: matching on anything else would
// hand a release the poster of a show it merely resembles.
func (w *web) linkFromCatalog(ctx context.Context) (int, error) {
	if usersDB == nil || w.catalogCovers == nil {
		return 0, nil
	}
	rows, err := usersDB.QueryContext(ctx,
		`SELECT n.id, n.title
		   FROM usenet.nzbs n
		   LEFT JOIN catalog.release_cover rc ON rc.release_id = n.id
		  WHERE rc.release_id IS NULL
		    AND n.category_id / 1000 = 5
		  ORDER BY n.id DESC
		  LIMIT $1`, localLinkBatch)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id    int64
		title string
	}
	var pending []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.title); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	linked := 0
	for _, c := range pending {
		if ctx.Err() != nil {
			return linked, ctx.Err()
		}
		series := tvmaze.ParseReleaseName(c.title).Title
		if series == "" {
			continue
		}
		norm := catalog.DefaultNormalize(series)
		if norm == "" {
			continue
		}
		var coverURL string
		// kind='tv' because a film and a series can share a name, and the
		// release is already categorised as television by the caller's filter.
		// Newest entry wins, matching releaseArt's tie-break.
		err := usersDB.QueryRowContext(ctx,
			`SELECT cover_url FROM catalog.catalog_entry
			  WHERE kind = 'tv' AND norm_title = $1 AND cover_url <> ''
			  ORDER BY updated_at DESC LIMIT 1`, norm).Scan(&coverURL)
		if err != nil || coverURL == "" {
			continue
		}
		if err := w.linkCover(ctx, c.id, coverURL); err != nil {
			continue
		}
		linked++
	}
	return linked, nil
}

// runLocalLinks sweeps in the background for as long as the process lives.
//
// Separate from the scraper's match job on purpose. That job is paced by other
// people's rate limits; this one is not paced by anything, and tying it to a
// throttle it does not need would be inventing a bottleneck. It also runs
// BEFORE the network job gets its candidates, so the expensive pass is handed
// the releases that genuinely need an API call.
func (w *web) runLocalLinks(ctx context.Context, every time.Duration, log *slog.Logger) {
	if usersDB == nil || w.catalogCovers == nil {
		return
	}
	// A short first delay so a boot does not race the migrations, then settle
	// into the interval.
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		n, err := w.linkFromCatalog(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			log.Warn("local catalog link failed", "err", err)
		case n > 0:
			log.Info("linked releases to catalog entries already held", "count", n)
		}
		timer.Reset(every)
	}
}
