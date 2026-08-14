package handlers

import (
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/the-loon-clan/loon-site/internal/storage"

	"github.com/the-loon-clan/loon-site/internal/request"
)

// Gifts — points moving from one member to another.
//
// The points system already had everything except this. pgPoints.change spends
// and awards, guarded so a balance cannot go negative, and points_ledger keeps
// the history. What it could not do is move points BETWEEN two people, because
// change touches one row per call and a transfer is two rows that must both
// happen or neither.
//
// So the whole of this file's care is in one function: storage.TransferPoints runs the
// debit, the credit and both ledger rows inside a single transaction. Doing it
// with two change() calls would work almost always and lose somebody's points
// the one time the process died between them — and "almost always" is not a
// property you want in the part of a site that moves balances around.
//
// NO NEW BALANCE. A gift is not a separate kind of currency; it is the same
// points column, so a gifted point behaves exactly like an earned one
// everywhere else on the site. The only new table records who sent what to
// whom, which the ledger alone cannot say: it has two rows, one per side, and
// nothing joining them.

// giftsPage serves GET /gifts.
func (w *web) giftsPage(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	w.render(c, "gifts.html", map[string]any{
		"Title":   "Gifts",
		"Balance": pgPoints{db: w.db()}.balance(ctx, u.ID),
		"Gifts":   w.data.ListGifts(ctx, u.ID, 50),
		"Max":     storage.GiftMax,
		"NoteMax": storage.GiftNoteMax,
		// Pre-filled when arriving from a profile's "Gift points" link, so the
		// member does not retype a name they just clicked.
		"To":   c.Query("to"),
		"Sent": c.Query("sent"),
		"Err":  c.Query(queryErr),
	})
}

// giftsSend serves POST /gifts.
func (w *web) giftsSend(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	in, _ := readGiftInput(c)
	name := in.To
	back := "/gifts?to=" + url.QueryEscape(name)

	// The endpoint's own rules — see inputs.go. The LIMITS (at least GiftMin,
	// at most GiftMax, not to yourself, enough points) stay in
	// storage.TransferPoints, inside the transaction that moves them.
	if errs := request.Validate(in); errs.Any() {
		c.Redirect(http.StatusSeeOther, back+"&err="+url.QueryEscape(errs.First(in.fieldOrder()...)))
		return
	}
	amount := in.Amount
	target, err := w.store.ByUsername(ctx, name)
	if err != nil || target == nil {
		// Named plainly. Hiding whether an account exists matters on a login
		// form, where the answer is a step towards breaking in; here the member
		// list is public and refusing to say just makes a typo unfixable.
		c.Redirect(http.StatusSeeOther, back+"&err="+url.QueryEscape("there is nobody called "+name))
		return
	}
	if err := w.data.TransferPoints(ctx, u.ID, target.ID, amount, in.Note); err != nil {
		c.Redirect(http.StatusSeeOther, back+"&err="+url.QueryEscape(err.Error()))
		return
	}
	w.log.Info("points gifted", "from", u.ID, "to", target.ID, "amount", amount)
	c.Redirect(http.StatusSeeOther, "/gifts?sent="+url.QueryEscape(target.Username))
}
