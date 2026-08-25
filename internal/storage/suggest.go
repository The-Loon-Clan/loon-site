package storage

import (
	"context"
	"strings"
	"time"
)

// Search suggestions — what the quick-search box offers while you type.
//
// THEY COME FROM THE CATALOGUE, NOT FROM RELEASES, and that is the whole
// performance design rather than an optimisation of it. Measured on this
// database:
//
//	catalog.catalog_entry   12,079 rows    6-8ms   ILIKE, no index needed
//	usenet.nzbs            160,692 rows   40-50ms  ILIKE, would need pg_trgm
//
// Six times faster before any tuning, and it stays that way as the index grows:
// releases accumulate forever, while the catalogue grows with the number of
// distinct WORKS, which is a far slower curve.
//
// The 40ms figure is worse than it looks. That is 40ms of CPU per keystroke, so
// ten people typing at once saturates a core on suggestions alone — on the box
// that also has to serve the searches those keystrokes turn into.
//
// The suggestions are also the only READABLE option. A dropdown of release
// titles is eight lines of Show.S01E05.1080p.WEB-DL.DDP5.1.H.264-GRP, which is
// unusable on a desktop and impossible on a phone. The catalogue holds the
// names people actually type.

// Suggestion is one row of the dropdown.
type Suggestion struct {
	Title string `db:"title"`
	Kind  string `db:"kind"` // "tv", "movie" — whatever the catalogue chain recorded
	Year  int    `db:"year"`
}

// suggestLimit is how many rows the dropdown can show. Also the LIMIT, so the
// database never sorts more than is displayed.
const suggestLimit = 8

// suggestMinLen is the shortest query worth answering. One character matches
// most of the catalogue and helps nobody; it is rejected here as well as in the
// markup, because the markup is a suggestion and this is the rule.
const suggestMinLen = 2

// suggestTimeout caps the query. A dropdown that arrives after you have
// finished typing is worse than one that never arrived, so a slow answer is
// abandoned rather than shown late.
const suggestTimeout = 200 * time.Millisecond

// Suggest returns catalogue titles matching q, best-looking first.
//
// Ordering is deliberate and worth reading before changing:
//
//   - a title that STARTS with the query comes first. Somebody typing "star"
//     wants Star Trek before The Death Star, and a plain ILIKE cannot tell them
//     apart.
//   - then shorter titles, because a shorter title containing the query is more
//     likely to BE the thing: "Breaking Bad" over "Breaking Bad: The Movie".
//   - then newer, which breaks the remaining ties the way a viewer expects.
//
// The bool is "the query ran", not "there were results" — an empty dropdown and
// a broken one look identical to a reader, and only one of them should be
// logged.
func (st *Store) Suggest(ctx context.Context, q string) ([]Suggestion, bool) {
	q = strings.TrimSpace(q)
	if len([]rune(q)) < suggestMinLen || !st.db.Valid() {
		return nil, true
	}
	ctx, cancel := context.WithTimeout(ctx, suggestTimeout)
	defer cancel()

	// LIKE metacharacters are escaped rather than rejected: somebody typing
	// "100%" is asking a reasonable question, and a bare % here would match the
	// whole catalogue and then rank the results by nothing.
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)

	var out []Suggestion
	if err := st.db.SelectContext(ctx, &out, `
		SELECT title, kind, year
		  FROM catalog.catalog_entry
		 WHERE title ILIKE '%' || $1 || '%' ESCAPE '\'
		 ORDER BY (title ILIKE $1 || '%' ESCAPE '\') DESC,
		          length(title) ASC,
		          year DESC
		 LIMIT $2`, esc, suggestLimit); err != nil {
		return nil, false
	}
	return out, true
}

// CarriedShowIDs is the upstream id of every series this site has a catalogue
// entry for, in one namespace.
//
// For the TV calendar (web/handlers/tvschedule_web.go): the public schedule is
// about 140 episodes a day and a calendar showing all of them is a listings
// magazine. The useful question is the narrow one an indexer can act on --
// something we carry has a new episode -- and this is the set that answers it.
//
// A map because the caller tests membership per episode across a fortnight of
// them; a slice would be a linear scan a few thousand times.
//
// Constant SQL with the namespace as a PARAMETER: scripts/sqllint.py refuses
// anything assembled, and a namespace is exactly the kind of value that looks
// safe to concatenate right up until it comes from a config file.
func (st *Store) CarriedShowIDs(ctx context.Context, namespace string) (map[string]bool, error) {
	var ids []string
	// TWO sources, UNION'd, because a carried show's id in this namespace can
	// live in either place. A show first seen FROM this namespace has it on
	// the entry (ext_namespace = $1). A show first seen from ANOTHER source --
	// TMDB is the preferred TV metadata source, and then the entry's namespace
	// is 'tmdb' -- keeps this namespace's id in catalog_external instead. The
	// first version read only the entry, so on a TMDB-primary host every
	// carried show had ext_namespace='tmdb' and the schedule (keyed on tvmaze
	// ids) matched NONE of them, silently emptying the whole TV pipeline for
	// the documented preferred configuration. The sibling TVCrossIDs already
	// went through catalog_external for exactly this reason.
	if err := st.db.SelectContext(ctx, &ids, `
		SELECT ext_id
		  FROM catalog.catalog_entry
		 WHERE kind = 'tv' AND ext_namespace = $1 AND ext_id <> ''
		UNION
		SELECT x.value
		  FROM catalog.catalog_external x
		  JOIN catalog.catalog_entry e ON e.id = x.entry_id
		 WHERE e.kind = 'tv' AND x.namespace = $1 AND x.value <> ''`, namespace); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// TVCrossIDs resolves a carried show's other identities from its TVmaze id.
//
// For the tracker search (web/handlers/tvgapsadmin_web.go): EZTV answers only
// an IMDb id, and a gap arrives carrying the TVmaze one, because that is what
// the schedule provider speaks. The catalog's cross-id table already holds
// both sides of the translation -- 10,627 imdb rows at last count -- so this
// is a self-join, not a network call.
//
// Both joins go through catalog_external rather than reading ext_id off the
// entry: an entry's own namespace says where it was FIRST seen, and a show
// discovered via TMDB still carries its tvmaze id in the external table.
func (st *Store) TVCrossIDs(ctx context.Context, tvmazeID string) (imdb, tvdb string, err error) {
	var rows []struct {
		Namespace string `db:"namespace"`
		Value     string `db:"value"`
	}
	if err := st.db.SelectContext(ctx, &rows, `
		SELECT x2.namespace, x2.value
		  FROM catalog.catalog_external x1
		  JOIN catalog.catalog_external x2 ON x2.entry_id = x1.entry_id
		 WHERE x1.namespace = 'tvmaze' AND x1.value = $1
		   AND x2.namespace IN ('imdb', 'tvdb')`, tvmazeID); err != nil {
		return "", "", err
	}
	for _, r := range rows {
		switch r.Namespace {
		case "imdb":
			imdb = r.Value
		case "tvdb":
			tvdb = r.Value
		}
	}
	return imdb, tvdb, nil
}
