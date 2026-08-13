package handlers

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/the-loon-clan/loon-site/internal/storage"
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

// giftsMigrate creates the record of who gave what to whom.
func giftsMigrate(db *sqlx.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS point_gifts (
		    id          BIGSERIAL PRIMARY KEY,
		    from_user   BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    to_user     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		    amount      INTEGER NOT NULL CHECK (amount > 0),
		    note        TEXT NOT NULL DEFAULT '',
		    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS point_gifts_from ON point_gifts (from_user, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS point_gifts_to   ON point_gifts (to_user,   created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// giftsPage serves GET /gifts.
func (w *web) giftsPage(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	w.render(c, "gifts.html", map[string]any{
		"Title":   "Gifts",
		"Balance": pgPoints{db: w.data.DB()}.balance(ctx, u.ID),
		"Gifts":   w.data.ListGifts(ctx, u.ID, 50),
		"Max":     storage.GiftMax,
		"NoteMax": storage.GiftNoteMax,
		// Pre-filled when arriving from a profile's "Gift points" link, so the
		// member does not retype a name they just clicked.
		"To":   c.Query("to"),
		"Sent": c.Query("sent"),
		"Err":  c.Query("err"),
	})
}

// giftsSend serves POST /gifts.
func (w *web) giftsSend(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	name := strings.TrimSpace(c.PostForm("to"))
	back := "/gifts?to=" + url.QueryEscape(name)

	amount, err := strconv.Atoi(strings.TrimSpace(c.PostForm("amount")))
	if err != nil {
		c.Redirect(http.StatusSeeOther, back+"&err="+url.QueryEscape("that is not a number of points"))
		return
	}
	target, err := w.store.ByUsername(ctx, name)
	if err != nil || target == nil {
		// Named plainly. Hiding whether an account exists matters on a login
		// form, where the answer is a step towards breaking in; here the member
		// list is public and refusing to say just makes a typo unfixable.
		c.Redirect(http.StatusSeeOther, back+"&err="+url.QueryEscape("there is nobody called "+name))
		return
	}
	if err := w.data.TransferPoints(ctx, u.ID, target.ID, amount, c.PostForm("note")); err != nil {
		c.Redirect(http.StatusSeeOther, back+"&err="+url.QueryEscape(err.Error()))
		return
	}
	w.log.Info("points gifted", "from", u.ID, "to", target.ID, "amount", amount)
	c.Redirect(http.StatusSeeOther, "/gifts?sent="+url.QueryEscape(target.Username))
}
