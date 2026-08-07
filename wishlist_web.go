package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

// The wishlist — what members want indexed that is not here yet.
//
// UNIT3D's version hangs off TMDB, so an entry is a real film with a poster.
// This stack has neither a metadata source wired nor an index with anything in
// it, and building a TMDB-shaped wishlist against neither would be a page of
// empty posters. So an entry is TEXT: what you are looking for, in your own
// words.
//
// That is not a downgrade so much as a different, honest reading of the same
// feature. The point of a wishlist on a tracker is not the poster; it is that
// somebody who UPLOADS can see what people are asking for. So the site-wide
// view is the important half, and it is the one that works with an empty index.
//
// Deliberately NOT tied to releases. Matching free text against a catalogue
// that does not exist would mean guessing, and a wishlist that silently marks
// your entry "found" against the wrong thing is worse than one that never
// does. Marking an entry filled is a decision a person makes.
//
// Worth noting where this stops: pluginapi declares CalculatePoints, "the
// upload reward for a fulfilled request, given size, age and vote count" -- a
// REQUESTS system, with bounties and voting, that nothing on this host
// implements. A wishlist is the small honest half of that. The rest is a
// plugin nobody has written, and this file does not pretend to be it.

const (
	// wishTitleMax bounds an entry. A title, not a description.
	wishTitleMax = 160
	// wishNoteMax is the room for "1080p or better, not the extended cut".
	wishNoteMax = 300
	// wishPerUser caps how many open entries one member may hold. A wishlist
	// everybody can add to without limit becomes a place to shout rather than
	// a list anybody reads.
	wishPerUser = 25
)

var wishlistDB *sqlx.DB

