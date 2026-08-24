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
	"github.com/the-loon-clan/loon-site/internal/catalogchain"
	"github.com/the-loon-clan/loon/catalog"
	"github.com/the-loon-clan/loon/core"
	"strings"
)

// wireMetadataSources builds the shared catalog.Registry, registers whichever
// sources are configured, and publishes it as a core extension.
//
// Fatal on a failed registration on purpose: a host that cannot publish its
// registry will serve a catalogue with no art and no obvious reason why.
// tvmazeSrc is the TVmaze client, shared by the metadata chain and the TV
// calendar. Package level because the two are wired at different points in
// boot and neither owns the other.
var tvmazeSrc *tvmaze.Source

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

	// addChain registers several sources for ONE domain as a single source that
	// tries them in order. Unconfigured sources drop out; a chain with nothing
	// left registers nothing.
	addChain := func(domain string, named map[string]catalog.MetadataSource, order ...string) {
		live := map[string]catalog.MetadataSource{}
		for name, src := range named {
			if !isNilSource(src) {
				live[name] = src
			}
		}
		ch, err := catalogchain.New(live, order...)
		if err != nil {
			logger.Error("build catalog chain", "domain", domain, "err", err)
			return
		}
		if ch == nil {
			return // nothing configured for this domain
		}
		if err := reg.RegisterSource(ch); err != nil {
			logger.Error("register catalog chain", "domain", domain, "err", err)
			return
		}
		providers = append(providers, ch.Sources()...)
		logger.Info("catalog chain", "domain", domain, "sources", strings.Join(ch.Sources(), " -> "))
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
	// Chains, not either/or.
	//
	// The registry refuses a duplicate domain, so this used to be a choice made
	// at boot: TMDB when a key was set, TVmaze and Wikipedia when it was not.
	// The fallbacks were registered INSTEAD of the primary, which meant a title
	// TMDB had never heard of was simply not found — there was nothing behind
	// it.
	//
	// A chain is several sources presented to the registry as one, so the
	// one-per-domain rule is satisfied and the fallback happens inside. It also
	// CONFIRMS a hit before accepting it, which is the step NNTmux's movie
	// chain ends with and the one whose absence is invisible: a source that
	// answered is not a source that was right, and a wrong poster looks exactly
	// like a right one. See internal/catalogchain and docs/METADATA-METHODS.md.
	key := os.Getenv("TMDB_API_KEY")
	// Built once and kept, because it answers TWO questions. The chain below
	// asks it "what show is this release?"; the TV calendar asks it "what airs
	// on Tuesday?" (tvschedule_web.go). Constructing a second one would be a
	// second client under a shared, keyless rate limit -- two clients each
	// politely inside twenty calls per ten seconds and together outside it.
	tvmazeSrc = tvmaze.New("")
	addChain("movie", map[string]catalog.MetadataSource{
		"tmdb":      tmdb.New(key, tmdb.KindMovie, ""),
		"wikipedia": wikipedia.New(""),
	}, "tmdb", "wikipedia")
	addChain("tv", map[string]catalog.MetadataSource{
		"tmdb":   tmdb.New(key, tmdb.KindTV, ""),
		"tvmaze": tvmazeSrc,
	}, "tmdb", "tvmaze")
	// AniDB returns nil without a client name — which is what made this a
	// choice at all. It used to build itself regardless, hold the domain, and
	// answer every lookup from an empty index, leaving anime at 6.2% cover art
	// while television sat at 59%. Now AniList sits behind it rather than
	// replacing it.
	addChain("anime", map[string]catalog.MetadataSource{
		"anidb":   anidb.New(os.Getenv("ANIDB_CLIENT"), nil),
		"anilist": anilist.New(""),
	}, "anidb", "anilist")
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
