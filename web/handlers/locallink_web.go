package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/lib/pq"

	"github.com/the-loon-clan/loon-plugins/scraper/sources/anilist"
	"github.com/the-loon-clan/loon-plugins/scraper/sources/tvmaze"
	"github.com/the-loon-clan/loon-plugins/scraper/sources/wikipedia"
	"github.com/the-loon-clan/loon-site/internal/storage"
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

// linkSpec is one kind of release this pass knows how to place: which
// top-level category it reads, which catalog kind it looks the release up in,
// its own sweep position, and how a release name reduces to the entry title.
//
// Two of them rather than one generic pass, because the reduction differs. A
// series name is what survives after the season/episode marker; a film name is
// what survives after the year — and the ENTRY side differs too, since
// Wikipedia disambiguates film articles and TVmaze does not.
type linkSpec struct {
	// catWhere is a SQL predicate on n.category_id. A constant in this file,
	// never user input, which is why it is interpolated.
	catWhere storage.SQL
	// kinds are the catalog kinds an entry may have. Anime accepts BOTH its
	// own and "tv": AniList files a show under anime, TVmaze files the same
	// show under tv, and either is a correct answer for the release.
	kinds     []string
	cursorKey string
	// keys returns the entry titles that would satisfy this release, already
	// normalised, most specific first. More than one because Wikipedia titles
	// carry a qualifier — see movieKeys.
	keys func(releaseTitle string) []catalogKey
}

func linkSpecs() []linkSpec {
	return []linkSpec{
		// Television EXCEPT anime, which has its own naming and its own source.
		{
			catWhere:  "n.category_id / 1000 = 5 AND n.category_id <> 5070",
			kinds:     []string{"tv"},
			cursorKey: "catalog_local_link_cursor",
			keys:      seriesKeys,
		},
		// Anime, parsed by the fansub convention rather than the scene one:
		// "[Erai-raws] Ragna Crimson - 04" defeats a season/episode parser
		// entirely, and TVmaze's reduction would hand back the group tag.
		{
			catWhere:  "n.category_id = 5070",
			kinds:     []string{"anime", "tv"},
			cursorKey: "catalog_local_link_anime_cursor",
			keys:      animeKeys,
		},
		{
			catWhere:  "n.category_id / 1000 = 2",
			kinds:     []string{"movie"},
			cursorKey: "catalog_local_link_movie_cursor",
			keys:      movieKeys,
		},
	}
}

// catalogKey is one acceptable form of a title, and what has to be true of the
// entry that matches it.
//
// Year is a CONSTRAINT, not a hint: 0 means "any entry with this name", and a
// non-zero value means only an entry whose own year agrees will do. That
// distinction is the whole reason this is a struct rather than a string —
// see seriesKeys.
type catalogKey struct {
	Norm string
	Year int
}

// animeKeys reduces an anime release name to its series.
func animeKeys(releaseTitle string) []catalogKey {
	norm := catalog.DefaultNormalize(anilist.ParseReleaseName(releaseTitle).Title)
	if norm == "" {
		return nil
	}
	return []catalogKey{{Norm: norm}}
}

// sceneYearSuffix matches a trailing four-digit year on a normalised name.
//
// Anchored and four digits exactly, because the alternative eats real titles:
// "Ben 10" is a series, "Ben 10 2016" is that series' reboot, and a rule that
// strips any trailing number turns the first into "ben".
var sceneYearSuffix = regexp.MustCompile(`^(.*?)[ ]((?:19|20)[0-9]{2})$`)

