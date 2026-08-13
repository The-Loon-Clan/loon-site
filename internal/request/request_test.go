package request

import (
	"strings"
	"testing"
)

// The checks here are shared by every endpoint that adopts the pattern, so a
// bug in one of them is a bug in all of them at once. That is the argument for
// having them in a package, and the same argument for testing them here rather
// than through whichever endpoint happened to call them.

// stubInput exists to exercise the generic entry point. Its rules are trivial;
// what is being checked is that Validate reaches them.
type stubInput struct{ name string }

func (s stubInput) Validate() Errors {
	var e Errors
	Required(&e, "name", s.name, "A name")
	return e
}

func TestValidateReachesTheInputsRules(t *testing.T) {
	if errs := Validate(stubInput{name: "ok"}); errs.Any() {
		t.Errorf("a valid input reported %v", errs)
	}
	if errs := Validate(stubInput{}); !errs.Any() {
		t.Error("an invalid input reported nothing")
	}
}

func TestTheFirstMessageForAFieldWins(t *testing.T) {
	// Rules are written most-fundamental first — "you left this blank" before
	// "this is too short" — and the earlier message is the more useful one.
	// Overwriting would tell somebody who submitted an empty box that it needs
	// to be at least three characters, which is true and absurd.
	var e Errors
	e.Add("username", "A username is required.")
	e.Add("username", "A username must be at least 3 characters.")

	if got := e["username"]; !strings.Contains(got, "required") {
		t.Errorf("username = %q, want the first message", got)
	}
	if len(e) != 1 {
		t.Errorf("one field produced %d entries; a form shows one message per input", len(e))
	}
}

func TestAZeroErrorsIsUsableWithoutInitialising(t *testing.T) {
	// Validate methods declare `var e Errors` and add to it. A nil map panics
	// on write, so Add has to handle the zero value — otherwise every rule
	// would need a constructor call nobody would remember on the first one.
	var e Errors
	if e.Any() {
		t.Error("a zero Errors reports failures")
	}
	e.Add("f", "msg")
	if !e.Any() || e["f"] != "msg" {
		t.Errorf("writing to a zero Errors did not take: %v", e)
	}
}

func TestFirstIsStableAcrossCalls(t *testing.T) {
	// Map iteration order is randomised in Go. Without the explicit ordering,
	// a form with three problems would name a different one on each refresh,
	// which reads as the site changing its mind rather than the member having
	// three things to fix.
	var e Errors
	e.Add("password", "p")
	e.Add("username", "u")
	e.Add("email", "m")

	for range 50 {
		if got := e.First("username", "email", "password"); got != "u" {
			t.Fatalf("First returned %q, want the first field in the given order", got)
		}
	}
}

func TestFirstFallsBackWhenTheOrderNamesNothing(t *testing.T) {
	// A field that failed but was not listed must still be reported. Returning
	// "" would show a blank error banner: the form refuses and says nothing.
	var e Errors
	e.Add("surprise", "unexpected")

	if got := e.First("username", "email"); got != "unexpected" {
		t.Errorf("First = %q, want the unlisted field's message rather than silence", got)
	}
}

func TestRequiredTreatsOnlyEmptyAsMissing(t *testing.T) {
	var e Errors
	if Required(&e, "f", "", "A field") {
		t.Error("an empty value passed Required")
	}
	if !Required(&e, "g", "0", "A field") {
		t.Error(`"0" was treated as missing; it is a value`)
	}
	if !Required(&e, "h", " ", "A field") {
		t.Error(`" " was treated as missing — Required takes an already-trimmed ` +
			`value, because whether to trim is the caller's decision`)
	}
}

