package handlers

import (
	"crypto/rand"
	"encoding/base32"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
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
	balance, _ := w.inviteBalance(c.Request.Context(), u.ID)
	tree := w.data.InviteTree(c.Request.Context(), u.ID)
	w.render(c, "invites.html", map[string]any{
		"Title":   "Invites",
		"Balance": balance,
		"Codes":   w.data.ListInviteCodes(c.Request.Context(), u.ID),
		// Said plainly, because a member holding invites on an OPEN site is
		// reasonably confused about what they are for.
		"RegMode": registrationMode(),
		"Err":     c.Query(queryErr),
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
	// Spending the balance and minting the code is one transaction, and the
	// race guard is inside it — see MintInviteCode. A false with no error is
	// "you have none left", which is a refusal rather than a failure.
	ok2, err := w.data.MintInviteCode(ctx, u.ID, code, inviteCodeTTL.String())
	if err != nil {
		w.log.Error("mint invite code", "user", u.ID, "err", err)
		c.Redirect(http.StatusFound, "/invites?err=could+not+create+a+code")
		return
	}
	if !ok2 {
		c.Redirect(http.StatusFound, "/invites?err=you+have+no+invites+left")
		return
	}
	c.Redirect(http.StatusFound, "/invites")
}
