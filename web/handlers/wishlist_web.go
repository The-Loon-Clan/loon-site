package handlers

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/request"
)

// The wishlist — what members want indexed that is not here yet.
//
// UNIT3D's version hangs off TMDB, so an entry is a real film with a poster.
// This stack has neither a metadata source wired nor an index with anything in
// it, and building a TMDB-shaped wishlist against neither would be a page of
// empty posters. So an entry is TEXT: what you are looking for, in your own
// words.
//
// That is not a downgrade so much as a different, honest reading of the same
// feature. The point of a wishlist on a tracker is not the poster; it is that
// somebody who UPLOADS can see what people are asking for. So the site-wide
// view is the important half, and it is the one that works with an empty index.
//
// Deliberately NOT tied to releases. Matching free text against a catalogue
// that does not exist would mean guessing, and a wishlist that silently marks
// your entry "found" against the wrong thing is worse than one that never
// does. Marking an entry filled is a decision a person makes.
//
// Worth noting where this stops: pluginapi declares CalculatePoints, "the
// upload reward for a fulfilled request, given size, age and vote count" -- a
// REQUESTS system, with bounties and voting, that nothing on this host
// implements. A wishlist is the small honest half of that. The rest is a
// plugin nobody has written, and this file does not pretend to be it.

const (
	// wishTitleMax bounds an entry. A title, not a description.
	wishTitleMax = 160
	// wishNoteMax is the room for "1080p or better, not the extended cut".
	wishNoteMax = 300
	// wishPerUser caps how many open entries one member may hold. A wishlist
	// everybody can add to without limit becomes a place to shout rather than
	// a list anybody reads.
	wishPerUser = 25
)

// wishlistPage serves GET /wishlist.
func (w *web) wishlistPage(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	mine := c.Query("all") != "1"
	w.render(c, "wishlist.html", map[string]any{
		"Title":    "Wishlist",
		"Items":    w.data.ListWishlist(ctx, u.ID, mine),
		"Mine":     mine,
		"Open":     w.data.CountOpenWishes(ctx, u.ID),
		"Cap":      wishPerUser,
		"TitleMax": wishTitleMax,
		"NoteMax":  wishNoteMax,
		"Err":      c.Query("err"),
	})
}

// wishlistAdd serves POST /wishlist.
func (w *web) wishlistAdd(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	in := readWishInput(c)
	// Reported rather than silently truncated — see inputs.go. Storing a title
	// the member did not write is something they find out about by noticing it
	// on their own list later.
	if errs := request.Validate(in); errs.Any() {
		c.Redirect(http.StatusSeeOther, "/wishlist?err="+url.QueryEscape(errs.First(in.fieldOrder()...)))
		return
	}
	title, note := in.Title, in.Note
	if w.data.CountOpenWishes(ctx, u.ID) >= wishPerUser {
		c.Redirect(http.StatusSeeOther, "/wishlist?err="+
			url.QueryEscape("that is your "+strconv.Itoa(wishPerUser)+" open entries; fill or remove one first"))
		return
	}
	if err := w.data.AddWish(ctx, u.ID, title, note); err != nil {
		w.log.Error("wishlist add", "user", u.ID, "err", err)
		c.Redirect(http.StatusSeeOther, "/wishlist?err="+url.QueryEscape("could not add that"))
		return
	}
	c.Redirect(http.StatusSeeOther, "/wishlist")
}

// wishlistUpdate serves POST /wishlist/:id — mark filled, reopen, or remove.
//
// One handler for three actions because they are the same authorisation
// question asked once: is this row yours? Splitting them into three routes
// would mean asking it three times and getting it wrong once.
func (w *web) wishlistUpdate(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.Redirect(http.StatusSeeOther, "/wishlist")
		return
	}
	ctx := c.Request.Context()
	// Which edit is an HTTP decision; each store method carries the ownership
	// check in its own WHERE, so the viewer's id travels with the row id.
	var err2 error
	switch c.PostForm("action") {
	case "remove":
		err2 = w.data.RemoveWish(ctx, id, u.ID)
	case "reopen":
		err2 = w.data.ReopenWish(ctx, id, u.ID)
	default:
		err2 = w.data.FillWish(ctx, id, u.ID)
	}
	if err2 != nil {
		w.log.Error("wishlist update", "item", id, "user", u.ID, "err", err2)
	}
	back := "/wishlist"
	if c.PostForm("all") == "1" {
		back += "?all=1"
	}
	c.Redirect(http.StatusSeeOther, back)
}