func TestLengthsAreMeasuredInCharacters(t *testing.T) {
	// len() on a UTF-8 string counts bytes. A 32-byte limit is about ten
	// characters in Japanese and fifteen for a name with accents — a limit that
	// looks generous to whoever wrote it and arbitrary to whoever hits it.
	var e Errors
	if !MaxRunes(&e, "f", strings.Repeat("あ", 10), "A name", 10) {
		t.Error("ten characters failed a ten-character limit; it is counting bytes")
	}
	if MaxRunes(&e, "g", strings.Repeat("あ", 11), "A name", 10) {
		t.Error("eleven characters passed a ten-character limit")
	}
	if MinRunes(&e, "h", "ab", "A name", 3) {
		t.Error("two characters passed a three-character minimum")
	}
	if !MinRunes(&e, "i", "abc", "A name", 3) {
		t.Error("exactly three characters failed a three-character minimum; " +
			"the bound is inclusive")
	}
}

func TestTheLengthMessageSaysBothNumbers(t *testing.T) {
	// "Too long" without the numbers leaves somebody deleting characters one at
	// a time to find the limit.
	var e Errors
	MaxRunes(&e, "f", strings.Repeat("a", 40), "A name", 32)

	msg := e["f"]
	if !strings.Contains(msg, "40") || !strings.Contains(msg, "32") {
		t.Errorf("message = %q, want both the actual length and the limit", msg)
	}
}

func TestEmailAcceptsAddressesThatRegexesReject(t *testing.T) {
	// The reason this uses net/mail. Every one of these is a real, deliverable
	// address, and every hand-rolled regex rejects at least one of them —
	// leaving the owner with a form that says their own address is invalid.
	for _, addr := range []string{
		"member@example.com",
		"tagged+loon@example.com",
		"first.last@example.co.uk",
		"someone@example.museum",
		"x@example.io",
	} {
		var e Errors
		if !Email(&e, "email", addr, "Email") {
			t.Errorf("%q was rejected", addr)
		}
	}
}

func TestEmailRefusesWhatIsNotAnAddress(t *testing.T) {
	for _, addr := range []string{"", "not an address", "@example.com", "member@", "member example.com"} {
		var e Errors
		if Email(&e, "email", addr, "Email") {
			t.Errorf("%q was accepted as an email address", addr)
		}
	}
}

func TestOneOfIsAClosedSet(t *testing.T) {
	var e Errors
	if !OneOf(&e, "mode", "invite", "Mode", "open", "invite", "closed") {
		t.Error("a listed option was rejected")
	}
	if OneOf(&e, "mode", "Invite", "Mode", "open", "invite", "closed") {
		t.Error("matching is case-insensitive; these are wire values, not prose")
	}
	if OneOf(&e, "mode", "", "Mode", "open", "invite", "closed") {
		t.Error("an empty value passed a closed set")
	}
}

func TestOneOfDoesNotReciteTheOptions(t *testing.T) {
	// A value outside the set means the request did not come from the form, so
	// the message tells an attacker nothing about what the options are.
	var e Errors
	OneOf(&e, "mode", "sneaky", "Mode", "open", "invite", "closed")

	msg := e["mode"]
	for _, secret := range []string{"open", "invite", "closed", "sneaky"} {
		if strings.Contains(msg, secret) {
			t.Errorf("the message quotes %q back: %q", secret, msg)
		}
	}
}

func TestMatchesComparesExactly(t *testing.T) {
	var e Errors
	if !Matches(&e, "confirm", "abc", "abc", "They do not match.") {
		t.Error("identical values did not match")
	}
	if Matches(&e, "confirm2", "abc", "abc ", "They do not match.") {
		t.Error("a trailing space was ignored; a password's spaces are part of it")
	}
}

func TestTrimTakesTheEdgesAndNothingElse(t *testing.T) {
	if got := Trim("  hello world  "); got != "hello world" {
		t.Errorf("Trim = %q, want the inner space kept", got)
	}
	if got := Trim("\t\n name \r\n"); got != "name" {
		t.Errorf("Trim = %q, want tabs and newlines gone too", got)
	}
}
