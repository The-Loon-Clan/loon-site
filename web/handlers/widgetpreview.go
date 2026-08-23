package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Which pages a widget rule actually matches.
//
// The rule language in widgetpages.go is small on purpose, and it still has
// the worst property a setting can have: a typo is silent. An operator who
// means /browse and types /brwose gets "Layout updated." and a widget that has
// vanished from the entire site, with nothing anywhere saying why. That is this
// codebase's definition of broken — indistinguishable from working — and the
// rule box shipped with it.
//
// So the box answers. Every rule is matched against the pages this site is
// known to have and the result shown beside the field: live as it is typed, and
// again from the stored value after a save, so the no-JavaScript path is told
// the same thing rather than nothing.
//
// THE HONESTY PROBLEM, which is the whole difficulty here. The known list is
// real pages, but it is not every page: a release, a thread, a community and a
// member profile are all served from :id routes that no list can enumerate. A
// preview that said "matches nothing" for /release* would be a check that lies,
// and replacing a silent failure with a confident wrong answer is not progress.
//
// Hence two questions, not one. First: which known pages match, asked with the
// same pagesMatch the renderer will run, so what is shown is what will happen.
// Second, and ONLY when the first found nothing: does the rule reach a route
// this site serves at all? /release* matches no listed page and does reach
// /release/:id, and is reported that way. /brwose* reaches nothing, and that is
// the typo — the one case worth a warning.

// previewCap is how many matched pages are listed before the rest become a
// count. A rule matching everything would otherwise print sixty links into a
// table cell, which is not something anybody reads.
const previewCap = 10

// previewPath is one page the rule was tested against.
type previewPath struct {
	Path  string
	Label string
}

// widgetPagesVM is the answer for one rule.
//
// Every and Unknown are separate booleans rather than one state enum because
// they are not opposite ends of a scale: Every is the default and is fine,
// Unknown is a probable typo and is not, and the states between them are
// ordinary. A template forced to rank them would invent an order that does not
// exist.
type widgetPagesVM struct {
	Every   bool          // no rule: every page, which is what empty has always meant
	Known   int           // how many pages were tested
	Total   int           // how many matched, counting the ones not listed
	Matched []previewPath // up to previewCap of them
	More    int           // matched beyond the cap
	Route   string        // a served route the rule reaches when no listed page matched
	Unknown bool          // matched no page AND reached no route: almost certainly a typo
}

// knownPages is the set a rule is previewed against.
//
// Three sources, and each is the host's OWN answer to "what pages are there"
// rather than a fourth list maintained here: the sitemap's curated groups, the
// plugin pages registered right now, and the admin bar. A list written here is
// the one that would go stale, and it would go stale in the direction that
// matters — a page added tomorrow previewing as unmatched.
//
// Filtered by what THIS viewer can see, for the reason the sitemap filters it:
// an operator previewing a rule should be shown the site, not a catalogue of
// pages a feature switch has already taken away.
func (w *web) knownPages(c *gin.Context) []previewPath {
	var out []previewPath
	seen := map[string]bool{}
	add := func(path, label string) {
		// A widget lives in the chrome, so anything the chrome does not wrap
		// cannot show one however well it matches: the XML sitemap, the API
		// and the feeds are documents, not pages.
		if path == "" || !strings.HasPrefix(path, "/") ||
			strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/rss") ||
			strings.HasPrefix(path, "/static") || strings.HasSuffix(path, ".xml") {
			return
		}
		if i := strings.IndexAny(path, "?#"); i >= 0 {
			path = path[:i]
		}
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, previewPath{Path: path, Label: label})
	}

	for _, g := range sitemapGroups {
		for _, l := range g.Links {
			add(l.Href, l.Label)
		}
	}
	for _, v := range w.sitePages {
		if w.canView(v, c) {
			add("/p/"+v.Slug, v.Title)
		}
	}
	for _, it := range w.liveAdminNav() {
		add(it.Href, it.Label)
	}
	return out
}

// servedRoutes is every GET path the engine answers, patterns and all.
//
// Read from gin at request time rather than kept as a list, so it cannot
// disagree with what the site serves. Returns nil before mount, which makes the
// reachability question unanswerable rather than answered wrongly — see
// previewPages, which then declines to call any rule a typo.
func (w *web) servedRoutes() []string {
	if w.engine == nil {
		return nil
	}
	var out []string
	for _, r := range w.engine.Routes() {
		if r.Method == http.MethodGet {
			out = append(out, r.Path)
		}
	}
	return out
}

