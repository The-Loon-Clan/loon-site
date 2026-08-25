package catalogchain

import (
	"context"
	"errors"
	"testing"

	"github.com/the-loon-clan/loon/catalog"
)

// A chain of metadata sources, and the confirmation step that makes it worth
// having.
//
// The failure this exists to prevent is silent: a source that answers is not a
// source that was right, and a wrong poster looks exactly like a right one.
// Nothing downstream ever disagrees with it, so it is never reported — the
// only place it can be caught is here, at the moment of accepting the answer.

// fakeSource is a metadata source with a fixed catalogue.
type fakeSource struct {
	key     string
	titles  map[string]int64 // normalized title -> local id
	entries map[int64]catalog.CatalogEntry
	err     error // TitleIndex fails
	fetched int   // how many times Fetch was called
}

func (f *fakeSource) Domain() catalog.DomainInfo {
	return catalog.DomainInfo{Key: f.key, UnitNoun: "thing"}
}
func (f *fakeSource) Normalize(raw string) string { return raw }
func (f *fakeSource) TitleIndex(context.Context) (map[string]int64, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.titles, nil
}
func (f *fakeSource) Fetch(_ context.Context, id int64) (catalog.CatalogEntry, error) {
	f.fetched++
	e, ok := f.entries[id]
	if !ok {
		return catalog.CatalogEntry{}, errors.New("no such entry")
	}
	return e, nil
}

func src(key string, entries map[int64]string) *fakeSource {
	f := &fakeSource{key: key, titles: map[string]int64{}, entries: map[int64]catalog.CatalogEntry{}}
	for id, title := range entries {
		f.titles[title] = id
		f.entries[id] = catalog.CatalogEntry{Title: title}
	}
	return f
}

