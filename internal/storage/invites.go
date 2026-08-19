package storage

import (
	"context"
	"log/slog"
	"strings"
)

// InviteCodeValid reports whether a code may be redeemed right now.
//
// A READ only. Redemption happens in RedeemInviteCode, inside the same
// transaction that consumes it, because checking and using in two steps is how
// one code creates two accounts.
func (st *Store) InviteCodeValid(ctx context.Context, code string) bool {
	if code == "" {
		return false
	}
	var n int
	if err := st.db.GetContext(ctx, &n, `
		SELECT count(*) FROM invite_codes
		 WHERE replace(upper(code), '-', '') = $1
		   AND used_by IS NULL AND expires_at > now()`, st.NormaliseInviteCode(code)); err != nil {
		return false
	}
	return n > 0
}

// RedeemInviteCode marks a code used by a new account, atomically.
//
// The UPDATE ... WHERE used_by IS NULL is the whole safety argument: two
// registrations racing on one code both run this, and exactly one updates a
// row. Checking first and updating second would let both through.
func (st *Store) RedeemInviteCode(ctx context.Context, code string, userID int64) bool {
	if code == "" || userID <= 0 {
		return false
	}
	res, err := st.db.ExecContext(ctx, `
		UPDATE invite_codes SET used_by = $1, used_at = now()
		 WHERE replace(upper(code), '-', '') = $2
		   AND used_by IS NULL AND expires_at > now()`, userID, st.NormaliseInviteCode(code))
	if err != nil {
		return false
	}
	n, err := res.RowsAffected()
	return err == nil && n == 1
}

// ListInviteCodes returns the codes a member has issued, newest first.
func (st *Store) ListInviteCodes(ctx context.Context, userID int64) []InviteCodeRow {
	if userID <= 0 {
		return nil
	}
	var rows []InviteCodeRow
	if err := st.db.SelectContext(ctx, &rows, `
		SELECT i.code,
		       to_char(i.created_at, 'DD Mon YYYY')      AS created,
		       to_char(i.expires_at, 'DD Mon YYYY')      AS expires,
		       COALESCE(u.username, '')                  AS used_by_name,
		       (i.used_by IS NOT NULL)                   AS spent,
		       (i.used_by IS NULL AND i.expires_at <= now()) AS expired
		  FROM invite_codes i
		  LEFT JOIN users u ON u.id = i.used_by
		 WHERE i.created_by = $1
		 ORDER BY i.created_at DESC
		 LIMIT 50`, userID); err != nil {
		slog.Error("invite codes read", "err", err)
		return nil
	}
	return rows
}

// InviteCodeRow is one code as its owner sees it.
type InviteCodeRow struct {
	Code    string `db:"code"`
	Created string `db:"created"`
	Expires string `db:"expires"`
	UsedBy  string `db:"used_by_name"`
	Spent   bool   `db:"spent"`
	Expired bool   `db:"expired"`
}

// NormaliseInviteCode makes matching forgiving about how a code was typed.
//
// ui-patterns calls this Forgiving Format: the code is the same code whether it
// arrived lowercased by a chat client, with the dashes stripped, or wrapped in
// whitespace by a copy-paste. Rejecting those is rejecting the right person for
// the wrong reason.
func (st *Store) NormaliseInviteCode(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
}