// wishlistMigrate creates the table.
func wishlistMigrate(db *sqlx.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS wishlist_items (
		    id         BIGSERIAL PRIMARY KEY,
		    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    title      TEXT   NOT NULL,
		    note       TEXT   NOT NULL DEFAULT '',
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    -- Set by a PERSON deciding it turned up, never by a matcher. See
		    -- the note above about guessing.
		    filled_at  TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS wishlist_open ON wishlist_items (created_at DESC) WHERE filled_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS wishlist_user ON wishlist_items (user_id, created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// wishRow is one entry.
type wishRow struct {
	ID      int64  `db:"id"`
	Title   string `db:"title"`
	Note    string `db:"note"`
	Owner   string `db:"owner"`
	Added   string `db:"added"`
	Filled  string `db:"filled"`
	IsMine  bool   `db:"is_mine"`
	IsOpen  bool   `db:"is_open"`
	Wanters int    `db:"wanters"`
}

// listWishlist returns either one member's entries or the whole site's.
//
// One function for both, because they differ by a WHERE clause and nothing
// else; two would drift the moment either grew a column.
func listWishlist(ctx context.Context, viewerID int64, mineOnly bool) []wishRow {
	if wishlistDB == nil {
		return nil
	}
	where := `w.filled_at IS NULL`
	if mineOnly {
		where = `w.user_id = $1`
	}
	var rows []wishRow
	if err := wishlistDB.SelectContext(ctx, &rows, `
		SELECT w.id, w.title, w.note,
		       u.username                                      AS owner,
		       to_char(w.created_at, 'DD Mon YYYY')             AS added,
		       COALESCE(to_char(w.filled_at, 'DD Mon YYYY'), '') AS filled,
		       (w.user_id = $1)                                 AS is_mine,
		       (w.filled_at IS NULL)                            AS is_open,
		       -- How many OTHER people asked for the same thing, matched on the
		       -- title as typed. Crude on purpose: a fuzzy match would group
		       -- entries that are not the same request, and the number is here
		       -- to show demand, not to be authoritative.
		       (SELECT count(*) FROM wishlist_items w2
		         WHERE lower(w2.title) = lower(w.title) AND w2.filled_at IS NULL) AS wanters
		  FROM wishlist_items w
		  JOIN users u ON u.id = w.user_id
		 WHERE `+where+`
		 ORDER BY w.created_at DESC
		 LIMIT 200`, viewerID); err != nil {
		slog.Error("wishlist read", "mine", mineOnly, "err", err)
		return nil
	}
	return rows
}

// countOpenWishes is the per-member cap check.
func countOpenWishes(ctx context.Context, userID int64) int {
	var n int
	if wishlistDB == nil {
		return 0
	}
	_ = wishlistDB.GetContext(ctx, &n,
		`SELECT count(*) FROM wishlist_items WHERE user_id = $1 AND filled_at IS NULL`, userID)
	return n
}

// wishlistPage serves GET /wishlist.
func (w *web) wishlistPage(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	mine := c.Query("all") != "1"
	w.render(c, "wishlist.html", map[string]any{
		"Title":    "Wishlist",
		"Items":    listWishlist(ctx, u.ID, mine),
		"Mine":     mine,
		"Open":     countOpenWishes(ctx, u.ID),
		"Cap":      wishPerUser,
		"TitleMax": wishTitleMax,
		"NoteMax":  wishNoteMax,
		"Err":      c.Query("err"),
	})
}

// wishlistAdd serves POST /wishlist.
func (w *web) wishlistAdd(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" {
		c.Redirect(http.StatusSeeOther, "/wishlist?err="+url.QueryEscape("say what you are looking for"))
		return
	}
	if r := []rune(title); len(r) > wishTitleMax {
		title = string(r[:wishTitleMax])
	}
	note := strings.TrimSpace(c.PostForm("note"))
	if r := []rune(note); len(r) > wishNoteMax {
		note = string(r[:wishNoteMax])
	}
	if countOpenWishes(ctx, u.ID) >= wishPerUser {
		c.Redirect(http.StatusSeeOther, "/wishlist?err="+
			url.QueryEscape("that is your "+strconv.Itoa(wishPerUser)+" open entries; fill or remove one first"))
		return
	}
	if _, err := wishlistDB.ExecContext(ctx,
		`INSERT INTO wishlist_items (user_id, title, note) VALUES ($1,$2,$3)`,
		u.ID, title, note); err != nil {
		w.log.Error("wishlist add", "user", u.ID, "err", err)
		c.Redirect(http.StatusSeeOther, "/wishlist?err="+url.QueryEscape("could not add that"))
		return
	}
	c.Redirect(http.StatusSeeOther, "/wishlist")
}

// wishlistUpdate serves POST /wishlist/:id — mark filled, reopen, or remove.
//
// One handler for three actions because they are the same authorisation
// question asked once: is this row yours? Splitting them into three routes
// would mean asking it three times and getting it wrong once.
func (w *web) wishlistUpdate(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.Redirect(http.StatusSeeOther, "/wishlist")
		return
	}
	ctx := c.Request.Context()
	// The ownership check is IN each statement rather than a lookup first: a
	// separate read leaves a window, and a WHERE that matches nothing is the
	// same refusal without one.
	var q string
	switch c.PostForm("action") {
	case "remove":
		q = `DELETE FROM wishlist_items WHERE id = $1 AND user_id = $2`
	case "reopen":
		q = `UPDATE wishlist_items SET filled_at = NULL WHERE id = $1 AND user_id = $2`
	default:
		q = `UPDATE wishlist_items SET filled_at = now() WHERE id = $1 AND user_id = $2`
	}
	if _, err := wishlistDB.ExecContext(ctx, q, id, u.ID); err != nil {
		w.log.Error("wishlist update", "item", id, "user", u.ID, "err", err)
	}
	back := "/wishlist"
	if c.PostForm("all") == "1" {
		back += "?all=1"
	}
	c.Redirect(http.StatusSeeOther, back)
}

// wishlistCount is the number of open entries site-wide, for the stats page.
func wishlistCount(ctx context.Context) int {
	var n int
	if wishlistDB == nil {
		return 0
	}
	_ = wishlistDB.GetContext(ctx, &n, `SELECT count(*) FROM wishlist_items WHERE filled_at IS NULL`)
	return n
}