// seriesKeys reduces a TV release name to its series.
//
// Two forms, and the second is where most of the remaining gap was. A scene
// series name often carries a disambiguating year — "Monk.2002.S06E09" — and
// the catalogue does not: it holds "Monk" with 2002 in its year column. Of the
// twelve largest TV series here with zero covered releases, eight were exactly
// this, every one of them ALREADY in catalog_entry. Hundreds of releases sat
// uncovered beside the poster that belonged to them, and no API call could have
// helped, because nothing was wrong with either side — only with the key.
//
// The full form stays first: a catalogue that really does hold "Monk 2002"
// under that name should win over the bare one. The bare form then carries the
// year as a constraint rather than dropping it, because The Flash 1990 and The
// Flash 2014 are different shows, and a wrong poster is worse than none.
func seriesKeys(releaseTitle string) []catalogKey {
	norm := catalog.DefaultNormalize(tvmaze.ParseReleaseName(releaseTitle).Title)
	if norm == "" {
		return nil
	}
	keys := []catalogKey{{Norm: norm}}
	if m := sceneYearSuffix.FindStringSubmatch(norm); m != nil && m[1] != "" {
		year, _ := strconv.Atoi(m[2])
		keys = append(keys, catalogKey{Norm: m[1], Year: year})
	}
	return keys
}

// movieKeys reduces a film release name to the ways Wikipedia might have
// titled its article.
//
// Wikipedia disambiguates: 321 of the 1,029 film entries here are stored as
// "Aquaman (film)" or "Dhoom Dhaam (2025 film)" rather than the bare name, so
// a release parsing to "aquaman" matches none of them on equality alone.
//
// The forms are ENUMERATED and matched exactly rather than approached with a
// prefix or a LIKE, because "the crow" is a prefix of "the crow salvation" —
// a different film, and a wrong poster is worse than no poster.
func movieKeys(releaseTitle string) []catalogKey {
	q := wikipedia.ParseReleaseName(releaseTitle)
	norm := catalog.DefaultNormalize(q.Title)
	if norm == "" {
		return nil
	}
	keys := []catalogKey{{Norm: norm}, {Norm: norm + " film"}}
	if q.Year > 0 {
		keys = append(keys, catalogKey{Norm: fmt.Sprintf("%s %d film", norm, q.Year)})
	}
	return keys
}

