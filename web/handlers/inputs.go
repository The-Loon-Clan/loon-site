package handlers

import (
	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/request"
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
