package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Filtering and sorting for /browse and /search.
//
// The whole module was at 0%, and it is the code a member touches most: every
// facet click, every sort change, every bookmarked filtered URL comes through
// here. It is also entirely pure — rows in, rows out — which makes the absence
// of tests the odd part rather than the difficult one.
//
// filters_web_test.go already covers the facet LINKS — building them, composing
// them, clearing them — and the counts. Four tests written here restated those
// and were deleted rather than kept: a second test of the same property is
// noise that makes the suite slower to read and no harder to break.
//
// What was genuinely uncovered, and is covered here: parseFilter, Active, the
// whole of apply (filtering and every sort), and the facet view model.

func row(title, res, src, grp string, size int64, grabs int, posted time.Time) searchRow {
	return searchRow{
		Title: title, Resolution: res, Source: src, Group: grp,
		SizeBytes: size, Grabs: grabs, PostedAt: posted,
	}
}

func sample() []searchRow {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return []searchRow{
		row("Bravo", "1080p", "BluRay", "alt.bin.movies", 300, 5, t0.AddDate(0, 0, 2)),
		row("alpha", "2160p", "WEB-DL", "alt.bin.movies", 100, 50, t0),
		row("Charlie", "1080p", "WEB-DL", "alt.bin.tv", 200, 0, t0.AddDate(0, 0, 1)),
		row("delta", "", "", "", 400, 9, time.Time{}), // nothing parsed, no date
	}
}

func queryFilter(t *testing.T, rawQuery string) releaseFilter {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/browse?"+rawQuery, nil)
	return parseFilter(c)
}

// ── parsing ─────────────────────────────────────────────────────────────

func TestAStaleBookmarkDegradesInsteadOfErroring(t *testing.T) {
	// An unknown sort is dropped, not rejected. Somebody's bookmark from before
	// a sort was renamed should show them the page, not a 400 — they did
	// nothing wrong and there is nothing for them to fix.
	f := queryFilter(t, "sort=by-vibes&res=1080p")

	if f.Sort != "" {
		t.Errorf("Sort = %q, want it dropped", f.Sort)
	}
	if f.Resolution != "1080p" {
		t.Errorf("the rest of the query was discarded with it: %+v", f)
	}
}

func TestEveryOfferedSortIsAccepted(t *testing.T) {
	// The menu and the parser have to agree. A sort offered in the UI and
	// rejected by the parser is a control that silently does nothing.
	for _, o := range sortOptions {
		if f := queryFilter(t, "sort="+o.Key); f.Sort != o.Key {
			t.Errorf("sort=%s was dropped; it is in the menu as %q", o.Key, o.Label)
		}
	}
}

func TestFilterValuesAreTrimmed(t *testing.T) {
	// "%201080p" arrives from hand-edited URLs and from copy-paste. Untrimmed,
	// it matches nothing and the page looks empty rather than unfiltered.
	f := queryFilter(t, "res=%20%201080p%20&group=%20alt.bin.tv")
	if f.Resolution != "1080p" || f.Group != "alt.bin.tv" {
		t.Errorf("values not trimmed: %+v", f)
	}
}

func TestActiveIsTrueOnlyWhenSomethingFilters(t *testing.T) {
	// Drives whether the "clear" control is drawn at all.
	if (releaseFilter{}).Active() {
		t.Error("an empty filter reports itself as active")
	}
	for _, f := range []releaseFilter{
		{Resolution: "1080p"}, {Source: "WEB-DL"}, {Group: "alt.bin.tv"}, {Sort: "newest"},
	} {
		if !f.Active() {
			t.Errorf("%+v reports itself inactive", f)
		}
	}
}

// ── filtering ───────────────────────────────────────────────────────────

func TestFilteringIsCaseInsensitive(t *testing.T) {
	// The values come from URLs, which people type and edit. "bluray" and
	// "BluRay" are the same request.
	got := releaseFilter{Source: "bluray"}.apply(sample())
	if len(got) != 1 || got[0].Title != "Bravo" {
		t.Errorf("got %d rows %v, want the one BluRay release", len(got), titles(got))
	}
}

func TestFiltersCompose(t *testing.T) {
	got := releaseFilter{Resolution: "1080p", Group: "alt.bin.movies"}.apply(sample())
	if len(got) != 1 || got[0].Title != "Bravo" {
		t.Errorf("got %v, want only Bravo — the filters are not combining", titles(got))
	}
}

func TestApplyLeavesTheCallersSliceAlone(t *testing.T) {
	// Stated in the comment on apply, and load-bearing: the facet counts are
	// computed from the UNFILTERED rows, so sorting or filtering in place would
	// make every count drift from what the page then shows.
	in := sample()
	before := titles(in)

	releaseFilter{Sort: "title", Resolution: "1080p"}.apply(in)

	if after := titles(in); !equal(before, after) {
		t.Errorf("apply mutated the caller's slice: %v -> %v", before, after)
	}
}

