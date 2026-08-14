package handlers

import (
	"context"
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
		rows = bookmarkedRows(ctx, w.data, w.usenet, u.ID, bookmarkRows)
		w.attachCovers(ctx, rows)
		rows = w.attachSwarm(ctx, w.attachGrabs(ctx, rows))
	}
	data["Results"] = rows
	w.render(c, "bookmarks.html", data)
}

// bookmarkList is the one thing bookmarkedRows needs from storage.
//
// Declared HERE, at the consumer, and holding a single method — not exported
// from internal/storage as a mirror of *Store. That is the Go convention and
// it is load-bearing rather than decorative: *Store has around ninety methods,
// so an interface shaped like it would be unimplementable by anything except
// *Store, which is not swappability, only ceremony.
//
// The narrow version is what makes the function below testable without a
// database, which is the actual reason the handler layer sat at 16% coverage.
type bookmarkList interface {
	BookmarkedIDs(ctx context.Context, userID int64, limit int) []int64
}

// bookmarkedRows resolves a member's saved ids into rows to render.
//
// Split out of the handler so the interesting half can be tested: a bookmark
// can outlive the release it points at. Usenet retention removes posts, and the
// saved pointer stays behind — so a bookmark list is always partly a list of
// things that may no longer be there, and the resolution has to survive that
// rather than error or render a blank row.
//
// It takes what it uses and nothing else. pluginapi.UsenetIndex is already an
// interface, so the plugin side was swappable; this makes the storage side
// match.
func bookmarkedRows(ctx context.Context, list bookmarkList, index pluginapi.UsenetIndex, userID int64, limit int) []searchRow {
	rows := make([]searchRow, 0, limit)
	for _, id := range list.BookmarkedIDs(ctx, userID, limit) {
		detail, found, err := index.ReleaseByID(ctx, id)
		if err != nil || !found {
			continue // retention removed it; a saved pointer can outlive its target
		}
		rows = append(rows, toSearchRows([]pluginapi.Release{detail.Release})[0])
	}
	return rows
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
	on, err := w.data.ToggleBookmark(c.Request.Context(), u.ID, id)
	if err != nil {
		w.log.Error("toggle bookmark", "release", id, "err", err)
	}
	next, _ := readNextInput(c)

	// htmx gets the button back in its new state; everyone else gets the
	// redirect that has always happened here. This branch is the ONLY
	// difference between the two paths, which is what keeps the no-JavaScript
	// behaviour real rather than theoretical — see htmx.go.
	//
	// The state comes from ToggleBookmark's own return, not from a re-read: the
	// toggle already knows which way it went, and asking the database again
	// would answer a different question (what is true NOW, after any concurrent
	// press) than the one the button is reporting.
	if isHTMX(c) {
		w.renderFragment(c, "release.html", "bookmark-button", map[string]any{
			"ID":         id,
			"Bookmarked": on,
			"PathQuery":  next.Next,
		})
		return
	}

	// Back to whichever listing the button was pressed on, falling back to the
	// release itself. Same-origin only — this is user input.
	if back, okRef := sameOriginPath(next.Next, c.Request.Host); okRef {
		c.Redirect(http.StatusSeeOther, back)
		return
	}
	c.Redirect(http.StatusSeeOther, "/release/"+strconv.FormatInt(id, 10))
}
