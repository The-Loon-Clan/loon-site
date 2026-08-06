package main

import (
	"net/url"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// Result filtering and sorting for /browse and /search — UNIT3D's torrent-search
// facets, cut down to the ones this stack can actually answer.
//
// UNIT3D offers dozens (seeders, freeleech, doubleup, bookmarked, alive/dead,
// incomplete…). Most rest on peers or an upload economy and are n/a here. What
// IS real: the quality metadata the usenet plugin already parses out of a
// release name, the newsgroup it came from, its category, and — since grabs are
// recorded — how often it has been downloaded.
//
// IMPORTANT BOUND: filtering happens in Go over the page the capability
// returned, not in SQL. The UsenetIndex read path takes a limit and no
// predicates, so "1080p only" means "1080p within the most recent N", not
// "1080p across the whole index". The facet counts below are computed from the
// same window, so they always agree with what filtering will produce — a facet
// offering 40 results that then yields 3 is worse than a smaller honest number.
// Pushing this into the query needs a capability that accepts predicates.

// releaseFilter is the parsed query state for a listing page.
type releaseFilter struct {
	Resolution string // "1080p"
	Source     string // "BluRay"
	Group      string // newsgroup
	Sort       string // one of sortOptions
}

// sortOptions are the orderings offered, in menu order. Keyed by the ?sort=
// value so a bookmarked URL keeps working.
var sortOptions = []struct{ Key, Label string }{
	{"newest", "Newest first"},
	{"oldest", "Oldest first"},
	{"largest", "Largest first"},
	{"smallest", "Smallest first"},
	{"grabs", "Most grabbed"},
	{"title", "Title A–Z"},
}

// parseFilter reads the facet params. Unknown values are dropped rather than
// erroring: a stale bookmark or a hand-edited URL should degrade to the
// unfiltered page, not a 400.
func parseFilter(c *gin.Context) releaseFilter {
	f := releaseFilter{
		Resolution: strings.TrimSpace(c.Query("res")),
		Source:     strings.TrimSpace(c.Query("source")),
		Group:      strings.TrimSpace(c.Query("group")),
		Sort:       strings.TrimSpace(c.Query("sort")),
	}
	valid := false
	for _, o := range sortOptions {
		if o.Key == f.Sort {
			valid = true
			break
		}
	}
	if !valid {
		f.Sort = "" // empty means "whatever order the index returned"
	}
	return f
}

// Active reports whether anything is filtering, so a template can offer a
// "clear" control only when there is something to clear.
func (f releaseFilter) Active() bool {
	return f.Resolution != "" || f.Source != "" || f.Group != "" || f.Sort != ""
}

// apply filters and sorts a page of rows. Returns a new slice; the caller's is
// untouched, because the facet counts are computed from the UNFILTERED set and
// mutating it would make them drift.
func (f releaseFilter) apply(rows []searchRow) []searchRow {
	out := make([]searchRow, 0, len(rows))
	for _, r := range rows {
		if f.Resolution != "" && !strings.EqualFold(r.Resolution, f.Resolution) {
			continue
		}
		if f.Source != "" && !strings.EqualFold(r.Source, f.Source) {
			continue
		}
		if f.Group != "" && !strings.EqualFold(r.Group, f.Group) {
			continue
		}
		out = append(out, r)
	}
	switch f.Sort {
	case "newest":
		sort.SliceStable(out, func(i, j int) bool { return out[i].PostedAt.After(out[j].PostedAt) })
	case "oldest":
		// A zero PostedAt means the crawler never learned a date. Sorting those
		// FIRST would fill the top of an oldest-first page with unknowns, which
		// is the least useful thing the page could show, so they sink.
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].PostedAt.IsZero() != out[j].PostedAt.IsZero() {
				return !out[i].PostedAt.IsZero()
			}
			return out[i].PostedAt.Before(out[j].PostedAt)
		})
	case "largest":
		sort.SliceStable(out, func(i, j int) bool { return out[i].SizeBytes > out[j].SizeBytes })
	case "smallest":
		sort.SliceStable(out, func(i, j int) bool { return out[i].SizeBytes < out[j].SizeBytes })
	case "grabs":
		sort.SliceStable(out, func(i, j int) bool { return out[i].Grabs > out[j].Grabs })
	case "title":
		sort.SliceStable(out, func(i, j int) bool {
			return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
		})
	}
	return out
}