// routeCompatible reports whether path could be served by route, treating gin's
// :param and *wildcard segments as matching anything.
//
// Segment-wise rather than by string prefix, which is what pagesMatch does:
// there the operator is naming pages and a prefix is what they mean, while here
// the question is whether a route EXISTS — and "/rel" should not be judged to
// reach /release/:id merely because the characters line up.
func routeCompatible(route, path string, prefix bool) bool {
	rs := splitPath(route)
	ps := splitPath(path)
	for i, p := range ps {
		if i >= len(rs) {
			return false // deeper than the route goes, and no catch-all took it
		}
		switch r := rs[i]; {
		case strings.HasPrefix(r, "*"):
			// gin's catch-all: this segment and every one after it belong to
			// this route. Treating it as a single :param instead is what made
			// /static/*filepath look incompatible with /static/css/site.css.
			return true
		case strings.HasPrefix(r, ":"):
			continue // a parameter: any one segment
		case r != p:
			return false
		}
	}
	if prefix {
		return true // the rule names a prefix; the route going deeper is the point
	}
	return len(rs) == len(ps)
}

func splitPath(p string) []string {
	if p = strings.Trim(p, "/"); p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// reachedRoute finds a served route that one of the rule's include lines points
// at, and returns the first.
//
// Includes only. An exclude that matches nothing is harmless — it removes a
// page that was not there — while an include that matches nothing is the whole
// failure being hunted, so the two are not worth reporting alike.
func reachedRoute(rule string, routes []string) (string, bool) {
	for _, line := range strings.Split(rule, "\n") {
		p := strings.TrimSpace(line)
		if p == "" || strings.HasPrefix(p, "#") || strings.HasPrefix(p, "!") {
			continue
		}
		pre := strings.HasSuffix(p, "*")
		p = strings.TrimSuffix(p, "*")
		if p != "/" {
			p = strings.TrimRight(p, "/")
		}
		for _, r := range routes {
			if routeCompatible(r, p, pre) {
				return r, true
			}
		}
	}
	return "", false
}

// previewPages answers one rule for this request: gather, then decide.
//
// The gathering is here and the deciding is in previewRule, which is a pure
// function over the two lists. Split because the deciding is the part with
// four outcomes and an easy wrong answer, and a test that had to stand up a
// wired site to reach it would be a test nobody writes.
func (w *web) previewPages(rule string, c *gin.Context) widgetPagesVM {
	return previewRule(rule, w.knownPages(c), w.servedRoutes())
}

// previewRule decides, given the pages a rule was tested against and the routes
// the site serves.
func previewRule(rule string, pages []previewPath, routes []string) widgetPagesVM {
	var vm widgetPagesVM
	vm.Known = len(pages)

	if stripComments(rule) == "" {
		// Empty, blank, or nothing but a note: every page, and no list. Said
		// plainly because it is both the default and a real answer — an
		// operator who has just cleared the box needs to see that the widget
		// came back, not an empty result that reads like a failure.
		vm.Every = true
		return vm
	}

	for _, p := range pages {
		// The renderer's own function, not a second implementation of it. A
		// preview that agreed with the rule only most of the time would be
		// worse than none, because it would be believed.
		if pagesMatch(rule, p.Path) {
			vm.Total++
			if len(vm.Matched) < previewCap {
				vm.Matched = append(vm.Matched, p)
			} else {
				vm.More++
			}
		}
	}
	if len(vm.Matched) > 0 {
		return vm
	}

	// Nothing listed matched. Before calling that a mistake, ask whether the
	// rule reaches a route at all — /release*, /u* and /c* are all correct
	// rules for pages no list can hold.
	if len(routes) == 0 {
		return vm // cannot tell; say nothing rather than guess
	}
	if r, ok := reachedRoute(rule, routes); ok {
		vm.Route = r
		return vm
	}
	vm.Unknown = true
	return vm
}

// stripComments drops blank and #-prefixed lines, so a box holding only a note
// is recognised as the empty rule it is — the same reading pagesMatch gives it.
func stripComments(rule string) string {
	var keep []string
	for _, line := range strings.Split(rule, "\n") {
		if p := strings.TrimSpace(line); p != "" && !strings.HasPrefix(p, "#") {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "\n")
}

// widgetsAdminPreview answers POST /admin/widgets/preview: the rule in the box
// right now, without saving it.
//
// A POST rather than a GET because the rule is multi-line free text typed by an
// operator, and it changes nothing — the endpoint reads no store and writes
// none, which is why it needs no more than the admin gate the group applies.
func (w *web) widgetsAdminPreview(c *gin.Context) {
	vm := w.previewPages(c.PostForm("pages"), c)
	w.renderFragment(c, "admin_widgets.html", "widget-pages-preview", vm)
}
