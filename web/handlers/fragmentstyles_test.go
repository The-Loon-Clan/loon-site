package handlers

import (
	"html/template"
	"strings"
	"testing"
)

func TestHoistFragmentStyles(t *testing.T) {
	for _, c := range []struct {
		name, in, wantBody, wantStyles string
	}{
		{"no styles", "<p>hello</p>", "<p>hello</p>", ""},
		{"one block",
			`<style>.a{color:red}</style><p>hi</p>`,
			"<p>hi</p>", "<style>.a{color:red}</style>\n"},
		{"attributes on the tag",
			`<style type="text/css">.a{}</style><p>hi</p>`,
			"<p>hi</p>", "<style type=\"text/css\">.a{}</style>\n"},
		{"uppercase",
			`<STYLE>.a{}</STYLE><p>hi</p>`,
			"<p>hi</p>", "<STYLE>.a{}</STYLE>\n"},
		{"spans lines",
			"<style>\n.a{color:red}\n</style>\n<p>hi</p>",
			"\n<p>hi</p>", "<style>\n.a{color:red}\n</style>\n"},
	} {
		body, styles := hoistFragmentStyles(template.HTML(c.in))
		if string(body) != c.wantBody {
			t.Errorf("%s: body = %q, want %q", c.name, body, c.wantBody)
		}
		if string(styles) != c.wantStyles {
			t.Errorf("%s: styles = %q, want %q", c.name, styles, c.wantStyles)
		}
	}
}

// Two blocks must come out in the order they went in, or a fragment whose
// second rule overrides its first stops working the moment they are hoisted.
func TestHoistFragmentStylesKeepsOrder(t *testing.T) {
	in := `<style>.a{color:red}</style><p>x</p><style>.a{color:blue}</style>`
	body, styles := hoistFragmentStyles(template.HTML(in))
	if string(body) != "<p>x</p>" {
		t.Fatalf("body = %q", body)
	}
	red, blue := strings.Index(string(styles), "red"), strings.Index(string(styles), "blue")
	if red == -1 || blue == -1 || red > blue {
		t.Fatalf("order lost: %q", styles)
	}
}
