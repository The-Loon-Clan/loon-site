package handlers

import (
	"context"
	"fmt"

	"github.com/the-loon-clan/loon-plugins/pluginapi"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

// The host's side of pluginapi.InviteIssuer: a plugin decided somebody may
// join, and this is the door opening.
//
// It reuses the whole member path deliberately — the same mint, the same
// expiry window the operator configured, the same email, the same sent_at
// stamp. A separate "staff invite" path would be a second set of rules to keep
// in step, and the first thing to drift would be the one nobody notices: the
// validity window, silently different for people admitted through an
// application than for people invited by a friend.

type inviteIssuer struct{ w *web }

var _ pluginapi.InviteIssuer = inviteIssuer{}

// IssueInvite mints an invite for the address and sends it.
func (i inviteIssuer) IssueInvite(ctx context.Context, req pluginapi.InviteRequest) (pluginapi.IssuedInvite, error) {
	w := i.w
	email := storage.NormaliseEmail(req.Email)
	if email == "" {
		return pluginapi.IssuedInvite{}, fmt.Errorf("issue invite: no email address")
	}
	code, err := newInviteCode()
	if err != nil {
		return pluginapi.IssuedInvite{}, fmt.Errorf("issue invite: %w", err)
	}

	if req.ChargeBalance && req.IssuedBy > 0 {
		// The member's own allowance, spent through the same transaction a
		// member's own invite goes through — so a staff member with none left
		// is refused rather than quietly overdrawn.
		ok, err := w.data.MintInviteCode(ctx, req.IssuedBy, code, inviteTTL().String(), email, req.Note)
		if err != nil {
			return pluginapi.IssuedInvite{}, fmt.Errorf("issue invite: %w", err)
		}
		if !ok {
			return pluginapi.IssuedInvite{}, fmt.Errorf("issue invite: %d has no invites left", req.IssuedBy)
		}
	} else {
		// Not charged to anybody. The invite still records who approved it, so
		// the chain answers "who vouched for them" with a person — see
		// InviteRequest.IssuedBy on why that matters more than the accounting.
		if err := w.data.MintInviteUncharged(ctx, req.IssuedBy, code, inviteTTL().String(), email, req.Note); err != nil {
			return pluginapi.IssuedInvite{}, fmt.Errorf("issue invite: %w", err)
		}
	}

	out := pluginapi.IssuedInvite{Code: code}
	if currentInviteOptions().SendEmail {
		from := w.siteName()
		if req.IssuedBy > 0 {
			if u, err := w.store.ByID(ctx, req.IssuedBy); err == nil && u != nil {
				from = u.Username
			}
		}
		// Best-effort, and the result is REPORTED rather than swallowed: an
		// approval queue that told somebody they were accepted needs to know
		// whether the mail went, because if it did not, a human has to.
		out.Sent = w.sendInviteEmailNow(ctx, from, email, code, req.Note)
	}
	return out, nil
}

// sendInviteEmailNow is sendInviteEmail with the outcome returned.
func (w *web) sendInviteEmailNow(ctx context.Context, from, to, code, message string) bool {
	if w.mail == nil {
		w.log.Warn("invite email not sent — no mailer configured", "to", to)
		return false
	}
	subject, body := inviteEmail(w.siteName(), w.baseURL(), from, code, message)
	if err := w.mail(to, subject, body); err != nil {
		w.log.Error("invite email", "to", to, "err", err)
		return false
	}
	w.data.MarkInviteSent(ctx, code)
	return true
}