// InviteTree returns everybody below one member, deepest chain intact.
//
// Ordered by an accumulated PATH rather than by depth or date. Ordering by
// depth prints every generation as a flat block and loses which parent each
// child belongs to, which is the one thing a tree is for.
func (st *Store) InviteTree(ctx context.Context, rootID int64) []InviteTreeRow {
	if rootID <= 0 {
		return nil
	}
	var rows []InviteTreeRow
	if err := st.db.SelectContext(ctx, &rows, `
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
		 ORDER BY t.path`, rootID, InviteTreeDepth); err != nil {
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

// InviteTreeRow is one member in the chain.
type InviteTreeRow struct {
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

// InviteTreeTotals summarises a tree for the line above it.
type InviteTreeTotals struct {
	People      int
	Generations int
}

// InviteTreeDepth caps the recursion.
//
// A cap rather than trust: an invite chain cannot cycle in practice, because a
// used code is used once and an account is invited once, but "in practice" is
// doing a lot of work in a recursive query against a table anybody with an
// invite can add rows to. Five generations is far past what an operator reads.
const InviteTreeDepth = 5

// MintInviteCode spends one invite and creates a code, atomically.
//
// Returns (false, nil) when the member has none left — a refusal, not a
// failure, and the caller says so differently.
//
// Both statements are in ONE transaction because either alone is wrong:
// decrementing outside it loses an invite when the insert fails, and inserting
// outside it mints a code nobody paid for. The `WHERE invites > 0` is the check
// itself — two clicks racing cannot both take the last invite, because the
// second updates no rows, which is why the row count is examined rather than
// the balance being read first.
func (st *Store) MintInviteCode(ctx context.Context, userID int64, code, ttl string) (bool, error) {
	tx, err := st.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET invites = invites - 1 WHERE id = $1 AND invites > 0`, userID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil // no invites left
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO invite_codes (code, created_by, expires_at) VALUES ($1, $2, now() + $3::interval)`,
		code, userID, ttl); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ── Reading the chain the other way, and from the outside ───────────────────
//
// InviteTree above walks DOWN from a member: who they brought, and who those
// people brought. It is the member's own view, and /invites has rendered it
// since codes landed.
//
// What follows is the half a moderator needs, and it is a different question
// asked of the same two columns. The schema comment on invite_codes states the
// purpose outright — "if an account turns out to be a problem, 'who vouched for
// them' is the first question, and a schema that cannot answer it makes
// invite-only a formality" — and until now nothing asked it. The chain could
// only be walked downward, by the one person who could not be asked about it
// impartially: the recruiter themselves.

// InviteUplineRow is one member in the chain that vouched for somebody.
type InviteUplineRow struct {
	// Distance is 1 for the direct inviter, 2 for whoever invited them, and so
	// on — how many degrees of vouching separate them from the account in
	// question.
	Distance int    `db:"distance"`
	Username string `db:"username"`
	Role     int    `db:"role"`
	Joined   string `db:"joined"`
	// Invited is how many that member has brought in total. A recruiter with
	// one bad account is unlucky; one with forty is the actual problem, and
	// this is the number that tells them apart.
	Invited int `db:"invited"`
}

// InviteUpline returns who vouched for a member, nearest first.
//
// The mirror of InviteTree — the same recursion over the same two columns with
// created_by and used_by swapped — and the same depth cap for the same reason:
// the invariant that an account is invited once is an invariant of the SITE,
// not of the table, and a recursive query should not stake a database
// connection on a uniqueness nothing enforces.
//
// An empty result is the ordinary answer for a founder, for anyone who joined
// while registration was open, and for a seeded account. That is not a gap to
// fill with a guess: "nobody vouched for them" and "we do not know" are the
// same fact here, and the page says the honest version of it.
func (st *Store) InviteUpline(ctx context.Context, userID int64) []InviteUplineRow {
	if userID <= 0 {
		return nil
	}
	var rows []InviteUplineRow
	if err := st.db.SelectContext(ctx, &rows, `
		WITH RECURSIVE up AS (
		    SELECT ic.created_by AS user_id, 1 AS distance
		      FROM invite_codes ic
		     WHERE ic.used_by = $1
		    UNION ALL
		    SELECT ic.created_by, u.distance + 1
		      FROM invite_codes ic
		      JOIN up u ON ic.used_by = u.user_id
		     WHERE u.distance < $2
		)
		SELECT up.distance,
		       usr.username,
		       usr.role,
		       to_char(usr.created_at, 'DD Mon YYYY') AS joined,
		       (SELECT count(*) FROM invite_codes c2
		         WHERE c2.created_by = usr.id AND c2.used_by IS NOT NULL) AS invited
		  FROM up
		  JOIN users usr ON usr.id = up.user_id
		 ORDER BY up.distance`, userID, InviteTreeDepth); err != nil {
		// Logged, never swallowed — for the reason InviteTree gives: an empty
		// chain and a failed query look identical on the page, and "nobody
		// vouched for them" is a confident answer to a question that was never
		// asked.
		slog.Error("invite upline read", "user", userID, "err", err)
		return nil
	}
	return rows
}

// RecruiterRow is one member on the recruiters overview.
type RecruiterRow struct {
	UserID   int64  `db:"user_id"`
	Username string `db:"username"`
	Role     int    `db:"role"`
	// Direct is how many accounts they personally brought in. Descendants is
	// the whole subtree beneath them, which is the number that matters: a
	// recruiter who brought three people who each brought ten is responsible
	// for thirty-three accounts, and a page ranking on Direct alone would list
	// them below somebody who brought four and stopped.
	Direct      int    `db:"direct"`
	Descendants int    `db:"descendants"`
	LastInvited string `db:"last_invited"`
}

// TopRecruiters ranks members by the size of the chain beneath them.
//
// The ENTRY POINT for the staff page, and it exists because the obvious design
// does not work: a lookup box alone requires already suspecting somebody, and
// the accounts worth looking at are precisely the ones nobody has thought to
// type in. Ranking by subtree size surfaces them without being asked.
func (st *Store) TopRecruiters(ctx context.Context, limit int) []RecruiterRow {
	if limit <= 0 || limit > 200 {
		limit = 25
	}
	var rows []RecruiterRow
	// One recursive pass over the whole table, then a group-by: the subtree
	// size for every recruiter at once. The alternative — a per-recruiter
	// recursive subquery — re-walks the same edges once per row.
	if err := st.db.SelectContext(ctx, &rows, `
		WITH RECURSIVE edges AS (
		    SELECT created_by AS root, used_by AS descendant, 1 AS depth
		      FROM invite_codes
		     WHERE used_by IS NOT NULL
		    UNION ALL
		    SELECT e.root, ic.used_by, e.depth + 1
		      FROM invite_codes ic
		      JOIN edges e ON ic.created_by = e.descendant
		     WHERE ic.used_by IS NOT NULL AND e.depth < 5
		)
		SELECT u.id AS user_id, u.username, u.role,
		       -- DISTINCT on both, and the join to invite_codes is a SUBQUERY
		       -- rather than a LEFT JOIN. Joining it here multiplies every edge
		       -- row by the recruiter's code count: alice with two codes and two
		       -- direct invites reported four. Descendants survived only because
		       -- it already counted DISTINCT, which is what made the bug look
		       -- like a plausible number instead of an obvious one.
		       count(DISTINCT e.descendant) FILTER (WHERE e.depth = 1) AS direct,
		       count(DISTINCT e.descendant)                            AS descendants,
		       COALESCE((SELECT to_char(max(ic.used_at), 'DD Mon YYYY')
		                   FROM invite_codes ic
		                  WHERE ic.created_by = u.id AND ic.used_by IS NOT NULL), '') AS last_invited
		  FROM edges e
		  JOIN users u ON u.id = e.root
		 GROUP BY u.id, u.username, u.role
		 ORDER BY descendants DESC, direct DESC, u.username
		 LIMIT $1`, limit); err != nil {
		slog.Error("top recruiters read", "err", err)
		return nil
	}
	return rows
}
