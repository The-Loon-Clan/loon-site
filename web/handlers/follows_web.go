package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
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
	on, err := w.data.ToggleFollow(c.Request.Context(), viewer.ID, subject.ID)
	if err != nil {
		w.log.Error("toggle follow", "followee", subject.ID, "err", err)
	}
	// State from the write's own return, not a re-read — docs/ASYNC.md rule 7.
	if isHTMX(c) {
		w.renderFragment(c, "profile.html", "follow-button", map[string]any{
			"Username":  name,
			"Following": on,
		})
		return
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
		people, title := w.data.ListFollowers(ctx, subject.ID), "Followers"
		switch kind {
		case followKindFollowing:
			people, title = w.data.ListFollowing(ctx, subject.ID), "Following"
		case followKindFriends:
			people, title = w.data.ListFriends(ctx, subject.ID), "Friends"
		}
		w.render(c, "follows.html", map[string]any{
			"Title":   title,
			"Subject": subject.Username,
			"Kind":    string(kind),
			"People":  people,
		})
	}
}
