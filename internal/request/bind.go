package request

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Bind fills a request struct from the request, deriving each wire key from the
// FIELD NAME. There is no mapping table and, in the ordinary case, no tags.
//
//	type settingsPrivacyInput struct {
//	    PrivateProfile bool
//	}
//
//	var in settingsPrivacyInput
//	request.Bind(c, &in)   // reads private_profile
//
// Why not gin's own binder: it falls back to the field name VERBATIM
// (`PrivateProfile`), so every field whose wire name is snake_case needs a
// `form:"private_profile"` tag. That is a mapping table again — the same
// strings, spread across the struct instead of across the handlers. This
// snake-cases the field name, so the struct is the whole declaration.
//
// SCOPE: form and query values. JSON bodies are not handled here — this site
// has one JSON endpoint and gin's binder with json tags is the right tool for
// it. Pretending otherwise would mean a second convention nobody could predict.
//
// Values are TRIMMED by default, because a leading space in a pasted value is
// an artefact rather than intent. A field that must keep exactly what was typed
// says so:
//
//	Password string `form:",raw"`
//
// which is not decoration: a password of spaces is a password, and trimming one
// signs somebody up with something other than what they typed, then refuses to
// let them back in.
func Bind(c *gin.Context, in any) error {
	rv := reflect.ValueOf(in)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("request.Bind: want a non-nil pointer to a struct, got %T", in)
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("request.Bind: want a pointer to a struct, got pointer to %s", rv.Kind())
	}
	return bindStruct(rv, func(key string) (string, bool) {
		if v, ok := c.GetPostForm(key); ok {
			return v, true
		}
		return c.GetQuery(key)
	}, func(key string) []string {
		if v := c.PostFormArray(key); len(v) > 0 {
			return v
		}
		return c.QueryArray(key)
	})
}

// bindStruct is Bind without gin, so the mapping can be tested directly.
func bindStruct(rv reflect.Value, get func(string) (string, bool), getAll func(string) []string) error {
	rt := rv.Type()
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		key, raw, skip := fieldKey(f)
		if skip {
			continue
		}
		fv := rv.Field(i)

		if f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.String {
			fv.Set(reflect.ValueOf(getAll(key)))
			continue
		}

		s, present := get(key)
		if !raw {
			s = strings.TrimSpace(s)
		}
		if err := setField(fv, s, present); err != nil {
			return fmt.Errorf("request.Bind: field %s (%s): %w", f.Name, key, err)
		}
	}
	return nil
}

// fieldKey decides the wire name for a field.
//
// `form:"-"` skips it — for fields the handler fills itself, like a mode read
// from site settings rather than from the form.
func fieldKey(f reflect.StructField) (key string, raw, skip bool) {
	tag, hasTag := f.Tag.Lookup("form")
	name, opts, _ := strings.Cut(tag, ",")
	raw = slicesContains(strings.Split(opts, ","), "raw")

	switch {
	case hasTag && name == "-":
		return "", raw, true
	case hasTag && name != "":
		return name, raw, false
	default:
		return snake(f.Name), raw, false
	}
}

func setField(fv reflect.Value, s string, present bool) error {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(s)
	case reflect.Bool:
		// A checkbox posts "on" by default and "1" where the template sets a
		// value; an unticked box posts NOTHING. Absence is false, which is the
		// whole reason a form cannot be read by iterating what arrived.
		fv.SetBool(present && truthy(s))
	case reflect.Int, reflect.Int64:
		if s == "" {
			fv.SetInt(0)
			return nil
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			// NOT an error from Bind: "abc" in a number box is something the
			// member did, and Validate is where a member's mistake gets a
			// message. Bind reports only what the PROGRAM got wrong.
			fv.SetInt(0)
			return nil
		}
		fv.SetInt(n)
	default:
		return fmt.Errorf("unsupported kind %s — add it to setField, or give the "+
			"field a type Bind understands", fv.Kind())
	}
	return nil
}

// truthy accepts what a checkbox or a hand-written client actually sends.
// Deliberately the same spellings as internal/config's flags, plus "on", which
// is what a checkbox with no value attribute posts.
func truthy(s string) bool {
	switch strings.ToLower(s) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// snake turns a Go field name into its wire form: PrivateProfile →
// private_profile, RegMode → reg_mode, ID → id, AmountRaw → amount_raw.
//
// Runs of capitals stay together, so APIKey becomes api_key rather than
// a_p_i_key, and Base64Data becomes base64_data.
//
// Where a name is genuinely ambiguous — capitals next to digits are the usual
// case — do not argue with the convention: give the field a `form:"…"` tag and
// the wire name stops being a guess.
func snake(name string) string {
	var b strings.Builder
	rs := []rune(name)
	for i, r := range rs {
		if r >= 'A' && r <= 'Z' {
			prevLower := i > 0 && rs[i-1] >= 'a' && rs[i-1] <= 'z'
			nextLower := i+1 < len(rs) && rs[i+1] >= 'a' && rs[i+1] <= 'z'
			// No break after a DIGIT: Enabled2FA reads better as enabled2fa
			// than enabled2_fa, and Base64Data still gets its underscore from
			// nextLower. Digits beside capitals are where any convention starts
			// guessing, which is what the form tag is for.
			if i > 0 && (prevLower || nextLower) {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func slicesContains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
