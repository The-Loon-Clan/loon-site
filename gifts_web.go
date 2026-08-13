package site

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

// Gifts — points moving from one member to another.
//
// The points system already had everything except this. pgPoints.change spends
// and awards, guarded so a balance cannot go negative, and points_ledger keeps
// the history. What it could not do is move points BETWEEN two people, because
// change touches one row per call and a transfer is two rows that must both
// happen or neither.
//
// So the whole of this file's care is in one function: transferPoints runs the
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

const (
	// giftMin is the smallest gift worth the row it costs to record.
	giftMin = 1

	// giftMax caps a single transfer. Not a security control -- the balance
	// guard is that -- but a typo guard: a member meaning 100 and typing 10000
	// should be stopped by the form rather than by regret.
	giftMax = 100000

	// giftNoteMax bounds the message. Long enough for a thank-you, short
	// enough that it is not a messaging system growing inside a points table.
	giftNoteMax = 200
)

var giftsDB *sqlx.DB

// errGiftSelf is separate from the rest because it is the one refusal that is
// worth naming: it is a mistake, not an attack, and a member doing it has
// misunderstood the form rather than tried to cheat.
var errGiftSelf = errors.New("you cannot gift points to yourself")

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

// transferPoints moves points between two members atomically.
//
// The debit carries the same guard pgPoints.change uses -- the balance check is
// IN the UPDATE, so a concurrent spend cannot slip a balance negative between a
// read and a write -- and everything else rides in the same transaction behind
// it.
func transferPoints(ctx context.Context, db *sqlx.DB, fromID, toID int64, amount int, note string) error {
	switch {
	case db == nil:
		return errors.New("points are not available")
	case fromID == toID:
		return errGiftSelf
	case amount < giftMin:
		return fmt.Errorf("a gift has to be at least %d point", giftMin)
	case amount > giftMax:
		return fmt.Errorf("that is more than the %d point limit for one gift", giftMax)
	}
	if r := []rune(note); len(r) > giftNoteMax {
		// Cut by RUNES: halving a multi-byte character stores invalid UTF-8,
		// and the first thing that breaks is the page trying to show it.
		note = string(r[:giftNoteMax])
	}

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return errors.New("could not send that gift")
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	var fromBal int
	if err := tx.QueryRowContext(ctx,
		`UPDATE users SET points = points - $2
		  WHERE id = $1 AND points - $2 >= 0
		  RETURNING points`, fromID, amount).Scan(&fromBal); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("you do not have that many points")
		}
		return errors.New("could not send that gift")
	}

	var toBal int
	if err := tx.QueryRowContext(ctx,
		`UPDATE users SET points = points + $2 WHERE id = $1 RETURNING points`,
		toID, amount).Scan(&toBal); err != nil {
		// No rows here means the recipient vanished between the lookup and
		// now. The rollback puts the sender's points back.
		return errors.New("that member no longer exists")
	}

	var giftID int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO point_gifts (from_user, to_user, amount, note) VALUES ($1,$2,$3,$4) RETURNING id`,
		fromID, toID, amount, note).Scan(&giftID); err != nil {
		return errors.New("could not send that gift")
	}

	// Both ledger rows reference the gift, so the two halves of one transfer
	// can be joined back together later. The ledger on its own has two
	// unrelated rows and no way to say they were the same event.
	from, _ := usernameOf(ctx, db, fromID)
	to, _ := usernameOf(ctx, db, toID)
	for _, row := range []struct {
		user int64
		amt  int
		bal  int
		desc string
	}{
		{fromID, -amount, fromBal, "Gift to " + to},
		{toID, amount, toBal, "Gift from " + from},
	} {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO points_ledger (user_id, amount, balance, kind, description, reference_id, created_at)
			 VALUES ($1,$2,$3,'gift',$4,$5,$6)`,
			row.user, row.amt, row.bal, row.desc, giftID, time.Now()); err != nil {
			return errors.New("could not send that gift")
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.New("could not send that gift")
	}
	return nil
}

// usernameOf resolves a name for the ledger description. Best effort: a
// description reading "Gift to " is worse than one reading "Gift to bob" and
// far better than a failed transfer.
func usernameOf(ctx context.Context, db *sqlx.DB, id int64) (string, error) {
	var n string
	err := db.GetContext(ctx, &n, `SELECT username FROM users WHERE id = $1`, id)
	return n, err
}

// giftRow is one transfer as either side sees it.
type giftRow struct {
	Other  string `db:"other"`
	Amount int    `db:"amount"`
	Note   string `db:"note"`
	When   string `db:"when_at"`
	// Sent is true when the viewer was the giver, which is the only thing
	// distinguishing the two directions on the page.
	Sent bool `db:"sent"`
}

// listGifts returns a member's transfers, both directions, newest first.
func listGifts(ctx context.Context, db *sqlx.DB, userID int64, limit int) []giftRow {
	if db == nil || userID <= 0 {
		return nil
	}
	var rows []giftRow
	if err := db.SelectContext(ctx, &rows, `
		SELECT CASE WHEN g.from_user = $1 THEN t.username ELSE f.username END AS other,
		       g.amount,
		       g.note,
		       to_char(g.created_at, 'DD Mon YYYY HH24:MI')                   AS when_at,
		       (g.from_user = $1)                                             AS sent
		  FROM point_gifts g
		  JOIN users f ON f.id = g.from_user
		  JOIN users t ON t.id = g.to_user
		 WHERE g.from_user = $1 OR g.to_user = $1
		 ORDER BY g.created_at DESC
		 LIMIT $2`, userID, limit); err != nil {
		slog.Error("gifts read", "user", userID, "err", err)
		return nil
	}
	return rows
}

// giftsPage serves GET /gifts.
func (w *web) giftsPage(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	w.render(c, "gifts.html", map[string]any{
		"Title":   "Gifts",
		"Balance": pgPoints{db: giftsDB}.balance(ctx, u.ID),
		"Gifts":   listGifts(ctx, giftsDB, u.ID, 50),
		"Max":     giftMax,
		"NoteMax": giftNoteMax,
		// Pre-filled when arriving from a profile's "Gift points" link, so the
		// member does not retype a name they just clicked.
		"To":   c.Query("to"),
		"Sent": c.Query("sent"),
		"Err":  c.Query("err"),
	})
}

// giftsSend serves POST /gifts.
func (w *web) giftsSend(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
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
	if err := transferPoints(ctx, giftsDB, u.ID, target.ID, amount, c.PostForm("note")); err != nil {
		c.Redirect(http.StatusSeeOther, back+"&err="+url.QueryEscape(err.Error()))
		return
	}
	w.log.Info("points gifted", "from", u.ID, "to", target.ID, "amount", amount)
	c.Redirect(http.StatusSeeOther, "/gifts?sent="+url.QueryEscape(target.Username))
}