func TestAnEmptyFilterKeepsEverything(t *testing.T) {
	if got := (releaseFilter{}).apply(sample()); len(got) != len(sample()) {
		t.Errorf("an unfiltered page lost rows: %d of %d", len(got), len(sample()))
	}
}

// ── sorting ─────────────────────────────────────────────────────────────

func TestSortsOrderByWhatTheyName(t *testing.T) {
	for _, tc := range []struct {
		sort string
		want []string
	}{
		{"newest", []string{"Bravo", "Charlie", "alpha", "delta"}},
		{"largest", []string{"delta", "Bravo", "Charlie", "alpha"}},
		{"smallest", []string{"alpha", "Charlie", "Bravo", "delta"}},
		{"grabs", []string{"alpha", "delta", "Bravo", "Charlie"}},
		{"title", []string{"alpha", "Bravo", "Charlie", "delta"}},
	} {
		got := titles(releaseFilter{Sort: tc.sort}.apply(sample()))
		if !equal(got, tc.want) {
			t.Errorf("sort=%s gave %v, want %v", tc.sort, got, tc.want)
		}
	}
}

func TestTitleSortIgnoresCase(t *testing.T) {
	// Without folding, every capitalised title sorts ahead of every lowercase
	// one and "Title A–Z" produces two alphabets one after the other.
	got := titles(releaseFilter{Sort: "title"}.apply(sample()))
	if !equal(got, []string{"alpha", "Bravo", "Charlie", "delta"}) {
		t.Errorf("got %v — the sort is comparing raw bytes, so case splits the list", got)
	}
}

func TestOldestFirstSinksTheUnknownDates(t *testing.T) {
	// A zero PostedAt means the crawler never learned a date. Sorting those
	// first would fill the top of an oldest-first page with unknowns, which is
	// the least useful thing that page could show.
	got := titles(releaseFilter{Sort: "oldest"}.apply(sample()))

	if got[len(got)-1] != "delta" {
		t.Errorf("oldest-first gave %v, want the undated release last", got)
	}
	if !equal(got[:3], []string{"alpha", "Charlie", "Bravo"}) {
		t.Errorf("the dated rows are not oldest-first: %v", got)
	}
}

func TestAnUnknownSortLeavesTheOrderAlone(t *testing.T) {
	// apply is reachable with a sort parseFilter would have dropped, so it must
	// not reorder on a value it does not recognise.
	got := titles(releaseFilter{Sort: "by-vibes"}.apply(sample()))
	if !equal(got, titles(sample())) {
		t.Errorf("an unrecognised sort reordered the page: %v", got)
	}
}

// ── facets ──────────────────────────────────────────────────────────────

func TestFacetsAreBuiltFromTheUnfilteredPage(t *testing.T) {
	// The bound stated at the top of filters_web.go: every value offered must
	// actually match something, and the count must be what clicking it yields.
	// Building facets from FILTERED rows would offer only the facet already
	// chosen, so a filter could never be changed, only cleared.
	vm := buildFacets(sample(), releaseFilter{Resolution: "1080p"}, "/browse", url.Values{})

	var values []string
	for _, f := range vm.Resolutions {
		values = append(values, f.Value)
	}
	if len(values) != 2 {
		t.Errorf("resolutions offered = %v, want both even though one is active", values)
	}
	if !vm.Active {
		t.Error("the filter bar does not report itself active with a filter set")
	}
}

func TestTheClearLinkKeepsTheSearchAndDropsTheFacets(t *testing.T) {
	keep := url.Values{"q": {"dune"}}
	vm := buildFacets(sample(), releaseFilter{Resolution: "1080p", Sort: "title"}, "/search", keep)

	if !strings.Contains(vm.ClearHref, "q=dune") {
		t.Errorf("ClearHref = %q, want the search kept", vm.ClearHref)
	}
	for _, p := range []string{"res=", "source=", "group=", "sort="} {
		if strings.Contains(vm.ClearHref, p) {
			t.Errorf("ClearHref = %q still carries %s", vm.ClearHref, p)
		}
	}
}

func TestEverySortIsOfferedAndExactlyOneIsActive(t *testing.T) {
	vm := buildFacets(sample(), releaseFilter{Sort: "grabs"}, "/browse", url.Values{})

	if len(vm.Sorts) != len(sortOptions) {
		t.Fatalf("offered %d sorts, want %d", len(vm.Sorts), len(sortOptions))
	}
	active := 0
	for _, s := range vm.Sorts {
		if s.Active {
			active++
		}
	}
	if active != 1 {
		t.Errorf("%d sorts marked active, want exactly 1", active)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────

func titles(rows []searchRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Title)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
