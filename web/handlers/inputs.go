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

	// PasswordConfirm is the second box, and it exists because the failure it
	// catches is silent and unrecoverable. The password is stored hashed, so a
	// typo in it cannot be read back, cannot be reset by anybody, and produces
	// no error at signup — the first sign of it is a login that will never
	// work, on an account that was created successfully.
	//
	// Raw for the same reason as Password: trimming one side and not the other
	// would report a mismatch between two identical entries.
	PasswordConfirm string `form:",raw"`

	Invite string

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
		// Only once there IS a password to confirm. "The passwords do not
		// match" underneath an empty password field is a second message about
		// a field the member has not filled in yet, and the first one already
		// said what to do about it.
		request.Matches(&e, "password_confirm", in.PasswordConfirm, in.Password,
			"The passwords do not match.")
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
	return []string{"username", "email", "password", "password_confirm", "invite"}
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

// ── account security ────────────────────────────────────────────────────

// securityActionInput is POST /settings/security — one form, several buttons.
//
// Action selects which: begin, confirm, cancel, regenerate, disable, email. The
// handler's switch is the check, and its default does nothing, which is the
// right answer for a value that did not come from the page.
type securityActionInput struct {
	Action string
	// Code is a TOTP code from an authenticator, or a recovery code. Not
	// trimmed of internal spacing here — totpVerify handles the formatting
	// authenticators use.
	Code string
	// Email is the NEW address for a change request. Confirmed at that address
	// before it is applied, never on submit.
	Email string
}

func readSecurityActionInput(c *gin.Context) (securityActionInput, error) {
	var in securityActionInput
	return in, request.Bind(c, &in)
}

// Validate: each action's own step checks what it needs — totpConfirm verifies
// the code, requestEmailChange judges the address — and those checks sit beside
// the state they act on.
func (in securityActionInput) Validate() request.Errors { return nil }

// twoFactorInput is POST /login/2fa — the second step, where a recovery code is
// as welcome as a TOTP code because "I have lost my phone" is exactly when
// somebody is looking at this form.
type twoFactorInput struct {
	Code string
}

func readTwoFactorInput(c *gin.Context) (twoFactorInput, error) {
	var in twoFactorInput
	return in, request.Bind(c, &in)
}

// Validate: an empty or wrong code is answered identically by twoFactorPost —
// "that code did not match" — and deliberately so. Telling somebody they left
// the box blank is a marginal improvement; telling them their code was
// well-formed but wrong is an oracle.
func (in twoFactorInput) Validate() request.Errors { return nil }

// ── profile ─────────────────────────────────────────────────────────────

// profileBioInput is POST for the profile's free-text block.
type profileBioInput struct {
	Bio string
}

func readProfileBioInput(c *gin.Context) (profileBioInput, error) {
	var in profileBioInput
	return in, request.Bind(c, &in)
}

// Validate: the handler truncates by RUNES where it stores, because cutting a
// multi-byte character in half writes invalid UTF-8 and the first thing that
// breaks is the page trying to show it. That is the right place for the rule.
func (in profileBioInput) Validate() request.Errors { return nil }

// ── small forms that carry only a return path ───────────────────────────

// nextInput is the shape of a form whose only field is where to go afterwards —
// the bookmark toggle is the one that matters, since it POSTs and returns you
// to the release you were reading.
type nextInput struct {
	Next string
}

func readNextInput(c *gin.Context) (nextInput, error) {
	var in nextInput
	return in, request.Bind(c, &in)
}

func (in nextInput) Validate() request.Errors { return nil }

// ── password reset ──────────────────────────────────────────────────────

// forgotInput is POST /forgot — ask for a reset link.
type forgotInput struct {
	Email   string
	Captcha string `form:"-"`
}

func readForgotInput(c *gin.Context) (forgotInput, error) {
	var in forgotInput
	err := request.Bind(c, &in)
	in.Captcha = c.PostForm(captcha.FormField)
	return in, err
}

