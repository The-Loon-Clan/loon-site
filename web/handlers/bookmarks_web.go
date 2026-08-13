package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// Bookmarks — saved releases. This retires docs/MOCKS.md M4, which stood in
// for it with an em dash on the profile.
//
// The indexer analogue of UNIT3D's bookmarks is "saved release", not "saved
// torrent": there is nothing to seed, so the only thing worth keeping is a
// pointer back to something you found. MOCKS suggested this might belong to the
// usenet plugin; it lives here instead, for the same reason release_grab does —
// it is a relation between a USER and a release id, the users are the host's,
// and the plugin owns neither side of that.
//
// Same shape as grabs throughout: host-owned table, ids not titles, and a
// release that leaves the index simply stops resolving rather than lingering as
// a stale row with a remembered name.

const bookmarkRows = 100

// bookmarksPage renders /bookmarks — the viewer's own saved releases.
func (w *web) bookmarksPage(c *gin.Context) {
	ctx := c.Request.Context()
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	data := map[string]any{"Title": "Bookmarks", "Results": []searchRow{}}
	rows := make([]searchRow, 0, bookmarkRows)
	if w.usenet != nil {
		for _, id := range w.data.BookmarkedIDs(ctx, u.ID, bookmarkRows) {
			detail, found, err := w.usenet.ReleaseByID(ctx, id)
			if err != nil || !found {
				continue // retention removed it; a saved pointer can outlive its target
			}
			rows = append(rows, toSearchRows([]pluginapi.Release{detail.Release})[0])
		}
		w.attachCovers(ctx, rows)
		rows = w.attachSwarm(ctx, w.attachGrabs(ctx, rows))
	}
	data["Results"] = rows
	w.render(c, "bookmarks.html", data)
}

// bookmarkToggle handles the button on the release page and returns to it.
//
// POST + redirect rather than a link: this WRITES, and a GET that mutates is
// one prefetching browser or link-checker away from bookmarking a user's whole
// history for them.
func (w *web) bookmarkToggle(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.String(http.StatusBadRequest, "invalid release id")
		return
	}
	if _, err := w.data.ToggleBookmark(c.Request.Context(), u.ID, id); err != nil {
		w.log.Error("toggle bookmark", "release", id, "err", err)
	}
	// Back to whichever listing the button was pressed on, falling back to the
	// release itself. Same-origin only — this is user input.
	if back, okRef := sameOriginPath(c.PostForm("next"), c.Request.Host); okRef {
		c.Redirect(http.StatusSeeOther, back)
		return
	}
	c.Redirect(http.StatusSeeOther, "/release/"+strconv.FormatInt(id, 10))
}
