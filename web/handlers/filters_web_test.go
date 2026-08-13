package handlers

import (
	"net/url"
	"strings"
	"testing"
)

// The facet links carried three rules and no tests. Each rule is one a filter
// UI gets wrong in a way nobody notices from a screenshot: the search survives
// a filter click, filters compose rather than replace, and clicking the active
// value clears it.

func TestFacetLinkKeepsTheNonFacetParams(t *testing.T) {
	// ?q= and ?cat= are not facets. A filter click that dropped them would
	// silently widen the result set from "HD releases matching dune" to "all
	// HD releases", which looks like a working filter and is not.
	keep := url.Values{"q": {"dune"}, "cat": {"2040"}}
	got := facetLink("/browse", keep, releaseFilter{}, "res", "1080p")
	for _, want := range []string{"q=dune", "cat=2040", "res=1080p"} {
		if !strings.Contains(got, want) {
			t.Errorf("facetLink = %q, missing %q", got, want)
		}
	}
}

func TestFacetsCompose(t *testing.T) {
	// Choosing a source while a resolution is active must keep the resolution.
	// Replacing it would make the filters mutually exclusive, which is not
	// what a list of independent facets says it does.
	f := releaseFilter{Resolution: "1080p"}
	got := facetLink("/browse", url.Values{}, f, "source", "WEB-DL")
	if !strings.Contains(got, "res=1080p") {
		t.Errorf("facetLink = %q, dropped the active resolution", got)
	}
	if !strings.Contains(got, "source=WEB-DL") {
		t.Errorf("facetLink = %q, missing the clicked facet", got)
	}
}

func TestClickingTheActiveFacetClearsIt(t *testing.T) {
	// The same control both ways: this is why the link cannot simply
	// Set(param, value), and it is the rule most likely to be broken by
	// someone "simplifying" that line.
	f := releaseFilter{Resolution: "1080p"}
	got := facetLink("/browse", url.Values{}, f, "res", "1080p")
	if strings.Contains(got, "res=") {
		t.Errorf("facetLink = %q, still carries res — clicking the active value must clear it", got)
	}
	if got != "/browse" {
		t.Errorf("facetLink = %q, want a bare /browse once the only facet is cleared", got)
	}
}

func TestClearingIsCaseInsensitive(t *testing.T) {
	// The stored value and the rendered one differ in case across sources
	// (WEB-DL vs web-dl), and a case-sensitive compare would turn the toggle
	// into a no-op that re-applies the filter it was meant to remove.
	f := releaseFilter{Source: "WEB-DL"}
	if got := facetLink("/browse", url.Values{}, f, "source", "web-dl"); strings.Contains(got, "source=") {
		t.Errorf("facetLink = %q, case difference defeated the toggle", got)
	}
}

func TestFacetCountsSkipEmptyValues(t *testing.T) {
	// A release with no parsed group is not a group anyone can filter by, and
	// counting it under "" would offer an empty chip.
	res, src, grp := facetCounts([]searchRow{
		{Resolution: "1080p", Source: "WEB-DL", Group: "NTb"},
		{Resolution: "1080p", Source: "", Group: ""},
	})
	if res["1080p"] != 2 {
		t.Errorf("resolution count = %d, want 2", res["1080p"])
	}
	if _, ok := src[""]; ok {
		t.Error("empty source was counted as a facet value")
	}
	if _, ok := grp[""]; ok {
		t.Error("empty group was counted as a facet value")
	}
}

func TestFacetListOrdersByCountThenName(t *testing.T) {
	// Ties broken alphabetically so the order does not shuffle between
	// requests — a filter list that reorders itself on refresh reads as a bug
	// even when every value is right.
	link := func(param, value string) string { return "/x?" + param + "=" + value }
	got := facetList(map[string]int{"b": 2, "a": 2, "c": 9}, "res", "", link)
	order := []string{got[0].Value, got[1].Value, got[2].Value}
	want := []string{"c", "a", "b"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestFacetListCapsTheList(t *testing.T) {
	counts := map[string]int{}
	for i := 0; i < 20; i++ {
		counts[string(rune('a'+i))] = i
	}
	if n := len(facetList(counts, "res", "", func(string, string) string { return "" })); n != 8 {
		t.Errorf("facet list length = %d, want it capped at 8", n)
	}
}
