package handlers

import (
	"html/template"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	site "github.com/the-loon-clan/loon-site"
)

// suggestTmpl parses the chrome, which is where the fragment lives — beside the
// region it replaces, per docs/ASYNC.md rule 3. It is not its own file because a
// standalone page template must define "content", and a dropdown has no
// business defining a page body.
func suggestTmpl(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("site_chrome.html").
		Funcs(tmplHelpers()).ParseFS(site.FS, "web/templates/site_chrome.html"))
}

// The dropdown's markup, rendered rather than read.
//
// html/template streams: a field the markup wants and the data lacks aborts the
// render part way through, and the caller gets a 200 carrying half a fragment.
// For a fragment that is swapped into a live page that is worse than for a
// page — the reader keeps whatever was there before, mixed with whatever
// arrived.
func TestSuggestFragmentRenders(t *testing.T) {
	tmpl := suggestTmpl(t)

	var sb strings.Builder
	if err := tmpl.ExecuteTemplate(&sb, "suggest", map[string]any{
		"Suggestions": []suggestVM{
			{Title: "Breaking Bad", Kind: "tv", Year: 2008},
			{Title: "A Film With No Year", Kind: "movie", Year: 0},
		},
	}); err != nil {
		t.Fatal(err)
	}
	got := sb.String()

	for _, want := range []string{
		"Breaking Bad",
		`role="listbox"`,
		"2008",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("suggest fragment missing %q", want)
		}
	}

	// The link is asserted by shape rather than by literal, because the literal
	// is not what it looks like: url.QueryEscape turns the space into "+", and
	// html/template then writes that as "&#43;" in an attribute. The browser
	// decodes it back, so the URL is right — but an assertion on
	// `q=Breaking+Bad` fails against markup that is correct, which is the kind
	// of test that gets the CODE changed to satisfy it.
	href := regexp.MustCompile(`href="(/search\?q=[^"]*)"`).FindStringSubmatch(got)
	if href == nil {
		t.Fatal("no /search link in the fragment — a suggestion that goes nowhere")
	}
	if strings.Contains(href[1], " ") {
		t.Errorf("href %q carries a raw space — the title was not encoded", href[1])
	}
	// A zero year is a gap in the catalogue, not a year. Printing it would be a
	// claim the data does not support.
	if strings.Contains(got, ">0<") {
		t.Error("a zero year was rendered — that is 'unknown', not 1970")
	}
}

// Nothing to show renders NOTHING — not an empty list, not a "no results" box.
//
// The dropdown has no open state: the stylesheet hides it while it is :empty.
// So a fragment that returns an empty <ul> leaves a bordered empty box hanging
// under the search field, and one that returns "no matches" leaves it there
// while somebody is still typing the second letter of a word.
func TestSuggestFragmentIsEmptyWhenThereIsNothing(t *testing.T) {
	tmpl := suggestTmpl(t)
	for _, data := range []map[string]any{
		{},
		{"Suggestions": []suggestVM{}},
	} {
		var sb strings.Builder
		if err := tmpl.ExecuteTemplate(&sb, "suggest", data); err != nil {
			t.Fatal(err)
		}
		if s := strings.TrimSpace(sb.String()); s != "" {
			t.Errorf("rendered %q for %v, want nothing at all — the dropdown is "+
				"hidden by being :empty", s, data)
		}
	}
}

// The dropdown is an accelerator, never the only way through.
//
// If the form ever stops being a real GET to /search, the search box becomes
// JavaScript-only: no submit without htmx, nothing for a crawler, and nothing
// for anyone whose request for /search/suggest failed. The attributes that make
// it fast are additions to a working form, and this is what says so.
func TestQuickSearchStillSubmitsWithoutJavaScript(t *testing.T) {
	b, err := fs.ReadFile(site.FS, "web/templates/site_chrome.html")
	if err != nil {
		t.Fatal(err)
	}
	form := regexp.MustCompile(`(?s)<form class="quick-search".*?</form>`).Find(b)
	if form == nil {
		t.Fatal("no .quick-search form found — the scan is stale")
	}
	s := string(form)
	for _, want := range []string{`action="/search"`, `method="get"`, `type="submit"`} {
		if !strings.Contains(s, want) {
			t.Errorf("the quick-search form lost %s — it is now JavaScript-only", want)
		}
	}

	// And the three attributes that carry the speed, each of which is silently
	// survivable: without the debounce it still works but sends a request per
	// keystroke, and without hx-sync it still works until two answers race and
	// the older one lands last.
	for attr, why := range map[string]string{
		"delay:150ms":                       "the debounce — without it every keystroke is a request",
		"hx-sync":                           "the in-flight abort — without it a slow answer can overwrite a newer one",
		`hx-target="#quick-search-suggest"`: "the target — without it the fragment replaces the input",
	} {
		if !strings.Contains(s, attr) {
			t.Errorf("quick-search lost %s: %s", attr, why)
		}
	}
}
