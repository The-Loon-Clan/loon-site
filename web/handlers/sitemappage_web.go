package handlers

import (
	"github.com/gin-gonic/gin"
)

// /sitemap — the human index of the site.
//
// Not to be confused with /sitemap.xml (sitemap_web.go), which is the crawler's
// copy: a flat list of URLs with change frequencies, correct for a robot and
// useless to a reader. The nav and the footer have linked "Sitemap" at a page
// that nothing served since the link was written, so it 404'd.
//
// On a demo whose whole point is showing what a loon site can do, this is the
// one page that shows the entire surface at once — which is why it is worth
// building rather than unlinking.

// sitemapGroup is one section of the index, named the way the nav names it so
// the two read as the same site.
type sitemapGroup struct {
	Title string
	Links []sitemapLink
}

type sitemapLink struct {
	Href, Label, Note string
}

// sitemapGroups are the host's own pages, grouped as the main nav groups them.
// Plugin pages are NOT here — they come off the view registry at request time,
// since which of them exist depends on what is wired.
//
// Curated rather than reflected off gin's route table, which also holds POST
// targets, :param routes, admin pages and API endpoints — a reader wants the
// pages they can visit, not every path that answers. The test keeps it honest:
// every href here must be a route the site serves, and every internal link the
// chrome offers must appear here.
var sitemapGroups = []sitemapGroup{
	{Title: "Releases", Links: []sitemapLink{
		{Href: "/browse", Label: "Browse", Note: "Everything indexed, by category"},
		{Href: "/series", Label: "Series", Note: "Shows, season by season and episode by episode"},
		{Href: "/search", Label: "Search", Note: "Search releases by name, group or category"},
		{Href: "/groups", Label: "Newsgroups", Note: "The groups being crawled, and how much each holds"},
		{Href: "/trending", Label: "Trending", Note: "Most grabbed, by window"},
	}},
	{Title: "Community", Links: []sitemapLink{
		{Href: "/community/forums", Label: "Forums"},
		{Href: "/c", Label: "Communities"},
		{Href: "/news", Label: "News"},
		{Href: "/playlists", Label: "Playlists"},
		{Href: "/store", Label: "Points store", Note: "Spend points on invites and ranks"},
	}},
	{Title: "Support", Links: []sitemapLink{
		{Href: "/rules", Label: "Rules"},
		{Href: "/faq", Label: "FAQ"},
		{Href: "/wiki", Label: "Wiki"},
		{Href: "/support", Label: "Helpdesk"},
		{Href: "/staff", Label: "Staff"},
	}},
	{Title: "Your account", Links: []sitemapLink{
		{Href: "/inbox", Label: "Inbox"},
		{Href: "/p/inbox", Label: "Notifications"},
		{Href: "/achievements", Label: "Achievements"},
		{Href: "/p/topics", Label: "Topics", Note: "Threads you started"},
		{Href: "/p/posts", Label: "Posts", Note: "Your replies"},
		{Href: "/bookmarks", Label: "Bookmarks"},
		{Href: "/calendar", Label: "Calendar", Note: "Your claims and followed releases, by day"},
		{Href: "/store/history", Label: "Points", Note: "Your points ledger"},
		{Href: "/rewards", Label: "Rewards", Note: "Rewards waiting to be claimed"},
		{Href: "/p/account", Label: "Account settings"},
		{Href: "/settings/privacy", Label: "Privacy"},
		{Href: "/settings/notifications", Label: "Alerts"},
		{Href: "/p/api-key", Label: "API key"},
	}},
	{Title: "This site", Links: []sitemapLink{
		{Href: "/", Label: "Home"},
		{Href: "/stats", Label: "Stats"},
		{Href: "/about", Label: "About"},
		{Href: "/sitemap", Label: "Sitemap", Note: "This page"},
	}},
	{Title: "For machines", Links: []sitemapLink{
		{Href: "/api?t=caps", Label: "Newznab API", Note: "Capabilities document; the API a downloader talks to"},
		{Href: "/rss?t=search", Label: "RSS feed", Note: "The latest releases as a feed"},
		{Href: "/sitemap.xml", Label: "sitemap.xml", Note: "The crawler's index, not this one"},
	}},
}

// sitemapPage serves /sitemap.
func (w *web) sitemapPage(c *gin.Context) {
	groups := make([]sitemapGroup, len(sitemapGroups))
	copy(groups, sitemapGroups)

	// The member's own tracker standing, appended only when the tracker is on.
	//
	// Gated for the same reason the account menu gates it, and it matters more
	// here: a sitemap is a promise that a page is there, and this is the page a
	// site links to from an error message a member has just hit.
	if g, ok := trackerAccountGroup(); ok {
		links := make([]sitemapLink, 0, len(g.Items))
		for _, it := range g.Items {
			links = append(links, sitemapLink{Href: it.Href, Label: it.Label})
		}
		groups = append(groups, sitemapGroup{Title: "Your tracker standing", Links: links})
	}

	// Plugin pages, appended as their own group. Read from the registry rather
	// than listed above because a host with a different plugin set has a
	// different sitemap, and a hardcoded list would promise pages it does not
	// serve — the exact failure this page exists to fix.
	var plugin []sitemapLink
	for _, v := range w.sitePages {
		if !w.canView(v, c) {
			continue
		}
		href := "/p/" + v.Slug
		if sitemapLists(groups, href) {
			continue // already placed by hand, in the group it belongs to
		}
		label := v.Title
		if p, ok := navPlacement[href]; ok {
			label = p.Label // the host renamed it in the nav; say the same here
		}
		plugin = append(plugin, sitemapLink{Href: href, Label: label})
	}
	if len(plugin) > 0 {
		groups = append(groups, sitemapGroup{Title: "Plugin pages", Links: plugin})
	}

	w.render(c, "sitemap.html", map[string]any{
		"Title":  "Sitemap",
		"Groups": groups,
	})
}

// sitemapLists reports whether href is already somewhere in the index.
func sitemapLists(groups []sitemapGroup, href string) bool {
	for _, g := range groups {
		for _, l := range g.Links {
			if l.Href == href {
				return true
			}
		}
	}
	return false
}