// facet is one filter value plus how many rows carry it.
type facet struct {
	Value  string
	Count  int
	Active bool
	// Href is the full query string for toggling this value — built here
	// rather than in the template so the other params are preserved and a
	// second click clears it.
	Href string
}

// facetsVM is everything a listing template needs to draw the filter bar.
type facetsVM struct {
	Resolutions []facet
	Sources     []facet
	Groups      []facet
	Sorts       []facet
	Active      bool
	ClearHref   string
}

// buildFacets derives the available filters from the UNFILTERED page, so every
// value offered is one that will actually match something. Counts come from the
// same set for the same reason.
//
// base is the page path plus any params that are NOT facets (?q=, ?cat=), so a
// facet link keeps the search or category it was clicked from.
func buildFacets(rows []searchRow, f releaseFilter, base string, keep url.Values) facetsVM {
	resCount, srcCount, grpCount := map[string]int{}, map[string]int{}, map[string]int{}
	for _, r := range rows {
		if r.Resolution != "" {
			resCount[r.Resolution]++
		}
		if r.Source != "" {
			srcCount[r.Source]++
		}
		if r.Group != "" {
			grpCount[r.Group]++
		}
	}
	link := func(param, value string) string {
		v := url.Values{}
		for k, vals := range keep {
			for _, s := range vals {
				v.Add(k, s)
			}
		}
		// Carry the other facets so filters compose.
		add := func(k, cur string) {
			if k != param && cur != "" {
				v.Set(k, cur)
			}
		}
		add("res", f.Resolution)
		add("source", f.Source)
		add("group", f.Group)
		add("sort", f.Sort)
		// Clicking the active value clears it — the same control both ways, so
		// there is no separate "remove" affordance to find.
		cur := map[string]string{"res": f.Resolution, "source": f.Source, "group": f.Group, "sort": f.Sort}[param]
		if !strings.EqualFold(cur, value) {
			v.Set(param, value)
		}
		if q := v.Encode(); q != "" {
			return base + "?" + q
		}
		return base
	}
	build := func(counts map[string]int, param, active string) []facet {
		out := make([]facet, 0, len(counts))
		for val, n := range counts {
			out = append(out, facet{
				Value: val, Count: n,
				Active: strings.EqualFold(val, active),
				Href:   link(param, val),
			})
		}
		// Most common first, then alphabetically so the order is stable between
		// requests with equal counts.
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].Value < out[j].Value
		})
		if len(out) > 8 {
			out = out[:8] // a facet list longer than the results helps nobody
		}
		return out
	}
	sorts := make([]facet, 0, len(sortOptions))
	for _, o := range sortOptions {
		sorts = append(sorts, facet{
			Value: o.Label, Active: o.Key == f.Sort, Href: link("sort", o.Key),
		})
	}
	clear := base
	if q := keep.Encode(); q != "" {
		clear = base + "?" + q
	}
	return facetsVM{
		Resolutions: build(resCount, "res", f.Resolution),
		Sources:     build(srcCount, "source", f.Source),
		Groups:      build(grpCount, "group", f.Group),
		Sorts:       sorts,
		Active:      f.Active(),
		ClearHref:   clear,
	}
}

// keepParams returns the non-facet params for a request, for facet links to
// carry. Explicit allow-list rather than "everything except the facets", so a
// stray param cannot ride along into every generated URL.
func keepParams(c *gin.Context, names ...string) url.Values {
	v := url.Values{}
	for _, n := range names {
		if s := strings.TrimSpace(c.Query(n)); s != "" {
			v.Set(n, s)
		}
	}
	return v
}

// listingLimit is how many rows a listing page fetches before filtering.
//
// Bigger than the page shows, because filtering happens after the read: fetch
// only a screenful and a narrow filter would find almost nothing. Bounded
// anyway — this is a window, not the index.
const listingLimit = 200
