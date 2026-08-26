package handlers

import "testing"

// A typed query must survive a facet click.
//
// ?group= is both a search MODE (list that newsgroup) and a FACET (narrow
// these results to it), and which one it means depends on whether a query was
// typed. Getting that precedence backwards is invisible in the shape of the
// response — the page renders a full, valid results table with the search box
// still showing what you typed — and only the CONTENT is wrong.
//
// Measured on the live index before the fix: "Breaking Bad" returned 100 rows
// of which 100 matched, and "Breaking Bad" with a newsgroup chip returned 100
// rows of which none did.
func TestSearchSourcePrefersTheTypedQuery(t *testing.T) {
	cases := []struct {
		name, query, group, want string
	}{
		{"query alone searches", "Breaking Bad", "", "search"},
		{"group alone browses that group", "", "alt.binaries.boneless", "browse"},
		{"both: the query chooses the source, the group narrows it",
			"Breaking Bad", "alt.binaries.boneless", "search"},
		{"neither shows the latest feed", "", "", "feed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := searchSource(tc.query, tc.group); got != tc.want {
				t.Errorf("searchSource(%q, %q) = %q, want %q — a query dropped here "+
					"returns a full page of unrelated releases with the search box "+
					"still showing what was typed", tc.query, tc.group, got, tc.want)
			}
		})
	}
}
