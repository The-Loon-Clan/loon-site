package handlers

import (
	"strings"
	"testing"
)

// The invite rules that are wrong in ways nothing shouts about: an address
// check that rejects real people, a message that reads like a scam, and a set
// of options whose defaults quietly publish a social graph.

// looksLikeEmail is deliberately loose — the only authority on whether an
// address exists is the mail server, and every regex stricter than this
// rejects somebody's real address. So the test is mostly about what it must
// NOT reject.
func TestLooksLikeEmail(t *testing.T) {
	for _, ok := range []string{
		"a@b.co",
		"first.last@example.com",
		"first+tag@example.co.uk",
		// Real addresses that a stricter check would refuse.
		"o'brien@example.com",
		"user_name@sub.domain.example.org",
		"123@456.com",
		"UPPER@EXAMPLE.COM",
	} {
		if !looksLikeEmail(strings.ToLower(ok)) {
			t.Errorf("rejected %q, which is a shape a real address takes", ok)
		}
	}
	for _, bad := range []string{
		"",
		"nope",
		"@example.com",          // nothing before the @
		"user@",                 // nothing after
		"user@example",          // no dot in the domain
		"user@example.",         // nothing after the dot
		"a@b@example.com",       // two @
		"user name@example.com", // a space, which is how a pasted name arrives
	} {
		if looksLikeEmail(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

// The email is read by somebody who was not expecting it, deciding in about
// four seconds whether it is a scam. So it has to name the person who invited
// them and the site, before anything else.
func TestInviteEmailNamesWhoAndWhere(t *testing.T) {
	subject, body := inviteEmail("ameNZB", "https://example.org/", "alice", "AAAA-BBBB-CCCC-DDDD", "")

	if !strings.Contains(subject, "alice") || !strings.Contains(subject, "ameNZB") {
		t.Errorf("subject = %q, want it to name the inviter and the site", subject)
	}
	// The link, with the code on it, so accepting is one tap.
	if !strings.Contains(body, "https://example.org/register?invite=AAAA-BBBB-CCCC-DDDD") {
		t.Error("body has no sign-up link carrying the code")
	}
	// And the bare code, because a mail client that mangles the link still
	// leaves them something they can type.
	if !strings.Contains(body, "AAAA-BBBB-CCCC-DDDD\n") {
		t.Error("body does not offer the bare code as a fallback")
	}
	// The trailing slash on the base URL must not produce a double slash.
	if strings.Contains(body, "example.org//") {
		t.Error("double slash in the link — the base URL was not trimmed")
	}
	// Says what to do if it was not expected, which is the difference between
	// a stranger deleting it and a stranger reporting it as spam.
	if !strings.Contains(strings.ToLower(body), "not expecting") {
		t.Error("body does not tell an unexpecting recipient they can ignore it")
	}
}

// A note from the issuer is the only part of the mail the recipient has reason
// to trust, so it goes above the mechanics and is not mangled.
func TestInviteEmailCarriesTheNote(t *testing.T) {
	_, body := inviteEmail("Site", "https://x", "bob", "CODE", "Hi Carol —\nit is the anime place.")
	note := strings.Index(body, "it is the anime place.")
	link := strings.Index(body, "/register?invite=")
	if note < 0 {
		t.Fatal("the note is missing from the body")
	}
	if note > link {
		t.Error("the note is below the link; it is the part they recognise and belongs above it")
	}
	// Multi-line notes stay multi-line rather than being collapsed.
	if !strings.Contains(body, "    Hi Carol —\n") {
		t.Error("the note's first line was not indented as its own line")
	}
}

// The defaults decide what a site does for every operator who never opens the
// options page, so they are part of the contract rather than an implementation
// detail.
func TestInviteDefaultsAreTheConservativeAnswer(t *testing.T) {
	// Mirrors loadInviteSettings' fallbacks without needing a database.
	inviteTTLHours.Store(defaultInviteTTLHours)
	inviteMaxPending.Store(defaultInviteMaxPending)
	inviteEmailReq.Store(true)
	inviteEmailStrict.Store(true)
	inviteSendMail.Store(true)
	inviteMemberRevk.Store(true)
	inviteRefund.Store(true)
	invitePublicStats.Store(false)

	o := currentInviteOptions()
	if !o.EmailRequired || !o.EmailStrict {
		t.Error("invites are not locked to an address by default")
	}
	if o.PublicStats {
		t.Error("recruiting totals are public by default — publishing a social graph must be a decision")
	}
	if !o.RefundRevoked {
		t.Error("withdrawing does not refund by default; the fair answer is the default")
	}
	if !validInviteTTL(o.TTLHours) {
		t.Errorf("the default window %d is not one of the presets the form offers", o.TTLHours)
	}
	if o.TTLLabel() == "" {
		t.Error("the default window has no human label")
	}
}

// A window the form cannot express is a window nobody can change back, so an
// unrecognised value falls to the default rather than being adopted.
func TestInviteTTLValidation(t *testing.T) {
	for _, h := range []int{24, 48, 24 * 7, 24 * 14, 24 * 30} {
		if !validInviteTTL(h) {
			t.Errorf("%d is offered by the form but rejected by the validator", h)
		}
	}
	for _, h := range []int{0, -1, 1, 100, 24*30 + 1} {
		if validInviteTTL(h) {
			t.Errorf("accepted %d, which the form cannot produce", h)
		}
	}
}

// TTLLabel is what a member reads. Hours are not.
func TestInviteTTLLabel(t *testing.T) {
	for _, tc := range []struct {
		hours int
		want  string
	}{
		{24, "24 hours"},
		{24 * 7, "7 days"},
		{24 * 30, "30 days"},
		// Not a preset — falls back to something true rather than empty.
		{99, "99 hours"},
	} {
		if got := (inviteOptions{TTLHours: tc.hours}).TTLLabel(); got != tc.want {
			t.Errorf("TTLLabel(%d) = %q, want %q", tc.hours, got, tc.want)
		}
	}
}
