package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/the-loon-clan/loon-demo-site/internal/storage"
)

// Topics and Posts — a member's own forum activity, the two account entries
// UNIT3D has at /users/<me>/topics and /users/<me>/posts.
//
// Pure host reads: forum_threads and forum_posts live in `public`, not in the
// forum plugin's schema, so this needs no seam and no plugin change. The
// forum plugin owns the forum's pages; this owns two listings of the viewer's
// own rows, which is a different surface with a different audience.
//
// Both exclude moderator-hidden rows. A hidden post is not part of the forum
// any more, and hidden_reason is a moderator's note rather than something to
// surface back to its author on their own account page.

// forumActivityPage serves /p/topics and /p/posts.
//
// One handler for both: they differ in which query runs and what the heading
// says. The same reasoning as followPage — two near-identical handlers is how
// the two drift.
func (w *web) forumActivityPage(posts bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		u, ok := w.currentUser(c)
		if !ok || u == nil {
			c.Redirect(http.StatusFound, "/login")
			return
		}
		ctx := c.Request.Context()

		rows, title := storage.ListTopics(ctx, u.ID), "Topics"
		if posts {
			rows, title = storage.ListPosts(ctx, u.ID), "Posts"
		}
		w.render(c, "forum_activity.html", map[string]any{
			"Title": title,
			"Posts": posts,
			"Rows":  rows,
			// Capped listings must say so, or a member with 400 posts reads
			// 100 as their total.
			"Capped": len(rows) >= storage.ForumActivityRows,
			"Cap":    storage.ForumActivityRows,
		})
	}
}
