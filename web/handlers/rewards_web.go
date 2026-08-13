package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// /rewards — the claim page, third tab of the points area.
//
// The rewards plugin publishes its claim card as a SlotSiteWidget, which used
// to mean "on the home page", because home was the only page rendering the
// whole widget set. It is a per-viewer control on the site's front page, and it
// belongs where points already live: beside Store and History.
//
// Host-owned and OUTSIDE /store/*, which the store plugin owns — a host route
// inside a plugin's namespace reads like the plugin's until someone goes
// looking. It still appears on the store's tab strip, because a tab strip is
// about where the reader can go rather than about who serves it; the store
// takes that tab from the host through Deps.ExtraTabs (store_web.go), so the
// store plugin never learns that rewards exist.

// rewardsPage serves /rewards.
func (w *web) rewardsPage(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}

	data := map[string]any{"Title": "Rewards"}
	// The card renders nothing at all when there is nothing to claim — that is
	// the plugin's decision, and a deliberate one: an empty "you have no
	// rewards" panel is noise. So the page distinguishes three states, and the
	// template says which:
	//
	//	no widget registered  -> the plugin is not wired here
	//	widget, empty render  -> wired, nothing to claim right now
	//	widget, content       -> the card
	if card, ok := w.siteWidget(c, rewardsClaimWidget); ok {
		data["Card"] = card
	} else if !w.hasSiteWidget(rewardsClaimWidget) {
		data["Unavailable"] = true
	}
	w.render(c, "rewards.html", data)
}
