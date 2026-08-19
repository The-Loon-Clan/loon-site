package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

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

// How long an unused code lives is an OPERATOR setting now — see
// inviteoptions_web.go. Long enough to send someone a link and have them get
// round to it, short enough that a leaked code is not a standing invitation,
// and where exactly that lands differs between a site with a queue of eager
// applicants and one inviting people who check their email on Sundays.

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
		// What this site allows, so the form and the row controls describe the
		// rules actually in force rather than the ones that were true when the
		// markup was written.
		"Opt": currentInviteOptions(),
	})
}

// Why an invite could not be issued, as a KEY rather than a sentence.
//
// The words live in invites.html. Two reasons and the second is the one that
// matters: a message built here cannot be translated, because the i18n
// catalogue reads templates — so every sentence in Go is a sentence that is
// English forever. The first is that the audit says so, which is the same
// point written down.
const (
	inviteErrNoEmail    = "no-email"
	inviteErrBadEmail   = "bad-email"
	inviteErrTaken      = "taken"
	inviteErrLongNote   = "long-note"
	inviteErrTooMany    = "too-many"
	inviteErrNoneLeft   = "none-left"
	inviteErrMintFailed = "mint-failed"
	inviteErrNoRevoke   = "revoke-off"
	inviteErrRevoke     = "revoke-failed"
	inviteErrRemove     = "remove-failed"
)

// invitesCreate serves POST /invites — spend one balance, issue one invite.
func (w *web) invitesCreate(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	opt := currentInviteOptions()
	email := storage.NormaliseEmail(c.PostForm("email"))
	message := strings.TrimSpace(c.PostForm("message"))

	fail := func(msg string) {
		c.Redirect(http.StatusFound, "/invites?err="+url.QueryEscape(msg))
	}

	// The address, checked before anything is spent. Every refusal below
	// happens before MintInviteCode so a rejected form never costs an invite.
	if opt.EmailRequired {
		if email == "" {
			fail(inviteErrNoEmail)
			return
		}
		if !looksLikeEmail(email) {
			fail(inviteErrBadEmail)
			return
		}
		// Two reasons an address cannot be invited, answered with ONE message.
		//
		// It already has an account, or it already has a live invite. Both
		// waste the member's invite on somebody who cannot use it, so both are
		// refused — and neither is named, because the check runs BEFORE
		// anything is spent and a distinguishing error would be a free oracle
		// for "does this address have an account here". Wasting an invite is
		// the worse harm; leaking which of the two it is buys the asker
		// something and costs them nothing.
		if w.data.InviteEmailPending(ctx, email) || w.emailHasAccount(ctx, email) {
			fail(inviteErrTaken)
			return
		}
	}
	if len(message) > inviteMessageMax {
		fail(inviteErrLongNote)
		return
	}
	// The per-member cap. Zero means no cap — see loadInviteSettings on why
	// zero is stored as itself rather than treated as unset.
	if opt.MaxPending > 0 && w.data.PendingInviteCount(ctx, u.ID) >= opt.MaxPending {
		fail(inviteErrTooMany)
		return
	}

	code, err := newInviteCode()
	if err != nil {
		w.log.Error("generate invite code", "err", err)
		fail(inviteErrMintFailed)
		return
	}
	// Spending the balance and minting the code is one transaction, and the
	// race guard is inside it — see MintInviteCode. A false with no error is
	// "you have none left", which is a refusal rather than a failure.
	minted, err := w.data.MintInviteCode(ctx, u.ID, code, inviteTTL().String(), email, message)
	if err != nil {
		w.log.Error("mint invite code", "user", u.ID, "err", err)
		fail(inviteErrMintFailed)
		return
	}
	if !minted {
		fail(inviteErrNoneLeft)
		return
	}

	// Sending is best-effort and deliberately AFTER the invite exists. A send
	// that fails leaves a perfectly good invite the issuer can still pass on by
	// hand, and sent_at stays NULL so both they and staff can see it never
	// went. Minting after a successful send would be the other order and the
	// wrong one: it makes the mail server a dependency of issuing an invite.
	if opt.SendEmail && email != "" {
		w.sendInviteEmail(ctx, u.Username, email, code, message)
	}
	c.Redirect(http.StatusFound, "/invites")
}

// inviteMessageMax bounds the note to the recipient. Generous enough for a
// real greeting, bounded because it goes in an email this site sends.
const inviteMessageMax = 500

// invitesRevoke serves POST /invites/revoke — cancel a pending invite.
func (w *web) invitesRevoke(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	if !currentInviteOptions().MemberRevoke {
		c.Redirect(http.StatusFound, "/invites?err="+inviteErrNoRevoke)
		return
	}
	ctx := c.Request.Context()
	code := c.PostForm("code")
	// Ownership is checked inside the statement, not here — see
	// RevokeInviteCode. A false is "nothing to cancel", which covers somebody
	// else's code, an already-claimed one, and a double-click alike.
	if !w.data.RevokeInviteCode(ctx, code, u.ID, false) {
		c.Redirect(http.StatusFound, "/invites?err="+inviteErrRevoke)
		return
	}
	// The refund is a setting, so it is decided here rather than in the store.
	// Only ever after a revoke that actually happened, which is what stops a
	// double-click minting invites out of nothing.
	if currentInviteOptions().RefundRevoked {
		w.data.RefundInvite(ctx, u.ID)
	}
	w.log.Info("invite revoked", "user", u.ID)
	c.Redirect(http.StatusFound, "/invites")
}

// invitesDelete serves POST /invites/delete — hide a dead invite from the list.
//
// A SOFT delete, and only ever that: the invite chain is the site's
// accountability record and a member who could erase who they vouched for
// would be curating it. This hides a row from their own page and changes
// nothing else.
func (w *web) invitesDelete(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	if !w.data.SoftDeleteInviteCode(c.Request.Context(), c.PostForm("code"), u.ID) {
		c.Redirect(http.StatusFound, "/invites?err="+inviteErrRemove)
		return
	}
	c.Redirect(http.StatusFound, "/invites")
}

// looksLikeEmail is a shape check, not a validation.
//
// Deliberately loose: the only authority on whether an address exists is the
// mail server, and every regex that tries to be stricter than this rejects
// somebody's real address. It catches the typo that matters — no @, nothing
// before or after it, no dot in the domain — and lets the send decide the rest.
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 || strings.Count(s, "@") != 1 {
		return false
	}
	domain := s[at+1:]
	dot := strings.IndexByte(domain, '.')
	return dot > 0 && dot < len(domain)-1 && !strings.ContainsAny(s, " 	")
}

// emailHasAccount reports whether an address is already a member here.
//
// Errors count as "no". A lookup that failed must not block a member from
// spending an invite they own — the cost of being wrong in that direction is
// one duplicate invite, and in the other it is a feature that stops working
// whenever the database hiccups.
func (w *web) emailHasAccount(ctx context.Context, email string) bool {
	if w.store == nil || email == "" {
		return false
	}
	u, err := w.store.ByEmail(ctx, email)
	return err == nil && u != nil
}
