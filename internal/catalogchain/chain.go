// Package catalogchain tries several metadata sources for one domain, in
// order, and confirms a hit before accepting it.
//
// catalog.Registry keys a source by its Domain().Key and refuses a duplicate,
// so a host gets exactly ONE source per domain and has to choose at boot: TMDB
// when a key is set, TVmaze and Wikipedia when it is not. There is no "try
// TMDB, fall back to TVmaze" — the fallbacks are registered INSTEAD, and a
// source that is present but has nothing for a particular release is the end of
// the line.
//
// NNTmux does the other thing, and it is the better shape for an indexer whose
// input is a release name of uncertain quality:
//
//	searchLocalDatabase → IMDb → OMDb → Trakt → TMDB → similarityPercent
//
// A Chain is that: an ordered list of sources presented to the registry as one
// source, so the registry's one-per-domain rule is satisfied and the fallback
// happens inside.
//
// # Confirming the hit
//
// The last step of NNTmux's chain is the one a naive implementation leaves out.
// A search that returns SOMETHING is not a search that returned the right
// thing, and the failure is silent and permanent: a wrong poster looks exactly
// like a right one, and nothing downstream ever disagrees with it.
//
// So a candidate is fetched and its title compared with the release title that
// was searched for. Below the threshold the chain keeps looking rather than
// accepting what it has.
package catalogchain

import (
	"context"
	"fmt"
	"strings"

	"github.com/the-loon-clan/loon/catalog"
)

// idStride namespaces each source's ids within the chain.
//
// Fetch takes a LOCAL id, and local ids collide: TVmaze 42 and TMDB 42 are
// different programmes. The chain hands out sourceIndex*idStride + localID so a
// later Fetch can route back to the source the id came from, and unpacks it on
// the way in.
//
// 1e12 because local ids are database keys in the millions at most, and a
// chain has a handful of sources — so neither half can reach the other. A
// source returning an id at or above the stride is refused rather than
// silently aliased onto its neighbour.
const idStride int64 = 1_000_000_000_000

// minSimilarity is how close a candidate's title must be to the release title.
//
// 0.6 is deliberately forgiving: release names carry years, resolutions, group
// tags and punctuation the catalogue does not, so an exact match is rare even
// when the answer is right. It exists to reject the confidently wrong — a
// search for "The Thing" answered with "Thing Called Love" — rather than to
// grade near misses.
const minSimilarity = 0.6

// Chain presents several sources for one domain as a single source.
type Chain struct {
	domain  catalog.DomainInfo
	sources []catalog.MetadataSource
	names   []string // for logging, parallel to sources

	// minSim is the confirmation threshold; zero means minSimilarity.
	minSim float64
}

// New builds a chain. The FIRST source decides the domain — every source in a
// chain must serve the same one, which is checked here rather than discovered
// when a lookup returns something from the wrong catalogue.
func New(named map[string]catalog.MetadataSource, order ...string) (*Chain, error) {
	c := &Chain{}
	for _, name := range order {
		src := named[name]
		if src == nil {
			continue // an unconfigured source is absent, not an error
		}
		if len(c.sources) == 0 {
			c.domain = src.Domain()
		} else if got := src.Domain().Key; got != c.domain.Key {
			return nil, fmt.Errorf("catalogchain: %s serves domain %q, chain is %q",
				name, got, c.domain.Key)
		}
		c.sources = append(c.sources, src)
		c.names = append(c.names, name)
	}
	if len(c.sources) == 0 {
		return nil, nil // nothing configured: the caller registers nothing
	}
	return c, nil
}

// Sources names what ended up in the chain, in order, for the boot log and the
// credits footer — a host should be able to see which fallbacks are live
// without reading the environment.
func (c *Chain) Sources() []string { return append([]string(nil), c.names...) }

func (c *Chain) Domain() catalog.DomainInfo { return c.domain }

// Normalize uses the FIRST source's policy.
//
// Normalisation decides what the title index is keyed by, so a chain cannot mix
// policies without the later sources' keys being unreachable. The first source
// is the primary; its policy is the chain's.
func (c *Chain) Normalize(raw string) string { return c.sources[0].Normalize(raw) }

// TitleIndex merges every source's index, EARLIER SOURCES WINNING.
//
// A title both TMDB and TVmaze know should resolve to TMDB's entry, because
// that is what the order means. Merging the other way round would make the
// order decorative.
func (c *Chain) TitleIndex(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for i, src := range c.sources {
		idx, err := src.TitleIndex(ctx)
		if err != nil {
			// One source failing must not lose the others. A chain exists
			// precisely because any single source can be down.
			continue
		}
		for title, id := range idx {
			if _, taken := out[title]; taken {
				continue
			}
			enc, ok := encode(i, id)
			if !ok {
				continue
			}
			out[title] = enc
		}
	}
	return out, nil
}

