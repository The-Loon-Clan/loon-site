package handlers

import (
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The async layer's invariants.
//
// These are cheap tests for expensive mistakes. The failures they describe are
// all silent — a control that posts to the wrong place, a fragment that drifts
// from the page it replaces, a token nobody attached — and every one of them is
// present in the prod indexer's tree today. See htmx.go for the numbers.

func TestIsHTMXWantsTheLiteralTrue(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   bool
	}{
		{"true", true},
		{"", false},      // absent: an ordinary form post
		{"false", false}, // htmx sends this for a boosted history restore
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/x", nil)
		if tc.header != "" {
			c.Request.Header.Set(hxRequestHeader, tc.header)
		}
		if got := isHTMX(c); got != tc.want {
			t.Errorf("isHTMX with %s:%q = %v, want %v", hxRequestHeader, tc.header, got, tc.want)
		}
	}
}

// The back button must get a whole page.
//
// htmx marks a history-restore request HX-Request: true as well, so a handler
// that checks only that header answers a back-navigation with the fragment it
// would send for a filter click — and the browser paints a bare results table
// where the site used to be. The failure needs a real back button to reproduce,
// which is exactly the interaction a test suite never performs.
func TestAHistoryRestoreIsNotAFragmentRequest(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/browse?cat=2000", nil)
	c.Request.Header.Set(hxRequestHeader, "true")
	c.Request.Header.Set(hxHistoryRestoreHeader, "true")

	if isHTMX(c) {
		t.Error("a history-restore request was treated as a fragment request; " +
			"the back button would render a bare fragment as the whole page")
	}
}

// A FORM's two submit paths must agree.
//
// The two are written separately -- action="..." for the no-JavaScript path,
// hx-post="..." for the enhanced one -- so nothing but this test stops them
// diverging. If they ever do, the site works with JavaScript disabled and
// silently posts somewhere else with it enabled, which is the hardest version
// of this bug to see: the developer has JavaScript on, so they only ever
// exercise the broken half.
//
// Scoped to elements that are actually forms, which is what the rule has always
// been about. An hx-post on something else is not a second submit path, because
// there is no first one -- and demanding a form action for it would have exactly
// one effect: whoever hit it would add a bogus action="" to quiet the test, and
// the no-JavaScript path would then post to a URL nobody meant. Non-form posts
// are checked instead by the case below, which is the narrower question.
func TestASwappablePartialPostsWhereItsFormPosts(t *testing.T) {
	var checked int
	forEachHXPost(t, func(file, tag, url, elem string) {
		if tag != "form" {
			return
		}
		checked++
		// THE SAME ELEMENT, not the same file. Searching the file was this
		// test's original reading and it let a real divergence through: this
		// page carries seven forms posting to /admin/widgets/apply, so a typo
		// in any ONE of their actions still found the string somewhere else
		// and passed. Six of the seven could have been wrong.
		if !strings.Contains(elem, `action="`+url+`"`) {
			t.Errorf("%s: <form hx-post=%q> does not carry action=%q -- "+
				"the enhanced path posts somewhere the plain form does not",
				file, url, url)
		}
	})
	// The reverse is not required: plenty of forms are deliberately NOT
	// enhanced (login, register, logout). Only assert that an enhanced one
	// agrees with its own form.
	if checked == 0 {
		t.Error("no hx-post found on any form; this test is asserting nothing")
	}
	t.Logf("checked %d form hx-post attributes", checked)
}

// readOnlyHXPosts are the endpoints allowed to be posted to from something that
// is not a form.
//
// An hx-post outside a form has no no-JavaScript path by construction, so it
// must not be how anything gets CHANGED -- that would be a write reachable one
// way only, which is the failure the test above exists to prevent, arriving
// through the door that test does not watch.
//
// A list rather than a rule because no test can read an endpoint and see that
// it writes. Naming them is the point: three lines of justification is a low
// bar, and it is a bar, which an unchecked "not a form, carry on" would not be.
var readOnlyHXPosts = map[string]string{
	// The widget page-rule preview (widgetpreview.go). Reads the rule out of
	// the box being typed in and answers which pages it reaches; touches no
	// store. Degrades by rendering the SAME partial from the saved rule when
	// the table draws, so the no-JavaScript operator is told the same thing one
	// save later rather than nothing at all.
	"/admin/widgets/preview": "reads a rule, writes nothing",
}

func TestANonFormHXPostIsReadOnly(t *testing.T) {
	forEachHXPost(t, func(file, tag, url, _ string) {
		if tag == "form" {
			return
		}
		if _, ok := readOnlyHXPosts[url]; !ok {
			t.Errorf("%s: <%s> posts to %q with no form around it. If that "+
				"endpoint writes, it is unreachable without JavaScript; if it "+
				"does not, say so in readOnlyHXPosts and say why.", file, tag, url)
		}
	})
	// The list must not outlive its entries: one left behind after its element
	// went away is a permission nobody is using and nobody will re-examine.
	seen := map[string]bool{}
	forEachHXPost(t, func(_, tag, url, _ string) {
		if tag != "form" {
			seen[url] = true
		}
	})
	for url := range readOnlyHXPosts {
		if !seen[url] {
			t.Errorf("readOnlyHXPosts names %q, which no template posts to any "+
				"more; drop it rather than leaving the exemption standing", url)
		}
	}
}

// forEachHXPost calls fn for every hx-post in every template, with the tag it
// sits on and that element's own opening tag.
//
// Element-aware rather than a bare attribute scan, because the tests above ask
// different things of a form and of anything else, and because "does a matching
// action exist" is a question about one element -- asking it of the whole file
// is what let a wrong action hide behind a right one elsewhere on the page.
func forEachHXPost(t *testing.T, fn func(file, tag, url, elem string)) {
	t.Helper()
	entries, err := os.ReadDir("../templates")
	if err != nil {
		t.Fatal(err)
	}
	// [^>]* stops at the element's own close, so the tag captured is the one
	// carrying the attribute rather than whatever opened earlier on the line.
	elem := regexp.MustCompile(`(?s)<([a-zA-Z0-9]+)\s[^>]*?hx-post="([^"]+)"[^>]*?>`)
	var found int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		b, err := os.ReadFile("../templates/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, m := range elem.FindAllStringSubmatch(src, -1) {
			found++
			fn(e.Name(), strings.ToLower(m[1]), m[2], m[0])
		}
	}
	if found == 0 {
		t.Fatal("no hx-post matched in any template; the scan itself is broken")
	}
}

// Every htmx control must sit inside a real form.
//
// This is rule 1 of docs/ASYNC.md made enforceable. A bare <button hx-post>
// works perfectly with JavaScript on and does nothing at all with it off, and
// nothing else in the build would ever tell you.
func TestEveryHXPostIsOnARealForm(t *testing.T) {
	entries, err := os.ReadDir("../templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		b, err := os.ReadFile("../templates/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, "hx-post=") {
				continue
			}
			// The attribute may sit a few lines below the opening tag, so the
			// check is that the element it belongs to is a <form ...>: either
			// this line opens one, or it is a continuation of one.
			if strings.Contains(line, "<form") || strings.HasPrefix(strings.TrimSpace(line), "hx-") {
				continue
			}
			t.Errorf("%s: hx-post on something that is not a form:\n    %s",
				e.Name(), strings.TrimSpace(line))
		}
	}
}
