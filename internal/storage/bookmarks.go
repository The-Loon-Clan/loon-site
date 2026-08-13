package storage

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// ToggleBookmark adds or removes one bookmark and reports the state AFTER the
// change, so the caller can render the button without a second read.
func ToggleBookmark(ctx context.Context, userID, releaseID int64) (saved bool, err error) {
	if BookmarksDB == nil || userID <= 0 || releaseID <= 0 {
		return false, nil
	}
	// One statement per direction, both keyed on the unique pair: DELETE
	// reports whether it removed anything, which is the toggle's answer.
	res, err := BookmarksDB.ExecContext(ctx,
		`DELETE FROM release_bookmark WHERE user_id = $1 AND release_id = $2`, userID, releaseID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return false, nil // it was bookmarked; now it is not
	}
	_, err = BookmarksDB.ExecContext(ctx,
		`INSERT INTO release_bookmark (user_id, release_id) VALUES ($1, $2)
		 ON CONFLICT (user_id, release_id) DO NOTHING`, userID, releaseID)
	return err == nil, err
}

// BookmarkCount is the profile figure that used to be a mock. Returns ok=false
// when the table is unreachable, so the tile can stay an em dash rather than
// claim a confident zero — the same rule the staff dashboard follows.
func BookmarkCount(ctx context.Context, userID int64) (int, bool) {
	if BookmarksDB == nil || userID <= 0 {
		return 0, false
	}
	var n int
	if err := BookmarksDB.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM release_bookmark WHERE user_id = $1`, userID); err != nil {
		return 0, false
	}
	return n, true
}

// BookmarksDB is the handle. Package-level for the same reason GrabsDB is:
// host-owned data with no plugin behind it.
var BookmarksDB *sqlx.DB
