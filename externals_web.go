package site

import (
	"context"
	"net/url"
	"strings"
)

// Links out to the databases a release was matched against.
//
// A release page that says "matched on TVmaze" and gives you no way to get
// there is asking you to retype the title into a search box. These are the
// buttons that close that gap: IMDb, TheTVDB, AniDB, Wikipedia, whichever ids
// we actually hold.
//
// The ids come from two places, unioned:
//
//   - catalog_entry's own (ext_namespace, ext_id) — the source that produced
//     the row. Present on every entry ever scraped.
//   - catalog_external — the cross-ids a source hands back about OTHER
//     databases. Only populated since 003_external_ids.sql, so older entries
//     grow these as they are re-scraped.
//
// Reading both means the page shows something for every matched release today,
// rather than staying blank until a full re-scrape has been through.

// externalLink is one rendered button.
type externalLink struct {
	Label string
	URL   string
}

// externalSites maps a namespace to a link builder, in the order the buttons
// should render. A slice rather than a map because map iteration order is
// random and the buttons would reshuffle on every page load.
//
// kind ("movie", "tv", "anime", "book", "xxx") is passed in because some sites
// key the path off the media type — TMDB's film and series live under
// different roots and the id alone does not say which.
var externalSites = []struct {
	Namespace string
	Label     string
	Build     func(kind, id string) string
}{
	{"imdb", "IMDb", func(_, id string) string {
		// IMDb ids are conventionally stored with the "tt" prefix, but not
		// every source includes it.
		if !strings.HasPrefix(id, "tt") {
			id = "tt" + id
		}
		return "https://www.imdb.com/title/" + url.PathEscape(id) + "/"
	}},
	{"tmdb", "TMDB", func(kind, id string) string {
		root := "movie"
		if kind == "tv" || kind == "anime" {
			root = "tv"
		}
		return "https://www.themoviedb.org/" + root + "/" + url.PathEscape(id)
	}},
	{"tvdb", "TheTVDB", func(kind, id string) string {
		// The dereferrer resolves a bare numeric id without needing the slug.
		root := "series"
		if kind == "movie" {
			root = "movie"
		}
		return "https://thetvdb.com/dereferrer/" + root + "/" + url.PathEscape(id)
	}},
	{"tvmaze", "TVmaze", func(_, id string) string {
		return "https://www.tvmaze.com/shows/" + url.PathEscape(id)
	}},
	{"anidb", "AniDB", func(_, id string) string {
		return "https://anidb.net/anime/" + url.PathEscape(id)
	}},
	{"anilist", "AniList", func(_, id string) string {
		return "https://anilist.co/anime/" + url.PathEscape(id)
	}},
	{"mal", "MyAnimeList", func(_, id string) string {
		return "https://myanimelist.net/anime/" + url.PathEscape(id)
	}},
	{"letterboxd", "Letterboxd", func(_, id string) string {
		// The /tmdb/ route, because the id here IS a TMDB id — see
		// buildExternalLinks. letterboxd.com/film/<number>/ 404s; that route
		// wants the slug, which Wikidata does not carry.
		return "https://letterboxd.com/tmdb/" + url.PathEscape(id) + "/"
	}},
	{"rottentomatoes", "Rotten Tomatoes", func(_, id string) string {
		// Wikidata stores this WITH its section prefix ("m/matrix"), so the
		// path is the id — do not escape the slash out of it.
		return "https://www.rottentomatoes.com/" + id
	}},
	{"metacritic", "Metacritic", func(_, id string) string {
		return "https://www.metacritic.com/" + id
	}},
	{"wikidata", "Wikidata", func(_, id string) string {
		return "https://www.wikidata.org/wiki/" + url.PathEscape(id)
	}},
	{"wikipedia", "Wikipedia", func(_, id string) string {
		return "https://en.wikipedia.org/wiki/" + url.PathEscape(id)
	}},
	{"openlibrary", "Open Library", func(_, id string) string {
		return "https://openlibrary.org/works/" + url.PathEscape(id)
	}},
	{"isbn", "ISBN", func(_, id string) string {
		return "https://openlibrary.org/isbn/" + url.PathEscape(id)
	}},
	{"musicbrainz", "MusicBrainz", func(_, id string) string {
		return "https://musicbrainz.org/release-group/" + url.PathEscape(id)
	}},
	{"tpdb", "ThePornDB", func(_, id string) string {
		return "https://theporndb.net/scenes/" + url.PathEscape(id)
	}},
}

// releaseExternals returns the outbound database links for a release, found via
// the cover URL that links it to its catalog entry.
//
// Joined on cover_url because that is the link the catalog already maintains
// between a release and its entry — the same join releaseArt uses. It is a
// weak key, so the newest entry wins rather than an arbitrary one.
func (w *web) releaseExternals(ctx context.Context, coverURL string) []externalLink {
	if coverURL == "" || usersDB == nil {
		return nil
	}
	rows, err := usersDB.QueryContext(ctx,
		`WITH e AS (
		     SELECT id, kind, ext_namespace, ext_id
		       FROM catalog.catalog_entry
		      WHERE cover_url = $1
		      ORDER BY updated_at DESC
		      LIMIT 1
		 )
		 SELECT e.kind, e.ext_namespace, e.ext_id FROM e
		 UNION ALL
		 SELECT e.kind, x.namespace, x.value
		   FROM e JOIN catalog.catalog_external x ON x.entry_id = e.id`, coverURL)
	if err != nil {
		return nil
	}
	defer rows.Close()

	kind := ""
	ids := map[string]string{}
	for rows.Next() {
		var k, ns, v string
		if err := rows.Scan(&k, &ns, &v); err != nil {
			return nil
		}
		kind = k
		if ns != "" && v != "" {
			ids[ns] = v
		}
	}
	if rows.Err() != nil {
		return nil
	}
	return buildExternalLinks(kind, ids)
}

// buildExternalLinks turns a namespace→id map into ordered buttons. Split from
// the query so the ordering and URL shapes are testable without a database.
func buildExternalLinks(kind string, ids map[string]string) []externalLink {
	// Letterboxd is DERIVED, not stored. Wikidata's Letterboxd property is a
	// bare number that its /film/ route rejects, but /tmdb/<id>/ resolves for
	// every film — so the button is built from the TMDB id when there is one.
	// Films only: Letterboxd does not catalogue television.
	if kind == "movie" && ids["letterboxd"] == "" && ids["tmdb"] != "" {
		// Copy first: callers own the map they passed, and silently growing it
		// would surprise anyone who reads it afterwards.
		copied := make(map[string]string, len(ids)+1)
		for k, v := range ids {
			copied[k] = v
		}
		copied["letterboxd"] = ids["tmdb"]
		ids = copied
	}
	out := make([]externalLink, 0, len(ids))
	for _, site := range externalSites {
		id, ok := ids[site.Namespace]
		if !ok || id == "" {
			continue
		}
		out = append(out, externalLink{Label: site.Label, URL: site.Build(kind, id)})
	}
	return out
}
