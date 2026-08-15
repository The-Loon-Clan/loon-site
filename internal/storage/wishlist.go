package storage

import (
	"context"
	"errors"
	"log/slog"
)

// ListWishlist returns either one member's entries or the whole site's.
//
// One function for both, because they differ by a WHERE clause and nothing
// else; two would drift the moment either grew a column.
func (st *Store) ListWishlist(ctx context.Context, viewerID int64, mineOnly bool) []WishRow {
	// Typed SQL, not string: the concatenation below then type-checks as SQL,
	// and a future edit that assigned a request value here would stop
	// compiling rather than becoming an injection.
	where := SQL(`w.filled_at IS NULL`)
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

// CountOpenWishes is the per-member cap check.
//
// Returns the error, because the caller that ENFORCES the cap cannot treat a
// failed read as an answer: this returned a bare int, a failed read gave 0,
// and `0 >= cap` is false — so the limit stopped applying exactly when the
// database was unhappy. The display caller may ignore it and show nothing.
func (st *Store) CountOpenWishes(ctx context.Context, userID int64) (int, error) {
	if st == nil || !st.db.Valid() {
		return 0, errors.New("storage: no database connection")
	}
	var n int
	err := st.db.GetContext(ctx, &n,
		`SELECT count(*) FROM wishlist_items WHERE user_id = $1 AND filled_at IS NULL`, userID)
	return n, err
}

// WishlistCount is the number of open entries site-wide, for the stats page.
func (st *Store) WishlistCount(ctx context.Context) int {
	var n int
	//nolint:errcheck // a site-wide stat: 0 is a wrong number on a stats page, not a decision
	_ = st.db.GetContext(ctx, &n, `SELECT count(*) FROM wishlist_items WHERE filled_at IS NULL`)
	return n
}

// AddWish records one open wishlist entry.
//
// The caller trims and caps the text: how long a title may be is a form
// decision, and the store should not silently truncate what it was handed.
func (st *Store) AddWish(ctx context.Context, userID int64, title, note string) error {
	_, err := st.db.ExecContext(ctx,
		`INSERT INTO wishlist_items (user_id, title, note) VALUES ($1,$2,$3)`,
		userID, title, note)
	return err
}

// The three edits below all carry the ownership check IN the statement rather
// than reading the row first. A separate read leaves a window between "is this
// yours" and "change it", and a WHERE that matches nothing is the same refusal
// without one. That is why each takes userID and none of them is a plain id.

// RemoveWish deletes one entry, if it belongs to userID.
func (st *Store) RemoveWish(ctx context.Context, id, userID int64) error {
	_, err := st.db.ExecContext(ctx,
		`DELETE FROM wishlist_items WHERE id = $1 AND user_id = $2`, id, userID)
	return err
}

// ReopenWish clears the filled mark, if the entry belongs to userID.
func (st *Store) ReopenWish(ctx context.Context, id, userID int64) error {
	_, err := st.db.ExecContext(ctx,
		`UPDATE wishlist_items SET filled_at = NULL WHERE id = $1 AND user_id = $2`, id, userID)
	return err
}

// FillWish marks an entry filled, if it belongs to userID.
func (st *Store) FillWish(ctx context.Context, id, userID int64) error {
	_, err := st.db.ExecContext(ctx,
		`UPDATE wishlist_items SET filled_at = now() WHERE id = $1 AND user_id = $2`, id, userID)
	return err
}
