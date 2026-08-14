package handlers

import (
	site "github.com/the-loon-clan/loon-site"

	"bytes"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http/httptest"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
	"text/template/parse"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/forum"
	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The repo's other tests never construct newWeb, so a broken template used to
// reach production as a boot-time panic (parse errors) or a silently truncated
// page (execute errors). These tests parse EVERY template file exactly the way
// the two production parse sites do:
//
//	views.go newWeb()            -> ParseFS(site.FS, pageFiles(page))
//	forum_web.go wireForumPlugin -> pluginTemplates(): ParseFS(site.FS, site_chrome.html, forum/*.html)
//
// They are separate sets on purpose: no PAGE file is reachable from both, so a
// {{define}} in one page is invisible in the other. site_chrome.html is the one
// file both sets name, and the only place the shell blocks are defined.

// shellTemplates are the files parsed alongside EVERY page rather than being a
// page themselves. TestEveryPageTemplateIsParsed excludes them.
var shellTemplates = map[string]bool{
	"base.html": true, "site_chrome.html": true,
	"listing.html": true, // shared partial, see sharedPartials in views.go
	"facets.html":  true, // shared partial: the /browse + /search filter bar
	// The Save/Saved toggle. A partial specifically so the page and the htmx
	// handler that swaps it back cannot render different markup — see htmx.go.
	"bookmark_button.html": true,
	// The site's prose editor, rendered INTO plugin fragments through
	// Deps.RenderEditor rather than being a page of its own — it is parsed by
	// pluginTemplates(), not by newWeb.
	"editor.html": true,
	// The dev-only UI inspector (uiinspect_web.go). Standalone by design: it
	// must NOT inherit base.html or the site's stylesheets, because anything
	// it inherited could differ between the tool and the page it inspects. It
	// is parsed per request by uiCompare rather than by newWeb, and its route
	// exists only when LOON_UI_INSPECT is set — so it is reachable, just
	// not through the page set this test walks.
	"dev_compare.html": true,
}

// parseSet builds a page's template set exactly the way newWeb does — by
// calling the SAME pageFiles the production path calls, so this test cannot
// drift from it the way a hand-copied file list would.
func parseSet(w *web, page string) (*template.Template, error) {
	return template.New(page).Funcs(w.tmplFuncs()).ParseFS(site.FS, pageFiles(page)...)
}

// TestPageTemplatesParse builds the host's per-page template sets the same way
// newWeb does. A parse error here is a boot-time panic in production.
func TestPageTemplatesParse(t *testing.T) {
	w := &web{log: slog.Default(), tmpls: map[string]*template.Template{}}
	for _, page := range pageTemplates {
		tmpl, err := parseSet(w, page)
		if err != nil {
			t.Errorf("%s: %v", page, err)
			continue
		}
		if tmpl.Lookup(page) == nil {
			t.Errorf("%s: parsed set has no template named %q", page, page)
		}
		// render() executes "base.html", not the page name.
		if tmpl.Lookup("base.html") == nil {
			t.Errorf("%s: set has no base.html — render() executes that name", page)
		}
		// Every page renders through base.html, which invokes these by name.
		// A missing one is an execute-time error, not a parse error, so it
		// would reach the browser as a half-written page.
		for _, blk := range []string{"site-head", "site-sprite", "site-header", "site-footer", "site-scripts", "content"} {
			if tmpl.Lookup(blk) == nil {
				t.Errorf("%s: set is missing {{define %q}}", page, blk)
			}
		}
		assertInvocationsResolve(t, page, tmpl)
	}
}

// assertInvocationsResolve walks the parse trees and checks every
// {{template "X"}} names a template that exists IN THIS SET. Go reports an
// unresolved name at execute time ("no such template"), which render() only
// logs — the browser gets a truncated page, the build and vet stay green. This
// is the check that makes the two disjoint parse sets safe to keep.
func assertInvocationsResolve(t *testing.T, label string, tmpl *template.Template) {
	t.Helper()
	var walk func(n parse.Node, in string)
	walk = func(n parse.Node, in string) {
		switch v := n.(type) {
		case nil:
			return
		case *parse.TemplateNode:
			if tmpl.Lookup(v.Name) == nil {
				t.Errorf("%s: {{template %q}} inside %q resolves to nothing in this set", label, v.Name, in)
			}
		case *parse.ListNode:
			if v == nil {
				return
			}
			for _, c := range v.Nodes {
				walk(c, in)
			}
		case *parse.IfNode:
			walk(v.List, in)
			walk(v.ElseList, in)
		case *parse.RangeNode:
			walk(v.List, in)
			walk(v.ElseList, in)
		case *parse.WithNode:
			walk(v.List, in)
			walk(v.ElseList, in)
		}
	}
	for _, sub := range tmpl.Templates() {
		if sub.Tree == nil {
			continue
		}
		walk(sub.Tree.Root, sub.Name())
	}
}

// TestEveryPageTemplateIsParsed asserts the embedded template dir and
// newWeb's page list agree, in both directions.
func TestEveryPageTemplateIsParsed(t *testing.T) {
	ents, err := fs.ReadDir(site.FS, "web/templates")
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]bool{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") || shellTemplates[e.Name()] {
			continue
		}
		onDisk[e.Name()] = true
	}
	wired := map[string]bool{}
	for _, p := range pageTemplates {
		wired[p] = true
		if !onDisk[p] {
			t.Errorf("newWeb parses %q but web/templates/%s does not exist", p, p)
		}
	}
	var orphans []string
	for name := range onDisk {
		if !wired[name] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("template files never parsed by newWeb (unreachable pages): %v", orphans)
	}
}

// TestForumTemplatesParse mirrors wireForumPlugin: site_chrome.html first, then
// the forum glob. It also asserts the forum pages can resolve the shared chrome
// blocks — the whole reason site_chrome.html is named by both parse sites.
func TestForumTemplatesParse(t *testing.T) {
	names, err := fs.Glob(site.FS, "web/templates/forum/*.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no forum templates found")
	}
	// Call the production parser itself rather than re-deriving the file list,
	// so this test cannot pass while wireForumPlugin's real set is broken.
	tmpl, err := pluginTemplates()
	if err != nil {
		t.Fatalf("pluginTemplates: %v", err)
	}
	for _, want := range []string{
		"community_forums.html", "community_category.html", "community_thread.html",
		"community_new_thread.html", "admin_forum_categories.html",
		// From site_chrome.html — invoked by fhead/ffoot, so a missing one is
		// an execute-time failure on every forum page.
		"fhead", "ffoot", "fpagination",
		"site-head", "site-sprite", "site-header", "site-footer", "site-scripts",
	} {
		if tmpl.Lookup(want) == nil {
			t.Errorf("forum set is missing %q", want)
		}
	}
	assertInvocationsResolve(t, "forum set", tmpl)

	// Execute each forum page with ONLY what forum.Deps.BaseData supplies —
	// which, since the chrome-parity fix, is exactly chromeData's always-set
	// keys (forum_web.go BaseData calls the host's chromeData, the same
	// function render() calls, so this list IS the host page list). The
	// forum's own keys (Categories, Threads, Posts, Pagination …) are all
	// legitimately empty on a fresh board, and the per-viewer optional keys
	// (Points/Unread/RoleLabel/MemberSince/EmailUnverified) are still absent
	// for a logged-out viewer — the shared chrome has to degrade rather than
	// error. This is the path that broke before: {{len}} over an absent key
	// aborts the render mid-document.
	for _, page := range []string{
		"community_forums.html", "community_category.html", "community_thread.html",
		"community_new_thread.html", "admin_forum_categories.html",
	} {
		data := chromeKeys()
		data["Path"] = "/community/forums"
		// community_thread.html is only ever reached for a thread that exists,
		// so .Thread is structural, not optional (same contract as
		// profile.html's .Subject). Everything else on the page — Posts,
		// Pagination, CurrentUserID — is legitimately absent on this path.
		if page == "community_thread.html" {
			data["Thread"] = forum.ForumThread{
				ID: 1, CategoryID: 1, UserID: 2, Username: "bob", Title: "Welcome",
				ThreadType: "discussion", CreatedAt: time.Now().Add(-24 * time.Hour),
				LastPostAt: time.Now(),
			}
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, page, data); err != nil {
			t.Errorf("%s: execute with an empty board: %v", page, err)
			continue
		}
		if !strings.Contains(buf.String(), "</html>") {
			t.Errorf("%s: render stopped early — no closing </html>", page)
		}
	}
}

