package handlers

import (
	"strings"
	"testing"
)

// The pages a rule is previewed against in these tests. Deliberately a mix of
// the three real sources — public pages, a plugin page, an admin page — because
// the outcomes differ by which of them a rule reaches.
func previewFixturePages() []previewPath {
	return []previewPath{
		{Path: "/", Label: "Home"},
		{Path: "/browse", Label: "Browse"},
		{Path: "/news", Label: "News"},
		{Path: "/community/forums", Label: "Forums"},
		{Path: "/p/api-key", Label: "API key"},
		{Path: "/admin/widgets", Label: "Widgets"},
	}
}

// Routes as gin reports them: patterns included, which is the entire point of
// the second question the preview asks.
func previewFixtureRoutes() []string {
	return []string{
		"/", "/browse", "/news", "/community/forums", "/p/:slug",
		"/admin/widgets", "/release/:id", "/u/:name", "/c/:slug",
		"/community/forums/thread/:id",
	}
}

func TestPreviewRule(t *testing.T) {
	pages, routes := previewFixturePages(), previewFixtureRoutes()

	for _, c := range []struct {
		name, rule string
		want       string // "every" | "matched" | "route" | "unknown"
		paths      string // comma-joined Matched paths, checked when want=="matched"
		route      string // checked when want=="route"
	}{
		// The default, and it has to SAY so: an operator who clears the box
		// needs to see the widget come back, not an empty answer that reads
		// like the failure they were trying to undo.
		{name: "empty is every page", rule: "", want: "every"},
		{name: "blank lines are empty", rule: "\n  \n\t", want: "every"},
		{name: "only a note is empty", rule: "# not decided yet", want: "every"},

		{name: "one page", rule: "/browse", want: "matched", paths: "/browse"},
		{name: "front page only", rule: "/", want: "matched", paths: "/"},
		{name: "a prefix", rule: "/community*", want: "matched", paths: "/community/forums"},
		{name: "several includes", rule: "/\n/news", want: "matched", paths: "/,/news"},

		// Excludes: the widget is everywhere the exclude does not reach, and
		// the preview has to show that as the large list it is rather than as
		// nothing.
		{name: "exclude only", rule: "!/admin*", want: "matched",
			paths: "/,/browse,/news,/community/forums,/p/api-key"},
		{name: "include and exclude", rule: "/*\n!/admin*", want: "matched",
			paths: "/,/browse,/news,/community/forums,/p/api-key"},

		// THE HONESTY CASE. These rules are correct and match no listed page,
		// because the pages they name are served from patterns. Reporting them
		// as a mistake would be the preview lying, which is worse than the
		// silence it replaces.
		{name: "release detail pages", rule: "/release*", want: "route", route: "/release/:id"},
		{name: "one release", rule: "/release/1234", want: "route", route: "/release/:id"},
		{name: "member profiles", rule: "/u*", want: "route", route: "/u/:name"},
		{name: "a community", rule: "/c/usenet", want: "route", route: "/c/:slug"},
		{name: "a thread", rule: "/community/forums/thread/9", want: "route",
			route: "/community/forums/thread/:id"},

		// THE CASE THIS WAS BUILT FOR. /brwose reaches no page and no route,
		// and before the preview existed it saved silently and hid the widget
		// everywhere.
		{name: "the typo", rule: "/brwose", want: "unknown"},
		{name: "the typo, as a prefix", rule: "/brwose*", want: "unknown"},
		{name: "a page that never existed", rule: "/shoutbox", want: "unknown"},

		// A near-miss must NOT be rescued by the route check: /rel shares a
		// prefix with /release/:id as a string and is not a route, which is
		// why reachability is asked segment-wise.
		{name: "a truncated word is not a route", rule: "/rel", want: "unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			vm := previewRule(c.rule, pages, routes)
			var got string
			switch {
			case vm.Every:
				got = "every"
			case len(vm.Matched) > 0:
				got = "matched"
			case vm.Route != "":
				got = "route"
			case vm.Unknown:
				got = "unknown"
			default:
				got = "nothing at all"
			}
			if got != c.want {
				t.Fatalf("rule %q: got %s (%+v), want %s", c.rule, got, vm, c.want)
			}
			if c.want == "matched" {
				var paths []string
				for _, p := range vm.Matched {
					paths = append(paths, p.Path)
				}
				if strings.Join(paths, ",") != c.paths {
					t.Fatalf("rule %q matched %v, want %s", c.rule, paths, c.paths)
				}
			}
			if c.want == "route" && vm.Route != c.route {
				t.Fatalf("rule %q reached %q, want %q", c.rule, vm.Route, c.route)
			}
		})
	}
}

