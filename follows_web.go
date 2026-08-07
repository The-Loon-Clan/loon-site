package main

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/core"
)

// Follows — docs/MOCKS.md M3. UNIT3D models this on the user; here it is its
// own table, for the same reason grabs and bookmarks are: a relation between
// two users is not a column on either of them.
//
// DIRECTIONAL, not mutual. Following someone is a subscription, not a
// friendship — it needs no acceptance, and reciprocity is a coincidence rather
// than a state to store. Modelling it as "friends" would mean a request/accept
// flow nobody asked for and a second status column to carry it.

// followsMigrate creates the table. Idempotent.
func followsMigrate(db *sqlx.DB) error {
	stmts := []string{
		// The pair is the primary key, so following twice is a no-op in the
		// DATABASE rather than in a read-then-write. CASCADE on both sides:
		// a deleted account should not leave dangling edges.
		`CREATE TABLE IF NOT EXISTS user_follow (
		    follower_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    followee_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		    PRIMARY KEY (follower_id, followee_id),
		    -- Following yourself is not a state worth having; the CHECK means
		    -- no handler has to remember to reject it.
		    CHECK (follower_id <> followee_id)
		)`,
		// "who follows X" — the reverse of the primary key, which only covers
		// "who does X follow".
		`CREATE INDEX IF NOT EXISTS idx_user_follow_followee
		     ON user_follow (followee_id)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

var followsDB *sqlx.DB

// followCounts returns (followers, following) for one user in ONE round trip.
// ok is false when the table cannot be read, so the tiles stay em dashes
// rather than claiming a confident zero.
func followCounts(ctx context.Context, userID int64) (followers, following int, ok bool) {
	if followsDB == nil || userID <= 0 {
		return 0, 0, false
	}
	var row struct {
		Followers int `db:"followers"`
		Following int `db:"following"`
	}
	if err := followsDB.GetContext(ctx, &row,
		`SELECT (SELECT COUNT(*) FROM user_follow WHERE followee_id = $1) AS followers,
		        (SELECT COUNT(*) FROM user_follow WHERE follower_id = $1) AS following`,
		userID); err != nil {
		return 0, 0, false
	}
	return row.Followers, row.Following, true
}

// isFollowing answers the profile button's question.
func isFollowing(ctx context.Context, followerID, followeeID int64) bool {
	if followsDB == nil || followerID <= 0 || followeeID <= 0 {
		return false
	}
	var n int
	if err := followsDB.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM user_follow WHERE follower_id = $1 AND followee_id = $2`,
		followerID, followeeID); err != nil {
		return false
	}
	return n > 0
}

// toggleFollow follows or unfollows, reporting the state AFTER the change.
func toggleFollow(ctx context.Context, followerID, followeeID int64) (following bool, err error) {
	if followsDB == nil || followerID <= 0 || followeeID <= 0 || followerID == followeeID {
		return false, nil
	}
	res, err := followsDB.ExecContext(ctx,
		`DELETE FROM user_follow WHERE follower_id = $1 AND followee_id = $2`,
		followerID, followeeID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return false, nil
	}
	_, err = followsDB.ExecContext(ctx,
		`INSERT INTO user_follow (follower_id, followee_id) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`, followerID, followeeID)
	return err == nil, err
}

// followToggle handles the profile button.
//
// POST, like every other write on this site: a GET that mutates is one
// prefetching browser away from following whoever a link happened to mention.
func (w *web) followToggle(c *gin.Context) {
	viewer, ok := w.currentUser(c)
	if !ok || viewer == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	name := c.Param("name")
	subject, err := w.store.ByUsername(c.Request.Context(), name)
	if err != nil || subject == nil {
		c.String(http.StatusNotFound, "no such user")
		return
	}
	if _, err := toggleFollow(c.Request.Context(), viewer.ID, subject.ID); err != nil {
		w.log.Error("toggle follow", "followee", subject.ID, "err", err)
	}
	c.Redirect(http.StatusSeeOther, "/u/"+name)
}

// followList is one member on a follower/following page.
type followList struct {
	Username string
	Role     any
	Since    string
}

// followers/following read the two directions of the same edge. Capped: these
// are unbounded relations, and a page that renders every follower of a popular
// account is the query nobody notices until there is one.
const followPageRows = 200

func listFollowers(ctx context.Context, userID int64) []followList {
	return followQuery(ctx,
		`SELECT u.username, u.role, to_char(f.created_at, 'Mon YYYY') AS since
		   FROM user_follow f JOIN users u ON u.id = f.follower_id
		  WHERE f.followee_id = $1 ORDER BY f.created_at DESC LIMIT $2`, userID)
}

func listFollowing(ctx context.Context, userID int64) []followList {
	return followQuery(ctx,
		`SELECT u.username, u.role, to_char(f.created_at, 'Mon YYYY') AS since
		   FROM user_follow f JOIN users u ON u.id = f.followee_id
		  WHERE f.follower_id = $1 ORDER BY f.created_at DESC LIMIT $2`, userID)
}

func followQuery(ctx context.Context, q string, userID int64) []followList {
	if followsDB == nil || userID <= 0 {
		return nil
	}
	var rows []struct {
		Username string `db:"username"`
		Role     int    `db:"role"`
		Since    string `db:"since"`
	}
	if err := followsDB.SelectContext(ctx, &rows, q, userID, followPageRows); err != nil {
		return nil
	}
	out := make([]followList, 0, len(rows))
	for _, r := range rows {
		out = append(out, followList{Username: r.Username, Role: core.Role(r.Role), Since: r.Since})
	}
	return out
}

// followPage serves /u/:name/followers and /u/:name/following.
//
// One handler for both directions: the pages differ only in which side of the
// edge they read and what the heading says, and two near-identical handlers is
// how the two drift.
// followKind is which of the three lists a request wants. A named kind rather
// than the bool this started as: two lists take a bool, three take a lie.
type followKind string

const (
	followKindFollowers followKind = "followers"
	followKindFollowing followKind = "following"
	followKindFriends   followKind = "friends"
)

func (w *web) followPage(kind followKind) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		name := c.Param("name")
		subject, err := w.store.ByUsername(ctx, name)
		if err != nil || subject == nil {
			c.Status(http.StatusNotFound)
			w.render(c, "follows.html", map[string]any{"Title": "Not found", "Missing": true, "People": []followList{}})
			return
		}
		people, title := listFollowers(ctx, subject.ID), "Followers"
		switch kind {
		case followKindFollowing:
			people, title = listFollowing(ctx, subject.ID), "Following"
		case followKindFriends:
			people, title = listFriends(ctx, subject.ID), "Friends"
		}
		w.render(c, "follows.html", map[string]any{
			"Title":   title,
			"Subject": subject.Username,
			"Kind":    string(kind),
			"People":  people,
		})
	}
}

// listFriends returns MUTUAL follows: people this member follows who follow
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
func listFriends(ctx context.Context, userID int64) []followList {
	return followQuery(ctx,
		`SELECT u.username, u.role,
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
