package handlers

import (
	"crypto/rand"
	"encoding/base32"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/the-loon-clan/loon-site/internal/storage"
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

// invitesPage serves GET /invites.
func (w *web) invitesPage(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	balance, _ := inviteBalance(c.Request.Context(), u.ID)
	tree := storage.InviteTree(c.Request.Context(), usersDB, u.ID)
	w.render(c, "invites.html", map[string]any{
		"Title":   "Invites",
		"Balance": balance,
		"Codes":   storage.ListInviteCodes(c.Request.Context(), u.ID),
		// Said plainly, because a member holding invites on an OPEN site is
		// reasonably confused about what they are for.
		"RegMode": registrationMode(),
		"Err":     c.Query("err"),
		// The chain below this member (invitetree_web.go). Read from the same
		// created_by/used_by columns the codes already carry.
		"Tree":   tree,
		"Totals": summariseTree(tree),
	})
}

// invitesCreate serves POST /invites — spend one balance, get one code.
func (w *web) invitesCreate(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
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
	tx, err := storage.InviteCodesDB.BeginTxx(ctx, nil)
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
