package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

// /subscriptions — everything this member has chosen to keep up with, in one
// place.
//
// The site had the subscriptions and not the list. You can join a community and
// bookmark a release, and each of those facts lived only where you made it: the
// community knows you joined, the release knows you bookmarked it, and nothing
// answered "what am I following?". ui-patterns files this under Social, and the
// reason it earns a page is that a subscription you cannot find is one you
// cannot cancel — the list is as much an unsubscribe surface as a reading one.
//
// Deliberately NOT a new table. Every row here already exists somewhere; this
// is a reading of community_subscribers and release_bookmark, the same way the
// friends list is a reading of user_follow. A `subscriptions` table would be a
// second source of truth for facts those two already own.
//
// Forum threads are absent because thread subscription does not exist in this
// stack — there is no table and no control. Listing an empty "Threads" section
// would advertise a feature that is not there; see docs/BACKLOG.md.

var subscriptionsDB *sqlx.DB

// subsLog reports a failed read. Package-level so the two list functions stay
// free of a *web receiver they need for nothing else.
var subsLog = func(ctx context.Context, what string, err error) {
	slog.Error("subscriptions read", "list", what, "err", err)
}

// subscriptionRow is one thing being followed, whatever kind it is.
type subscriptionRow struct {
	Title string `db:"title"`
	Href  string `db:"href"`
	Sub   string `db:"sub"` // the second line: members, category, size
	Since string `db:"since"`
}

// listCommunitySubs returns the communities this member has joined.
func listCommunitySubs(ctx context.Context, userID int64) []subscriptionRow {
	if subscriptionsDB == nil || userID <= 0 {
		return nil
	}
	var rows []subscriptionRow
	if err := subscriptionsDB.SelectContext(ctx, &rows, `
		SELECT c.name                                   AS title,
		       '/c/' || c.slug                          AS href,
		       (SELECT count(*)::text || ' member' ||
		               CASE WHEN count(*) = 1 THEN '' ELSE 's' END
		          FROM community_subscribers s2
		         WHERE s2.community_id = c.id)          AS sub,
		       to_char(s.created_at, 'DD Mon YYYY')     AS since
		  FROM community_subscribers s
		  JOIN communities c ON c.id = s.community_id
		 WHERE s.user_id = $1
		   AND c.hidden_at IS NULL
		 ORDER BY s.created_at DESC`, userID); err != nil {
		// Logged, not swallowed. A read that fails renders as "you are not in
		// any communities", which is a confident lie — and it is exactly how
		// the bookmarks half of this page shipped empty: the query named a
		// column that does not exist and said nothing.
		subsLog(ctx, "communities", err)
		return nil
	}
	return rows
}

// listBookmarkSubs returns the releases this member has bookmarked.
//
// Bookmarks already have their own page, and they are here too on purpose: this
// page answers "what am I keeping up with", and a bookmark is one of the
// answers. The link goes to the full list rather than duplicating it.
func listBookmarkSubs(ctx context.Context, userID int64, limit int) []subscriptionRow {
	if subscriptionsDB == nil || userID <= 0 {
		return nil
	}
	var rows []subscriptionRow
	if err := subscriptionsDB.SelectContext(ctx, &rows, `
		SELECT n.title                                  AS title,
		       '/release/' || n.id::text                AS href,
		       COALESCE(n.group_name, '')               AS sub,
		       to_char(b.created_at, 'DD Mon YYYY')     AS since
		  FROM release_bookmark b
		  JOIN usenet.nzbs n ON n.id = b.release_id
		 WHERE b.user_id = $1
		 ORDER BY b.created_at DESC
		 LIMIT $2`, userID, limit); err != nil {
		subsLog(ctx, "bookmarks", err)
		return nil
	}
	return rows
}

// subscriptionsPreview is how many bookmarks the page shows before deferring to
// the bookmarks page. Enough to recognise the list, few enough that it stays a
// summary.
const subscriptionsPreview = 5

// subscriptionsPage serves /subscriptions.
func (w *web) subscriptionsPage(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()

	bookmarks := listBookmarkSubs(ctx, u.ID, subscriptionsPreview+1)
	capped := len(bookmarks) > subscriptionsPreview
	if capped {
		bookmarks = bookmarks[:subscriptionsPreview]
	}
	communities := listCommunitySubs(ctx, u.ID)

	w.render(c, "subscriptions.html", map[string]any{
		"Title":       "Subscriptions",
		"Communities": communities,
		"Bookmarks":   bookmarks,
		"MoreMarks":   capped,
		// One flag rather than the template asking twice: an empty page needs a
		// different message from a page with one section filled.
		"Empty": len(communities) == 0 && len(bookmarks) == 0,
	})
}
