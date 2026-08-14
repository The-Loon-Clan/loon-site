package request

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// Binding a form into a struct by FIELD NAME.
//
// The point of the exercise is that the struct is the only place the wire names
// are written. That makes snake() load-bearing: get it wrong and a field
// silently reads empty, which is the exact failure the whole arrangement exists
// to remove.

func post(t *testing.T, form url.Values, in any) error {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Request = req
	return Bind(c, in)
}

func TestFieldNamesBecomeSnakeCaseKeys(t *testing.T) {
	for name, want := range map[string]string{
		"PrivateProfile": "private_profile",
		"Username":       "username",
		"RegMode":        "reg_mode",
		"AmountRaw":      "amount_raw",
		"ID":             "id",
		"APIKey":         "api_key",
		"NZBName":        "nzb_name",
		"To":             "to",
		"Enabled2FA":     "enabled2fa", // no break after a digit
		"Base64Data":     "base64_data",
	} {
		if got := snake(name); got != want {
			t.Errorf("snake(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAStructBindsWithNoTagsAtAll(t *testing.T) {
	var in struct {
		PrivateProfile bool
		Username       string
		Amount         int
	}
	err := post(t, url.Values{
		"private_profile": {"1"},
		"username":        {"alice"},
		"amount":          {"42"},
	}, &in)
	if err != nil {
		t.Fatal(err)
	}
	if !in.PrivateProfile || in.Username != "alice" || in.Amount != 42 {
		t.Errorf("bound %+v", in)
	}
}

func TestAnUntickedCheckboxIsFalse(t *testing.T) {
	// An unticked box posts NOTHING — not "0", not "off". Absence has to mean
	// false, or a member can never turn anything off.
	var in struct{ PrivateProfile bool }
	if err := post(t, url.Values{}, &in); err != nil {
		t.Fatal(err)
	}
	if in.PrivateProfile {
		t.Error("an absent checkbox bound as true")
	}
}

func TestTheSpellingsACheckboxActuallySends(t *testing.T) {
	// "on" is what a checkbox with no value attribute posts, and it is the one
	// a hand-written template is most likely to produce by accident.
	for _, v := range []string{"1", "true", "yes", "on", "ON", "True"} {
		var in struct{ PrivateProfile bool }
		if err := post(t, url.Values{"private_profile": {v}}, &in); err != nil {
			t.Fatal(err)
		}
		if !in.PrivateProfile {
			t.Errorf("%q did not bind as true", v)
		}
	}
	for _, v := range []string{"0", "false", "no", "off", ""} {
		var in struct{ PrivateProfile bool }
		if err := post(t, url.Values{"private_profile": {v}}, &in); err != nil {
			t.Fatal(err)
		}
		if in.PrivateProfile {
			t.Errorf("%q bound as true", v)
		}
	}
}

func TestValuesAreTrimmedExceptWhereTheyMustNotBe(t *testing.T) {
	// Trimming is right for almost everything — a leading space in a pasted
	// value is an artefact. A password is the exception, and it says so.
	var in struct {
		Username string
		Password string `form:",raw"`
	}
	err := post(t, url.Values{"username": {"  alice  "}, "password": {"  spaces  "}}, &in)
	if err != nil {
		t.Fatal(err)
	}
	if in.Username != "alice" {
		t.Errorf("username = %q, want it trimmed", in.Username)
	}
	if in.Password != "  spaces  " {
		t.Errorf("password = %q, want exactly what was typed", in.Password)
	}
}

func TestAHandlerOnlyFieldIsNeverReadFromTheForm(t *testing.T) {
	// `form:"-"` is how a field says the submitter does not get to set it. Here
	// the form tries anyway.
	var in struct {
		RegMode string `form:"-"`
	}
	if err := post(t, url.Values{"reg_mode": {"open"}}, &in); err != nil {
		t.Fatal(err)
	}
	if in.RegMode != "" {
		t.Errorf("RegMode = %q — a form:\"-\" field was filled from the request", in.RegMode)
	}
}

func TestAnExplicitNameWinsOverTheConvention(t *testing.T) {
	// For wire names that are not ours to choose.
	var in struct {
		Captcha string `form:"cf-turnstile-response"`
	}
	if err := post(t, url.Values{"cf-turnstile-response": {"token"}}, &in); err != nil {
		t.Fatal(err)
	}
	if in.Captcha != "token" {
		t.Errorf("Captcha = %q", in.Captcha)
	}
}

func TestARubbishNumberBindsToZeroRatherThanFailing(t *testing.T) {
	// "abc" in a number box is the member's mistake, and Validate is where a
	// member's mistake gets a message. Bind failing here would turn a typo into
	// a 500, or into an error message written by the wrong layer.
	var in struct{ Amount int }
	if err := post(t, url.Values{"amount": {"abc"}}, &in); err != nil {
		t.Fatalf("a non-numeric amount made Bind fail: %v", err)
	}
	if in.Amount != 0 {
		t.Errorf("Amount = %d, want 0", in.Amount)
	}
}

func TestQueryValuesBindToo(t *testing.T) {
	// Some forms are GETs — the search and filter pages are the obvious ones.
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/?username=bob&amount=7", nil)

	var in struct {
		Username string
		Amount   int
	}
	if err := Bind(c, &in); err != nil {
		t.Fatal(err)
	}
	if in.Username != "bob" || in.Amount != 7 {
		t.Errorf("bound %+v from the query string", in)
	}
}

func TestAnUnsupportedFieldTypeIsAnError(t *testing.T) {
	// Loud rather than silent. A field of a type Bind does not understand would
	// otherwise stay at its zero value on every request, which looks exactly
	// like a form that is not being filled in.
	var in struct{ When float64 }
	err := post(t, url.Values{"when": {"1.5"}}, &in)
	if err == nil {
		t.Fatal("an unsupported field type bound without complaint")
	}
	if !strings.Contains(err.Error(), "When") || !strings.Contains(err.Error(), "when") {
		t.Errorf("error %q names neither the field nor its key", err)
	}
}

func TestBindRefusesWhatIsNotAStructPointer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	var notAStruct string
	if err := Bind(c, &notAStruct); err == nil {
		t.Error("Bind accepted a pointer to a string")
	}
	if err := Bind(c, struct{ X string }{}); err == nil {
		t.Error("Bind accepted a non-pointer")
	}
	if err := Bind(c, (*struct{ X string })(nil)); err == nil {
		t.Error("Bind accepted a nil pointer")
	}
}

func TestUnexportedFieldsAreLeftAlone(t *testing.T) {
	// reflect cannot set them, and attempting to would panic.
	var in struct {
		Username string
		secret   string
	}
	if err := post(t, url.Values{"username": {"alice"}, "secret": {"nope"}}, &in); err != nil {
		t.Fatal(err)
	}
	if in.Username != "alice" || in.secret != "" {
		t.Errorf("bound %+v", in)
	}
}
