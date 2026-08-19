package handlers

import (
	"context"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/the-loon-clan/loon-site/internal/storage"
)

// What an operator gets to decide about invites.
//
// One file, because these settings only make sense against each other: a
// validity window is a different feature depending on whether the invite is
// locked to an address, and "members may revoke" is a different promise
// depending on whether revoking refunds. Spread across the handlers that read
// them, the combination is not something anybody can see.
//
// Persisted in site_settings — the same shared key/value table the access
// modes and cover mode use — and mirrored into atomics so the read path is
// free. Loaded at boot, because a restart must not silently reopen a site that
// was locked down, which is the reason those settings are persisted at all.
//
// DEFAULTS ARE THE CONSERVATIVE ANSWER, deliberately. Locked to an address,
// strict about matching, and no public disclosure of who recruited whom. An
// operator who wants the looser behaviour is asking for it on purpose; one who
// never opens this page does not accidentally publish a social graph.

// Setting keys. Prefixed so they cannot collide in a table three other
// subsystems share.
const (
	settingInviteTTL        = "invite.ttl_hours"
	settingInviteEmail      = "invite.email_required"
	settingInviteStrict     = "invite.email_strict"
	settingInviteSend       = "invite.send_email"
	settingInviteRevoke     = "invite.member_revoke"
	settingInviteRefund     = "invite.refund_on_revoke"
	settingInviteMaxPending = "invite.max_pending"
	settingInvitePublic     = "invite.public_stats"
)

// Defaults.
//
// The window is a WEEK rather than the 24 hours it could be. A day is long
// enough for somebody sitting at their inbox and short enough to expire on
// anybody who was at work, and an expired invite costs the issuer a balance
// and the recipient the thing they were promised. A week is presetable down to
// a day by anybody who wants the tighter rule — see inviteTTLChoices.
const (
	defaultInviteTTLHours   = 24 * 7
	defaultInviteMaxPending = 5
)

var (
	inviteTTLHours    atomic.Int64
	inviteEmailReq    atomic.Bool
	inviteEmailStrict atomic.Bool
	inviteSendMail    atomic.Bool
	inviteMemberRevk  atomic.Bool
	inviteRefund      atomic.Bool
	inviteMaxPending  atomic.Int64
	invitePublicStats atomic.Bool
	inviteStore       siteSettings
)

// inviteOptions is the whole set, for a page that renders the form.
type inviteOptions struct {
	TTLHours      int
	EmailRequired bool
	EmailStrict   bool
	SendEmail     bool
	MemberRevoke  bool
	RefundRevoked bool
	MaxPending    int
	PublicStats   bool
}

// TTLLabel is the window as the preset that produced it — "7 days", not "168
// hours". The member-facing page says how long their invite lasts, and nobody
// converts hours in their head.
func (o inviteOptions) TTLLabel() string {
	for _, c := range inviteTTLChoices {
		if c.Hours == o.TTLHours {
			return c.Label
		}
	}
	return strconv.Itoa(o.TTLHours) + " hours"
}

// inviteTTLChoices are the windows the form offers.
//
// Presets rather than a free number box: an operator typing hours into a text
// field is one fat finger from a two-minute invite nobody can ever redeem, and
// nothing about this setting benefits from arbitrary precision.
var inviteTTLChoices = []struct {
	Hours int
	Label string
}{
	{24, "24 hours"},
	{48, "2 days"},
	{24 * 7, "7 days"},
	{24 * 14, "14 days"},
	{24 * 30, "30 days"},
}

func currentInviteOptions() inviteOptions {
	return inviteOptions{
		TTLHours:      int(inviteTTLHours.Load()),
		EmailRequired: inviteEmailReq.Load(),
		EmailStrict:   inviteEmailStrict.Load(),
		SendEmail:     inviteSendMail.Load(),
		MemberRevoke:  inviteMemberRevk.Load(),
		RefundRevoked: inviteRefund.Load(),
		MaxPending:    int(inviteMaxPending.Load()),
		PublicStats:   invitePublicStats.Load(),
	}
}

