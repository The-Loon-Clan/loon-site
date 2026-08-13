package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/the-loon-clan/loon-site/internal/storage"
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

// followToggle handles the profile button.
//
// POST, like every other write on this site: a GET that mutates is one
// prefetching browser away from following whoever a link happened to mention.
func (w *web) followToggle(c *gin.Context) {
	viewer, ok := w.viewer(c)
	if !ok {
		return
	}
	name := c.Param("name")
	subject, err := w.store.ByUsername(c.Request.Context(), name)
	if err != nil || subject == nil {
		c.String(http.StatusNotFound, "no such user")
		return
	}
	if _, err := storage.ToggleFollow(c.Request.Context(), viewer.ID, subject.ID); err != nil {
		w.log.Error("toggle follow", "followee", subject.ID, "err", err)
	}
	c.Redirect(http.StatusSeeOther, "/u/"+name)
}

const (
	followKindFollowers storage.FollowKind = "followers"
	followKindFollowing storage.FollowKind = "following"
	followKindFriends   storage.FollowKind = "friends"
)

func (w *web) followPage(kind storage.FollowKind) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		name := c.Param("name")
		subject, err := w.store.ByUsername(ctx, name)
		if err != nil || subject == nil {
			c.Status(http.StatusNotFound)
			w.render(c, "follows.html", map[string]any{"Title": "Not found", "Missing": true, "People": []storage.FollowList{}})
			return
		}
		people, title := storage.ListFollowers(ctx, subject.ID), "Followers"
		switch kind {
		case followKindFollowing:
			people, title = storage.ListFollowing(ctx, subject.ID), "Following"
		case followKindFriends:
			people, title = storage.ListFriends(ctx, subject.ID), "Friends"
		}
		w.render(c, "follows.html", map[string]any{
			"Title":   title,
			"Subject": subject.Username,
			"Kind":    string(kind),
			"People":  people,
		})
	}
}
