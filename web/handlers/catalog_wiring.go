package handlers

// Metadata sources: where cover art and catalogue facts come from.
//
// Its own phase because it is the one part of boot a host copying this file
// will almost certainly change — different keys, different providers, or none
// at all — and it should be readable without paging through the rest of the
// startup sequence.
//
// Nothing here escapes: the registry, the source list and the provider record
// are all consumed by the Register call at the end. That is what made this the
// easiest of the remaining phases to lift.

import (
	"log/slog"
	"os"

	"github.com/the-loon-clan/loon-plugins/scraper/sources/anidb"
	"github.com/the-loon-clan/loon-plugins/scraper/sources/anilist"
	"github.com/the-loon-clan/loon-plugins/scraper/sources/openlibrary"
	"github.com/the-loon-clan/loon-plugins/scraper/sources/theporndb"
	"github.com/the-loon-clan/loon-plugins/scraper/sources/tmdb"
	"github.com/the-loon-clan/loon-plugins/scraper/sources/tvmaze"
	"github.com/the-loon-clan/loon-plugins/scraper/sources/wikipedia"
	"github.com/the-loon-clan/loon/catalog"
	"github.com/the-loon-clan/loon/core"
)

// wireMetadataSources builds the shared catalog.Registry, registers whichever
// sources are configured, and publishes it as a core extension.
//
// Fatal on a failed registration on purpose: a host that cannot publish its
// registry will serve a catalogue with no art and no obvious reason why.
func wireMetadataSources(c *core.Core, logger *slog.Logger) {
	// The shared catalog.Registry + its metadata sources. Sources are idle until
	// their key/client is set via env (hook up now, test later):
	//   TPDB_API_KEY  → ThePornDB (xxx) · ANIDB_CLIENT → AniDB (anime)
	//   TMDB_API_KEY  → TMDB (movie + tv)
	// TMDB is registered TWICE off the one key: the registry keys a source by
	// its single Domain().Key, and the scraper routes Newznab 2xxx → "movie"
	// and 5xxx → "tv" as separate domains, so each needs its own instance.
	reg := catalog.NewRegistry()
	// providers records WHICH provider filled each slot, for the footer's
	// attribution. Domain alone cannot answer it — "movie" is TMDB with a key
	// and Wikipedia without — and crediting the wrong source is a false claim
	// about provenance rather than a missing one. See credits_web.go.
	var providers []string
	add := func(name string, src catalog.MetadataSource) bool {
		// isNilSource, not src == nil. The keyed constructors return a nil
		// *Source when their credential is unset, and a nil POINTER stored in
		// an INTERFACE is not a nil interface — so `src == nil` is false, the
		// source registers, and the registry calls Domain() on nil. That
		// panicked the whole process at boot: the site came up, served nothing,
		// and restart-looped. The previous code checked each constructor's
		// concrete result before it ever became an interface, which is why the
		// trap only appeared when the checks moved in here.
		if isNilSource(src) {
			return false
		}
		if err := reg.RegisterSource(src); err != nil {
			logger.Error("register catalog source", "provider", name, "err", err)
			return false
		}
		providers = append(providers, name)
		return true
	}

	add("theporndb", theporndb.New(os.Getenv("TPDB_API_KEY"), ""))

	// TMDB when a key is set; the keyless pair when it is not.
	//
	// TMDB serves BOTH "movie" and "tv", and catalog.Registry refuses a
	// duplicate domain — so registering the fallbacks alongside it would
	// silently drop one and which one would depend on call order. The choice is
	// therefore made explicitly.
	//
	// TMDB wins where available: backdrops, structured genres, and a far larger
	// non-English catalogue. Without it, TVmaze covers television and Wikipedia
	// covers film, so a host with no credentials at all still gets posters,
	// summaries and dates across the two biggest categories on the index
	// instead of blank cards — see docs/METADATA-SOURCES.md.
	tmdbOn := false
	for _, kind := range []tmdb.Kind{tmdb.KindMovie, tmdb.KindTV} {
		if add("tmdb", tmdb.New(os.Getenv("TMDB_API_KEY"), kind, "")) {
			tmdbOn = true
		}
	}
	if !tmdbOn {
		add("tvmaze", tvmaze.New(""))
		add("wikipedia", wikipedia.New(""))
	}
	// AniDB when a client name is registered; AniList when it is not.
	//
	// Both serve "anime" and the registry refuses a duplicate, so the choice is
	// made explicitly rather than left to call order. AniDB returns nil without
	// a client name, which is what makes this an if at all — it used to build
	// itself regardless, hold the domain, and answer every lookup from an empty
	// index, leaving anime at 6.2% cover art while television sat at 59%.
	if !add("anidb", anidb.New(os.Getenv("ANIDB_CLIENT"), nil)) {
		add("anilist", anilist.New(""))
	}
	// Open Library needs no credential, so it registers unconditionally and is
	// the one source a fresh checkout actually exercises — every other source
	// here is idle until an operator goes and gets a key, which meant the
	// enrichment path had no way to be tested at all without one.
	add("openlibrary", openlibrary.New(""))

	for _, s := range reg.Sources() {
		logger.Info("catalog source registered", "domain", s.Domain().Key, "priority", s.Domain().Priority)
	}
	// Credit them in the footer. A licence condition for TVmaze and Wikipedia
	// (both CC BY-SA) and for TMDB (its required disclaimer).
	setSourceCredits(providers)
	if err := c.Register(catalog.RegistryExtension, reg); err != nil {
		logger.Error("register catalog registry", "err", err)
		os.Exit(1)
	}
}
