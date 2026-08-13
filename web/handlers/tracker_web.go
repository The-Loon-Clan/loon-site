package handlers

import (
	"github.com/the-loon-clan/loon-site/internal/middleware"

	"html/template"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/tracker"
)

// Host side of the BitTorrent tracker plugin.
//
// The plugin is self-contained — it builds its own Postgres store, its Redis
// peer store, and its entitlement gate — so the host supplies only three render
// seams, the config that turns it on, and the entitlement that lets a member
// through the door.
//
// It is off unless LOON_TRACKER is set, and that is the plugin's own
// default rather than caution on the host's part: a tracker publishes announce
// endpoints, mints passkeys, and starts keeping ratio accounting the moment it
// is reachable. Everything else in this demo is inert until somebody visits it.

// trackerSiteURL is the absolute base baked into every downloaded .torrent.
//
// There is no sensible default and a wrong value does not fail loudly — it
// produces .torrent files pointing somewhere unable to answer, and the member
// finds out when their client reports the tracker as dead. So it is read from
// the environment and falls back to the address this demo actually serves on.
func trackerSiteURL() string {
	if u := os.Getenv("LOON_SITE_URL"); u != "" {
		return u
	}
	return "http://localhost:8090"
}

// wireTrackerPlugin installs the SetDeps seams. Always called: SetDeps runs
// before core.Boot and the plugin decides for itself whether to mount anything,
// so wiring an unused seam costs nothing and forgetting one costs a 500 on a
// page a member opened.
func (w *web) wireTrackerPlugin() {
	tracker.SetDeps(tracker.Deps{
		// The site's layout — navbar, footer, session context. Without it the
		// tracker's pages render as though nobody is signed in, which reads as
		// a broken session rather than a missing seam.
		RenderPage: func(gc *gin.Context, title string, body template.HTML) {
			w.render(gc, "site_page.html", map[string]any{"Title": title, "Fragment": body})
		},
		// The double-submit token for the passkey-rotate form. The host's
		// session concern, which is why the plugin asks rather than invents.
		CSRFToken: middleware.Token,
		// Borrowed so the tracker's "last seen" column does not drift from
		// every other relative time on the site.
		//
		// Adapted rather than passed through: the host's helper takes `any`
		// because it is called from templates, and this plugin asks for a
		// time.Time. The wrapper is the whole difference.
		RelativeTime: func(t time.Time) string { return relativeTime(t) },
	})
}