// Fetch routes back to the source the id came from.
func (c *Chain) Fetch(ctx context.Context, id int64) (catalog.CatalogEntry, error) {
	i, local, ok := decode(id, len(c.sources))
	if !ok {
		return catalog.CatalogEntry{}, fmt.Errorf("catalogchain: id %d is not from this chain", id)
	}
	return c.sources[i].Fetch(ctx, local)
}

// FindByTitle is the chain proper: each source in turn, and every candidate
// confirmed before it is accepted.
func (c *Chain) FindByTitle(raw string) (int64, bool) {
	for i, src := range c.sources {
		local, ok := c.askOne(src, raw)
		if !ok {
			continue
		}
		enc, ok := encode(i, local)
		if !ok {
			continue
		}
		// Confirm. A source that answered is not a source that was right.
		if !c.confirms(src, local, raw) {
			continue
		}
		return enc, true
	}
	return 0, false
}

// askOne uses a source's own matcher when it has one, and its index otherwise.
func (c *Chain) askOne(src catalog.MetadataSource, raw string) (int64, bool) {
	if f, ok := src.(catalog.TitleFinder); ok {
		return f.FindByTitle(raw)
	}
	idx, err := src.TitleIndex(context.Background())
	if err != nil {
		return 0, false
	}
	id, ok := idx[src.Normalize(raw)]
	return id, ok
}

// confirms fetches the candidate and compares its title with what was searched
// for. A source that cannot be fetched is not confirmed — the chain moves on
// rather than accepting an entry it could not read.
func (c *Chain) confirms(src catalog.MetadataSource, local int64, raw string) bool {
	entry, err := src.Fetch(context.Background(), local)
	if err != nil {
		return false
	}
	return Similar(entry.Title, raw) >= c.threshold()
}

func (c *Chain) threshold() float64 {
	if c.minSim > 0 {
		return c.minSim
	}
	return minSimilarity
}

// ResolveExternalID asks each source that can answer, in order.
//
// An id is an id — unlike a title, "imdb"/"tt0133093" means the same thing to
// every source that knows it — so the first source that resolves it wins and no
// confirmation step is needed.
func (c *Chain) ResolveExternalID(ctx context.Context, namespace, value string) (int64, bool) {
	for i, src := range c.sources {
		r, ok := src.(catalog.CrossIDResolver)
		if !ok {
			continue
		}
		local, ok := r.ResolveExternalID(ctx, namespace, value)
		if !ok {
			continue
		}
		if enc, ok := encode(i, local); ok {
			return enc, true
		}
	}
	return 0, false
}

func encode(sourceIndex int, local int64) (int64, bool) {
	if local < 0 || local >= idStride {
		return 0, false
	}
	return int64(sourceIndex)*idStride + local, true
}

func decode(id int64, n int) (sourceIndex int, local int64, ok bool) {
	if id < 0 {
		return 0, 0, false
	}
	i := int(id / idStride)
	if i >= n {
		return 0, 0, false
	}
	return i, id % idStride, true
}

// Similar scores two titles from 0 to 1.
//
// Token overlap rather than edit distance, because release names differ from
// catalogue titles by whole WORDS — a year, a resolution, a group tag — far
// more often than by characters. "Blade Runner 2049 2017 2160p" against "Blade
// Runner 2049" should score high; edit distance punishes it for the extra
// tokens, which is the wrong shape of answer.
//
// Scored against the shorter side, so a release name padded with quality tags
// is not penalised for carrying them.
func Similar(a, b string) float64 {
	ta, tb := tokens(a), tokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	set := make(map[string]bool, len(tb))
	for _, t := range tb {
		set[t] = true
	}
	var hits int
	seen := make(map[string]bool, len(ta))
	for _, t := range ta {
		if seen[t] {
			continue
		}
		seen[t] = true
		if set[t] {
			hits++
		}
	}
	shorter := len(uniq(ta))
	if n := len(uniq(tb)); n < shorter {
		shorter = n
	}
	return float64(hits) / float64(shorter)
}

func tokens(s string) []string {
	f := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		alnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		return !alnum
	})
	out := f[:0]
	for _, t := range f {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func uniq(ts []string) []string {
	seen := map[string]bool{}
	out := ts[:0:0]
	for _, t := range ts {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