// TestChromeIsDefinedOnce is the guard that replaced a byte-for-byte diff of
// two duplicated shell regions. The chrome now lives in exactly one file; if a
// future edit re-introduces a copy in base.html or forum_chrome.html, whichever
// parses last silently wins and the two shells drift again.
func TestChromeIsDefinedOnce(t *testing.T) {
	chrome := []string{"site-head", "site-sprite", "site-header", "site-footer", "site-scripts", "stat-strip"}
	// admin-subnav is shell too, but it lives in base.html (only admin pages
	// render it, and the forum set has no AdminNav key at all).
	inBase := map[string]bool{"admin-subnav": true}
	chrome = append(chrome, "admin-subnav")
	var files []string
	err := fs.WalkDir(site.FS, "web/templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".html") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, blk := range chrome {
		var in []string
		for _, f := range files {
			b, err := fs.ReadFile(site.FS, f)
			if err != nil {
				t.Fatal(err)
			}
			// {{block "x" .}} is a define too — the shell uses it for the
			// seams a page is meant to override.
			body := stripTmplComments(string(b))
			if strings.Contains(body, `{{define "`+blk+`"}}`) || strings.Contains(body, `{{block "`+blk+`"`) {
				in = append(in, f)
			}
		}
		switch {
		case len(in) == 0:
			t.Errorf("no file defines %q", blk)
		case len(in) == 1:
			want := "web/templates/site_chrome.html"
			if inBase[blk] {
				want = "web/templates/base.html"
			}
			if in[0] != want {
				t.Errorf("%q is defined in %s, expected the shared %s", blk, in[0], want)
			}
		case len(in) > 1:
			// "stat-strip" is a legitimate override: home.html replaces the
			// viewer-scoped strip with one that also carries site figures.
			if blk == "stat-strip" && len(in) == 2 {
				continue
			}
			t.Errorf("%q is defined in %d files (%v) — the shell has been duplicated again", blk, len(in), in)
		}
	}
}

