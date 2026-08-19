package handlers

import (
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
		"Title":      "Invite chain",
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
