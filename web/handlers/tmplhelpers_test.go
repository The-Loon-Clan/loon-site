package handlers

import (
	"html/template"
	"testing"

	"github.com/the-loon-clan/loon-site/internal/markdown"
)

// These exercise tmplHelpers, which is the HANDLERS' template funcmap, so they
// stay here even though what they assert about is the markdown renderer: the
// point of both is that the template helper and the renderer are the same
// thing, and that can only be checked from the side that owns the funcmap.

// TestProseHelperIsTheSameRenderer: the {{prose}} template helper and the
// Deps.Markdown seam must not drift into two policies — the whole point of
// markdown_web.go is that there is exactly one. The plugin-rendered pages
// (tickets, DMs, announcements) reach the pipeline ONLY through this helper, so
// if it were ever pointed at a laxer renderer those pages would silently lose
// the sanitizer while the forum kept it.
func TestProseHelperIsTheSameRenderer(t *testing.T) {
	helper, ok := tmplHelpers()["prose"]
	if !ok {
		t.Fatal("no {{prose}} helper registered — the plugin pages render bodies through it")
	}
	fn, ok := helper.(func(string) template.HTML)
	if !ok {
		t.Fatalf("{{prose}} is %T, want func(string) template.HTML", helper)
	}
	for _, src := range []string{
		"a **b** and a [link](/wiki)",
		"[x](javascript:alert(1))",
		"<script>alert(1)</script>",
		"line one\nline two",
	} {
		if got, want := string(fn(src)), string(markdown.Render(src)); got != want {
			t.Errorf("{{prose}} diverged from Render for %q:\n got: %s\nwant: %s", src, got, want)
		}
	}
}

// cond is used in ARGUMENT position inside dict literals, where a type error
// is not a compile failure — it is an EXECUTE failure that truncates the page
// mid-document and still returns 200. It was declared func(bool, …) and got a
// string, which blanked two pages that looked fine by status code alone.
func TestCondMatchesTemplateTruthiness(t *testing.T) {
	fn, ok := tmplHelpers()["cond"].(func(a, b, c any) any)
	if !ok {
		t.Fatalf("cond is %T", tmplHelpers()["cond"])
	}
	for _, tc := range []struct {
		name string
		in   any
		want any
	}{
		{"true bool", true, "yes"},
		{"false bool", false, "no"},
		{"non-empty string", "Mar 2026", "yes"},
		{"empty string", "", "no"},
		{"nonzero int", 3, "yes"},
		{"zero int", 0, "no"},
		{"nil", nil, "no"},
		{"non-empty slice", []int{1}, "yes"},
		{"empty slice", []int{}, "no"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fn(tc.in, "yes", "no"); got != tc.want {
				t.Errorf("cond(%#v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