// Validate checks presence and shape, and NOT whether the address is known.
//
// The handler answers the same way either way — see resetFlow.RequestReset —
// because "no account has that address" is a fact about the member list, and a
// forgot-password form is the easiest place in a site to ask that question a
// thousand times.
func (in forgotInput) Validate() request.Errors {
	var e request.Errors
	if request.Required(&e, "email", in.Email, "An email address") {
		request.Email(&e, "email", in.Email, "Email")
	}
	return e
}

func (in forgotInput) fieldOrder() []string { return []string{"email"} }

// resetInput is POST /reset — the token from the emailed link, and a new
// password.
type resetInput struct {
	Token    string
	Password string `form:",raw"`
}

func readResetInput(c *gin.Context) (resetInput, error) {
	var in resetInput
	return in, request.Bind(c, &in)
}

// Validate: the token's validity and the password's strength both belong to
// authtoken.PerformReset — the token is single-use and time-limited, and both
// facts live with the row rather than with the form.
func (in resetInput) Validate() request.Errors { return nil }

// ── theme, avatar, wishlist actions ─────────────────────────────────────

// themeInput is POST for the theme picker.
type themeInput struct {
	Theme string
	Next  string
}

func readThemeInput(c *gin.Context) (themeInput, error) {
	var in themeInput
	return in, request.Bind(c, &in)
}

// Validate: an unknown theme name resolves to the default in themeByName, which
// is the right answer for a picker whose options come from the stylesheet list.
func (in themeInput) Validate() request.Errors { return nil }

// avatarSaveInput is POST /settings/profile's avatar form. The FILE is read
// separately by readAvatarUpload — multipart is its own thing — and this is the
// rest of the form.
type avatarSaveInput struct {
	// Remove is a submit button rather than a checkbox: present means "take my
	// picture down", and its value is whatever the button carries.
	Remove string
}

func readAvatarSaveInput(c *gin.Context) (avatarSaveInput, error) {
	var in avatarSaveInput
	return in, request.Bind(c, &in)
}

func (in avatarSaveInput) Validate() request.Errors { return nil }

// wishActionInput is POST /wishlist's per-entry controls.
type wishActionInput struct {
	Action string
	// All clears every filled entry at once, from the "clear filled" button.
	All bool
}

func readWishActionInput(c *gin.Context) (wishActionInput, error) {
	var in wishActionInput
	return in, request.Bind(c, &in)
}

func (in wishActionInput) Validate() request.Errors { return nil }

// ── the Newznab API ─────────────────────────────────────────────────────

// newznabQueryInput is GET /api and GET /rss.
//
// The only struct here whose field names are all tagged, and the reason is that
// these names are not ours: t, q, cat, apikey and the rest are the Newznab
// spec, which Sonarr and Radarr send and which cannot be renamed to suit a Go
// field. Where a wire name belongs to somebody else, the tag says so — that is
// what tags are for, as against restating a name we chose ourselves.
type newznabQueryInput struct {
	Function string `form:"t"`
	Query    string `form:"q"`
	Cats     string `form:"cat"`
	ID       string `form:"id"`
	APIKey   string `form:"apikey"`
	Limit    int    `form:"limit"`
	Offset   int    `form:"offset"`
}

func readNewznabQueryInput(c *gin.Context) (newznabQueryInput, error) {
	var in newznabQueryInput
	return in, request.Bind(c, &in)
}

// Validate: nothing, deliberately. A downloader sending a malformed parameter
// wants results or an empty feed, not a 400 it will report to its user as "the
// indexer is broken" — see clamp, which brings paging into range instead.
func (in newznabQueryInput) Validate() request.Errors { return nil }

// clamp brings the paging parameters into range.
//
// A negative or enormous limit must not reach the query layer, and neither
// should be an error: these arrive from tools this site does not control, and
// the useful response to "limit=999999" is a hundred results rather than a
// refusal.
func (in newznabQueryInput) clamp() newznabQueryInput {
	if in.Limit <= 0 || in.Limit > 200 {
		in.Limit = 100
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	return in
}