// TestSpriteSymbolsCoverUses checks every <use href="#id"> in every template
// resolves to a <symbol id="…"> in the inline sprite. A missing symbol renders
// nothing at all — no error, no console warning, just a blank slot.
func TestSpriteSymbolsCoverUses(t *testing.T) {
	symbols := map[string]bool{}
	var files []string
	err := fs.WalkDir(site.FS, "web/templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".html") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[string]string{}
	for _, f := range files {
		b, err := fs.ReadFile(site.FS, f)
		if err != nil {
			t.Fatal(err)
		}
		bodies[f] = stripTmplComments(string(b))
		for _, id := range scanAttr(bodies[f], `<symbol id="`) {
			symbols[id] = true
		}
	}
	for _, f := range files {
		for _, ref := range scanAttr(bodies[f], `<use href="#`) {
			// Template-interpolated ids cannot be checked statically.
			if strings.Contains(ref, "{{") {
				continue
			}
			if !symbols[ref] {
				t.Errorf("%s: <use href=\"#%s\"> has no <symbol id=\"%s\"> in any template", f, ref, ref)
			}
		}
	}
}

// structuralKeys are the keys a page's handler ALWAYS sets — the contract
// between handler and template, as opposed to the optional keys that are
// legitimately absent when a capability is unwired.
//
// Templates read these unguarded, which is correct but unforgiving: `{{range
// .X}}` or `{{len .X}}` on an ABSENT map key is an execute error that aborts
// the whole render mid-document (the forum's admin page shipped exactly that
// bug against a nil .Categories). Listing them here makes the contract
// explicit: if a handler ever stops setting one, this test says so.
//
//	admin_settings.html  Tabs — admin_views.go renderSettingsPage always builds
//	                     a (possibly empty) non-nil slice. Section is NOT
//	                     structural: it is absent when no plugin registered one,
//	                     and the page guards it with {{with}}.
//	profile.html         exactly ONE of Missing / Subject — views.go profile
//	home.html            Blocks — views.go home always sets the (possibly
//	                     empty) ordered block list the page ranges over
var structuralKeys = map[string]map[string]any{
	"admin_settings.html": {"Tabs": []settingsTab{}},
	// NOTE Dash is NOT caught by the no-optional-data sweep the way a {{range}}
	// or {{len}} is: {{if .Dash.Alerts}} on an ABSENT .Dash does not error, it
	// renders the whole page as an empty dashboard and passes. So a handler
	// that stopped setting it would degrade silently. The real-data case below
	// is what actually pins it — this entry only supplies the shape.
	"admin_dashboard.html": {"Dash": dashVM{}},
	"profile.html":         {"Missing": true},
	"home.html":            {"Blocks": []homeBlock{}},
}

// chromeKeys are the keys views.go chromeData sets on EVERY render, in both
// template sets — the host's render() and the forum plugin's BaseData. They are
// spelled out here (rather than derived) so the sweeps below render the same
// shape production does, and so a key silently disappearing from chromeData
// shows up as a template failure rather than as a quietly degraded page.
func chromeKeys() map[string]any {
	return map[string]any{
		"User":      (*core.User)(nil),
		"IsAdmin":   false,
		"IsMod":     false,
		"CSRFToken": "",
		"Path":      "/",
		"PathQuery": "/",
		"AdminNav":  []navItem(nil),
		"SiteNav":   []navNode(nil),
		// The nav does `index $.SiteNavGroup "Community"` unguarded, and index
		// on an ABSENT key is an execute error ("index of untyped nil") that
		// kills the whole render — a nil map of the right type is fine, a
		// missing key is not. chromeData always sets it; so must the fixture.
		"SiteNavGroup":   map[string][]navItem(nil),
		"SiteNavAccount": []navItem(nil),
		// chromeData sets this on every render, so the fixture must too — the
		// nav reads it to decide whether Donate appears.
		"DonateEnabled": false,
		"Theme":         defaultTheme(),
		"Themes":        siteThemes,
	}
}

// TestPagesExecuteWithNoData renders every page with the keys render() always
// sets plus that page's structural contract, and nothing else — the shape a
// request gets when every OPTIONAL capability is missing (no usenet plugin, no
// catalog, no forum, no points, no inbox, anonymous viewer). That is a real
// production state, and it is where an unguarded {{range}} blows up.
//
// Execute errors are the failure mode nothing else in this repo catches: they
// are not build errors, not vet errors, and not boot panics. render() only
// logs them, so the browser silently receives a truncated page.
func TestPagesExecuteWithNoData(t *testing.T) {
	w := &web{log: slog.Default(), tmpls: map[string]*template.Template{}}
	for _, page := range pageTemplates {
		tmpl := template.Must(parseSet(w, page))

		// Exactly what render() sets for a logged-out viewer.
		data := chromeKeys()
		for k, v := range structuralKeys[page] {
			data[k] = v
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
			t.Errorf("%s: execute with no optional data: %v", page, err)
			continue
		}
		if !strings.Contains(buf.String(), "</html>") {
			t.Errorf("%s: render stopped early — no closing </html>", page)
		}
	}
}

// TestPagesExecuteForSignedInViewer is the same sweep with a viewer present and
// every per-viewer key render() can set, including a GENUINE ZERO for points
// and unread — the case the Has* sentinels exist for.
func TestPagesExecuteForSignedInViewer(t *testing.T) {
	w := &web{log: slog.Default(), tmpls: map[string]*template.Template{}}
	u := &core.User{ID: 1, Username: "alice", Role: core.RoleAdmin, CreatedAt: time.Now().Add(-72 * time.Hour)}
	for _, page := range pageTemplates {
		tmpl := template.Must(parseSet(w, page))
		data := chromeKeys()
		for k, v := range map[string]any{
			"User":            u,
			"IsAdmin":         true,
			"IsMod":           true,
			"CSRFToken":       "tok",
			"AdminNav":        []navItem{{Label: "Settings", Href: "/admin/settings"}},
			"RoleLabel":       roleName(u.Role),
			"MemberSince":     u.CreatedAt,
			"Points":          0,
			"HasPoints":       true,
			"Unread":          0,
			"HasUnread":       true,
			"EmailUnverified": true,
		} {
			data[k] = v
		}
		for k, v := range structuralKeys[page] {
			data[k] = v
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
			t.Errorf("%s: execute for signed-in viewer: %v", page, err)
			continue
		}
		out := buf.String()
		if !strings.Contains(out, "</html>") {
			t.Errorf("%s: render stopped early — no closing </html>", page)
		}
		// A genuine zero must still produce its figure; that is the whole point
		// of HasPoints/HasUnread over {{if .Points}}. The viewer's points moved
		// out of the per-page stat strip and into the shared top-nav ratio bar,
		// so the guard now has to hold on EVERY page rather than only on the two
		// that happened to carry tiles.
		//
		// The profile's OWN tiles are not asserted here: they are the subject's
		// figures, guarded on HasSubjectPoints / IsSelf, and legitimately absent
		// on this no-optional-data path. TestPagesExecuteWithRealData pins them,
		// with IsSelf set. Before points moved, this branch nominally covered
		// home and profile — but it was satisfied by the chrome strip both
		// times, so it never actually reached either page's own markup.
		// stat-figure--points since the points tile became one of the shared
		// stat figures; the guard it pins (HasPoints, so a genuine 0 still
		// renders) is unchanged.
		if !strings.Contains(out, "stat-figure--points") {
			t.Errorf("%s: no points figure in the top nav for a zero-balance viewer", page)
		}
	}
}

// TestPagesExecuteWithRealData is the other half of the sweep: the data-heavy
// pages rendered with POPULATED view models, so the loops, the cover/fallback
// branches, the tag rows and the pagination arithmetic all actually run. The
// no-data test proves the guards hold; this proves the bodies work.
func TestPagesExecuteWithRealData(t *testing.T) {
	w := &web{log: slog.Default(), tmpls: map[string]*template.Template{}}
	u := &core.User{ID: 1, Username: "alice", Role: core.RoleAdmin, CreatedAt: time.Now().Add(-500 * time.Hour)}
	now := time.Now()

	// Two rows on purpose: one WITH cover art, one without, so both the
	// <img class="poster__img"> branch and the .poster__fallback branch run.
	rows := []searchRow{
		{ID: 1, Title: "Some.Show.S01E02.1080p.WEB-DL.x264-GRP", Size: "1.4 GB", SizeBytes: 1503238553,
			Posted: "2026-08-01", PostedAt: now.Add(-72 * time.Hour), Category: "TV/HD", CategoryID: 5040,
			Group: "alt.binaries.tv", Resolution: "1080p", Source: "WEB-DL",
			Cover: "https://image.tmdb.org/t/p/w300/x.jpg", Tags: []string{"1080p", "WEB-DL", "x264"}},
		{ID: 2, Title: "[SubGrp] Another Title - 03", Size: "512 MB", SizeBytes: 536870912,
			Posted: "—", Category: "TV/Anime", CategoryID: 5070, Group: "alt.binaries.anime",
			Tags: []string{"720p"}},
	}
	stats := siteStatsVM{Releases: 128394, Groups: 12, Categories: 6}
	groups := []groupRowVM{{Rank: 1, Name: "alt.binaries.tv", NZBs: 90210, URL: "/search?group=alt.binaries.tv"}}
	threads := []forumThreadVM{{ID: 7, Title: "Welcome", URL: "/community/forums/thread/7",
		Author: "bob", Category: "General", CategoryID: 1, Replies: 4, LastPostAt: now.Add(-time.Hour), Pinned: true}}
	posters := []forumPosterVM{{Rank: 1, UserID: 2, Username: "bob", URL: "/u/bob", Posts: 41}}

	widgets := []widgetVM{{Title: "Guestbook", Fragment: template.HTML("<div class=\"card-body\">hi</div>")}}

	viewer := func() map[string]any {
		data := chromeKeys()
		for k, v := range map[string]any{
			"User": u, "IsAdmin": true, "IsMod": true, "CSRFToken": "tok",
			"AdminNav":  []navItem{{Label: "Settings", Href: "/admin/settings"}},
			"RoleLabel": roleName(u.Role), "MemberSince": u.CreatedAt,
			"Points": 1250, "HasPoints": true, "Unread": 3, "HasUnread": true,
		} {
			data[k] = v
		}
		return data
	}

	cases := map[string]map[string]any{
		// Built through the PRODUCTION orderedBlocks so the page is rendered
		// with the same block order home() ships, and so every arm of the
		// template's switch runs at least once.
		"home.html": {
			"Title": "Home", "Stats": stats,
			"Blocks": orderedBlocks(map[string]any{
				blockWidgets:        widgets,
				blockFeatured:       rows,
				blockLatestReleases: rows,
				blockTopGroups:      groups,
				blockLatestTopics:   threads,
				blockTopPosters:     posters,
			}),
		},
		"browse.html": {"Title": "Browse", "CatID": 5000, "CatName": "TV", "Results": rows, "Total": 4210},
		"search.html": {"Title": "Search", "Q": "some show", "Results": rows},
		"groups.html": {"Title": "Groups", "Configured": true,
			"Groups": []pluginapi.GroupInfo{{Name: "alt.binaries.tv", Active: true, NZBs: 90210}}},
		"release.html": {"Title": "Some.Show", "Release": releaseVM{
			ID: 1, Title: "Some.Show.S01E02.1080p.WEB-DL.x264-GRP", Size: "1.4 GB",
			Posted: "2026-08-01 09:00", Group: "alt.binaries.tv", Poster: "u@example.invalid",
			Category: "TV/HD", Cover: "https://image.tmdb.org/t/p/w300/x.jpg",
			Tags:  []string{"1080p", "WEB-DL", "x264"},
			Files: []releaseFileVM{{Name: "show.s01e02.mkv", Size: "1.4 GB", Segments: 2048}},
		}},
		"profile.html": {"Title": "alice", "Subject": u, "IsSelf": true,
			"Widgets": []widgetVM{{Title: "Streak", Fragment: template.HTML("<div class=\"card-body\">7 days</div>")}}},
		"admin_settings.html": {"Title": "Settings",
			"Tabs": []settingsTab{
				{Href: "/admin/settings/usenet", Label: "Usenet", Active: true},
				{Href: "/admin/settings/catalog", Label: "Catalog"}},
			"Section": &settingsSection{Slug: "usenet", Title: "Usenet",
				Fragment: template.HTML("<div class=\"card\">cfg</div>")}},
		"site_page.html": {"Title": "Inbox", "Page": template.HTML("<div class=\"card\">body</div>")},
		// Populated, because an EMPTY dashboard renders happily — see the note
		// on structuralKeys. This is the case that fails if the tiles stop
		// reaching the page.
		"admin_dashboard.html": {"Title": "Dashboard", "Dash": dashVM{
			Tiles: []statTile{
				{Label: "Releases", Value: "7,829", Href: "/browse"},
				{Label: "Members", Value: "2", Sub: "0 in the last 7 days"}},
			Alerts: []statTile{
				{Label: "Tickets awaiting a reply", Value: "1", Href: "/admin/tickets", Warn: true}},
		}},
	}

	for page, extra := range cases {
		tmpl := template.Must(parseSet(w, page))
		data := viewer()
		for k, v := range extra {
			data[k] = v
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
			t.Errorf("%s: execute with real data: %v", page, err)
			continue
		}
		out := buf.String()
		if !strings.Contains(out, "</html>") {
			t.Errorf("%s: render stopped early — no closing </html>", page)
		}
		// "<no value>" is text/template printing an absent map key. It is not
		// an error, it just leaks into the page — the silent-typo failure mode.
		if strings.Contains(out, "<no value>") {
			t.Errorf("%s: rendered a literal \"<no value>\" — a key the template reads is not set", page)
		}
	}
}

// stripTmplComments removes {{/* … */}} regions so illustrative markup inside a
// template comment is not mistaken for real markup.
func stripTmplComments(s string) string {
	for {
		i := strings.Index(s, "{{/*")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "*/}}")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+len("*/}}"):]
	}
}