// The preview must say what the renderer will do, or it is worse than nothing:
// an operator would believe it.
//
// Not a restatement of the cases above — it asserts the two agree for every
// page, so a future edit to previewRule that "improved" the matching would
// fail here rather than shipping a preview that quietly describes a different
// rule from the one being enforced.
func TestPreviewAgreesWithTheRenderer(t *testing.T) {
	pages := previewFixturePages()
	for _, rule := range []string{
		"", "/", "/browse", "/community*", "!/admin*", "/*\n!/admin*",
		"/\n/news", "# note\n/browse", "/nothing-here",
	} {
		vm := previewRule(rule, pages, previewFixtureRoutes())
		shown := map[string]bool{}
		for _, p := range vm.Matched {
			shown[p.Path] = true
		}
		for _, p := range pages {
			// Every is the one case with no list, because listing every page
			// of the site is not an answer anybody reads. It still has to mean
			// what the renderer means.
			want := pagesMatch(rule, p.Path)
			if vm.Every {
				if !want {
					t.Fatalf("rule %q previewed as every page, but the renderer "+
						"would not draw it on %s", rule, p.Path)
				}
				continue
			}
			if shown[p.Path] != want {
				t.Fatalf("rule %q: preview shows %s = %v, renderer says %v",
					rule, p.Path, shown[p.Path], want)
			}
		}
	}
}

// Above previewCap the list becomes a count, and the count has to be right —
// a "+3 more" that was wrong would be a second silent lie in the same box.
func TestPreviewCapsTheList(t *testing.T) {
	var pages []previewPath
	for i := 0; i < previewCap+7; i++ {
		pages = append(pages, previewPath{Path: "/page" + string(rune('a'+i))})
	}
	vm := previewRule("/page*", pages, previewFixtureRoutes())
	if len(vm.Matched) != previewCap || vm.More != 7 {
		t.Fatalf("got %d listed and %d more, want %d and 7",
			len(vm.Matched), vm.More, previewCap)
	}
	// The count is of everything that matched, not of what fitted. Reporting
	// the cap here is what "Shows on 10+ of 82 known pages" was, when the
	// honest answer was 47.
	if vm.Total != previewCap+7 {
		t.Fatalf("Total = %d, want %d", vm.Total, previewCap+7)
	}
	if vm.Known != previewCap+7 {
		t.Fatalf("tested %d pages, want %d", vm.Known, previewCap+7)
	}
}

// With no route table the reachability question cannot be asked, so it must not
// be answered. Saying "no match" here would call every correct :id rule a typo
// on any path where the engine is not wired — including the tests around this
// one, which is how the failure would reach production unnoticed.
func TestPreviewWithoutRoutesDoesNotCryTypo(t *testing.T) {
	vm := previewRule("/brwose", previewFixturePages(), nil)
	if vm.Unknown || vm.Route != "" {
		t.Fatalf("got Unknown=%v Route=%q with no route table, want silence",
			vm.Unknown, vm.Route)
	}
}

func TestRouteCompatible(t *testing.T) {
	for _, c := range []struct {
		route, path string
		prefix      bool
		want        bool
	}{
		{"/release/:id", "/release", true, true},     // /release* reaches it
		{"/release/:id", "/release/12", false, true}, // an exact id does too
		{"/release/:id", "/release", false, false},   // but /release alone is not that route
		{"/release/:id", "/rel", true, false},        // a truncated segment is not a prefix of it
		{"/browse", "/browse", false, true},
		{"/browse", "/browse/tv", false, false},
		{"/", "/", false, true},
		{"/p/:slug", "/p", true, true},
		{"/static/*filepath", "/static/css/site.css", false, true},
		// Depth matters: a two-segment rule cannot be served by a one-segment
		// route, prefix or not.
		{"/news", "/news/2026", true, false},
	} {
		if got := routeCompatible(c.route, c.path, c.prefix); got != c.want {
			t.Errorf("routeCompatible(%q, %q, prefix=%v) = %v, want %v",
				c.route, c.path, c.prefix, got, c.want)
		}
	}
}

// An exclude that matches nothing is harmless; only an unreachable INCLUDE is
// the failure worth naming. Asserted separately because treating the two alike
// is the obvious simplification, and it would warn about every "!/admin*" on a
// site with no admin area.
func TestReachedRouteIgnoresExcludesAndComments(t *testing.T) {
	routes := previewFixtureRoutes()
	if r, ok := reachedRoute("!/nowhere*\n# /alsonowhere", routes); ok {
		t.Fatalf("an exclude-only rule reached %q; excludes are not includes", r)
	}
	if r, ok := reachedRoute("!/nowhere*\n/release/7", routes); !ok || r != "/release/:id" {
		t.Fatalf("got %q %v, want the include to be the one that counts", r, ok)
	}
}
