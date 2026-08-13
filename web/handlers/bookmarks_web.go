package handlers

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

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

// bookmarksMigrate creates the table. Idempotent.
func bookmarksMigrate(db *sqlx.DB) error {
	stmts := []string{
		// UNIQUE(user_id, release_id) makes the toggle idempotent in the
		// DATABASE rather than in a read-then-write the double-click of an
		// impatient user can slip between.
		`CREATE TABLE IF NOT EXISTS release_bookmark (
		    id         BIGSERIAL PRIMARY KEY,
		    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    release_id BIGINT NOT NULL,
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    UNIQUE (user_id, release_id)
		)`,
		// "my bookmarks, newest first" is the only listing query.
		`CREATE INDEX IF NOT EXISTS idx_release_bookmark_user
		     ON release_bookmark (user_id, created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// bookmarksDB is the handle. Package-level for the same reason grabsDB is:
// host-owned data with no plugin behind it.
var bookmarksDB *sqlx.DB

// toggleBookmark adds or removes one bookmark and reports the state AFTER the
// change, so the caller can render the button without a second read.
func toggleBookmark(ctx context.Context, userID, releaseID int64) (saved bool, err error) {
	if bookmarksDB == nil || userID <= 0 || releaseID <= 0 {
		return false, nil
	}
	// One statement per direction, both keyed on the unique pair: DELETE
	// reports whether it removed anything, which is the toggle's answer.
	res, err := bookmarksDB.ExecContext(ctx,
		`DELETE FROM release_bookmark WHERE user_id = $1 AND release_id = $2`, userID, releaseID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return false, nil // it was bookmarked; now it is not
	}
	_, err = bookmarksDB.ExecContext(ctx,
		`INSERT INTO release_bookmark (user_id, release_id) VALUES ($1, $2)
		 ON CONFLICT (user_id, release_id) DO NOTHING`, userID, releaseID)
	return err == nil, err
}

// isBookmarked answers the release page's single question.
func isBookmarked(ctx context.Context, userID, releaseID int64) bool {
	if bookmarksDB == nil || userID <= 0 || releaseID <= 0 {
		return false
	}
	var n int
	if err := bookmarksDB.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM release_bookmark WHERE user_id = $1 AND release_id = $2`,
		userID, releaseID); err != nil {
		return false
	}
	return n > 0
}

// bookmarkCount is the profile figure that used to be a mock. Returns ok=false
// when the table is unreachable, so the tile can stay an em dash rather than
// claim a confident zero — the same rule the staff dashboard follows.
func bookmarkCount(ctx context.Context, userID int64) (int, bool) {
	if bookmarksDB == nil || userID <= 0 {
		return 0, false
	}
	var n int
	if err := bookmarksDB.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM release_bookmark WHERE user_id = $1`, userID); err != nil {
		return 0, false
	}
	return n, true
}

// bookmarkedIDs lists one user's saved release ids, newest first.
func bookmarkedIDs(ctx context.Context, userID int64, limit int) []int64 {
	if bookmarksDB == nil || userID <= 0 {
		return nil
	}
	var ids []int64
	if err := bookmarksDB.SelectContext(ctx, &ids,
		`SELECT release_id FROM release_bookmark
		  WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`, userID, limit); err != nil {
		return nil
	}
	return ids
}

const bookmarkRows = 100

// bookmarksPage renders /bookmarks — the viewer's own saved releases.
func (w *web) bookmarksPage(c *gin.Context) {
	ctx := c.Request.Context()
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	data := map[string]any{"Title": "Bookmarks", "Results": []searchRow{}}
	rows := make([]searchRow, 0, bookmarkRows)
	if w.usenet != nil {
		for _, id := range bookmarkedIDs(ctx, u.ID, bookmarkRows) {
			detail, found, err := w.usenet.ReleaseByID(ctx, id)
			if err != nil || !found {
				continue // retention removed it; a saved pointer can outlive its target
			}
			rows = append(rows, toSearchRows([]pluginapi.Release{detail.Release})[0])
		}
		w.attachCovers(ctx, rows)
		rows = attachSwarm(ctx, attachGrabs(ctx, rows))
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
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.String(http.StatusBadRequest, "invalid release id")
		return
	}
	if _, err := toggleBookmark(c.Request.Context(), u.ID, id); err != nil {
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
