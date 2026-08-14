package handlers

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/request"
	"github.com/the-loon-clan/loon-site/internal/storage"
)

// One struct per endpoint that accepts input, each stating its own rules.
//
// The pattern, and why it is worth the file:
//
//	type xInput struct{ … }                       // what the form sends
//	func readXInput(c *gin.Context) xInput        // parse, and ONLY parse
//	func (in xInput) Validate() request.Errors    // the rules, in one place
//
// The rules become readable without reading the handler, and testable without
// building a request — which is the whole difference between "we validate
// carefully" and "here is what this endpoint accepts, and here is the test".
//
// Validate is required by request.Input, and inputs_test.go asserts that every
// type in this package named *Input satisfies it — so a struct added here
// without rules fails the build's tests rather than quietly accepting anything.
//
// What does NOT belong in Validate: anything needing the database or the
// session. "Is this username taken" and "is this invite code real" are
// questions for the handler, because they can be true when Validate runs and
// false a moment later. Validate answers only what the request itself can
// settle.

// registerInput is POST /register.
type registerInput struct {
	Username string
	Email    string // optional — an account without one simply gets no verification mail
	Password string
	Invite   string
	Captcha  string

	// RegMode is the site's registration mode at the moment of the request.
	// Carried on the input because it changes what is REQUIRED: an invite code
	// is mandatory on an invite-only site and meaningless on an open one, and
	// that is a rule about this submission rather than a fact about the form.
	RegMode string
}

func readRegisterInput(c *gin.Context, captchaField string) registerInput {
	return registerInput{
		Username: request.Trim(c.PostForm("username")),
		Email:    request.Trim(c.PostForm("email")),
		// NOT trimmed. A password of spaces is a password, and trimming one
		// silently signs the member up with something other than what they
		// typed — then fails to let them back in.
		Password: c.PostForm("password"),
		Invite:   request.Trim(c.PostForm("invite")),
		Captcha:  c.PostForm(captchaField),
		RegMode:  registrationMode(),
	}
}

// Validate states what this endpoint accepts.
//
// Deliberately NOT stricter than the flow behind it. authflow.Register already
// rejects an empty username, a short password and a taken name, and this is a
// worked example rather than a change of policy — so the rules here are the
// ones the handler was already enforcing by other means, plus the length
// ceilings that were previously enforced nowhere at all.
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
	Password string
	Captcha  string
}

func readLoginInput(c *gin.Context, captchaField string) loginInput {
	return loginInput{
		Username: request.Trim(c.PostForm("username")),
		// Never trimmed — a password of spaces is a password.
		Password: c.PostForm("password"),
		Captcha:  c.PostForm(captchaField),
	}
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
	// AmountRaw is what was typed, kept so a non-numeric entry can be reported
	// as such rather than silently becoming 0 — which the storage layer would
	// then refuse as "a gift has to be at least 1 point", an answer about the
	// wrong thing.
	AmountRaw string
	Numeric   bool
	Note      string
}

func readGiftInput(c *gin.Context) giftInput {
	raw := request.Trim(c.PostForm("amount"))
	n, err := strconv.Atoi(raw)
	return giftInput{
		To:        request.Trim(c.PostForm("to")),
		Amount:    n,
		AmountRaw: raw,
		Numeric:   err == nil,
		Note:      c.PostForm("note"),
	}
}

// Validate covers what the REQUEST can settle, and stops there.
//
// The limits — at least GiftMin, at most GiftMax, not to yourself, and enough
// points to cover it — live in storage.TransferPoints, checked inside the
// transaction that moves the points. That is the right place and this must not
// duplicate them: a balance checked here is a balance that can change before
// the UPDATE runs, and two copies of a rule are two things to keep in step.
//
// What is here is what a form can answer on its own: a recipient was named, and
// the amount is a number at all.
func (in giftInput) Validate() request.Errors {
	var e request.Errors
	request.Required(&e, "to", in.To, "A member to send to")
	if in.AmountRaw == "" {
		e.Add("amount", "How many points?")
	} else if !in.Numeric {
		e.Add("amount", "That is not a number of points.")
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

func readWishInput(c *gin.Context) wishInput {
	return wishInput{
		Title: request.Trim(c.PostForm("title")),
		Note:  request.Trim(c.PostForm("note")),
	}
}

// Validate reports what is wrong rather than quietly repairing it.
//
// The handler used to TRUNCATE an over-long title to wishTitleMax and carry on,
// which stores something the member did not write and does not find out about
// until they look at their own list. Telling them is both cheaper and more
// honest; the truncation stays in place underneath as a backstop for anything
// that reaches the store by another path.
//
// How many entries are already open is NOT here: that is a count over the
// database, true when read and possibly false a moment later.
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
// A single checkbox, and it still gets a struct. There is nothing here to
// validate — `== "1"` is already total, since every other value is false — so
// the struct earns its place on the other half of the pattern: the handler
// reads in.PrivateProfile rather than c.PostForm(fieldPrivateProfile), and the
// form's field name is written down once, next to the type that models the
// form, instead of appearing as a bare string in the middle of a handler.
//
// Cost: none. gin parses the body once into c.formCache and every PostForm
// after that is a map lookup on url.Values, so reading fields into a struct is
// the same work — done once, in one place, instead of wherever somebody
// happened to need it.
type settingsPrivacyInput struct {
	PrivateProfile bool
}

func readSettingsPrivacyInput(c *gin.Context) settingsPrivacyInput {
	return settingsPrivacyInput{
		PrivateProfile: c.PostForm(fieldPrivateProfile) == checked,
	}
}

// Validate has nothing to say, and says so explicitly.
//
// A checkbox cannot be malformed: it is present or it is not. The method exists
// because request.Input requires it, and returning nil here is a statement —
// "this endpoint accepts anything a form can send" — rather than an omission
// somebody has to go and check.
func (in settingsPrivacyInput) Validate() request.Errors { return nil }

// settingsNotificationsInput is POST /settings/notifications.
//
// A map rather than a field per kind, because the kinds are DATA: they live in
// notifiableKinds, a plugin-facing list that grows. A struct with a field per
// kind would have to be edited every time one is added, and the compiler would
// not remind anybody — the new kind would simply never be readable.
type settingsNotificationsInput struct {
	// Enabled holds every KNOWN kind, including the ones the form did not send.
	// An unchecked box posts nothing at all, so reading only what arrived would
	// silently leave a kind the member just switched off still enabled.
	Enabled map[string]bool
}

func readSettingsNotificationsInput(c *gin.Context) settingsNotificationsInput {
	in := settingsNotificationsInput{Enabled: make(map[string]bool, len(notifiableKinds))}
	for _, k := range notifiableKinds {
		in.Enabled[k.Kind] = c.PostForm(k.Kind) == checked
	}
	return in
}

// Validate: the kinds come from notifiableKinds, so an unknown one cannot be in
// the map — readSettingsNotificationsInput builds it from that list rather than
// from what was posted.
func (in settingsNotificationsInput) Validate() request.Errors { return nil }
