package handlers

import (
	"os"
	"strings"
	"testing"
)

// TestNavConditionOffTracksTheFlavour is the regression for /admin/nav offering
// a live link to /tracker on an indexer-flavoured site: the entry is in the
// menu table (correctly -- it returns when the flavour changes), the chrome
// does not draw it, and the editor linked it anyway. The operator got a 404
// from their own admin page, with nothing saying why.
func TestNavConditionOffTracksTheFlavour(t *testing.T) {
	prev := siteFlavour()
	t.Cleanup(func() { flavourMode.Store(prev) })

	for _, c := range []struct {
		flavour string
		wantOff bool
	}{
		{FlavourIndexer, true},  // no tracker routes: the link would 404
		{FlavourTorrent, false}, // tracker mounted
		{FlavourBoth, false},
	} {
		flavourMode.Store(c.flavour)
		if got := navConditionOff("/tracker"); got != c.wantOff {
			t.Errorf("flavour %q: navConditionOff(/tracker) = %v, want %v",
				c.flavour, got, c.wantOff)
		}
	}
}

// TestNavConditionOffIgnoresUngatedEntries: an entry with no condition is never
// "off". Only the operator's own Hidden checkbox applies to those, and saying
// "off - site flavour" about one would be a lie.
func TestNavConditionOffIgnoresUngatedEntries(t *testing.T) {
	for _, href := range []string{"/browse", "/news", "/pages/privacy", ""} {
		if navConditionOff(href) {
			t.Errorf("navConditionOff(%q) = true; nothing gates it", href)
		}
	}
}

// TestNavEditorDoesNotLinkSuppressedTargets pins the template half. The Go
// flag is useless if the row goes back to linking unconditionally, and that is
// a one-character edit away.
func TestNavEditorDoesNotLinkSuppressedTargets(t *testing.T) {
	b, err := os.ReadFile("../templates/admin_nav.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `{{if .Off}}`) {
		t.Error("the menu editor's 'Links to' cell no longer branches on .Off; " +
			"a flavour-suppressed entry is being linked again")
	}
}
