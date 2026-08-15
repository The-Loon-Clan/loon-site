package handlers

import (
	"go/ast"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon-site/internal/request"
	"github.com/the-loon-clan/loon-site/internal/storage"
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
		"accessSaveInput":            accessSaveInput{},
		"avatarModInput":             avatarModInput{},
		"avatarSaveInput":            avatarSaveInput{},
		"cheatFlagInput":             cheatFlagInput{},
		"communityVoteInput":         communityVoteInput{},
		"coverModeInput":             coverModeInput{},
		"forgotInput":                forgotInput{},
		"giftInput":                  giftInput{},
		"jobControlInput":            jobControlInput{},
		"loginInput":                 loginInput{},
		"newznabQueryInput":          newznabQueryInput{},
		"nextInput":                  nextInput{},
		"profileBioInput":            profileBioInput{},
		"registerInput":              registerInput{},
		"reportAvatarInput":          reportAvatarInput{},
		"resetInput":                 resetInput{},
		"securityActionInput":        securityActionInput{},
		"settingsNotificationsInput": settingsNotificationsInput{},
		"settingsPrivacyInput":       settingsPrivacyInput{},
		"themeInput":                 themeInput{},
		"twoFactorInput":             twoFactorInput{},
		"undoInput":                  undoInput{},
		"widgetActionInput":          widgetActionInput{},
		"wishActionInput":            wishActionInput{},
		"wishInput":                  wishInput{},
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
		Username:        "newmember",
		Email:           "member@example.com",
		Password:        "correct horse battery",
		PasswordConfirm: "correct horse battery",
		RegMode:         RegOpen,
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

// The confirmation field, and the failure it exists for: the password is
// stored hashed, so a typo in it is not readable, not resettable and not
// reported. Registration succeeds, and the account is unreachable forever by
// the person who just made it.

func TestAMistypedConfirmationIsRejected(t *testing.T) {
	in := validRegister()
	in.PasswordConfirm = "correct horse batttery"

	errs := request.Validate(in)
	if errs["password_confirm"] == "" {
		t.Error("a registration whose two passwords differ was accepted")
	}
}

func TestAMissingConfirmationIsRejected(t *testing.T) {
	// The case that matters most in practice: not a typo but a field nothing
	// filled in — an old cached form, a script posting the fields it knew
	// about last week. Comparing against an empty string has to fail, or the
	// field is decorative for exactly the submitters who skip it.
	in := validRegister()
	in.PasswordConfirm = ""

	if errs := request.Validate(in); errs["password_confirm"] == "" {
		t.Error("a registration with no confirmation at all was accepted")
	}
}

func TestABlankPasswordIsReportedOnceNotTwice(t *testing.T) {
	// An empty form should say "a password is required", not that plus "the
	// passwords do not match" — the second is true, useless, and points at the
	// field below the one to fix.
	in := validRegister()
	in.Password, in.PasswordConfirm = "", ""

	errs := request.Validate(in)
	if errs["password"] == "" {
		t.Fatal("a blank password was accepted")
	}
	if errs["password_confirm"] != "" {
		t.Errorf("a blank form also complained about matching: %q", errs["password_confirm"])
	}
}

// The two fields must be treated identically, whatever that treatment is.
//
// This test asserted the opposite of what it asserts now, and the rewrite is
// the point. Both fields were `form:",raw"`, so it checked that a trailing
// space on ONE side was a mismatch — true then, and unreachable now: Bind
// trims both, so two entries differing only at the edges arrive here already
// equal, and rejecting them would mean rejecting a member who typed the same
// thing twice.
//
// What survives is the invariant underneath, which never depended on raw: the
// two fields get the same treatment. Trimming one and not the other is the bug
// in either direction.
func TestTheTwoPasswordFieldsAreTreatedIdentically(t *testing.T) {
	// Equal after binding: accepted.
	in := validRegister()
	in.Password, in.PasswordConfirm = "spaces both ends", "spaces both ends"
	if errs := request.Validate(in); errs.Any() {
		t.Errorf("two identical passwords were rejected: %v", errs)
	}

	// Genuinely different in the middle, where trimming never reaches: rejected.
	in.PasswordConfirm = "spaces  both ends"
	if errs := request.Validate(in); errs["password_confirm"] == "" {
		t.Error("passwords differing by an internal space were accepted")
	}
}

func TestTheConfirmationBindsFromTheFormKeyTheTemplatePosts(t *testing.T) {
	// The form key is DERIVED from the field name rather than declared, so
	// nothing here fails loudly if the derivation changes — the field just
	// arrives empty, and every registration on the site starts failing with
	// "the passwords do not match". This is the test that names the key the
	// template actually posts.
	form := url.Values{
		"username":         {"newmember"},
		"password":         {"correct horse battery"},
		"password_confirm": {"correct horse battery"},
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	in, err := readRegisterInput(c)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if in.PasswordConfirm != "correct horse battery" {
		t.Errorf("password_confirm bound to %q; the template posts that key", in.PasswordConfirm)
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

// ── loginInput: what a door may and may not say ──

func TestTheLoginFormOnlyAsksThatTheBoxesAreFilled(t *testing.T) {
	// Presence, and nothing else. A length or shape rule here is a free oracle:
	// a form that rejects "ab" as too short has told an attacker no account is
	// named "ab" without checking a single password, and one that answers
	// differently for a malformed name than for a well-formed unknown one has
	// separated those two groups for them.
	//
	// So these all pass validation and go on to fail authentication, which is
	// the same answer an ordinary wrong password gets.
	for _, name := range []string{"a", "ab", "!!!", strings.Repeat("x", 200), "robert'); DROP TABLE"} {
		in := loginInput{Username: name, Password: "something"}
		if errs := request.Validate(in); errs.Any() {
			t.Errorf("login rejected username %q before checking it: %v", name, errs)
		}
	}
}

func TestAnEmptyLoginBoxIsReported(t *testing.T) {
	// An empty box is the member's own mistake rather than a fact about the
	// account list, so saying so gives nothing away and saves a round trip.
	if errs := request.Validate(loginInput{Password: "x"}); errs["username"] == "" {
		t.Error("an empty username was accepted")
	}
	if errs := request.Validate(loginInput{Username: "x"}); errs["password"] == "" {
		t.Error("an empty password was accepted")
	}
}

func TestALoginPasswordOfSpacesIsAPassword(t *testing.T) {
	// Not trimmed, so it is not empty. Trimming would reject a legitimate
	// password and, worse, would have accepted it at registration.
	if errs := request.Validate(loginInput{Username: "someone", Password: "   "}); errs.Any() {
		t.Errorf("a password of spaces was rejected as missing: %v", errs)
	}
}

// ── giftInput ──

func TestAGiftNeedsARecipientAndANumber(t *testing.T) {
	if errs := request.Validate(giftInput{Amount: 5}); errs["to"] == "" {
		t.Error("a gift with no recipient was accepted")
	}
	if errs := request.Validate(giftInput{To: "bob"}); errs["amount"] == "" {
		t.Error("a gift with no amount was accepted")
	}
	if errs := request.Validate(giftInput{To: "bob", Amount: 0}); errs["amount"] == "" {
		t.Error("a non-numeric amount was accepted")
	}
}

func TestTheGiftLimitsStayWithTheTransaction(t *testing.T) {
	// Deliberately NOT duplicated here. GiftMin, GiftMax, self-gifting and
	// "do you have that many points" are checked inside storage.TransferPoints,
	// in the transaction that moves them — a balance checked in a handler can
	// change before the UPDATE runs, and two copies of a limit are two things
	// to keep in step.
	//
	// So a negative or enormous amount passes VALIDATION and is refused by the
	// store. This test exists so that stays a decision rather than an oversight.
	// Zero is excluded: Bind leaves an unparseable number at zero, so zero is
	// how "abc" arrives and Validate reports it as "how many points?". The
	// LIMITS are what stays with the transaction.
	for _, n := range []int{-100, 1, 999999999} {
		in := giftInput{To: "bob", Amount: n}
		if errs := request.Validate(in); errs.Any() {
			t.Errorf("amount %d was refused by Validate; the limits belong to "+
				"TransferPoints, inside the transaction: %v", n, errs)
		}
	}
}

func TestAnOverlongGiftNoteIsRefused(t *testing.T) {
	in := giftInput{To: "bob", Amount: 5,
		Note: strings.Repeat("x", storage.GiftNoteMax+1)}
	if errs := request.Validate(in); errs["note"] == "" {
		t.Errorf("a note over %d characters was accepted", storage.GiftNoteMax)
	}
}

// ── wishInput ──

func TestAWishNeedsATitle(t *testing.T) {
	if errs := request.Validate(wishInput{Note: "please"}); errs["title"] == "" {
		t.Error("a wish with no title was accepted")
	}
}

func TestAnOverlongWishIsReportedRatherThanTruncated(t *testing.T) {
	// The handler used to cut the title to wishTitleMax and carry on, storing
	// something the member did not write. They found out by noticing it on
	// their own list later, if at all.
	in := wishInput{Title: strings.Repeat("x", wishTitleMax+1)}
	errs := request.Validate(in)
	if errs["title"] == "" {
		t.Errorf("a title over %d characters was accepted and would be silently cut", wishTitleMax)
	}
	if !strings.Contains(errs["title"], strconv.Itoa(wishTitleMax)) {
		t.Errorf("the message %q does not say what the limit is", errs["title"])
	}
}

func TestAWishTitleAtTheLimitIsFine(t *testing.T) {
	// Inclusive, so the member who used exactly the allowance is not told off.
	in := wishInput{Title: strings.Repeat("x", wishTitleMax)}
	if errs := request.Validate(in); errs.Any() {
		t.Errorf("a title of exactly %d characters was refused: %v", wishTitleMax, errs)
	}
}

// ── the reset form's confirmation ──
//
// Same failure as registration — a hashed password cannot be read back, so a
// typo is unreadable, unresettable and unreported — with one difference that
// makes it worse. This flow spends a single-use token. Mistype here and you
// have locked yourself out AND used the one link that could have let you back
// in, with nothing on screen saying so.

func TestResetRequiresTheTwoEntriesToAgree(t *testing.T) {
	in := resetInput{Token: "tok", Password: "correct horse battery", PasswordConfirm: "correct horse batttery"}
	if errs := request.Validate(in); errs["password_confirm"] == "" {
		t.Error("a reset whose two passwords differ was accepted")
	}
}

func TestResetAcceptsAMatchingPair(t *testing.T) {
	in := resetInput{Token: "tok", Password: "correct horse battery", PasswordConfirm: "correct horse battery"}
	if errs := request.Validate(in); errs.Any() {
		t.Errorf("a matching pair was rejected: %v", errs)
	}
}

func TestResetLeavesStrengthToTheFlowThatOwnsIt(t *testing.T) {
	// A short password is authtoken.PerformReset's business, not this form's:
	// the rule lives with the row, and duplicating it here is two things to
	// keep in step. Validate must not invent a second opinion about it.
	in := resetInput{Token: "tok", Password: "short", PasswordConfirm: "short"}
	if errs := request.Validate(in); errs.Any() {
		t.Errorf("the form second-guessed the reset flow's strength rule: %v", errs)
	}
}

func TestResetSaysNothingAboutAnEmptyPassword(t *testing.T) {
	// Nothing typed at all is the browser's required attribute and the reset
	// flow's job. Reporting "the passwords do not match" on a blank form would
	// be true, useless, and about the wrong field.
	in := resetInput{Token: "tok"}
	if errs := request.Validate(in); errs.Any() {
		t.Errorf("a blank form produced a message about matching: %v", errs)
	}
}

// ── whitespace in passwords ──

// Leading and trailing whitespace must never be significant in a password.
//
// The bug this replaces was invisible and permanent: registering with
// "secretpass " (a fat-fingered space bar, a copy-paste that caught one, a
// phone keyboard adding one after autocomplete) created the account, and every
// later attempt at "secretpass" was refused with nothing on screen able to
// explain why. Confirmed against the running site before the change — 401 for
// the password the member thought they chose, 303 for the stray space.
//
// The property that makes trimming safe is that it happens at EVERY point a
// password is accepted. Trimming where it is set but not where it is checked,
// or the reverse, is what locks people out — so this asserts all three
// together rather than one at a time.
func TestWhitespaceIsNeverSignificantInAPassword(t *testing.T) {
	const typed = "  correct horse battery  "
	const want = "correct horse battery"

	post := func(t *testing.T, path string, form url.Values) *gin.Context {
		t.Helper()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return c
	}

	t.Run("register", func(t *testing.T) {
		in, err := readRegisterInput(post(t, "/register", url.Values{
			"username": {"newmember"}, "password": {typed}, "password_confirm": {typed},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if in.Password != want {
			t.Errorf("password bound as %q, want %q", in.Password, want)
		}
		if in.PasswordConfirm != want {
			t.Errorf("confirmation bound as %q, want %q", in.PasswordConfirm, want)
		}
	})

	t.Run("login", func(t *testing.T) {
		in, err := readLoginInput(post(t, "/login", url.Values{
			"username": {"newmember"}, "password": {typed},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if in.Password != want {
			t.Errorf("password bound as %q, want %q — an account created with the "+
				"trimmed form could not be logged into", in.Password, want)
		}
	})

	t.Run("reset", func(t *testing.T) {
		in, err := readResetInput(post(t, "/reset", url.Values{
			"token": {"t"}, "password": {typed}, "password_confirm": {typed},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if in.Password != want || in.PasswordConfirm != want {
			t.Errorf("reset bound %q / %q, want %q on both",
				in.Password, in.PasswordConfirm, want)
		}
	})
}

// Internal spaces are the ones that carry the entropy, and they must survive.
func TestInternalSpacesInAPasswordAreKept(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	form := url.Values{"username": {"u"}, "password": {"correct horse battery staple"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	in, err := readLoginInput(c)
	if err != nil {
		t.Fatal(err)
	}
	if in.Password != "correct horse battery staple" {
		t.Errorf("password bound as %q — a passphrase lost its spaces", in.Password)
	}
}

// The username was already trimmed and must stay that way. Asserted because it
// is the half of this that was NOT broken, and a change to the binder could
// take it out without anything else noticing.
func TestAUsernameIsTrimmed(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	form := url.Values{"username": {" alice "}, "password": {"x"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	in, err := readLoginInput(c)
	if err != nil {
		t.Fatal(err)
	}
	if in.Username != "alice" {
		t.Errorf("username bound as %q, want %q", in.Username, "alice")
	}
}
