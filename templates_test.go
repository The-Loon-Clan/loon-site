package main

import (
	"bytes"
	"html/template"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"text/template/parse"
	"time"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/forum"
	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The repo's other tests never construct newWeb, so a broken template used to
// reach production as a boot-time panic (parse errors) or a silently truncated
// page (execute errors). These tests parse EVERY template file exactly the way
// the two production parse sites do:
//
//	views.go newWeb()            -> ParseFS(siteFS, pageFiles(page))
//	forum_web.go wireForumPlugin -> forumTemplates(): ParseFS(siteFS, site_chrome.html, forum/*.html)
//
// They are separate sets on purpose: no PAGE file is reachable from both, so a
// {{define}} in one page is invisible in the other. site_chrome.html is the one
// file both sets name, and the only place the shell blocks are defined.

// shellTemplates are the files parsed alongside EVERY page rather than being a
// page themselves. TestEveryPageTemplateIsParsed excludes them.
var shellTemplates = map[string]bool{
	"base.html": true, "site_chrome.html": true,
	"listing.html": true, // shared partial, see sharedPartials in views.go
}

// parseSet builds a page's template set exactly the way newWeb does — by
// calling the SAME pageFiles the production path calls, so this test cannot
// drift from it the way a hand-copied file list would.
func parseSet(w *web, page string) (*template.Template, error) {
	return template.New(page).Funcs(w.tmplFuncs()).ParseFS(siteFS, pageFiles(page)...)
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
	ents, err := fs.ReadDir(siteFS, "web/templates")
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
	names, err := fs.Glob(siteFS, "web/templates/forum/*.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no forum templates found")
	}
	// Call the production parser itself rather than re-deriving the file list,
	// so this test cannot pass while wireForumPlugin's real set is broken.
	tmpl, err := forumTemplates()
	if err != nil {
		t.Fatalf("forumTemplates: %v", err)
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

	// Execute each forum page with ONLY what forum.Deps.BaseData supplies. The
	// forum's own keys (Categories, Threads, Posts, Pagination …) are all
	// legitimately empty on a fresh board, and Points/Unread/SiteNav/AdminNav
	// are never supplied on this side at all — the shared chrome has to
	// degrade rather than error. This is the path that broke before: {{len}}
	// over an absent key aborts the render mid-document.
	for _, page := range []string{
		"community_forums.html", "community_category.html", "community_thread.html",
		"community_new_thread.html", "admin_forum_categories.html",
	} {
		data := map[string]any{
			"User": (*core.User)(nil), "IsAdmin": false, "IsMod": false,
			"CSRFToken": "", "Path": "/community/forums",
		}
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
	err := fs.WalkDir(siteFS, "web/templates", func(p string, d fs.DirEntry, err error) error {
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
			b, err := fs.ReadFile(siteFS, f)
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
	err := fs.WalkDir(siteFS, "web/templates", func(p string, d fs.DirEntry, err error) error {
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
		b, err := fs.ReadFile(siteFS, f)
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
//	admin_settings.html  Sections — admin_views.go renderSettingsPage always
//	                     builds a (possibly empty) non-nil slice
//	profile.html         exactly ONE of Missing / Subject — views.go profile
var structuralKeys = map[string]map[string]any{
	"admin_settings.html": {"Sections": []settingsSection{}},
	"profile.html":        {"Missing": true},
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
		data := map[string]any{
			"User":      (*core.User)(nil),
			"IsAdmin":   false,
			"CSRFToken": "",
			"Path":      "/",
			"AdminNav":  []navItem(nil),
			"SiteNav":   []siteNavEntry(nil),
		}
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
		data := map[string]any{
			"User":            u,
			"IsAdmin":         true,
			"CSRFToken":       "tok",
			"Path":            "/",
			"AdminNav":        []navItem{{Label: "Settings", Href: "/admin/settings"}},
			"SiteNav":         []siteNavEntry(nil),
			"RoleLabel":       roleName(u.Role),
			"MemberSince":     u.CreatedAt,
			"Points":          0,
			"HasPoints":       true,
			"Unread":          0,
			"HasUnread":       true,
			"EmailUnverified": true,
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
		// A genuine zero must still produce its tile; that is the whole point
		// of HasPoints/HasUnread over {{if .Points}}.
		if page == "home.html" || page == "profile.html" {
			if !strings.Contains(out, "stat-tile__label") && !strings.Contains(out, "stat-strip__label") {
				t.Errorf("%s: no viewer stat tiles rendered for a zero-balance user", page)
			}
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

	viewer := func() map[string]any {
		return map[string]any{
			"User": u, "IsAdmin": true, "CSRFToken": "tok", "Path": "/",
			"AdminNav":  []navItem{{Label: "Settings", Href: "/admin/settings"}},
			"SiteNav":   []siteNavEntry(nil),
			"RoleLabel": roleName(u.Role), "MemberSince": u.CreatedAt,
			"Points": 1250, "HasPoints": true, "Unread": 3, "HasUnread": true,
		}
	}

	cases := map[string]map[string]any{
		"home.html": {
			"Title": "Home", "Configured": true,
			"Recent": rows, "Featured": rows,
			"Stats": stats, "TopGroups": groups,
			"ForumThreads": threads, "ForumPosters": posters,
			"Widgets": []widgetVM{{Title: "Guestbook", Fragment: template.HTML("<div class=\"card-body\">hi</div>")}},
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
		"admin_settings.html": {"Title": "Settings", "Sections": []settingsSection{
			{Slug: "usenet", Title: "Usenet", Fragment: template.HTML("<div class=\"card\">cfg</div>")}}},
		"site_page.html": {"Title": "Inbox", "Page": template.HTML("<div class=\"card\">body</div>")},
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
