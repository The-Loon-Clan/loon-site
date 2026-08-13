package handlers

import (
	"go/ast"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon-site/internal/request"
)

// Every *Input type in this package must state its rules.
//
// request.Validate is generic over request.Input, so passing a struct with no
// Validate method to it does not compile — that half is enforced by the
// language. What the compiler cannot see is a struct DECLARED here, filled from
// a form, and then used directly, never going near Validate. It looks exactly
// like the ones that do, and it accepts anything.
//
// So this reads the package's own declarations: a type named …Input either
// implements request.Input or fails here, by name.
func TestEveryInputTypeStatesItsRules(t *testing.T) {
	_, files := parseNonTestFiles(t)

	var found []string
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || !strings.HasSuffix(ts.Name.Name, "Input") {
				return true
			}
			if _, isStruct := ts.Type.(*ast.StructType); !isStruct {
				return true
			}
			found = append(found, ts.Name.Name)
			return true
		})
	}

	if len(found) == 0 {
		t.Fatal("no *Input types found at all — the scan is broken, not the code")
	}

	// The compile-time half: every type listed here is asserted to satisfy the
	// interface. A new *Input the scan finds but this list does not is reported
	// below, which is what sends somebody back here to add it.
	implemented := map[string]request.Input{
		"registerInput": registerInput{},
	}

	for _, name := range found {
		if _, ok := implemented[name]; !ok {
			t.Errorf("%s is an input struct with no entry in this test.\n"+
				"    Give it a Validate() request.Errors method and add it to the map "+
				"above — an input that validates nothing accepts everything, and it "+
				"looks identical to one that does not.", name)
		}
	}
	t.Logf("checked %d input types", len(found))
}

// ── registerInput's rules, which is the point of naming them ──

func validRegister() registerInput {
	return registerInput{
		Username: "newmember",
		Email:    "member@example.com",
		Password: "correct horse battery",
		RegMode:  RegOpen,
	}
}

func TestAGoodRegistrationPasses(t *testing.T) {
	if errs := request.Validate(validRegister()); errs.Any() {
		t.Errorf("a valid registration was rejected: %v", errs)
	}
}

func TestRegistrationRequiresAUsernameAndPassword(t *testing.T) {
	in := validRegister()
	in.Username, in.Password = "", ""

	errs := request.Validate(in)
	if errs["username"] == "" {
		t.Error("a blank username was accepted")
	}
	if errs["password"] == "" {
		t.Error("a blank password was accepted")
	}
}

func TestAnEmailIsOptionalButMustBeRealIfGiven(t *testing.T) {
	// Blank is a supported choice — the account simply gets no verification
	// mail. Making it required here would lock out every existing member who
	// registered without one.
	in := validRegister()
	in.Email = ""
	if errs := request.Validate(in); errs.Any() {
		t.Errorf("a registration with no email was rejected: %v", errs)
	}

	in.Email = "not an address"
	if errs := request.Validate(in); errs["email"] == "" {
		t.Error("a malformed email was accepted")
	}
}

func TestAnEmailWithAPlusIsAccepted(t *testing.T) {
	// The case every hand-rolled email regex gets wrong, and the reason this
	// uses net/mail instead. Someone using a tagged address has no way to argue
	// with a form that tells them their own address is invalid.
	in := validRegister()
	in.Email = "member+loon@example.com"
	if errs := request.Validate(in); errs.Any() {
		t.Errorf("a tagged address was rejected: %v", errs)
	}
}

func TestAnInviteIsRequiredOnlyOnAnInviteOnlySite(t *testing.T) {
	// The rule that depends on site state rather than on the form.
	in := validRegister()
	in.RegMode, in.Invite = RegInvite, ""
	if errs := request.Validate(in); errs["invite"] == "" {
		t.Error("an invite-only registration was accepted with no code")
	}

	in.RegMode, in.Invite = RegOpen, ""
	if errs := request.Validate(in); errs.Any() {
		t.Errorf("an OPEN registration demanded an invite code: %v", errs)
	}
}

func TestLengthsAreCountedInCharactersNotBytes(t *testing.T) {
	// A 32-byte limit is about ten characters in Japanese and fifteen for a
	// name with accents in it. The limit is on characters, so a name that
	// LOOKS short is short.
	in := validRegister()
	in.Username = strings.Repeat("あ", 20) // 60 bytes, 20 characters
	if errs := request.Validate(in); errs.Any() {
		t.Errorf("a 20-character name was rejected as too long — the limit is "+
			"counting bytes: %v", errs)
	}

	in.Username = strings.Repeat("a", 33)
	if errs := request.Validate(in); errs["username"] == "" {
		t.Error("a 33-character username passed a 32-character limit")
	}
}

func TestAShortPasswordIsRefused(t *testing.T) {
	in := validRegister()
	in.Password = strings.Repeat("x", minPasswordLen-1)
	if errs := request.Validate(in); errs["password"] == "" {
		t.Errorf("a %d-character password passed the %d-character minimum",
			minPasswordLen-1, minPasswordLen)
	}
}

func TestTheFirstFailureForAFieldIsTheOneShown(t *testing.T) {
	// A blank username fails "required" and would also fail "too short". The
	// member needs to be told it is blank, not that it is under three
	// characters — which is true, unhelpful, and slightly absurd.
	in := validRegister()
	in.Username = ""
	if msg := request.Validate(in)["username"]; !strings.Contains(msg, "required") {
		t.Errorf("a blank username reported %q, want the required message", msg)
	}
}

func TestTheReportedErrorFollowsTheFormOrder(t *testing.T) {
	// With several fields wrong, the one-line message names the first field on
	// the page rather than whichever the map happened to yield — map iteration
	// is unordered, and an error that changes between refreshes reads as a
	// flapping site.
	in := validRegister()
	in.Username, in.Email, in.Password = "", "nope", ""

	errs := request.Validate(in)
	for range 20 {
		if got := errs.First(in.fieldOrder()...); !strings.Contains(got, "username") {
			t.Fatalf("First() returned %q, want the username message every time", got)
		}
	}
}
