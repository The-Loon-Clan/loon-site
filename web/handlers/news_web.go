package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/sanitize"

	"fmt"
	"html/template"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"

	"github.com/the-loon-clan/loon-plugins/news"
)

// News (loon-plugins/news) host wiring. The plugin owns its routes (/news,
// /news/:slug, /admin/news/*) and renders the HOST's templates by name through
// gin's HTML set, the same contract the forum plugin uses — so its four
// templates live in web/templates/plugin/ and are parsed by pluginTemplates().
//
// The plugin ships no migration (its table appears only in its own integration
// test), so the host creates it here, the same way forumMigrate does.

// newsIndexPath is the feed. Named because the crumb below and the plugin's
// own route have to agree, and a literal in two places is how they stop.
const newsIndexPath = "/news"

// crumb is one intermediate step in the header's breadcrumb trail — everything
// between Home and the page you are on. The last crumb is the page itself and
// comes from Title, so it is deliberately not one of these: a crumb here is a
// link, and a link to the page you are already on is a dead control.
//
// Set as data rather than by overriding the "breadcrumbs" block because the
// pages that need it are plugin FRAGMENTS: they are rendered by the plugin's
// own template set and cannot define a block in the host's.
type crumb struct {
	Label string
	Href  string
}

// wireNewsPlugin installs the SetDeps seams. Call after core.New (chromeData
// resolves the session user) and before core.Boot (SetDeps is checked at
// Provision), exactly like wireForumPlugin.
func wireNewsPlugin(c *core.Core, w *web) error {
	if db := c.Storage.DB(); db != nil {
		if err := w.data.MigrateNews(); err != nil {
			return fmt.Errorf("news migrate: %w", err)
		}
	}
	news.SetDeps(news.Deps{
		// Fourth plugin today to take its markup back and ask for chrome
		// instead of a data map. No status parameter on this one — none of its
		// four pages re-render on a validation failure.
		RenderPage: func(gc *gin.Context, title string, body template.HTML) {
			data := map[string]any{"Title": title, "Fragment": body}
			// A post sits UNDER the feed, so the trail says so: Home › News ›
			// this post. Without it the bar read "Home › News" on a post page —
			// the last crumb is the one that does not link, so the only way back
			// to the feed was the browser's back button or the link at the
			// bottom of the article.
			//
			// Decided from the PATH rather than passed by the plugin, because
			// RenderPage's contract is shared with three other plugins and the
			// parent of a page is a fact about this site's routes, which is the
			// host's to know. The plugin mounts /news and /news/:slug; anything
			// longer than the first is the second.
			if strings.HasPrefix(gc.Request.URL.Path, newsIndexPath+"/") {
				data["Crumbs"] = []crumb{{Label: "News", Href: newsIndexPath}}
			}
			w.render(gc, "site_page.html", data)
		},
		Sanitize: sanitize.HTML,
	})
	return nil
}