// linkFromCatalog gives cover art to releases whose SERIES is already known,
// and returns how many it linked.
//
// The join is on the normalised series name, which is what catalog_entry
// already indexes (norm_title, written by DefaultNormalize). The release side
// has to be reduced to the same thing — a release name is a series name plus
// episode identity plus packaging — and that reduction is the source's own
// ParseReleaseName, not a second guess at it: matching on anything else would
// hand a release the poster of a show it merely resembles.
// linkFromCatalog runs every spec and returns how many releases it placed.
func (w *web) linkFromCatalog(ctx context.Context) (int, error) {
	total := 0
	for _, spec := range linkSpecs() {
		n, err := w.linkOneKind(ctx, spec)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// linkOneKind gives cover art to releases of one kind whose title is already
// known, and returns how many it linked.
//
// The join is on the normalised title, which is what catalog_entry already
// indexes (norm_title, written by DefaultNormalize). The release side has to be
// reduced to the same thing, and that reduction is the SOURCE's own
// ParseReleaseName rather than a second guess at it: the entry on the other
// side was created from that function's output, so a separate reduction would
// drift, and the failure mode is a release wearing another title's poster.
func (w *web) linkOneKind(ctx context.Context, spec linkSpec) (int, error) {
	if !w.db().Valid() || w.catalogCovers == nil {
		return 0, nil
	}
	cursor, err := w.sweepCursor(ctx, spec.cursorKey)
	if err != nil {
		return 0, err
	}
	// The cursor is not optional. "Uncovered" alone re-selects the same newest
	// rows every pass, and a release with no catalog entry is uncovered
	// forever — so the unmatchable ones pile up at the top of the window and
	// eventually fill it, at which point the pass runs every minute and links
	// nothing while matchable releases sit below it. Same trap as
	// catalogCandidates; same fix.
	// The suppression sits HERE, on the Go line, and not where it used to —
	// inside the string, spelled "//".
	//
	// SQL comments are "--". Postgres read "// sqllint:allow catWhere is…" as
	// statement text and stopped at the first thing it could not parse, which
	// was the colon:
	//
	//	pq: syntax error at or near ":"
	//
	// So this query had not run since the directive was added. Every tick of
	// the local-link sweep failed, once a minute, into a WARN that nothing
	// reads — and the pass that fails is the one that gives a release a poster
	// WITHOUT an API call, which is the cheapest coverage this site has.
	//
	// sqllint:allow catWhere is a constant in the linkSpecs table above; no request value reaches it
	q := storage.SQL(`SELECT n.id, n.title
		   FROM usenet.nzbs n
		   LEFT JOIN catalog.release_cover rc ON rc.release_id = n.id
		  WHERE rc.release_id IS NULL
		    AND `) + spec.catWhere + storage.SQL(`
		    AND n.id < $1::bigint
		  ORDER BY n.id DESC
		  LIMIT $2`)
	// sqllint:allow q is the constant above with catWhere spliced in; no request value reaches it
	rows, err := w.data.DB().QueryContext(ctx, q, cursor, localLinkBatch)
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
			//nolint:errcheck // closing a read after a scan failure: the scan
			// error is the one that matters and is returned below
			//nolint:errcheck // closing a read; rows.Err() is the real check
			_ = rows.Close()
			return 0, err
		}
		pending = append(pending, c)
	}
	//nolint:errcheck // closing a read; rows.Err() below is the real check
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Advance before doing the work, and wrap on a short page — the releases
	// this pass cannot link are exactly the ones that would otherwise hold the
	// window forever. The wrap brings them round again later, which is what
	// picks them up once the match job has learned their title.
	var lowest int64
	if len(pending) > 0 {
		lowest = pending[len(pending)-1].id
	}
	if err := w.setSweepCursor(ctx, spec.cursorKey,
		nextCandidateCursor(len(pending), localLinkBatch, lowest)); err != nil {
		return 0, err
	}

	linked := 0
	for _, c := range pending {
		if ctx.Err() != nil {
			return linked, ctx.Err()
		}
		keys := spec.keys(c.title)
		if len(keys) == 0 {
			continue
		}
		coverURL, err := w.coverForKeys(ctx, spec.kinds, keys)
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

// coverForKeys picks the cover for the first key that a catalogue entry
// satisfies, in the order the keys were offered.
//
// One query, then the choice in Go, rather than a query per key or one query
// with ANY(): the year constraint cannot be expressed as a set membership, and
// running the keys in order against the database would be several round trips
// per release for the ones that match nothing — which is most of them, on a
// sweep whose whole point is to be cheap.
//
// Exact match on the name, never a prefix: "the crow" is a prefix of "the crow
// salvation", a different film, and a wrong poster is worse than no poster.
func (w *web) coverForKeys(ctx context.Context, kinds []string, keys []catalogKey) (string, error) {
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		names = append(names, k.Norm)
	}
	type entry struct {
		Norm  string `db:"norm_title"`
		Year  int    `db:"year"`
		Cover string `db:"cover_url"`
	}
	var found []entry
	if err := w.data.DB().SelectContext(ctx, &found, `
		SELECT norm_title, year, cover_url
		  FROM catalog.catalog_entry
		 WHERE kind = ANY($1) AND norm_title = ANY($2) AND cover_url <> ''
		 ORDER BY updated_at DESC`, pq.Array(kinds), pq.Array(names)); err != nil {
		return "", err
	}
	for _, k := range keys {
		for _, e := range found {
			if e.Norm != k.Norm {
				continue
			}
			// A key with no year takes anything; a key WITH one requires the
			// entry to agree, or to have no year of its own to disagree with.
			// The Flash 1990 and The Flash 2014 are different shows, and this
			// is the line that keeps one from wearing the other's poster.
			if k.Year != 0 && e.Year != 0 && e.Year != k.Year {
				continue
			}
			return e.Cover, nil
		}
	}
	return "", nil
}

// runLocalLinks sweeps in the background for as long as the process lives.
//
// Separate from the scraper's match job on purpose. That job is paced by other
// people's rate limits; this one is not paced by anything, and tying it to a
// throttle it does not need would be inventing a bottleneck. It also runs
// BEFORE the network job gets its candidates, so the expensive pass is handed
// the releases that genuinely need an API call.
func (w *web) runLocalLinks(ctx context.Context, every time.Duration, log *slog.Logger) {
	if !w.db().Valid() || w.catalogCovers == nil {
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
