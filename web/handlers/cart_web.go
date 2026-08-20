package handlers

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-plugins/pluginapi"
)

// The cart — tick releases across listings, then do one thing with all of them.
//
// This is the oldest missing feature on the indexer list and the smallest: the
// site could already save a release, collect releases and download a release,
// and the only thing it could not do was any of those to TEN releases. A
// season is ten rows and grabbing it was ten round trips.
//
// NO JAVASCRIPT, and the two pieces that make that work are worth naming:
//
//   - The checkboxes are inside the table but the form is not. A <form> cannot
//     legally wrap a <tbody>, so every checkbox carries form="cart-form" and
//     the form sits beside the table. This is why no listing template had to be
//     restructured — one attribute, and the browser does the association.
//   - The action bar appears only when something is ticked, via
//     :has(input:checked) in the stylesheet. No script, no state to keep in
//     sync, and it degrades to a permanently visible bar on anything that does
//     not support :has.
//
// The checkbox also sits INSIDE the first cell rather than in a column of its
// own, because listing.html's own comment warns what a new column costs here:
// five tables share that row and a missed <th> silently misaligns one of them.

// cartMax bounds a single submission. Generous — a page of results is 100 rows
// and ticking all of them is exactly the use case — but not unbounded, because
// the ids arrive in a POST body somebody else writes.
const cartMax = 500

// cartCap bounds the whole cart, not just one submission.
//
// Ids are not checked against the index on the way in — that would be a lookup
// per id on a path whose whole point is arriving with fifty of them, and an id
// that resolves to nothing is already handled honestly at render ("no longer on
// the index"). What that leaves is somebody posting made-up ids all day, so the
// bound is on the total rather than on the truth of any one of them.
//
// Five hundred is far past useful — it is five pages of results — and a member
// who reaches it is told rather than silently ignored.
const cartCap = 500

// cartPage renders /cart.
func (w *web) cartPage(c *gin.Context) {
	ctx := c.Request.Context()
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ids := w.data.CartIDs(ctx, u.ID)
	rows := make([]searchRow, 0, len(ids))
	missing := 0
	if w.usenet != nil {
		for _, id := range ids {
			detail, found, err := w.usenet.ReleaseByID(ctx, id)
			if err != nil || !found {
				// Retention removed it. Counted rather than skipped silently:
				// somebody whose cart shrank between visits deserves to know
				// why, and "3 are no longer on the index" is the answer.
				missing++
				continue
			}
			rows = append(rows, toSearchRows([]pluginapi.Release{detail.Release})[0])
		}
		w.attachCovers(ctx, rows)
		rows = w.attachSwarm(ctx, w.attachGrabs(ctx, rows))
	}

	data := map[string]any{
		"Title":   "Cart",
		"Results": rows,
		"Missing": missing,
		"Done":    c.Query(queryDone),
		"N":       c.Query(queryCount),
		"CartOn": true,
	}
	// The ticks, on — but NOT through cartData, which would also set InCart.
	// Every row here IS in the cart, and an in-cart tick renders checked and
	// disabled: on a listing that reads as "already collected", and on this
	// page it would mean nothing could ever be removed.
	for i := range rows {
		rows[i].Selectable = true
	}
	// The collections this member may add to, if a plugin offers any. Absent is
	// normal and the page simply does not offer the choice.
	if sink, ok := w.collectionSink(); ok {
		if cols, err := sink.CollectionsOf(ctx, u.ID); err == nil {
			data["Collections"] = cols
		}
	}
	w.render(c, "cart.html", data)
}

