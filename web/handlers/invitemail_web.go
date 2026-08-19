package handlers

import (
	"context"
	"strings"
)

// The invite email.
//
// Sent through the same seam the password-reset and verification mail already
// uses (w.mail), which on this demo logs the message with the link in it and on
// a real host is SMTP. One seam, so a site that configures mail once gets every
// message this site sends rather than discovering that invites went somewhere
// else.
//
// BEST-EFFORT BY CONSTRUCTION. Nothing here can fail the issuing of an invite:
// the invite already exists by the time this runs, sent_at is only stamped when
// the send returns cleanly, and a failure is logged rather than surfaced as an
// error the member has to interpret. The recovery is the same in every case and
// the page already offers it — copy the link and send it yourself.

// sendInviteEmail delivers one invitation and records that it went.
func (w *web) sendInviteEmail(ctx context.Context, from, to, code, message string) {
	if w.mail == nil {
		// No mailer wired. Logged rather than silent: "my invite never arrived"
		// is otherwise indistinguishable from a bug, and this is the one state
		// where the answer is a site the operator has not finished configuring.
		w.log.Warn("invite email not sent — no mailer configured", "to", to)
		return
	}
	subject, body := inviteEmail(w.siteName(), w.baseURL(), from, code, message)
	if err := w.mail(to, subject, body); err != nil {
		w.log.Error("invite email", "to", to, "err", err)
		return
	}
	// Stamped only on success, so a NULL sent_at means exactly one thing: this
	// invite has never been emailed, and somebody may need to pass it on by
	// hand.
	w.data.MarkInviteSent(ctx, code)
	w.log.Info("invite email sent", "to", to)
}

// inviteEmail composes the message.
//
// Plain text, and short. It is read by somebody who was not expecting it, on a
// phone, deciding in about four seconds whether it is a scam — so it says who
// invited them and what the site is before it says anything else, and the link
// is on its own line where it can be tapped or copied without selecting half a
// paragraph.
//
// The issuer's own note goes ABOVE the mechanics, quoted, because it is the
// only part the recipient has any reason to trust: it is the bit that sounds
// like the person they know.
func inviteEmail(site, baseURL, from, code, message string) (subject, body string) {
	base := strings.TrimRight(baseURL, "/")
	subject = from + " invited you to " + site

	var b strings.Builder
	b.WriteString(from + " has invited you to join " + site + ".\n\n")
	if message != "" {
		// Indented rather than quoted with angle brackets: this is a note from
		// a person, not a reply, and > makes it look like a forwarded thread.
		for _, line := range strings.Split(message, "\n") {
			b.WriteString("    " + line + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("To accept, sign up here:\n")
	b.WriteString("    " + base + "/register?invite=" + code + "\n\n")
	b.WriteString("Or enter this invite code on the sign-up page:\n")
	b.WriteString("    " + code + "\n\n")
	// Said plainly, because it is the fact that decides whether they act now
	// or lose the invite. The exact expiry is on the site; a date in an email
	// that a delayed send makes wrong is worse than the shape of the rule.
	b.WriteString("The invite is for this address, and it expires — if it stops working,\n")
	b.WriteString("ask " + from + " for another.\n\n")
	b.WriteString("If you were not expecting this, you can ignore it. Nothing happens\n")
	b.WriteString("until somebody signs up with the code.\n")
	return subject, b.String()
}
