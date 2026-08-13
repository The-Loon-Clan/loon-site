package handlers

import (
	"github.com/gin-gonic/gin"
	"github.com/the-loon-clan/loon-site/internal/storage"
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

// subscriptionsPreview is how many bookmarks the page shows before deferring to
// the bookmarks page. Enough to recognise the list, few enough that it stays a
// summary.
const subscriptionsPreview = 5

// subscriptionsPage serves /subscriptions.
func (w *web) subscriptionsPage(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	bookmarks := storage.ListBookmarkSubs(ctx, u.ID, subscriptionsPreview+1)
	capped := len(bookmarks) > subscriptionsPreview
	if capped {
		bookmarks = bookmarks[:subscriptionsPreview]
	}
	communities := storage.ListCommunitySubs(ctx, u.ID)

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
