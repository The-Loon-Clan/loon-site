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

// The nonce is what lets script-src drop 'unsafe-inline', and a plugin cannot
// know it. These are the properties the host has to hold up on its behalf.
func TestNonceFragmentScripts(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"plain inline script",
			`<script>go()</script>`, `<script nonce="N1">go()</script>`},
		{"attributes are kept",
			`<script type="module">go()</script>`, `<script type="module" nonce="N1">go()</script>`},
		{"external is left alone",
			`<script src="/static/x.js"></script>`, `<script src="/static/x.js"></script>`},
		{"an existing nonce is not doubled",
			`<script nonce="other">go()</script>`, `<script nonce="other">go()</script>`},
		{"no script at all",
			`<p>hi</p>`, `<p>hi</p>`},
		{"two scripts both get it",
			`<script>a()</script><p>x</p><script>b()</script>`,
			`<script nonce="N1">a()</script><p>x</p><script nonce="N1">b()</script>`},
	} {
		got := string(nonceFragmentScripts(template.HTML(c.in), "N1"))
		if got != c.want {
			t.Errorf("%s:\n  got  %s\n  want %s", c.name, got, c.want)
		}
	}
}

// An empty nonce means crypto/rand failed. Stamping nonce="" would look wired
// and run nothing; leaving the tag alone fails the same way but honestly.
func TestNoNonceStampsNothing(t *testing.T) {
	in := `<script>go()</script>`
	if got := string(nonceFragmentScripts(template.HTML(in), "")); got != in {
		t.Errorf("got %s, want it untouched", got)
	}
}
