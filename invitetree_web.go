package site

import (
	"context"
	"log/slog"

	"github.com/jmoiron/sqlx"
)

// The invite tree — who brought whom, and who brought them.
//
// docs/UNIT3D-PARITY.md called this the feature our invite balance "declines to
// be", and that was true when invites were only a NUMBER on users.invites: a
// count records that somebody had the right to invite, and nothing about who
// they actually invited.
//
// It stopped being true when invitecodes_web.go landed. That file records
// created_by and used_by on every code and says why: "on an invite-only site
// that chain is the only accountability there is — if an account turns out to
// be a problem, 'who vouched for them' is the first question, and a schema that
// cannot answer it makes invite-only a formality."
//
// So this is a READ. No table, no migration, no new column: the chain has been
// accumulating since the day codes were added, and this is the page that asks
// it the question it was recorded for.

// inviteTreeDepth caps the recursion.
//
// A cap rather than trust: an invite chain cannot cycle in practice, because a
// used code is used once and an account is invited once, but "in practice" is
// doing a lot of work in a recursive query against a table anybody with an
// invite can add rows to. Five generations is far past what an operator reads.
const inviteTreeDepth = 5

// inviteTreeRow is one member in the chain.
type inviteTreeRow struct {
	Depth    int    `db:"depth"`
	Username string `db:"username"`
	Role     int    `db:"role"`
	Joined   string `db:"joined"`
	// Invited is how many people this member has gone on to invite. Shown
	// because it is the number that makes a tree worth reading: one account
	// that brought thirty is a different fact from thirty accounts that brought
	// one each.
	Invited int `db:"invited"`
	// Indent is Depth-1, so the template can range without arithmetic.
	Indent int `db:"-"`
}

// inviteTree returns everybody below one member, deepest chain intact.
//
// Ordered by an accumulated PATH rather than by depth or date. Ordering by
// depth prints every generation as a flat block and loses which parent each
// child belongs to, which is the one thing a tree is for.
func inviteTree(ctx context.Context, db *sqlx.DB, rootID int64) []inviteTreeRow {
	if db == nil || rootID <= 0 {
		return nil
	}
	var rows []inviteTreeRow
	if err := db.SelectContext(ctx, &rows, `
		WITH RECURSIVE tree AS (
		    SELECT ic.used_by       AS user_id,
		           1                AS depth,
		           ARRAY[ic.used_at] AS path
		      FROM invite_codes ic
		     WHERE ic.created_by = $1 AND ic.used_by IS NOT NULL
		    UNION ALL
		    SELECT ic.used_by,
		           t.depth + 1,
		           t.path || ic.used_at
		      FROM invite_codes ic
		      JOIN tree t ON ic.created_by = t.user_id
		     WHERE ic.used_by IS NOT NULL AND t.depth < $2
		)
		SELECT t.depth,
		       u.username,
		       u.role,
		       to_char(u.created_at, 'DD Mon YYYY') AS joined,
		       (SELECT count(*) FROM invite_codes c2
		         WHERE c2.created_by = u.id AND c2.used_by IS NOT NULL) AS invited
		  FROM tree t
		  JOIN users u ON u.id = t.user_id
		 ORDER BY t.path`, rootID, inviteTreeDepth); err != nil {
		// Logged, never swallowed. An empty tree and a failed query look
		// identical on the page, and "you have invited nobody" is a confident
		// answer to a question that was not asked.
		slog.Error("invite tree read", "user", rootID, "err", err)
		return nil
	}
	for i := range rows {
		rows[i].Indent = rows[i].Depth - 1
	}
	return rows
}

// inviteTreeTotals summarises a tree for the line above it.
type inviteTreeTotals struct {
	People      int
	Generations int
}

func summariseTree(rows []inviteTreeRow) inviteTreeTotals {
	t := inviteTreeTotals{People: len(rows)}
	for _, r := range rows {
		if r.Depth > t.Generations {
			t.Generations = r.Depth
		}
	}
	return t
}
