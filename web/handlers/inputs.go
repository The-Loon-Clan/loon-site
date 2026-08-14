package handlers

import (
	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-baseline/captcha"

	"github.com/the-loon-clan/loon-site/internal/request"
	"github.com/the-loon-clan/loon-site/internal/storage"
)

// One struct per endpoint that accepts input.
//
// The struct IS the declaration of the form. Field names are the wire names —
// request.Bind derives private_profile from PrivateProfile — so there is no
// mapping table, no tags in the ordinary case, and nothing to keep in step
// between a handler, a struct and a template.
//
//	type xInput struct{ … }                          // what the form sends
//	func readXInput(c *gin.Context) (xInput, error)  // bind, and only bind
//	func (in xInput) Validate() request.Errors       // the rules, in one place
//
// Validate is required by request.Input, and inputs_test.go asserts that every
// type in this package named *Input satisfies it — so a struct added here
// without rules fails the tests rather than quietly accepting anything.
//
// What does NOT belong in Validate: anything needing the database or the
// session. "Is this username taken", "is this invite code real", "do you have
// enough points" are questions for the handler, because they can be true when
// Validate runs and false a moment later. Validate answers only what the
// request itself can settle.
//
// Two tags appear, and each is a decision rather than a mapping:
//
//	`form:",raw"`   keep exactly what was typed (passwords)
//	`form:"-"`      the handler fills this, never the submitter

// ── register ────────────────────────────────────────────────────────────

// registerInput is POST /register.
type registerInput struct {
	Username string
	Email    string // optional — an account without one simply gets no verification mail
	// A password of spaces is a password. Trimming one signs somebody up with
	// something other than what they typed, then refuses to let them back in.
	Password string `form:",raw"`
	Invite   string

	// Captcha's field name belongs to Cloudflare, so it comes from the
	// library's own constant rather than being spelled out here.
	Captcha string `form:"-"`

	// RegMode is the site's registration mode at the moment of the request.
	// Filled by the handler, never by the form: it decides what is REQUIRED,
	// and a value the submitter could set would let them choose their own
	// rules.
	RegMode string `form:"-"`
}

func readRegisterInput(c *gin.Context) (registerInput, error) {
	var in registerInput
	err := request.Bind(c, &in)
	in.Captcha = c.PostForm(captcha.FormField)
	in.RegMode = registrationMode()
	return in, err
}

// Validate states what this endpoint accepts.
//
// Deliberately NOT stricter than the flow behind it. authflow.Register already
// rejects an empty username, a short password and a taken name, so the rules
// here are the ones the handler was already enforcing by other means, plus the
// length ceilings that were previously enforced nowhere at all.
func (in registerInput) Validate() request.Errors {
	var e request.Errors

	if request.Required(&e, "username", in.Username, "A username") {
		request.MinRunes(&e, "username", in.Username, "A username", 3)
		request.MaxRunes(&e, "username", in.Username, "A username", 32)
	}
	if request.Required(&e, "password", in.Password, "A password") {
		request.MinRunes(&e, "password", in.Password, "A password", minPasswordLen)
	}
	// Optional, and checked only if given: a blank email is a supported choice
	// here, and the verification mail is simply not sent.
	if in.Email != "" {
		request.Email(&e, "email", in.Email, "Email")
		request.MaxRunes(&e, "email", in.Email, "Email", 254) // RFC 5321 §4.5.3.1.3
	}
	// The mode decides whether this field exists at all.
	if in.RegMode == RegInvite {
		request.Required(&e, "invite", in.Invite, "An invite code")
	}
	return e
}

// fieldOrder is the order errors are reported in when the page shows one line
// rather than a message per input: the order the fields appear on the form, so
// the message names the first thing the member would fix.
func (in registerInput) fieldOrder() []string {
	return []string{"username", "email", "password", "invite"}
}

// ── the door ────────────────────────────────────────────────────────────

// loginInput is POST /login.
type loginInput struct {
	Username string
	Password string `form:",raw"`
	Captcha  string `form:"-"`
}

func readLoginInput(c *gin.Context) (loginInput, error) {
	var in loginInput
	err := request.Bind(c, &in)
	in.Captcha = c.PostForm(captcha.FormField)
	return in, err
}

// Validate deliberately checks presence and NOTHING else.
//
// No length rule, no character rule, no "that is not a valid username". Every
// one of those is a free oracle: a login form that rejects "abc" as too short
// before checking it has told an attacker the account cannot be named "abc",
// and one that answers differently for a well-formed unknown name than for a
// malformed one has separated the two for them.
//
// Presence is different — an empty box is the member's own mistake, not a fact
// about the account list, and saying so saves a round trip that would fail
// anyway.
func (in loginInput) Validate() request.Errors {
	var e request.Errors
	request.Required(&e, "username", in.Username, "A username")
	request.Required(&e, "password", in.Password, "A password")
	return e
}

