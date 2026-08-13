package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
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

var forumActivityDB *sqlx.DB

// forumActivityRows caps either listing. A prolific member's post history is
// unbounded, and this is an account page, not an archive.
const forumActivityRows = 100

// activityRow is one line of either listing. One struct for both because they
// differ only in what Title points at — a thread of one's own, or the thread
// a reply landed in — and two near-identical structs is how the two templates
// drift apart.
type activityRow struct {
	ThreadID int    `db:"thread_id"`
	Title    string `db:"title"`
	Category string `db:"category"`
	Excerpt  string `db:"excerpt"`
	Replies  int    `db:"replies"`
	At       string `db:"at"`
}

// listTopics returns threads this member started, most recently active first.
func listTopics(ctx context.Context, userID int64) []activityRow {
	if forumActivityDB == nil || userID <= 0 {
		return nil
	}
	var rows []activityRow
	if err := forumActivityDB.SelectContext(ctx, &rows, `
		SELECT t.id                              AS thread_id,
		       t.title,
		       COALESCE(c.name, '')              AS category,
		       ''                                AS excerpt,
		       t.reply_count                     AS replies,
		       to_char(t.last_post_at, 'DD Mon YYYY') AS at
		  FROM forum_threads t
		  LEFT JOIN forum_categories c ON c.id = t.category_id
		 WHERE t.user_id = $1 AND t.hidden_at IS NULL
		 ORDER BY t.last_post_at DESC
		 LIMIT $2`, userID, forumActivityRows); err != nil {
		return nil
	}
	return rows
}

// listPosts returns this member's replies, newest first, each with the thread
// it landed in so a row is worth clicking.
//
// Own threads are NOT excluded: a reply in your own thread is still a post,
// and filtering it out would make the two pages disagree about the same row.
func listPosts(ctx context.Context, userID int64) []activityRow {
	if forumActivityDB == nil || userID <= 0 {
		return nil
	}
	var rows []activityRow
	if err := forumActivityDB.SelectContext(ctx, &rows, `
		SELECT p.thread_id,
		       t.title,
		       COALESCE(c.name, '')             AS category,
		       -- Trimmed in SQL so a 40 kB post is not carried across the wire
		       -- to be cut to a line in Go. left() is by CHARACTERS here, not
		       -- bytes, so this cannot split a multi-byte rune.
		       left(p.body, 300)                AS excerpt,
		       0                                AS replies,
		       to_char(p.created_at, 'DD Mon YYYY') AS at
		  FROM forum_posts p
		  JOIN forum_threads t ON t.id = p.thread_id
		  LEFT JOIN forum_categories c ON c.id = t.category_id
		 WHERE p.user_id = $1
		   AND p.hidden_at IS NULL
		   -- A reply in a thread a moderator hid is not reachable either.
		   AND t.hidden_at IS NULL
		 ORDER BY p.created_at DESC
		 LIMIT $2`, userID, forumActivityRows); err != nil {
		return nil
	}
	return rows
}

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

		rows, title := listTopics(ctx, u.ID), "Topics"
		if posts {
			rows, title = listPosts(ctx, u.ID), "Posts"
		}
		w.render(c, "forum_activity.html", map[string]any{
			"Title": title,
			"Posts": posts,
			"Rows":  rows,
			// Capped listings must say so, or a member with 400 posts reads
			// 100 as their total.
			"Capped": len(rows) >= forumActivityRows,
			"Cap":    forumActivityRows,
		})
	}
}
