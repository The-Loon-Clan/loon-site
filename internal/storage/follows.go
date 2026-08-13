package storage

import (
	"context"

	"github.com/the-loon-clan/loon/core"
)

// FollowCounts returns (followers, following) for one user in ONE round trip.
// ok is false when the table cannot be read, so the tiles stay em dashes
// rather than claiming a confident zero.
func (st *Store) FollowCounts(ctx context.Context, userID int64) (followers, following int, ok bool) {
	if userID <= 0 {
		return 0, 0, false
	}
	var row struct {
		Followers int `db:"followers"`
		Following int `db:"following"`
	}
	if err := st.db.GetContext(ctx, &row,
		`SELECT (SELECT COUNT(*) FROM user_follow WHERE followee_id = $1) AS followers,
		        (SELECT COUNT(*) FROM user_follow WHERE follower_id = $1) AS following`,
		userID); err != nil {
		return 0, 0, false
	}
	return row.Followers, row.Following, true
}

// IsFollowing answers the profile button's question.
func (st *Store) IsFollowing(ctx context.Context, followerID, followeeID int64) bool {
	if followerID <= 0 || followeeID <= 0 {
		return false
	}
	var n int
	if err := st.db.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM user_follow WHERE follower_id = $1 AND followee_id = $2`,
		followerID, followeeID); err != nil {
		return false
	}
	return n > 0
}

// ToggleFollow follows or unfollows, reporting the state AFTER the change.
func (st *Store) ToggleFollow(ctx context.Context, followerID, followeeID int64) (following bool, err error) {
	if followerID <= 0 || followeeID <= 0 || followerID == followeeID {
		return false, nil
	}
	res, err := st.db.ExecContext(ctx,
		`DELETE FROM user_follow WHERE follower_id = $1 AND followee_id = $2`,
		followerID, followeeID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return false, nil
	}
	_, err = st.db.ExecContext(ctx,
		`INSERT INTO user_follow (follower_id, followee_id) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`, followerID, followeeID)
	return err == nil, err
}

// ListFollowers returns the people following userID, newest first.
//
// Followers and following are the same query against opposite ends of the same
// row, which is why both go through followQuery rather than duplicating the
// join and the avatar/role/since projection the template needs.
func (st *Store) ListFollowers(ctx context.Context, userID int64) []FollowList {
	return st.followQuery(ctx,
		`SELECT u.username, u.role, COALESCE(u.avatar_path, '') AS avatar_path,
		        to_char(f.created_at, 'Mon YYYY') AS since
		   FROM user_follow f JOIN users u ON u.id = f.follower_id
		  WHERE f.followee_id = $1 ORDER BY f.created_at DESC LIMIT $2`, userID)
}

// ListFollowing returns the people userID follows, newest first.
func (st *Store) ListFollowing(ctx context.Context, userID int64) []FollowList {
	return st.followQuery(ctx,
		`SELECT u.username, u.role, COALESCE(u.avatar_path, '') AS avatar_path,
		        to_char(f.created_at, 'Mon YYYY') AS since
		   FROM user_follow f JOIN users u ON u.id = f.followee_id
		  WHERE f.follower_id = $1 ORDER BY f.created_at DESC LIMIT $2`, userID)
}

// ListFriends returns MUTUAL follows: people this member follows who follow
// back.
//
// ui-patterns separates Follow from Friend, and the difference is not
// cosmetic. Follow is one-way and needs nobody's permission — it is a reading
// choice. Friend is reciprocal, and a site that has both can say things it
// otherwise cannot: "people you both know", "only friends may message me".
//
// Derived from user_follow rather than stored. A `friends` table would be a
// second source of truth for a fact user_follow already answers, and the two
// drift the first time a follow is removed without the friendship being
// cleaned up. The self-join costs one index lookup per row on a table that
// already has both directions indexed.
//
// `since` is the LATER of the two follows — the friendship began when the
// second person reciprocated, not when the first one started reading.
func (st *Store) ListFriends(ctx context.Context, userID int64) []FollowList {
	return st.followQuery(ctx,
		`SELECT u.username, u.role, COALESCE(u.avatar_path, '') AS avatar_path,
		        to_char(GREATEST(mine.created_at, theirs.created_at), 'Mon YYYY') AS since
		   FROM user_follow mine
		   JOIN user_follow theirs
		     ON theirs.follower_id = mine.followee_id
		    AND theirs.followee_id = mine.follower_id
		   JOIN users u ON u.id = mine.followee_id
		  WHERE mine.follower_id = $1
		  ORDER BY GREATEST(mine.created_at, theirs.created_at) DESC
		  LIMIT $2`, userID)
}

// FollowList is one member on a follower/following page.
type FollowList struct {
	Username string
	Role     any
	Since    string
	// Avatar is users.avatar_path, empty for a member who has not set one —
	// which the user-card partial renders as the initials tile.
	Avatar string
}

// followPage serves /u/:name/followers and /u/:name/following.
//
// One handler for both directions: the pages differ only in which side of the
// edge they read and what the heading says, and two near-identical handlers is
// how the two drift.
// FollowKind is which of the three lists a request wants. A named kind rather
// than the bool this started as: two lists take a bool, three take a lie.
type FollowKind string

// followers/following read the two directions of the same edge. Capped: these
// are unbounded relations, and a page that renders every follower of a popular
// account is the query nobody notices until there is one.
const FollowPageRows = 200

// followQuery runs one of the three follow-list queries in this file.
//
// The SQL is a parameter because the lists differ only in which side of
// user_follow they join on; the projection, the limit and the error handling
// are identical and belong in one place.
//
// UNEXPORTED, and that is the whole safety argument. A function that executes
// a caller-supplied statement is safe exactly as far as its callers can be
// enumerated — here they are the three above, each passing a literal. Exported,
// it would be an open door with a polite sign on it, and scripts/sqllint.py
// flagged it as one.
func (st *Store) followQuery(ctx context.Context, q SQL, userID int64) []FollowList {
	if userID <= 0 {
		return nil
	}
	var rows []struct {
		Username string `db:"username"`
		Role     int    `db:"role"`
		Since    string `db:"since"`
		Avatar   string `db:"avatar_path"`
	}
	// sqllint:allow q is one of the three literals above; followQuery is unexported so callers cannot supply their own
	if err := st.db.SelectContext(ctx, &rows, q, userID, FollowPageRows); err != nil {
		return nil
	}
	out := make([]FollowList, 0, len(rows))
	for _, r := range rows {
		out = append(out, FollowList{Username: r.Username, Role: core.Role(r.Role), Since: r.Since, Avatar: r.Avatar})
	}
	return out
}
