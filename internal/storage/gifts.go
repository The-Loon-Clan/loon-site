package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// TransferPoints moves points between two members atomically.
//
// The debit carries the same guard pgPoints.change uses -- the balance check is
// IN the UPDATE, so a concurrent spend cannot slip a balance negative between a
// read and a write -- and everything else rides in the same transaction behind
// it.
func (st *Store) TransferPoints(ctx context.Context, fromID, toID int64, amount int, note string) error {
	switch {
	case fromID == toID:
		return ErrGiftSelf
	case amount < GiftMin:
		return fmt.Errorf("a gift has to be at least %d point", GiftMin)
	case amount > GiftMax:
		return fmt.Errorf("that is more than the %d point limit for one gift", GiftMax)
	}
	if r := []rune(note); len(r) > GiftNoteMax {
		// Cut by RUNES: halving a multi-byte character stores invalid UTF-8,
		// and the first thing that breaks is the page trying to show it.
		note = string(r[:GiftNoteMax])
	}

	tx, err := st.db.BeginTxx(ctx, nil)
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
	from, _ := st.UsernameOf(ctx, fromID)
	to, _ := st.UsernameOf(ctx, toID)
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

// UsernameOf resolves a name for the ledger description. Best effort: a
// description reading "Gift to " is worse than one reading "Gift to bob" and
// far better than a failed transfer.
func (st *Store) UsernameOf(ctx context.Context, id int64) (string, error) {
	var n string
	err := st.db.GetContext(ctx, &n, `SELECT username FROM users WHERE id = $1`, id)
	return n, err
}

// ListGifts returns a member's transfers, both directions, newest first.
func (st *Store) ListGifts(ctx context.Context, userID int64, limit int) []GiftRow {
	if userID <= 0 {
		return nil
	}
	var rows []GiftRow
	if err := st.db.SelectContext(ctx, &rows, `
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

// GiftRow is one transfer as either side sees it.
type GiftRow struct {
	Other  string `db:"other"`
	Amount int    `db:"amount"`
	Note   string `db:"note"`
	When   string `db:"when_at"`
	// Sent is true when the viewer was the giver, which is the only thing
	// distinguishing the two directions on the page.
	Sent bool `db:"sent"`
}

// ErrGiftSelf is separate from the rest because it is the one refusal that is
// worth naming: it is a mistake, not an attack, and a member doing it has
// misunderstood the form rather than tried to cheat.
var ErrGiftSelf = errors.New("you cannot gift points to yourself")

const (
	// GiftMin is the smallest gift worth the row it costs to record.
	GiftMin = 1

	// GiftMax caps a single transfer. Not a security control -- the balance
	// guard is that -- but a typo guard: a member meaning 100 and typing 10000
	// should be stopped by the form rather than by regret.
	GiftMax = 100000

	// GiftNoteMax bounds the message. Long enough for a thank-you, short
	// enough that it is not a messaging system growing inside a points table.
	GiftNoteMax = 200
)
