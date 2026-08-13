package storage

import (
	"context"
	"log/slog"

	"github.com/jmoiron/sqlx"
)

// ListWishlist returns either one member's entries or the whole site's.
//
// One function for both, because they differ by a WHERE clause and nothing
// else; two would drift the moment either grew a column.
func ListWishlist(ctx context.Context, viewerID int64, mineOnly bool) []WishRow {
	if WishlistDB == nil {
		return nil
	}
	where := `w.filled_at IS NULL`
	if mineOnly {
		where = `w.user_id = $1`
	}
	var rows []WishRow
	if err := WishlistDB.SelectContext(ctx, &rows, `
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

var WishlistDB *sqlx.DB

// WishRow is one entry.
type WishRow struct {
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
