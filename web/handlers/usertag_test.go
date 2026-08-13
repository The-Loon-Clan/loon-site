package handlers

import (
	site "github.com/the-loon-clan/loon-demo-site"

	"bytes"
	"html/template"
	"log/slog"
	"strings"
	"testing"

	"github.com/the-loon-clan/loon/core"
)

// user-tag is rendered on nearly every page, by both parse sets, from data
// several different plugins own. These pin the two ways it silently produced
// wrong-but-plausible output.

// renderUserTag executes just the component, the way a page invokes it.
func renderUserTag(t *testing.T, arg map[string]any) string {
	t.Helper()
	w := &web{log: slog.Default(), tmpls: map[string]*template.Template{}}
	tmpl, err := template.New("t").Funcs(w.tmplFuncs()).ParseFS(site.FS,
		"web/templates/site_chrome.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var b bytes.Buffer
	if err := tmpl.ExecuteTemplate(&b, "user-tag", arg); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return b.String()
}

// TestUserTagPointerNameDoesNotLeakIntoHref is the bug this earned:
// {{print "/u/" .Name}} on a *string emits the POINTER — /u/0x1129d1d30910 —
// while {{.Name}} on its own line still prints "bob", because html/template
// auto-indirects a lone value but not one inside a multi-arg print. Nullable
// plugin columns are *string (the forum's LastPostUsername), so the row looked
// completely correct and every link on it was garbage.
func TestUserTagPointerNameDoesNotLeakIntoHref(t *testing.T) {
	name := "bob"
	out := renderUserTag(t, map[string]any{"Name": &name, "Role": "user"})
	if strings.Contains(out, "0x") {
		t.Errorf("a pointer address reached the output: %s", out)
	}
	if !strings.Contains(out, `href="/u/bob"`) {
		t.Errorf(`want href="/u/bob", got: %s`, out)
	}
	// The component pretty-prints, so the name is not tight against its tag —
	// assert it is the link's TEXT, not that it sits flush against "<".
	if !strings.Contains(out, "bob") {
		t.Errorf("want the name as link text, got: %s", out)
	}
	// A nil pointer must degrade to an empty name, not to "/u/<nil>".
	var missing *string
	out = renderUserTag(t, map[string]any{"Name": missing, "Role": "user"})
	if strings.Contains(out, "nil") || strings.Contains(out, "0x") {
		t.Errorf("nil name rendered as %q", out)
	}
}

// TestRoleSlugAcceptsThePointerShapeToo: roleSlug matched core.Role and string
// but not *string, so LastPostRole fell through every case and painted every
// last poster "member" — right-looking, uniformly wrong.
func TestRoleSlugAcceptsThePointerShapeToo(t *testing.T) {
	admin := "admin"
	for _, tc := range []struct {
		in   any
		want string
	}{
		{&admin, "admin"},
		{"moderator", "mod"},
		{core.RoleAdmin, "admin"},
		{(*string)(nil), "member"},
		{nil, "member"},
	} {
		if got := roleSlug(tc.in); got != tc.want {
			t.Errorf("roleSlug(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// roleLabel routes through roleSlug, so the pointer shape has to survive
	// the whole way to the title attribute.
	if got := roleLabel(&admin); got != "Admin" {
		t.Errorf("roleLabel(*string admin) = %q, want Admin", got)
	}
}

// TestEllipsisBoundsUntrustedTitles: a release title is arbitrary bytes off a
// Usenet header, so anything rendering one inline needs a bound.
func TestEllipsisBoundsUntrustedTitles(t *testing.T) {
	for _, tc := range []struct {
		n        int
		in, want string
	}{
		{10, "short", "short"},
		{5, "abcdefghij", "abcde…"},
		{0, "anything", ""},
		// Cuts at a space when one is NEAR the limit...
		{12, "abcdefghij kl", "abcdefghij…"},
		// ...but not by walking arbitrarily far back: a space at index 5 of a
		// 12-rune budget would throw away over half the allowance, so the cut
		// stays at the limit and lands mid-token.
		{12, "hello wonderful world", "hello wonder…"},
		// Runes, not bytes: slicing multi-byte text by byte index would emit
		// the replacement character.
		{3, "日本語テキスト", "日本語…"},
	} {
		if got := ellipsis(tc.n, tc.in); got != tc.want {
			t.Errorf("ellipsis(%d, %q) = %q, want %q", tc.n, tc.in, got, tc.want)
		}
	}
	// Never longer than the bound plus the one ellipsis rune.
	long := strings.Repeat("x", 500)
	if n := len([]rune(ellipsis(64, long))); n > 65 {
		t.Errorf("ellipsis(64) returned %d runes", n)
	}
}
