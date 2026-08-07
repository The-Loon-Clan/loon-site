package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

// Site invite CODES — the thing invite-only registration actually needs.
//
// invites_web.go added a BALANCE (users.invites) and said why it stopped there:
// "this host has no registration gate for a code to unlock." That was true, and
// adding the gate (access_web.go) is what makes codes necessary. The balance
// keeps its meaning and gains a use — it is now how many codes you may issue,
// spent when you issue one rather than at some later abstract moment.
//
// A code is single-use and expires. Both matter: a code that never expires is a
// permanent hole in a closed site, and a reusable one is a public registration
// link the moment somebody pastes it in a forum.
//
// Who invited whom is recorded and never deleted. On an invite-only site that
// chain is the only accountability there is — if an account turns out to be a
// problem, "who vouched for them" is the first question, and a schema that
// cannot answer it makes invite-only a formality.

var inviteCodesDB *sqlx.DB

// inviteCodeTTL is how long an unused code lives. Long enough to send someone a
// link and have them get round to it, short enough that a leaked code is not a
// standing invitation.
const inviteCodeTTL = 14 * 24 * time.Hour

// inviteCodesMigrate creates the table. Idempotent, like the other host
// migrations.
func inviteCodesMigrate(db *sqlx.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS invite_codes (
		    code        TEXT PRIMARY KEY,
		    created_by  BIGINT NOT NULL,
		    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		    expires_at  TIMESTAMPTZ NOT NULL,
		    -- Set on redemption and never cleared. The invite chain is the only
		    -- accountability an invite-only site has.
		    used_by     BIGINT,
		    used_at     TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS invite_codes_creator ON invite_codes (created_by, created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// newInviteCode returns a random, unambiguous code.
//
// Base32 without padding: it survives being read aloud, typed from a phone
// screen, and pasted through a chat client that helpfully lowercases things.
// crypto/rand, not math/rand — a guessable invite code on a closed site is the
// same as no gate at all.
func newInviteCode() (string, error) {
	b := make([]byte, 10) // 80 bits
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	return s[:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:], nil
}

// normaliseInviteCode makes matching forgiving about how a code was typed.
//
// ui-patterns calls this Forgiving Format: the code is the same code whether it
// arrived lowercased by a chat client, with the dashes stripped, or wrapped in
// whitespace by a copy-paste. Rejecting those is rejecting the right person for
// the wrong reason.
func normaliseInviteCode(s string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
}

// inviteCodeValid reports whether a code may be redeemed right now.
//
// A READ only. Redemption happens in redeemInviteCode, inside the same
// transaction that consumes it, because checking and using in two steps is how
// one code creates two accounts.
func inviteCodeValid(ctx context.Context, code string) bool {
	if inviteCodesDB == nil || code == "" {
		return false
	}
	var n int
	if err := inviteCodesDB.GetContext(ctx, &n, `
		SELECT count(*) FROM invite_codes
		 WHERE replace(upper(code), '-', '') = $1
		   AND used_by IS NULL AND expires_at > now()`, normaliseInviteCode(code)); err != nil {
		return false
	}
	return n > 0
}

// redeemInviteCode marks a code used by a new account, atomically.
//
// The UPDATE ... WHERE used_by IS NULL is the whole safety argument: two
// registrations racing on one code both run this, and exactly one updates a
// row. Checking first and updating second would let both through.
func redeemInviteCode(ctx context.Context, code string, userID int64) bool {
	if inviteCodesDB == nil || code == "" || userID <= 0 {
		return false
	}
	res, err := inviteCodesDB.ExecContext(ctx, `
		UPDATE invite_codes SET used_by = $1, used_at = now()
		 WHERE replace(upper(code), '-', '') = $2
		   AND used_by IS NULL AND expires_at > now()`, userID, normaliseInviteCode(code))
	if err != nil {
		return false
	}
	n, err := res.RowsAffected()
	return err == nil && n == 1
}

// inviteCodeRow is one code as its owner sees it.
type inviteCodeRow struct {
	Code    string `db:"code"`
	Created string `db:"created"`
	Expires string `db:"expires"`
	UsedBy  string `db:"used_by_name"`
	Spent   bool   `db:"spent"`
	Expired bool   `db:"expired"`
}

// listInviteCodes returns the codes a member has issued, newest first.
func listInviteCodes(ctx context.Context, userID int64) []inviteCodeRow {
	if inviteCodesDB == nil || userID <= 0 {
		return nil
	}
	var rows []inviteCodeRow
	if err := inviteCodesDB.SelectContext(ctx, &rows, `
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

// invitesPage serves GET /invites.
func (w *web) invitesPage(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	balance, _ := inviteBalance(c.Request.Context(), u.ID)
	w.render(c, "invites.html", map[string]any{
		"Title":   "Invites",
		"Balance": balance,
		"Codes":   listInviteCodes(c.Request.Context(), u.ID),
		// Said plainly, because a member holding invites on an OPEN site is
		// reasonably confused about what they are for.
		"RegMode": registrationMode(),
		"Err":     c.Query("err"),
	})
}

// invitesCreate serves POST /invites — spend one balance, get one code.
func (w *web) invitesCreate(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()
	code, err := newInviteCode()
	if err != nil {
		w.log.Error("generate invite code", "err", err)
		c.Redirect(http.StatusFound, "/invites?err=could+not+create+a+code")
		return
	}
	// Spend the balance and mint the code in ONE transaction. Decrementing
	// outside it loses an invite when the insert fails; inserting outside it
	// mints a code the member never paid for.
	tx, err := inviteCodesDB.BeginTxx(ctx, nil)
	if err != nil {
		c.Redirect(http.StatusFound, "/invites?err=could+not+create+a+code")
		return
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	// The WHERE invites > 0 is the check: two clicks racing cannot both take
	// the last invite, because the second updates no rows.
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET invites = invites - 1 WHERE id = $1 AND invites > 0`, u.ID)
	if err != nil {
		c.Redirect(http.StatusFound, "/invites?err=could+not+create+a+code")
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		c.Redirect(http.StatusFound, "/invites?err=you+have+no+invites+left")
		return
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO invite_codes (code, created_by, expires_at) VALUES ($1, $2, now() + $3::interval)`,
		code, u.ID, inviteCodeTTL.String()); err != nil {
		w.log.Error("insert invite code", "err", err)
		c.Redirect(http.StatusFound, "/invites?err=could+not+create+a+code")
		return
	}
	if err := tx.Commit(); err != nil {
		w.log.Error("commit invite code", "err", err)
		c.Redirect(http.StatusFound, "/invites?err=could+not+create+a+code")
		return
	}
	c.Redirect(http.StatusFound, "/invites")
}