// inviteTTL is the validity window as a duration.
func inviteTTL() time.Duration {
	h := inviteTTLHours.Load()
	if h <= 0 {
		h = defaultInviteTTLHours
	}
	return time.Duration(h) * time.Hour
}

// loadInviteSettings restores the set at boot, falling back to the defaults
// above for anything never saved.
func loadInviteSettings(ctx context.Context, db storage.Conn) error {
	inviteStore = siteSettings{db: db}
	inviteTTLHours.Store(defaultInviteTTLHours)
	inviteMaxPending.Store(defaultInviteMaxPending)
	// The conservative three, on unless an operator turned them off.
	inviteEmailReq.Store(true)
	inviteEmailStrict.Store(true)
	inviteSendMail.Store(true)
	inviteMemberRevk.Store(true)
	inviteRefund.Store(true)
	invitePublicStats.Store(false)

	for _, it := range []struct {
		key   string
		apply func(string)
	}{
		{settingInviteTTL, func(v string) {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				inviteTTLHours.Store(int64(n))
			}
		}},
		{settingInviteMaxPending, func(v string) {
			// Zero is MEANINGFUL here — "no cap" — so it is stored and read as
			// itself rather than treated as unset.
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				inviteMaxPending.Store(int64(n))
			}
		}},
		{settingInviteEmail, func(v string) { inviteEmailReq.Store(v == "1") }},
		{settingInviteStrict, func(v string) { inviteEmailStrict.Store(v == "1") }},
		{settingInviteSend, func(v string) { inviteSendMail.Store(v == "1") }},
		{settingInviteRevoke, func(v string) { inviteMemberRevk.Store(v == "1") }},
		{settingInviteRefund, func(v string) { inviteRefund.Store(v == "1") }},
		{settingInvitePublic, func(v string) { invitePublicStats.Store(v == "1") }},
	} {
		v, err := inviteStore.GetSetting(ctx, it.key)
		if err != nil {
			return err
		}
		if v != "" {
			it.apply(v)
		}
	}
	return nil
}

// saveInviteOptions persists the set and mirrors it.
//
// Every value is written, including the ones that did not change, so the row
// in site_settings always says what the site is doing. A half-written set is
// how a restart adopts a mixture of the old answer and the new one.
func saveInviteOptions(ctx context.Context, o inviteOptions) error {
	if !validInviteTTL(o.TTLHours) {
		// An unrecognised window is a bug in a form, never a state to adopt —
		// the same rule the access modes follow, and for the same reason: a
		// site running on a value nothing here describes is a site nobody can
		// reason about.
		o.TTLHours = defaultInviteTTLHours
	}
	if o.MaxPending < 0 {
		o.MaxPending = 0
	}
	for _, kv := range []struct{ k, v string }{
		{settingInviteTTL, strconv.Itoa(o.TTLHours)},
		{settingInviteMaxPending, strconv.Itoa(o.MaxPending)},
		{settingInviteEmail, boolSetting(o.EmailRequired)},
		{settingInviteStrict, boolSetting(o.EmailStrict)},
		{settingInviteSend, boolSetting(o.SendEmail)},
		{settingInviteRevoke, boolSetting(o.MemberRevoke)},
		{settingInviteRefund, boolSetting(o.RefundRevoked)},
		{settingInvitePublic, boolSetting(o.PublicStats)},
	} {
		if err := inviteStore.SetSetting(ctx, kv.k, kv.v); err != nil {
			return err
		}
	}
	inviteTTLHours.Store(int64(o.TTLHours))
	inviteMaxPending.Store(int64(o.MaxPending))
	inviteEmailReq.Store(o.EmailRequired)
	inviteEmailStrict.Store(o.EmailStrict)
	inviteSendMail.Store(o.SendEmail)
	inviteMemberRevk.Store(o.MemberRevoke)
	inviteRefund.Store(o.RefundRevoked)
	invitePublicStats.Store(o.PublicStats)
	return nil
}

func validInviteTTL(h int) bool {
	for _, c := range inviteTTLChoices {
		if c.Hours == h {
			return true
		}
	}
	return false
}

func boolSetting(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
