package storage

import (
	"context"

	"github.com/lib/pq"
)

// The cart — a selection of releases a member is accumulating.
//
// WHY IT IS SERVER-SIDE AND NOT A COOKIE. A cart's whole reason to exist is
// that it survives leaving the page: you tick four things on page one of a
// browse, three more on page two, one from a search, and then do something with
// all eight. A per-page selection is not a cart, it is a form; and a cookie
// carrying forty release ids is a cookie sent on every request to every route
// including the static assets.
//
// It is also why there is no expiry. A cart somebody left for a week is a cart
// they meant to come back to, and quietly emptying it is a way of losing
// somebody's work to save a few hundred rows.
//
// Every method here guards on st.db.Valid(). CartCount runs from chromeData —
// on EVERY page render, before any handler decides anything — and Conn's
// methods dereference the pool, so an unset handle is a nil dereference inside
// the shared chrome rather than an error one page can report. That is not
// hypothetical: it is how a template test that stands up a *Store without a
// database took the whole suite down with a SIGSEGV.

// AddToCart puts releases in, and reports how many were NEW.
//
// A batch and one statement, because the caller is a listing page where a
// member has just ticked ten rows. Already-in-the-cart is not an error and not
// a duplicate: ticking something twice across two pages should leave one of it.
func (st *Store) AddToCart(ctx context.Context, userID int64, releaseIDs []int64) (int, error) {
	if userID <= 0 || len(releaseIDs) == 0 || !st.db.Valid() {
		return 0, nil
	}
	res, err := st.db.ExecContext(ctx, `
		INSERT INTO release_cart (user_id, release_id)
		SELECT $1, rid FROM unnest($2::bigint[]) AS rid
		ON CONFLICT (user_id, release_id) DO NOTHING`,
		userID, pq.Array(releaseIDs))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RemoveFromCart takes releases out. An empty list is a no-op rather than
// "remove everything" — see ClearCart, which says what it means.
func (st *Store) RemoveFromCart(ctx context.Context, userID int64, releaseIDs []int64) error {
	if userID <= 0 || len(releaseIDs) == 0 || !st.db.Valid() {
		return nil
	}
	_, err := st.db.ExecContext(ctx,
		`DELETE FROM release_cart WHERE user_id = $1 AND release_id = ANY($2)`,
		userID, pq.Array(releaseIDs))
	return err
}

// ClearCart empties it.
func (st *Store) ClearCart(ctx context.Context, userID int64) error {
	if userID <= 0 || !st.db.Valid() {
		return nil
	}
	_, err := st.db.ExecContext(ctx, `DELETE FROM release_cart WHERE user_id = $1`, userID)
	return err
}

// CartIDs is the cart's contents, oldest first — the order they were ticked,
// which is the order somebody expects to find them in.
func (st *Store) CartIDs(ctx context.Context, userID int64) []int64 {
	if userID <= 0 || !st.db.Valid() {
		return nil
	}
	rows, err := st.db.QueryContext(ctx,
		`SELECT release_id FROM release_cart WHERE user_id = $1 ORDER BY created_at, release_id`,
		userID)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return out
		}
		out = append(out, id)
	}
	return out
}

// CartCount is the figure the header badge shows.
//
// Returns ok=false when the table is unreachable, so the badge can be absent
// rather than claim a confident zero — the same rule BookmarkCount follows, and
// for the same reason: "you have nothing selected" is a claim, and it is the
// wrong one to make when the truth is "we could not look".
func (st *Store) CartCount(ctx context.Context, userID int64) (int, bool) {
	if userID <= 0 || !st.db.Valid() {
		return 0, false
	}
	var n int
	if err := st.db.QueryRowContext(ctx,
		`SELECT count(*) FROM release_cart WHERE user_id = $1`, userID).Scan(&n); err != nil {
		return 0, false
	}
	return n, true
}

// InCart is which of these releases are already in the cart, so a listing can
// draw its ticks already ticked.
func (st *Store) InCart(ctx context.Context, userID int64, releaseIDs []int64) map[int64]bool {
	out := map[int64]bool{}
	if userID <= 0 || len(releaseIDs) == 0 || !st.db.Valid() {
		return out
	}
	rows, err := st.db.QueryContext(ctx,
		`SELECT release_id FROM release_cart WHERE user_id = $1 AND release_id = ANY($2)`,
		userID, pq.Array(releaseIDs))
	if err != nil {
		return out
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return out
		}
		out[id] = true
	}
	return out
}

// BookmarkMany saves a batch, and reports how many were new. The bulk sibling
// of ToggleBookmark: a cart empties into the bookmarks, and doing that one
// toggle at a time would UNSAVE anything already saved.
func (st *Store) BookmarkMany(ctx context.Context, userID int64, releaseIDs []int64) (int, error) {
	if userID <= 0 || len(releaseIDs) == 0 || !st.db.Valid() {
		return 0, nil
	}
	res, err := st.db.ExecContext(ctx, `
		INSERT INTO release_bookmark (user_id, release_id)
		SELECT $1, rid FROM unnest($2::bigint[]) AS rid
		ON CONFLICT (user_id, release_id) DO NOTHING`,
		userID, pq.Array(releaseIDs))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