// scanAttr returns the values that follow each occurrence of prefix, up to the
// closing quote. Deliberately dumb — these are our own hand-written templates.
func scanAttr(s, prefix string) []string {
	var out []string
	for i := 0; ; {
		j := strings.Index(s[i:], prefix)
		if j < 0 {
			return out
		}
		start := i + j + len(prefix)
		k := strings.IndexByte(s[start:], '"')
		if k < 0 {
			return out
		}
		out = append(out, s[start:start+k])
		i = start + k
	}
}

// ---------------------------------------------------------------------------
// Theme layer
//
// After the tokens/theme split, tokens.css DECLARES no visual value at all and
// each theme file carries the COMPLETE visual set. That makes the theme layer
// load-bearing in a way an override layer never was: a theme missing a token
// silently drops that one property rather than inheriting a sane base.
//
// tokens.css does @import the DEFAULT theme, but only as the floor for pages
// outside this app's chrome (loon's framework admin heads link bootstrap +
// tokens.css + theme.css and nothing else). It is not a base layer for our own
// pages: a linked theme sits later in the cascade and wins every property, so
// the parity tests below still have to hold on their own.
// ---------------------------------------------------------------------------

// themeFSPath maps a themeOption.Href ("/static/css/themes/x.css") to its path
// inside site.FS ("web/static/css/themes/x.css"). Href is a constant in
// theme.go's allowlist, so this is the only place the two spellings meet.
func themeFSPath(href string) string { return "web" + href }

// cssStripComments removes /* … */ so a token mentioned in prose is not counted
// as a declaration.
func cssStripComments(s string) string {
	for {
		i := strings.Index(s, "/*")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "*/")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+len("*/"):]
	}
}

// cssDeclaredTokens returns the custom properties a stylesheet DECLARES (the
// left-hand side of `--name:`). A var(--name) reference has no colon after the
// name, so references are not counted.
func cssDeclaredTokens(src string) map[string]bool {
	out := map[string]bool{}
	s := cssStripComments(src)
	for i := 0; ; {
		j := strings.Index(s[i:], "--")
		if j < 0 {
			return out
		}
		start := i + j
		k := start + 2
		for k < len(s) && (s[k] == '-' || s[k] == '_' ||
			(s[k] >= 'a' && s[k] <= 'z') || (s[k] >= 'A' && s[k] <= 'Z') ||
			(s[k] >= '0' && s[k] <= '9')) {
			k++
		}
		name := s[start:k]
		// only a declaration if the next non-space character is ':'
		e := k
		for e < len(s) && (s[e] == ' ' || s[e] == '\t') {
			e++
		}
		if e < len(s) && s[e] == ':' && name != "--" {
			out[name] = true
		}
		i = k
	}
}

// cssVarRefs returns every custom property a stylesheet READS via var(--x).
func cssVarRefs(src string) []string { all, _ := cssScanVarRefs(src); return all }

// cssRequiredVarRefs is cssVarRefs minus the references that carry a fallback.
// The distinction matters: var(--x, 8px) still renders when --x is undefined,
// while a bare var(--x) drops the whole DECLARATION. Only the bare form is a
// hard requirement on the token set.
func cssRequiredVarRefs(src string) []string { _, req := cssScanVarRefs(src); return req }

// cssScanVarRefs is the one scanner behind both: it walks var( … ) and splits
// the names by whether a comma (a fallback) followed the name or not.
func cssScanVarRefs(src string) (all, required []string) {
	s := cssStripComments(src)
	const open = "var("
	for i := 0; ; {
		j := strings.Index(s[i:], open)
		if j < 0 {
			return all, required
		}
		start := i + j + len(open)
		for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
			start++
		}
		k := start
		for k < len(s) && s[k] != ',' && s[k] != ')' {
			k++
		}
		name := strings.TrimSpace(s[start:k])
		if strings.HasPrefix(name, "--") {
			all = append(all, name)
			if k < len(s) && s[k] == ')' {
				required = append(required, name)
			}
		}
		i = start
	}
}

// cssImports returns the targets of every @import in a stylesheet, in source
// order. Both spellings are recognised — @import url("x.css") and
// @import "x.css" — because an import this scan MISSED would make the check
// below quietly more lenient than a browser.
func cssImports(src string) []string {
	var out []string
	s := cssStripComments(src)
	for i := 0; ; {
		j := strings.Index(s[i:], "@import")
		if j < 0 {
			return out
		}
		i += j + len("@import")
		rest := strings.TrimSpace(s[i:])
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "url("))
		if rest == "" {
			continue
		}
		if q := rest[0]; q == '"' || q == '\'' {
			if e := strings.IndexByte(rest[1:], q); e >= 0 {
				out = append(out, rest[1:1+e])
			}
			continue
		}
		if e := strings.IndexAny(rest, ");"); e >= 0 {
			out = append(out, strings.TrimSpace(rest[:e]))
		}
	}
}

// TestThemeStylesheetsExist guards the head's unconditional {{.Theme.Href}}:
// every allowlist entry must resolve to a real file, or that theme renders an
// unstyled page. Href is a literal in theme.go precisely so this is checkable.
func TestThemeStylesheetsExist(t *testing.T) {
	for _, th := range siteThemes {
		b, err := fs.ReadFile(site.FS, themeFSPath(th.Href))
		if err != nil {
			t.Errorf("theme %q: %s is not readable: %v", th.Key, th.Href, err)
			continue
		}
		if len(b) == 0 {
			t.Errorf("theme %q: %s is empty", th.Key, th.Href)
		}
	}
}

// TestThemeFilesAreRootOnly enforces the one structural rule of the theme
// layer: a theme file may contain nothing but :root. layout.css, components.css
// and theme.css all load AFTER it, so any actual rule a theme tried to carry
// would simply be overridden — silently, and only for that theme.
func TestThemeFilesAreRootOnly(t *testing.T) {
	for _, th := range siteThemes {
		b, err := fs.ReadFile(site.FS, themeFSPath(th.Href))
		if err != nil {
			t.Fatalf("theme %q: %v", th.Key, err)
		}
		src := cssStripComments(string(b))
		for i, prev := 0, 0; ; {
			j := strings.IndexByte(src[i:], '{')
			if j < 0 {
				break
			}
			open := i + j
			sel := strings.TrimSpace(src[prev:open])
			if sel != ":root" {
				t.Errorf("theme %q: contains a non-:root block %q — it would be overridden by the layers that load after it",
					th.Key, sel)
			}
			end := strings.IndexByte(src[open:], '}')
			if end < 0 {
				t.Errorf("theme %q: unbalanced braces", th.Key)
				break
			}
			i = open + end + 1
			prev = i
		}
	}
}