func chain(t *testing.T, srcs ...catalog.MetadataSource) *Chain {
	t.Helper()
	named := map[string]catalog.MetadataSource{}
	var order []string
	for i, s := range srcs {
		name := string(rune('a' + i))
		named[name] = s
		order = append(order, name)
	}
	c, err := New(named, order...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTheFirstSourceThatKnowsItWins(t *testing.T) {
	primary := src("movie", map[int64]string{1: "Blade Runner 2049"})
	backup := src("movie", map[int64]string{99: "Blade Runner 2049"})
	c := chain(t, primary, backup)

	id, ok := c.FindByTitle("Blade Runner 2049")
	if !ok {
		t.Fatal("a title both sources know was not found")
	}
	// Encoded to the FIRST source, so a later Fetch goes back to the primary.
	i, local, _ := decode(id, 2)
	if i != 0 || local != 1 {
		t.Errorf("resolved to source %d id %d, want the primary's id 1", i, local)
	}
}

func TestItFallsThroughToTheBackup(t *testing.T) {
	// The whole point. Today the registry allows one source per domain, so a
	// title the primary has never heard of is simply not found.
	primary := src("movie", map[int64]string{1: "Blade Runner 2049"})
	backup := src("movie", map[int64]string{7: "The Thing"})
	c := chain(t, primary, backup)

	id, ok := c.FindByTitle("The Thing")
	if !ok {
		t.Fatal("a title only the BACKUP knows was not found — the chain is not falling through")
	}
	i, local, _ := decode(id, 2)
	if i != 1 || local != 7 {
		t.Errorf("resolved to source %d id %d, want the backup's id 7", i, local)
	}
}

func TestAConfidentlyWrongAnswerIsRejected(t *testing.T) {
	// The step NNTmux ends its chain with, and the one a naive implementation
	// leaves out. This source ANSWERS every query — with the wrong film.
	wrong := &fakeSource{
		key:     "movie",
		titles:  map[string]int64{"Blade Runner 2049": 1},
		entries: map[int64]catalog.CatalogEntry{1: {Title: "Thing Called Love"}},
	}
	right := src("movie", map[int64]string{9: "Blade Runner 2049"})
	c := chain(t, wrong, right)

	id, ok := c.FindByTitle("Blade Runner 2049")
	if !ok {
		t.Fatal("nothing matched at all")
	}
	i, _, _ := decode(id, 2)
	if i != 1 {
		t.Error("the chain accepted a source whose entry was a different film; " +
			"the confirmation step is not running")
	}
}

func TestReleaseNamePaddingDoesNotBreakTheMatch(t *testing.T) {
	// Confirmation must not be so strict that it rejects the right answer. A
	// release name carries a year, a resolution and a group tag; the catalogue
	// title carries none of them.
	s := src("movie", map[int64]string{1: "Blade Runner 2049"})
	s.titles["Blade.Runner.2049.2017.2160p.UHD.BluRay-GROUP"] = 1
	c := chain(t, s)

	if _, ok := c.FindByTitle("Blade.Runner.2049.2017.2160p.UHD.BluRay-GROUP"); !ok {
		t.Error("a padded release name was rejected against its own catalogue title")
	}
}

func TestSimilarityScoresWhatItShould(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool // at or above the threshold
	}{
		{"Blade Runner 2049", "Blade Runner 2049", true},
		{"Blade Runner 2049", "Blade.Runner.2049.2017.2160p.BluRay-GRP", true},
		{"The Thing", "The Thing (1982)", true},
		{"Blade Runner 2049", "Thing Called Love", false},
		{"The Thing", "The Matrix", false},
		{"", "anything", false},
	} {
		got := Similar(tc.a, tc.b) >= minSimilarity
		if got != tc.want {
			t.Errorf("Similar(%q, %q) = %.2f; want %v at threshold %.2f",
				tc.a, tc.b, Similar(tc.a, tc.b), tc.want, minSimilarity)
		}
	}
}

func TestAnIdRoutesBackToTheSourceItCameFrom(t *testing.T) {
	// Local ids collide across sources: TVmaze 42 and TMDB 42 are different
	// programmes. Fetching the wrong one is how a chain shows the right title
	// with somebody else's poster.
	first := src("movie", map[int64]string{42: "First Film"})
	second := src("movie", map[int64]string{42: "Second Film"})
	c := chain(t, first, second)

	id, ok := c.FindByTitle("Second Film")
	if !ok {
		t.Fatal("not found")
	}
	entry, err := c.Fetch(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Title != "Second Film" {
		t.Errorf("Fetch returned %q — the id routed to the wrong source", entry.Title)
	}
}

func TestAnIdFromNowhereIsRefused(t *testing.T) {
	c := chain(t, src("movie", map[int64]string{1: "A"}))
	if _, err := c.Fetch(context.Background(), 5*idStride); err == nil {
		t.Error("an id belonging to no source in the chain was accepted")
	}
}

func TestOneSourceFailingDoesNotLoseTheOthers(t *testing.T) {
	// A chain exists because any single source can be down.
	broken := &fakeSource{key: "movie", err: errors.New("provider unreachable")}
	working := src("movie", map[int64]string{3: "Still Here"})
	c := chain(t, broken, working)

	idx, err := c.TitleIndex(context.Background())
	if err != nil {
		t.Fatalf("a failing source made the whole index fail: %v", err)
	}
	if len(idx) != 1 {
		t.Errorf("index has %d entries, want the working source's 1", len(idx))
	}
	if _, ok := c.FindByTitle("Still Here"); !ok {
		t.Error("a lookup failed because an EARLIER source was down")
	}
}

func TestEarlierSourcesWinTheMergedIndex(t *testing.T) {
	primary := src("movie", map[int64]string{1: "Shared Title"})
	backup := src("movie", map[int64]string{2: "Shared Title"})
	c := chain(t, primary, backup)

	idx, err := c.TitleIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	i, local, _ := decode(idx["Shared Title"], 2)
	if i != 0 || local != 1 {
		t.Errorf("the merged index points at source %d id %d; order should decide", i, local)
	}
}

func TestAMixedDomainChainIsRefused(t *testing.T) {
	// Every source in a chain serves one domain. Discovering otherwise at
	// lookup time means an anime entry answering a film query.
	_, err := New(map[string]catalog.MetadataSource{
		"a": src("movie", map[int64]string{1: "A"}),
		"b": src("anime", map[int64]string{1: "B"}),
	}, "a", "b")
	if err == nil {
		t.Error("a chain mixing domains was built without complaint")
	}
}

func TestNothingConfiguredIsNotAnError(t *testing.T) {
	// Every source in this project is idle until its credential is set, so an
	// empty chain is the ordinary unconfigured case and the caller registers
	// nothing.
	c, err := New(map[string]catalog.MetadataSource{}, "tmdb", "tvmaze")
	if err != nil {
		t.Fatalf("an unconfigured chain errored: %v", err)
	}
	if c != nil {
		t.Error("an unconfigured chain produced a source to register")
	}
}

func TestUnconfiguredSourcesAreSkippedNotCounted(t *testing.T) {
	named := map[string]catalog.MetadataSource{
		"tmdb":   nil, // no key
		"tvmaze": src("movie", map[int64]string{5: "Kept"}),
	}
	c, err := New(named, "tmdb", "tvmaze")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Sources(); len(got) != 1 || got[0] != "tvmaze" {
		t.Errorf("chain sources = %v, want just tvmaze", got)
	}
	if _, ok := c.FindByTitle("Kept"); !ok {
		t.Error("the configured source was not reachable")
	}
}

// fakeSearcher is a query-only source: empty index, matches through Search --
// the shape of TMDB/TVmaze/Wikipedia/AniList, which chain_test never exercised
// and which the Chain silently could not drive until it grew a Search method.
type fakeSearcher struct {
	key     string
	byQuery map[string]catalog.CatalogEntry
	calls   int
}

func (f *fakeSearcher) Domain() catalog.DomainInfo {
	return catalog.DomainInfo{Key: f.key, UnitNoun: "thing"}
}
func (f *fakeSearcher) Normalize(raw string) string { return raw }
func (f *fakeSearcher) TitleIndex(context.Context) (map[string]int64, error) {
	return map[string]int64{}, nil // no local id space
}
func (f *fakeSearcher) Fetch(context.Context, int64) (catalog.CatalogEntry, error) {
	return catalog.CatalogEntry{}, errors.New("no local id")
}
func (f *fakeSearcher) Search(_ context.Context, query string) (catalog.CatalogEntry, bool, error) {
	f.calls++
	e, ok := f.byQuery[query]
	return e, ok, nil
}

// A chain of query-only sources must resolve through Search -- the regression
// that zeroed movie and TV enrichment.
func TestChainSearchDrivesQueryOnlySources(t *testing.T) {
	tmdb := &fakeSearcher{key: "movie", byQuery: map[string]catalog.CatalogEntry{}}
	wiki := &fakeSearcher{key: "movie", byQuery: map[string]catalog.CatalogEntry{
		"Blade Runner 2049": {Title: "Blade Runner 2049"},
	}}
	c, err := New(map[string]catalog.MetadataSource{"tmdb": tmdb, "wikipedia": wiki}, "tmdb", "wikipedia")
	if err != nil {
		t.Fatal(err)
	}
	// The Chain must satisfy the scraper's Searcher shape structurally.
	var _ interface {
		Search(context.Context, string) (catalog.CatalogEntry, bool, error)
	} = c
	entry, ok, err := c.Search(context.Background(), "Blade Runner 2049")
	if err != nil || !ok {
		t.Fatalf("query-only chain did not resolve: ok=%v err=%v", ok, err)
	}
	if entry.Title != "Blade Runner 2049" {
		t.Fatalf("wrong entry: %q", entry.Title)
	}
	if tmdb.calls != 1 {
		t.Fatalf("primary source must be asked first; tmdb calls=%d", tmdb.calls)
	}
}

// Order holds: the first source that answers wins, later ones are not asked.
func TestChainSearchStopsAtTheFirstHit(t *testing.T) {
	tmdb := &fakeSearcher{key: "tv", byQuery: map[string]catalog.CatalogEntry{"Silo": {Title: "Silo (TMDB)"}}}
	tvmaze := &fakeSearcher{key: "tv", byQuery: map[string]catalog.CatalogEntry{"Silo": {Title: "Silo (TVmaze)"}}}
	c, _ := New(map[string]catalog.MetadataSource{"tmdb": tmdb, "tvmaze": tvmaze}, "tmdb", "tvmaze")
	entry, ok, _ := c.Search(context.Background(), "Silo")
	if !ok || entry.Title != "Silo (TMDB)" {
		t.Fatalf("primary should win; got ok=%v %q", ok, entry.Title)
	}
	if tvmaze.calls != 0 {
		t.Fatalf("the fallback must not be asked when the primary answers; tvmaze calls=%d", tvmaze.calls)
	}
}

// A query-only primary that misses falls through to the fallback.
func TestChainSearchFallsThroughAMiss(t *testing.T) {
	tmdb := &fakeSearcher{key: "tv", byQuery: map[string]catalog.CatalogEntry{}}
	tvmaze := &fakeSearcher{key: "tv", byQuery: map[string]catalog.CatalogEntry{"Silo": {Title: "Silo"}}}
	c, _ := New(map[string]catalog.MetadataSource{"tmdb": tmdb, "tvmaze": tvmaze}, "tmdb", "tvmaze")
	entry, ok, _ := c.Search(context.Background(), "Silo")
	if !ok || entry.Title != "Silo" {
		t.Fatalf("fallback should answer; got ok=%v %q", ok, entry.Title)
	}
	if tmdb.calls != 1 || tvmaze.calls != 1 {
		t.Fatalf("both should be asked; tmdb=%d tvmaze=%d", tmdb.calls, tvmaze.calls)
	}
}