// cartIDsFromForm reads the ticked ids.
//
// Bounded and de-duplicated here rather than in storage, because this is the
// edge: everything below it should be able to trust the slice it is handed.
func cartIDsFromForm(c *gin.Context) []int64 {
	raw := c.PostFormArray("id")
	if len(raw) > cartMax {
		raw = raw[:cartMax]
	}
	seen := make(map[int64]bool, len(raw))
	out := make([]int64, 0, len(raw))
	for _, s := range raw {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// cartAdd takes the ticked rows from a listing and returns the member to it.
func (w *web) cartAdd(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	ids := cartIDsFromForm(c)
	n := 0
	if len(ids) > 0 {
		if have, ok := w.data.CartCount(ctx, u.ID); ok && have+len(ids) > cartCap {
			// Refused whole rather than partially: taking the first few of a
			// ticked batch and dropping the rest is a silent, arbitrary choice
			// about somebody else's selection.
			c.Redirect(http.StatusSeeOther, "/cart?"+queryDone+"=full")
			return
		}
		var err error
		if n, err = w.data.AddToCart(ctx, u.ID, ids); err != nil {
			w.log.Error("cart add", "count", len(ids), "err", err)
		}
	}
	// BACK TO THE LISTING, not to the cart. Somebody ticking rows on page two
	// of a browse is not finished — the entire point of a cart is that they
	// carry on — and a redirect to /cart would end the thing it exists to
	// enable.
	c.Redirect(http.StatusSeeOther, cartBack(c)+cartFragment(n))
}

// cartRemove takes rows out, from the cart page itself.
func (w *web) cartRemove(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	if ids := cartIDsFromForm(c); len(ids) > 0 {
		if err := w.data.RemoveFromCart(c.Request.Context(), u.ID, ids); err != nil {
			w.log.Error("cart remove", "err", err)
		}
	}
	c.Redirect(http.StatusSeeOther, "/cart")
}

// cartClear empties it.
func (w *web) cartClear(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	if err := w.data.ClearCart(c.Request.Context(), u.ID); err != nil {
		w.log.Error("cart clear", "err", err)
	}
	c.Redirect(http.StatusSeeOther, "/cart")
}

// cartBookmark saves everything in the cart.
//
// SAVE, not toggle. Emptying a cart into the bookmarks one ToggleBookmark at a
// time would UNSAVE anything already saved — a member who bookmarked three of
// the ten last week would end with seven, which is the opposite of what the
// button says.
func (w *web) cartBookmark(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	ids := w.data.CartIDs(ctx, u.ID)
	n, err := w.data.BookmarkMany(ctx, u.ID, ids)
	if err != nil {
		w.log.Error("cart bookmark", "err", err)
		c.Redirect(http.StatusSeeOther, "/cart?"+queryDone+"=failed")
		return
	}
	c.Redirect(http.StatusSeeOther, "/cart?"+queryDone+"=saved&"+queryCount+"="+strconv.Itoa(n))
}

// cartCollect empties the cart into a collection (pluginapi.CollectionSink).
func (w *web) cartCollect(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	sink, has := w.collectionSink()
	if !has {
		c.Redirect(http.StatusSeeOther, "/cart?"+queryDone+"=nocollections")
		return
	}
	slug := c.PostForm("collection")
	if slug == "" {
		c.Redirect(http.StatusSeeOther, "/cart?"+queryDone+"=nocollection")
		return
	}
	ctx := c.Request.Context()
	ids := w.data.CartIDs(ctx, u.ID)
	// The slug is NOT checked here against the list this page rendered. It goes
	// to the sink, which resolves it against the member's own collections in
	// its own statement — that check belongs to whoever owns the rows, and
	// re-doing it here would be a second, weaker copy of it.
	n, err := sink.AddToCollection(ctx, u.ID, slug, ids)
	if err != nil {
		w.log.Error("cart collect", "slug", slug, "err", err)
		c.Redirect(http.StatusSeeOther, "/cart?"+queryDone+"=failed")
		return
	}
	c.Redirect(http.StatusSeeOther, "/cart?"+queryDone+"=collected&"+queryCount+"="+strconv.Itoa(n))
}

// cartZip serves every NZB in the cart as one archive.
//
// This is the feature. Ten NZBs is ten clicks, ten Save-As dialogs and ten
// files to find; a downloader takes a folder or a zip and does the rest.
//
// Built in MEMORY rather than streamed, deliberately. An NZB is kilobytes —
// five hundred of them is a few megabytes — and building it whole means a
// failure halfway through can still return an error page instead of a truncated
// archive that a client will happily accept and then fail to open.
func (w *web) cartZip(c *gin.Context) {
	if w.usenet == nil {
		// Back to the cart with a key the template renders, rather than a
		// sentence built here: a member-facing string in Go is one no
		// translator will ever find (loon-plugins/CHECKLIST.md section 10).
		c.Redirect(http.StatusSeeOther, "/cart?"+queryDone+"=noindex")
		return
	}
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	ids := w.data.CartIDs(ctx, u.ID)
	if len(ids) == 0 {
		c.Redirect(http.StatusSeeOther, "/cart")
		return
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	used := map[string]int{}
	written := 0
	for _, id := range ids {
		data, filename, err := w.usenet.NZB(ctx, id)
		if err != nil || len(data) == 0 {
			// Retention removed it, or the fetch failed. Skipped rather than
			// aborting the whole archive: nine of ten is worth having, and the
			// cart page already reports what is no longer on the index.
			continue
		}
		if filename == "" {
			filename = "release-" + strconv.FormatInt(id, 10) + ".nzb"
		}
		filename = sanitizeFilename(filename)
		// Two releases can share a title, and a zip with two identical entry
		// names is one most clients extract as a single file.
		if n := used[filename]; n > 0 {
			filename = fmt.Sprintf("%s (%d)", filename, n+1)
		}
		used[filename]++

		f, err := zw.Create(filename)
		if err != nil {
			continue
		}
		if _, err := f.Write(data); err != nil {
			continue
		}
		written++
		// Counted per NZB, exactly as the single download does. A grab is a
		// grab whether it arrived one at a time or in a batch, and not counting
		// these would make the cart a way to take a thousand releases off an
		// index without appearing in any figure.
		w.data.RecordGrab(ctx, id, u.ID)
	}
	if err := zw.Close(); err != nil || written == 0 {
		c.Redirect(http.StatusSeeOther, "/cart?"+queryDone+"=empty")
		return
	}

	name := fmt.Sprintf("cart-%s.zip", time.Now().Format("2006-01-02"))
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Data(http.StatusOK, "application/zip", buf.Bytes())
}

// cartBack is the listing to return to after ticking rows.
//
// Same-site paths only. The value comes out of a form, and a redirect target
// taken from a request is an open redirect the moment it is trusted.
func cartBack(c *gin.Context) string {
	b := c.PostForm("back")
	if len(b) > 1 && b[0] == '/' && b[1] != '/' {
		return b
	}
	return "/browse"
}

// cartFragment returns the member to the part of the page they were reading.
//
// A listing is long and the action bar is at the bottom of it; without this,
// adding four rows on page two throws you back to the top of page two, which
// reads as the page having reloaded and lost your place. Empty when nothing was
// added, so a no-op submission does not scroll anybody anywhere.
func cartFragment(added int) string {
	if added <= 0 {
		return ""
	}
	return "#cart-bar"
}

// collectionSink resolves whichever plugin owns collections, guarding the two
// nils on the way — views.go does the same for worn medals, and a template that
// panics inside a render returns a blank 200 rather than an error anybody can
// act on.
func (w *web) collectionSink() (pluginapi.CollectionSink, bool) {
	if w.rt == nil || w.rt.Core() == nil {
		return nil, false
	}
	return pluginapi.Collections(w.rt.Core())
}

// cartData switches the cart controls on for a listing page.
//
// Called by the pages that are LISTINGS somebody works through — browse,
// search, trending, bookmarks — and deliberately not by the home page, whose
// release table is a preview of the newest few. A tick box there would be a
// control on a page nobody is selecting from, and the form it needs would have
// to be threaded through the home page's block system for it.
//
// Anonymous viewers get nothing, because a cart belongs to an account and a
// tick box that silently does nothing is worse than no tick box.
func (w *web) cartData(c *gin.Context, data map[string]any, rows []searchRow) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		return
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	data["CartOn"] = true
	// Written onto the ROWS, because that is where the template can see them.
	// The slice in data["Results"] is this same backing array, so setting the
	// fields here is what the page renders.
	in := w.data.InCart(c.Request.Context(), u.ID, ids)
	for i := range rows {
		rows[i].Selectable = true
		rows[i].InCart = in[rows[i].ID]
	}
	// Where the add returns them: this page, with its query string, because a
	// cart is filled while paging through results.
	data["CartBack"] = c.Request.URL.RequestURI()
}
