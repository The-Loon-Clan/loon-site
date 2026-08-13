package storage

import (
	"context"
	"log/slog"
)

// ListWishlist returns either one member's entries or the whole site's.
//
// One function for both, because they differ by a WHERE clause and nothing
// else; two would drift the moment either grew a column.
func (st *Store) ListWishlist(ctx context.Context, viewerID int64, mineOnly bool) []WishRow {
	where := `w.filled_at IS NULL`
	if mineOnly {
		where = `w.user_id = $1`
	}
	var rows []WishRow
	if err := st.db.SelectContext(ctx, &rows, `
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

// WishRow is one entry.
type WishRow struct {
	ID      int64  `st.db:"id"`
	Title   string `st.db:"title"`
	Note    string `st.db:"note"`
	Owner   string `st.db:"owner"`
	Added   string `st.db:"added"`
	Filled  string `st.db:"filled"`
	IsMine  bool   `st.db:"is_mine"`
	IsOpen  bool   `st.db:"is_open"`
	Wanters int    `st.db:"wanters"`
}

// CountOpenWishes is the per-member cap check.
func (st *Store) CountOpenWishes(ctx context.Context, userID int64) int {
	var n int
	_ = st.db.GetContext(ctx, &n,
		`SELECT count(*) FROM wishlist_items WHERE user_id = $1 AND filled_at IS NULL`, userID)
	return n
}

// WishlistCount is the number of open entries site-wide, for the stats page.
func (st *Store) WishlistCount(ctx context.Context) int {
	var n int
	_ = st.db.GetContext(ctx, &n, `SELECT count(*) FROM wishlist_items WHERE filled_at IS NULL`)
	return n
}