func (in loginInput) fieldOrder() []string { return []string{"username", "password"} }

// ── gifts ───────────────────────────────────────────────────────────────

// giftInput is POST /gifts.
type giftInput struct {
	To     string
	Amount int
	Note   string
}

func readGiftInput(c *gin.Context) (giftInput, error) {
	var in giftInput
	return in, request.Bind(c, &in)
}

// Validate covers what the REQUEST can settle, and stops there.
//
// The limits — at least GiftMin, at most GiftMax, not to yourself, and enough
// points to cover it — live in storage.TransferPoints, checked inside the
// transaction that moves the points. That is the right place and this must not
// duplicate them: a balance checked here is a balance that can change before
// the UPDATE runs, and two copies of a rule are two things to keep in step.
func (in giftInput) Validate() request.Errors {
	var e request.Errors
	request.Required(&e, "to", in.To, "A member to send to")
	// Bind leaves an unparseable number at zero rather than failing: "abc" in a
	// number box is the member's mistake, and this is where a member's mistake
	// gets a message.
	if in.Amount == 0 {
		e.Add("amount", "How many points?")
	}
	request.MaxRunes(&e, "note", in.Note, "A note", storage.GiftNoteMax)
	return e
}

func (in giftInput) fieldOrder() []string { return []string{"to", "amount", "note"} }

// ── wishlist ────────────────────────────────────────────────────────────

// wishInput is POST /wishlist.
type wishInput struct {
	Title string
	Note  string
}

func readWishInput(c *gin.Context) (wishInput, error) {
	var in wishInput
	return in, request.Bind(c, &in)
}

// Validate reports what is wrong rather than quietly repairing it.
//
// The handler used to TRUNCATE an over-long title to wishTitleMax and carry on,
// which stores something the member did not write and which they do not find
// out about until they look at their own list. The truncation stays in place
// underneath as a backstop for anything reaching the store by another path.
func (in wishInput) Validate() request.Errors {
	var e request.Errors
	if request.Required(&e, "title", in.Title, "Say what you are looking for") {
		request.MaxRunes(&e, "title", in.Title, "A title", wishTitleMax)
	}
	request.MaxRunes(&e, "note", in.Note, "A note", wishNoteMax)
	return e
}

func (in wishInput) fieldOrder() []string { return []string{"title", "note"} }

// ── settings ────────────────────────────────────────────────────────────

// settingsPrivacyInput is POST /settings/privacy.
//
// One checkbox, and it still gets a struct: PrivateProfile is read as
// private_profile with nothing written down anywhere else, and the handler
// reads in.PrivateProfile rather than a quoted string in the middle of a
// function.
type settingsPrivacyInput struct {
	PrivateProfile bool
}

func readSettingsPrivacyInput(c *gin.Context) (settingsPrivacyInput, error) {
	var in settingsPrivacyInput
	return in, request.Bind(c, &in)
}

// Validate has nothing to say, and says so explicitly.
//
// A checkbox cannot be malformed: it is present or it is not. The method exists
// because request.Input requires it, and returning nil is a statement — "this
// endpoint accepts anything a form can send" — rather than an omission somebody
// has to go and check.
func (in settingsPrivacyInput) Validate() request.Errors { return nil }

// settingsNotificationsInput is POST /settings/notifications.
//
// The one form whose fields are DATA rather than a fixed shape: the kinds live
// in notifiableKinds, a plugin-facing list that grows. A struct field per kind
// would need editing every time one was added, and nothing would remind
// anybody — the new kind would simply never be readable.
//
// So it is a map, filled from the KNOWN kinds rather than from what arrived. An
// unticked checkbox posts nothing at all, so reading only what was sent would
// silently leave a kind the member just switched off still enabled.
type settingsNotificationsInput struct {
	Enabled map[string]bool `form:"-"`
}

func readSettingsNotificationsInput(c *gin.Context) (settingsNotificationsInput, error) {
	in := settingsNotificationsInput{Enabled: make(map[string]bool, len(notifiableKinds))}
	for _, k := range notifiableKinds {
		in.Enabled[k.Kind] = c.PostForm(k.Kind) == checked
	}
	return in, nil
}

// Validate: the kinds come from notifiableKinds, so an unknown one cannot be in
// the map — the reader builds it from that list rather than from what was
// posted.
func (in settingsNotificationsInput) Validate() request.Errors { return nil }
