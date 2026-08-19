package handlers

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

// The staff view of the invite chain: /admin/invites.
//
// /invites already shows a member the chain BELOW them, and has since codes
// landed. This is the half that was missing, and it is a different question
// asked of the same two columns:
//
//	who vouched for this account?
//
// invite_codes says outright why it records created_by and used_by — "if an
// account turns out to be a problem, 'who vouched for them' is the first
// question, and a schema that cannot answer it makes invite-only a formality"
// — and until this page the chain could only be walked DOWNWARD, by the one
// person who cannot be asked about it impartially: the recruiter.
//
// TWO SURFACES, and the overview is the load-bearing one. A lookup box alone
// requires already suspecting somebody, and the accounts worth looking at are
// exactly the ones nobody has thought to type in. So the page opens on the
// recruiters ranked by how many accounts sit beneath them, and the lookup is
// for when you arrive with a name.

// recruiterBoard is how many recruiters the overview lists.
const recruiterBoard = 25

// adminInvites serves GET /admin/invites.
func (w *web) adminInvites(c *gin.Context) {
	ctx := c.Request.Context()
	data := map[string]any{
		"Title": "Invite chain",
		// The rules in force, so the form below describes the site rather than
		// its defaults.
		"Opt":        currentInviteOptions(),
		"TTLChoices": inviteTTLChoices,
		"Recruiters": w.data.TopRecruiters(ctx, recruiterBoard),
		"Query":      "",
		"Upline":     []storage.InviteUplineRow{},
		"Tree":       []storage.InviteTreeRow{},
		"MaxDepth":   storage.InviteTreeDepth,
	}

	name := strings.TrimSpace(c.Query("user"))
	if name == "" {
		w.render(c, "admin_invites.html", data)
		return
	}
	data["Query"] = name

	subject, err := w.store.ByUsername(ctx, name)
	if err != nil || subject == nil {
		// A name nobody holds is a typo, not an error page: the operator is
		// standing at a search box and the useful answer is "no such member",
		// on the page they are already looking at.
		data["NotFound"] = true
		w.render(c, "admin_invites.html", data)
		return
	}

	up := w.data.InviteUpline(ctx, subject.ID)
	tree := w.data.InviteTree(ctx, subject.ID)
	data["Subject"] = subject
	// What they currently have OUT. The chain above is who they already
	// brought; this is who they are about to, which is the half staff can still
	// do something about — and the only place a withdraw button can live.
	data["Issued"] = w.data.ListInviteCodes(ctx, subject.ID)
	data["Upline"] = up
	data["Tree"] = tree
	data["Totals"] = summariseTree(tree)
	// Stated as its own flag rather than left to {{if not .Upline}}: an empty
	// chain is a real and common answer — a founder, an account from before
	// invite-only, a seeded user — and it must not read as though the lookup
	// failed. See InviteUpline on why this is not filled in with a guess.
	data["NoUpline"] = len(up) == 0
	w.render(c, "admin_invites.html", data)
}

// adminInvitesSave serves POST /admin/invites/options.
//
// Every field is read from the form and written, including the unchanged ones
// — see saveInviteOptions on why a partial write is the thing to avoid. Absent
// checkboxes are FALSE rather than unset, which is what an unchecked box means
// and the reason each one is read with a default rather than looked up.
func (w *web) adminInvitesSave(c *gin.Context) {
	ttl, err := strconv.Atoi(c.PostForm("ttl_hours"))
	if err != nil {
		ttl = defaultInviteTTLHours
	}
	maxPending, err := strconv.Atoi(c.PostForm("max_pending"))
	if err != nil || maxPending < 0 {
		maxPending = 0
	}
	opt := inviteOptions{
		TTLHours:      ttl,
		MaxPending:    maxPending,
		EmailRequired: c.PostForm("email_required") == "1",
		EmailStrict:   c.PostForm("email_strict") == "1",
		SendEmail:     c.PostForm("send_email") == "1",
		MemberRevoke:  c.PostForm("member_revoke") == "1",
		RefundRevoked: c.PostForm("refund_revoked") == "1",
		PublicStats:   c.PostForm("public_stats") == "1",
	}
	if err := saveInviteOptions(c.Request.Context(), opt); err != nil {
		w.log.Error("save invite options", "err", err)
		c.Redirect(http.StatusFound, "/admin/invites?err=save-failed")
		return
	}
	w.log.Info("invite options saved", "ttl_hours", opt.TTLHours, "email_required", opt.EmailRequired)
	c.Redirect(http.StatusFound, "/admin/invites")
}

// adminInvitesRevoke serves POST /admin/invites/revoke — staff cancelling
// somebody else's pending invite.
//
// The same store call the member path uses, with the override flag set, so
// there is one statement that decides what "revoke" means. No refund: the
// member did not ask for this, and handing an invite back to somebody whose
// recruiting staff just intervened in would be the wrong default. An operator
// who wants to give it back can, from the members page.
func (w *web) adminInvitesRevoke(c *gin.Context) {
	u, ok := w.viewer(c)
	if !ok {
		return
	}
	code := c.PostForm("code")
	back := "/admin/invites"
	if n := strings.TrimSpace(c.PostForm("user")); n != "" {
		back += "?user=" + url.QueryEscape(n)
	}
	if !w.data.RevokeInviteCode(c.Request.Context(), code, u.ID, true) {
		c.Redirect(http.StatusFound, back)
		return
	}
	w.log.Info("invite revoked by staff", "by", u.ID)
	c.Redirect(http.StatusFound, back)
}