// TestThemeFilesDeclareIdenticalTokenSets is the single most important check in
// this file's theme half. Custom-property lookup fails SILENTLY: a var(--x)
// with no --x in scope drops the declaration rather than erroring, so a token
// present in two themes and missing from the third is invisible until someone
// switches to that theme and notices one wrong colour.
//
// The three files must therefore declare exactly the same NAMES (the values are
// what differ). A new token goes in all three, or in tokens.css if every theme
// agrees on the value.
func TestThemeFilesDeclareIdenticalTokenSets(t *testing.T) {
	decls := map[string]map[string]bool{}
	union := map[string]bool{}
	for _, th := range siteThemes {
		b, err := fs.ReadFile(site.FS, themeFSPath(th.Href))
		if err != nil {
			t.Fatalf("theme %q: %v", th.Key, err)
		}
		decls[th.Key] = cssDeclaredTokens(string(b))
		for name := range decls[th.Key] {
			union[name] = true
		}
	}
	if len(union) == 0 {
		t.Fatal("no tokens found in any theme — the scan is broken, not the CSS")
	}
	names := make([]string, 0, len(union))
	for name := range union {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, th := range siteThemes {
			if !decls[th.Key][name] {
				t.Errorf("token %s is declared by other themes but MISSING from %q — it silently falls back to nothing there",
					name, th.Key)
			}
		}
	}
}

// TestEveryReferencedTokenResolves walks every var(--x) in the consumer layers
// and checks it is declared by tokens.css or by EACH theme. Same silent-failure
// mode as the parity test, approached from the consumer side: it catches a rule
// that reads a token no theme ever defined.
// setAtRenderTime lists custom properties a TEMPLATE supplies per request, so
// no stylesheet can declare them and their absence here is correct rather than
// a gap. Every entry carries its reason — an exception list without them is
// where findings go to die (scripts/README.md makes the same argument for
// audit_css.py's RUNTIME set).
var setAtRenderTime = map[string]bool{
	// release.html sets --backdrop to the show's background art, which is a
	// different image for every release. A theme cannot know it, and a default
	// would be an image chosen for all releases at once.
	"--backdrop": true,
}

func TestEveryReferencedTokenResolves(t *testing.T) {
	consumers := []string{
		"web/static/css/layout.css",
		"web/static/css/components.css",
		"web/static/css/theme.css",
	}
	base, err := fs.ReadFile(site.FS, "web/static/css/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	structural := cssDeclaredTokens(string(base))

	type ref struct{ file, name string }
	var refs []ref
	for _, f := range consumers {
		b, err := fs.ReadFile(site.FS, f)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range cssVarRefs(string(b)) {
			refs = append(refs, ref{f, name})
		}
	}
	if len(refs) == 0 {
		t.Fatal("found no var() references — the scan is broken, not the CSS")
	}
	for _, th := range siteThemes {
		b, err := fs.ReadFile(site.FS, themeFSPath(th.Href))
		if err != nil {
			t.Fatalf("theme %q: %v", th.Key, err)
		}
		themed := cssDeclaredTokens(string(b))
		for _, r := range refs {
			if structural[r.name] || themed[r.name] || setAtRenderTime[r.name] {
				continue
			}
			t.Errorf("%s references %s, declared neither by tokens.css nor by theme %q",
				r.file, r.name, th.Key)
		}
	}
}

// TestTokensAloneResolvesThemeCSS is the check the /admin/jobs/config bug
// earned. Not every page that renders against theme.css is one of ours: loon's
// framework admin pages build their own inline <head> and link exactly three
// sheets —
//
//	bootstrap.min.css + tokens.css + theme.css
//
// (schedule/config_admin.go, schedule/admin.go, core/admin.go). They know
// nothing about this app's theme allowlist and cannot read the viewer's cookie,
// so THAT set is the whole cascade they get. When tokens.css declared no visual
// value and imported nothing, 33 of the tokens theme.css reads resolved to
// nothing: .bg-dark, .text-light, .card, every .alert and every border lost its
// colour and /admin/jobs/config rendered black-on-white. tokens.css now
// @imports the default theme to close that, and this test is what keeps it
// closed — it models exactly what a browser loads from those three links.
//
// References with a fallback are excluded: var(--x, 8px) renders fine with no
// --x, so only the bare form is a requirement.
func TestTokensAloneResolvesThemeCSS(t *testing.T) {
	const dir = "web/static/css"

	base, err := fs.ReadFile(site.FS, dir+"/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	// Everything a browser has in scope after loading tokens.css by itself:
	// its own declarations plus those of whatever it pulls in.
	resolved := cssDeclaredTokens(string(base))
	imports := cssImports(string(base))
	if len(imports) == 0 {
		t.Fatal("tokens.css @imports nothing — it declares no visual token either, so loon's three-link admin heads resolve none of theme.css's colours and render unstyled")
	}
	for _, imp := range imports {
		b, err := fs.ReadFile(site.FS, path.Join(dir, imp))
		if err != nil {
			t.Fatalf("tokens.css @imports %q, which does not resolve to a file: %v", imp, err)
		}
		for name := range cssDeclaredTokens(string(b)) {
			resolved[name] = true
		}
	}

	tc, err := fs.ReadFile(site.FS, dir+"/theme.css")
	if err != nil {
		t.Fatal(err)
	}
	refs := cssRequiredVarRefs(string(tc))
	if len(refs) == 0 {
		t.Fatal("found no var() references in theme.css — the scan is broken, not the CSS")
	}
	seen := map[string]bool{}
	var missing []string
	for _, name := range refs {
		if resolved[name] || seen[name] {
			continue
		}
		seen[name] = true
		missing = append(missing, name)
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("theme.css reads %s with no fallback, and tokens.css does not resolve it on its own — every loon admin page that links only bootstrap+tokens+theme.css renders that declaration unstyled", name)
	}
}

// TestDefaultThemeIsTheImportedOne pins the OTHER half of that seam: the file
// tokens.css imports must be the allowlist's default, or an un-themed framework
// page and this app's own default page would disagree about what the site looks
// like.
func TestDefaultThemeIsTheImportedOne(t *testing.T) {
	base, err := fs.ReadFile(site.FS, "web/static/css/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	want := themeFSPath(defaultTheme().Href) // web/static/css/themes/<default>.css
	got := cssImports(string(base))
	if len(got) != 1 {
		t.Fatalf("tokens.css has %d @imports %v, want exactly 1 (the default theme)", len(got), got)
	}
	if p := path.Join("web/static/css", got[0]); p != want {
		t.Errorf("tokens.css imports %q (%s), but theme.go's default is %s", got[0], p, want)
	}
}

// wantSheetOrder is the stylesheet order site_chrome.html's "site-head" must
// emit. Load-bearing in both directions: the theme has to come after tokens.css
// and before the layers that consume it, and theme.css has to stay LAST because
// its Bootstrap-subset utilities must beat the component defaults that loon's
// admin pages and plugin-injected markup render against.
var wantSheetOrder = []string{
	"/static/css/bootstrap.min.css",
	"/static/css/tokens.css",
	"", // the active theme — filled in from .Theme.Href per case
	"/static/css/layout.css",
	"/static/css/components.css",
	"/static/css/theme.css",
}

// TestStylesheetOrder renders the real head for every theme and asserts the
// emitted <link> order — including that exactly ONE theme is linked (the themes
// are complete token sets, so a second would silently win) and theme.css last.
func TestStylesheetOrder(t *testing.T) {
	w := &web{log: slog.Default(), tmpls: map[string]*template.Template{}}
	tmpl := template.Must(parseSet(w, "home.html"))
	for _, th := range siteThemes {
		want := append([]string(nil), wantSheetOrder...)
		want[2] = th.Href

		data := chromeKeys()
		data["Theme"] = th
		for k, v := range structuralKeys["home.html"] {
			data[k] = v
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
			t.Fatalf("theme %q: %v", th.Key, err)
		}
		got := scanAttr(buf.String(), `<link rel="stylesheet" href="`)
		// Strip the cache-busting ?v=<hash> (assetversion_web.go). What this
		// test is about is ORDER — bootstrap, tokens, theme, layout,
		// components, and theme.css last — and a version query says nothing
		// about that. Comparing full URLs would make the test fail every time
		// a stylesheet's CONTENT changed, which is exactly when it matters
		// least.
		for i, u := range got {
			if q := strings.IndexByte(u, '?'); q >= 0 {
				got[i] = u[:q]
			}
		}
		if len(got) != len(want) {
			t.Fatalf("theme %q: got %d stylesheets %v, want %d %v", th.Key, len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("theme %q: stylesheet %d is %q, want %q (full order %v)", th.Key, i, got[i], want[i], got)
			}
		}
		if got[len(got)-1] != "/static/css/theme.css" {
			t.Errorf("theme %q: theme.css must be the LAST stylesheet, got %q", th.Key, got[len(got)-1])
		}
		n := 0
		for _, href := range got {
			if strings.Contains(href, "/themes/") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("theme %q: %d theme stylesheets linked, want exactly 1 (%v)", th.Key, n, got)
		}
	}
}

// TestHostileThemeNameNeverReachesThePage is the injection/traversal test at the
// level that actually matters: the rendered document. theme_test.go proves
// themeByName rejects hostile input; this proves the TEMPLATE cannot be made to
// emit it even so, because the head prints a constant Href off the matched
// allowlist entry rather than anything derived from the cookie.
func TestHostileThemeNameNeverReachesThePage(t *testing.T) {
	hostile := []string{
		"../../etc/passwd",
		"../../../../web/static/css/tokens.css",
		"cosmic-void/../nord",
		"cosmic-void/../../../../etc/shadow",
		`x" onload="alert(1)`,
		`" onerror="alert(1)`,
		"nord'><script>alert(1)</script>",
		"//evil.example/x.css",
		"https://evil.example/x.css",
		"data:text/css,body{display:none}",
		"javascript:alert(1)",
		"cosmic-void\x00nord",
		"COSMIC-VOID",   // exact match only: no case folding
		"  cosmic-void", // exact match only: no trimming
		"cosmic-void ",
		strings.Repeat("a", 4096),
		"",
	}
	allowed := map[string]bool{}
	for _, th := range siteThemes {
		allowed[th.Href] = true
	}

	w := &web{log: slog.Default(), tmpls: map[string]*template.Template{}}
	tmpl := template.Must(parseSet(w, "home.html"))

	for _, name := range hostile {
		// Exactly what chromeData does with the cookie value.
		th := themeByName(name)
		if !allowed[th.Href] {
			t.Fatalf("themeByName(%q) resolved outside the allowlist: %+v", name, th)
		}

		data := chromeKeys()
		data["Theme"] = th
		for k, v := range structuralKeys["home.html"] {
			data[k] = v
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
			t.Fatalf("theme %q: %v", name, err)
		}
		out := buf.String()

		for _, href := range scanAttr(out, `<link rel="stylesheet" href="`) {
			// Compare the PATH, without the cache-busting ?v=<hash>
			// (assetversion_web.go). Deliberately stripped only from the
			// right-hand side of a '?': everything before it still has to be
			// an exact allowlist member, so a traversal attempt cannot smuggle
			// itself through by ending up in a query string.
			path := href
			if q := strings.IndexByte(path, '?'); q >= 0 {
				path = path[:q]
			}
			if strings.Contains(path, "/themes/") && !allowed[path] {
				t.Errorf("input %q produced non-allowlisted stylesheet %q", name, href)
			}
		}
		for _, key := range scanAttr(out, `data-theme="`) {
			ok := false
			for _, s := range siteThemes {
				if s.Key == key {
					ok = true
				}
			}
			if !ok {
				t.Errorf("input %q produced data-theme=%q, which is not an allowlist key", name, key)
			}
		}
		// No fragment of the hostile string may appear anywhere. (Skip the
		// inputs that are trivially substrings of the legitimate default.)
		if len(name) > 8 && !strings.Contains("cosmic-void", strings.TrimSpace(name)) &&
			strings.Contains(out, name) {
			t.Errorf("input %q was echoed into the rendered page", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Home block stack
// ---------------------------------------------------------------------------

// homeBlockFixtures is one populated payload per block name, of the exact type
// home() puts in homeBlock.Data. Keyed by the same constants views.go uses, so
// a renamed block breaks compilation here rather than rendering nothing.
func homeBlockFixtures() map[string]any {
	now := time.Now()
	rows := []searchRow{
		{ID: 1, Title: "Some.Show.S01E02.1080p.WEB-DL.x264-GRP", Size: "1.4 GB", SizeBytes: 1503238553,
			Posted: "2026-08-01", PostedAt: now.Add(-72 * time.Hour), Category: "TV/HD", CategoryID: 5040,
			Group: "alt.binaries.tv", Resolution: "1080p", Source: "WEB-DL",
			Cover: "https://image.tmdb.org/t/p/w300/x.jpg", Tags: []string{"1080p", "WEB-DL"}},
		{ID: 2, Title: "[SubGrp] Another Title - 03", Size: "512 MB", SizeBytes: 536870912,
			Posted: "—", Category: "TV/Anime", CategoryID: 5070, Group: "alt.binaries.anime"},
	}
	return map[string]any{
		blockWidgets:        []widgetVM{{Title: "Guestbook", Fragment: template.HTML(`<div class="card-body">hi</div>`)}},
		blockFeatured:       rows,
		blockLatestReleases: rows,
		blockPopular:        []searchRow{{ID: 7, Title: "Grabbed", Size: "1 GB", Category: "TV", Grabs: 12}},
		blockTopGroups:      []groupRowVM{{Rank: 1, Name: "alt.binaries.tv", NZBs: 90210, URL: "/search?group=alt.binaries.tv"}},
		blockLatestTopics: []forumThreadVM{{ID: 7, Title: "Welcome", URL: "/community/forums/thread/7",
			Author: "bob", AuthorRole: "Member", Category: "General", CategoryID: 1,
			Replies: 4, LastPostAt: now.Add(-time.Hour), Pinned: true}},
		blockTopPosters: []forumPosterVM{{Rank: 1, UserID: 2, Username: "bob", Role: "Member", URL: "/u/bob", Posts: 41}},
		// The releases slot's empty state. Its "payload" is the Configured bool,
		// and in production it is mutually exclusive with blockLatestReleases —
		// the tests that render the whole map at once are the only place both
		// appear, which is fine: they assert per-block markers, not exclusivity.
		blockNoReleases: true,
	}
}

// renderHome executes home.html through base.html with the given block list.
func renderHome(t *testing.T, blocks []homeBlock, extra map[string]any) string {
	t.Helper()
	w := &web{log: slog.Default(), tmpls: map[string]*template.Template{}}
	tmpl := template.Must(parseSet(w, "home.html"))
	data := chromeKeys()
	data["Title"] = "Home"
	data["Blocks"] = blocks
	for k, v := range extra {
		data[k] = v
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "base.html", data); err != nil {
		t.Fatalf("execute home.html: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "</html>") {
		t.Fatal("render stopped early — no closing </html>")
	}
	if strings.Contains(out, "<no value>") {
		t.Error("rendered a literal \"<no value>\" — a key the template reads is not set")
	}
	return out
}

// blockMarker is the class each block's <section> carries, so a test can assert
// the arm actually RAN rather than that the page merely did not error.
var blockMarker = map[string]string{
	blockWidgets:        "blocks__widget",
	blockFeatured:       "blocks__featured",
	blockLatestReleases: "blocks__latest-releases",
	// The empty state presents itself AS the releases panel, so it carries
	// blocks__latest-releases too; blocks__no-releases is the class that tells
	// the two apart. The "no other block appeared" checks below match on
	// marker+'"', i.e. the last class in the attribute, which is why the empty
	// state's own marker has to come last in its class list.
	blockNoReleases:   "blocks__no-releases",
	blockPopular:      "blocks__popular",
	blockTopGroups:    "blocks__top-groups",
	blockLatestTopics: "blocks__latest-topics",
	blockTopPosters:   "blocks__top-posters",
}

// TestHomeBlockStackEachBlockAlone renders the page with exactly ONE block
// present, for each block in turn. home() only emits a block whose data
// resolved and is non-empty, so every subset of the stack is a real production
// state — including the one where a plugin is the only thing wired up.
func TestHomeBlockStackEachBlockAlone(t *testing.T) {
	fixtures := homeBlockFixtures()
	for _, name := range homeBlockOrder {
		data, ok := fixtures[name]
		if !ok {
			t.Fatalf("block %q has no fixture — add one when adding a block", name)
		}
		out := renderHome(t, orderedBlocks(map[string]any{name: data}), nil)

		marker := blockMarker[name]
		if marker == "" {
			t.Fatalf("block %q has no marker class registered", name)
		}
		if !strings.Contains(out, marker) {
			t.Errorf("block %q rendered alone but its %q section is missing — the switch arm did not run",
				name, marker)
		}
		for other, m := range blockMarker {
			if other == name {
				continue
			}
			if strings.Contains(out, m+`"`) {
				t.Errorf("block %q rendered alone but %q also appeared", name, other)
			}
		}
	}
}

// TestHomeBlockStackAllBlocks renders the full stack and asserts every arm ran
// and that they appear in homeBlockOrder — the order IS the page design, and a
// reordered switch in the template would otherwise go unnoticed.
func TestHomeBlockStackAllBlocks(t *testing.T) {
	out := renderHome(t, orderedBlocks(homeBlockFixtures()), map[string]any{
		"Stats": siteStatsVM{Releases: 128394, Groups: 12, Categories: 6},
	})
	prev := -1
	for _, name := range homeBlockOrder {
		at := strings.Index(out, blockMarker[name])
		if at < 0 {
			t.Errorf("block %q is missing from the full stack", name)
			continue
		}
		if at < prev {
			t.Errorf("block %q renders out of homeBlockOrder", name)
		}
		prev = at
	}
}

// TestHomeBlockStackEmpty is the other end: nothing resolved except the
// releases slot's empty state, which home() emits whenever latest_releases came
// back with no rows (no indexer in the build, or one that has not indexed
// anything yet). The page must render that guidance, and — the rule this whole
// indexer is built on — must not invent a single row it has no data for.
//
// Marker classes are deliberately NOT the probe here: "block-nothing-yet"
// presents itself as the Latest releases panel with an empty body, so it
// legitimately carries blocks__latest-releases. What must be absent is DATA.
func TestHomeBlockStackEmpty(t *testing.T) {
	// Markup that can only exist if a block actually had rows to render.
	dataMarkers := []string{
		"data-table__title",    // a release row
		"carousel__item",       // a featured poster
		"topic-listings__item", // a forum topic
		"rank-chip",            // a top-groups / top-posters rank
		"list-row__title",      // any populated list row
	}
	for _, configured := range []bool{true, false} {
		out := renderHome(t, orderedBlocks(map[string]any{blockNoReleases: configured}), nil)
		if !strings.Contains(out, "No releases.") {
			t.Errorf("Configured=%v: empty stack rendered no empty state at all", configured)
		}
		for _, m := range dataMarkers {
			if strings.Contains(out, m) {
				t.Errorf("Configured=%v: empty stack rendered %q — the page invented data it does not have",
					configured, m)
			}
		}
		// The two causes get two different messages, both pointing at Setup.
		wantsIndexerHint := strings.Contains(out, "no Usenet indexer")
		if configured && wantsIndexerHint {
			t.Error("Configured=true rendered the no-indexer message")
		}
		if !configured && !wantsIndexerHint {
			t.Error("Configured=false did not render the no-indexer message")
		}
	}
	// A nil slice is the same case as an empty one and must not panic. home()
	// no longer produces one (the releases slot always fills), but the template
	// must not depend on that.
	renderHome(t, nil, nil)
}

// TestHomeEmptyStateSurvivesOtherBlocks is the regression that made no_releases
// its own block. The guidance used to hang off {{range}}'s else arm, i.e. it
// rendered only when the ENTIRE stack was empty — and the seeded forum always
// has rows. So the single most common broken-install shape (no usenet plugin,
// a working forum) showed latest topics and top posters and NOTHING telling an
// admin where to add an indexer.
func TestHomeEmptyStateSurvivesOtherBlocks(t *testing.T) {
	fixtures := homeBlockFixtures()
	// Exactly what home() builds with w.usenet == nil and a seeded forum.
	out := renderHome(t, orderedBlocks(map[string]any{
		blockNoReleases:   false, // Configured=false: no indexer in this build
		blockLatestTopics: fixtures[blockLatestTopics],
		blockTopPosters:   fixtures[blockTopPosters],
	}), nil)

	if !strings.Contains(out, "no Usenet indexer") {
		t.Error("the forum blocks crowded out the no-indexer guidance — that is the bug this block exists to fix")
	}
	if !strings.Contains(out, blockMarker[blockNoReleases]) {
		t.Errorf("the %q section is missing — its switch arm did not run", blockNoReleases)
	}
	// …and the panels that DID have rows still render alongside it.
	for _, name := range []string{blockLatestTopics, blockTopPosters} {
		if !strings.Contains(out, blockMarker[name]) {
			t.Errorf("block %q stopped rendering once the empty state joined the stack", name)
		}
	}
	// The empty state must still invent nothing: no release table, no posters.
	for _, m := range []string{"data-table__title", "carousel__item"} {
		if strings.Contains(out, m) {
			t.Errorf("rendered %q with no releases at all", m)
		}
	}
}

// TestHomeBlockStackIgnoresUnknownNames pins the forward-compatibility half of
// the contract: home.html dispatches on .Name, so a block Go starts emitting
// before the template has an arm for it must render NOTHING rather than error
// or leak its data. That is what lets a new block ship Go-side first.
func TestHomeBlockStackIgnoresUnknownNames(t *testing.T) {
	blocks := []homeBlock{
		{Name: "not_a_real_block", Data: []string{"SENTINEL-VALUE"}},
		{Name: blockPopular, Data: []searchRow{{ID: 7, Title: "Grabbed", Size: "1 GB", Grabs: 12}}},
		{Name: blockTopGroups, Data: []groupRowVM{{Rank: 1, Name: "alt.binaries.tv", NZBs: 1, URL: "/x"}}},
	}
	out := renderHome(t, blocks, nil)
	if strings.Contains(out, "SENTINEL-VALUE") {
		t.Error("an unrecognised block leaked its data into the page")
	}
	if !strings.Contains(out, blockMarker[blockTopGroups]) {
		t.Error("an unrecognised block stopped the blocks after it from rendering")
	}
}

// TestHomeBlockOrderMatchesTemplateArms keeps the three places that must agree
// in step: the block constants, homeBlockOrder, and the arms in home.html. A
// block with no arm renders nothing at all, which is invisible in production.
func TestHomeBlockOrderMatchesTemplateArms(t *testing.T) {
	b, err := fs.ReadFile(site.FS, "web/templates/home.html")
	if err != nil {
		t.Fatal(err)
	}
	src := stripTmplComments(string(b))
	for _, name := range homeBlockOrder {
		if !strings.Contains(src, `eq .Name "`+name+`"`) {
			t.Errorf("home.html has no {{if/else if eq .Name %q}} arm — the block would render nothing", name)
		}
		if _, ok := blockMarker[name]; !ok {
			t.Errorf("block %q is in homeBlockOrder but has no marker registered in this test", name)
		}
	}
	if len(blockMarker) != len(homeBlockOrder) {
		t.Errorf("blockMarker has %d entries, homeBlockOrder has %d — they must list the same blocks",
			len(blockMarker), len(homeBlockOrder))
	}
}

// TestNewsTemplatesParse is GONE. The news plugin embeds and parses its own
// four templates now and asks the host only to wrap the finished fragment
// (Deps.RenderPage), so there is nothing here for this sweep to look at — it
// can only see templates the HOST renders. Same for store, tickets, wiki and
// messages; see plugin_templates_test.go.

// TestAdminNewsCSRFEscapesRangeScope is GONE with the template it guarded.
// It pinned a real bug — inside {{range .Posts}} the dot is a NewsPost, so the
// delete form must reach for $.CSRFToken or every delete 403s — but that markup
// belongs to the news plugin now. The equivalent guard has to live there,
// against its own template set; this sweep cannot see it.

// The daily-reward card is a plugin widget, and which PAGE renders it is a host
// decision — it was on the home page, and it is on the calendar now. The nav's
// claim control is a link to it, so those two facts have to agree.
//
// Asserted because the failure is silent in both directions: a link to a
// missing anchor scrolls nowhere and still returns 200, and a card left on a
// page nobody links to is simply never claimed. Neither shows up as an error.
func TestDailyRewardClaimLinkMatchesTheCardsAnchor(t *testing.T) {
	read := func(name string) string {
		b, err := fs.ReadFile(site.FS, "web/templates/"+name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}
	chrome, calendar, home := read("site_chrome.html"), read("calendar.html"), read("home.html")

	// The href the claim control points at, whatever it is.
	m := regexp.MustCompile(`href="([^"]*#daily-reward)"`).FindStringSubmatch(chrome)
	if m == nil {
		t.Fatal("site_chrome.html has no link to a #daily-reward anchor — the claim control lost its target")
	}
	href := m[1]
	path, anchor, _ := strings.Cut(href, "#")

	// The page it names must be the one carrying the anchor.
	if path != "/calendar" {
		t.Errorf("claim control points at %q; this test knows how to verify /calendar only — "+
			"if the card moved, move this assertion with it", href)
	}
	if !strings.Contains(calendar, `id="`+anchor+`"`) {
		t.Errorf("calendar.html has no id=%q, so %s scrolls nowhere", anchor, href)
	}
	// And it must render the card, not merely own the anchor.
	if !strings.Contains(calendar, ".DailyCard") {
		t.Error("calendar.html carries the anchor but never renders .DailyCard")
	}
	// Home must NOT still render it, or it is in two places and the exclusion
	// in homeWidgets is dead code.
	if strings.Contains(home, "daily-reward") {
		t.Error("home.html still references daily-reward — the card is meant to have moved")
	}
	if !homeWidgetsExcluded["daily-reward"] {
		t.Error("homeWidgets no longer excludes daily-reward, so home renders it again")
	}
}

// The rewards claim card came off the home page and became the points area's
// third tab, which is two halves that have to agree: the store plugin renders
// a tab at whatever Href the host hands it, and the host serves the page. A
// mismatch is a 404 behind a tab that looks fine, so it is asserted.
//
// The tab is also OFFERED conditionally — no rewards plugin, no tab — because
// the store plugin cannot know whether the page behind a host tab exists. That
// half is asserted too, since the failure is a dead tab on every site running
// store without rewards.
func TestRewardsTabAndPageAgree(t *testing.T) {
	w := &web{log: slog.Default(), tmpls: map[string]*template.Template{}}

	// No rewards widget registered: no tab, or the strip advertises a page
	// this host does not serve.
	if tabs := w.pointsAreaTabs(&gin.Context{Request: httptest.NewRequest("GET", "/store", nil)}); len(tabs) != 0 {
		t.Errorf("with no rewards widget registered, got %d tab(s), want none", len(tabs))
	}

	// Registered: exactly one tab, pointing at the route views.go mounts.
	w.siteWidgets = []core.View{{Slug: rewardsClaimWidget, Title: "Rewards to claim"}}
	tabs := w.pointsAreaTabs(&gin.Context{Request: httptest.NewRequest("GET", storeRewardsPath, nil)})
	if len(tabs) != 1 {
		t.Fatalf("got %d tabs, want 1", len(tabs))
	}
	if tabs[0].Href != storeRewardsPath {
		t.Errorf("tab points at %q, want %q", tabs[0].Href, storeRewardsPath)
	}
	// On the page itself the tab must read as current, or the strip says you
	// are somewhere you are not.
	if !tabs[0].Active {
		t.Errorf("tab is not marked active on %s", storeRewardsPath)
	}
	// And NOT current anywhere else.
	other := w.pointsAreaTabs(&gin.Context{Request: httptest.NewRequest("GET", "/store/history", nil)})
	if len(other) == 1 && other[0].Active {
		t.Error("tab is marked active on /store/history")
	}

	// The page behind it must render the card, and home must not.
	body, err := fs.ReadFile(site.FS, "web/templates/rewards.html")
	if err != nil {
		t.Fatalf("rewards.html: %v", err)
	}
	if !strings.Contains(string(body), ".Card") {
		t.Error("rewards.html never renders .Card")
	}
	if !homeWidgetsExcluded[rewardsClaimWidget] {
		t.Error("home still renders the rewards card, so it is in two places")
	}
}

// "View public profile" turns the owner into a stranger for one render, so the
// privacy setting can be checked by the person it protects — they are the one
// account it never hides anything from, so without this "is my profile actually
// private?" has no answer short of a second account.
//
// The link is what makes it usable in both directions, and it is the half that
// breaks quietly: rendered from .IsSelf it would vanish the moment preview
// turned .IsSelf off, stranding the owner in a view with no way back.
func TestProfilePreviewLinkSurvivesItsOwnEffect(t *testing.T) {
	b, err := fs.ReadFile(site.FS, "web/templates/profile.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	// The control must live INSIDE the .CanPreview block. Checked that way
	// round rather than by walking back from the link, because the link's own
	// href carries a {{if not .Preview}} and a naive scan finds that instead.
	if !strings.Contains(src, "preview=1") {
		t.Fatal("profile.html has no ?preview=1 link")
	}
	open := strings.Index(src, "{{if .CanPreview}}")
	if open < 0 {
		t.Fatal("profile.html has no {{if .CanPreview}} block — guarded by .IsSelf " +
			"instead, the link disappears in preview mode with no way back")
	}
	end := strings.Index(src[open:], "{{end}}")
	if end < 0 || !strings.Contains(src[open:open+end], "preview=1") {
		t.Error("the ?preview=1 link is not inside the .CanPreview block")
	}
	// And the page must say it is in preview, or a stripped page reads as a bug.
	if !strings.Contains(src, "{{if .Preview}}") {
		t.Error("nothing renders on .Preview, so preview mode is silent")
	}
}

// The quick-info card is rendered INSIDE .data-table__name-body, which makes it
// a grandchild of .data-table__name — so the CSS that reveals it on hover must
// use a descendant selector, not the child combinator.
//
// This shipped wrong: ".data-table__name:hover > .quick-info" matched nothing,
// on every row, and the card was never once shown. Nothing caught it because
// the MARKUP was correct and present on all 25 rows, which is what made it look
// finished. A stylesheet and a template can each be right and still not meet.
func TestQuickInfoHoverSelectorMatchesTheMarkup(t *testing.T) {
	css, err := fs.ReadFile(site.FS, "web/static/css/components.css")
	if err != nil {
		t.Fatalf("read components.css: %v", err)
	}
	s := string(css)
	if strings.Contains(s, ".data-table__name:hover > .quick-info") ||
		strings.Contains(s, ".data-table__name:focus-within > .quick-info") {
		t.Error("the hover rule uses the CHILD combinator; the card is a grandchild " +
			"of .data-table__name, so this matches nothing and the card never shows")
	}
	if !strings.Contains(s, ".data-table__name:hover .quick-info") {
		t.Error("no descendant hover rule for .quick-info — the card cannot appear")
	}
	// Keyboard reachability is half the point of the card being CSS-only.
	if !strings.Contains(s, ".data-table__name:focus-within .quick-info") {
		t.Error("no :focus-within rule — the card would be mouse-only")
	}
	// Hidden must be display:none, not visibility:hidden: a hidden-but-present
	// element still contributes to the table wrapper's scroll overflow, which
	// left a blank region under the last row.
	i := strings.Index(s, ".quick-info {")
	if i < 0 {
		t.Fatal("no .quick-info rule at all")
	}
	block := s[i:min(i+900, len(s))]
	if strings.Contains(block, "visibility: hidden") {
		t.Error(".quick-info hides with visibility:hidden, which still inflates " +
			"the scroll area of .data-table-wrapper")
	}
}
