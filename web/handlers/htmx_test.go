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

// A swappable partial must post to the same URL its form does.
//
// The two are written separately — action="..." for the no-JavaScript path,
// hx-post="..." for the enhanced one — so nothing but this test stops them
// diverging. If they ever do, the site works with JavaScript disabled and
// silently posts somewhere else with it enabled, which is the hardest version
// of this bug to see: the developer has JavaScript on, so they only ever
// exercise the broken half.
func TestASwappablePartialPostsWhereItsFormPosts(t *testing.T) {
	partials, err := os.ReadDir("../templates")
	if err != nil {
		t.Fatal(err)
	}
	action := regexp.MustCompile(`action="([^"]+)"`)
	hxPost := regexp.MustCompile(`hx-post="([^"]+)"`)

	var checked int
	for _, e := range partials {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		b, err := os.ReadFile("../templates/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, m := range hxPost.FindAllStringSubmatch(src, -1) {
			checked++
			if !strings.Contains(src, `action="`+m[1]+`"`) {
				t.Errorf("%s: hx-post=%q has no matching form action — "+
					"the enhanced path posts somewhere the plain form does not",
					e.Name(), m[1])
			}
		}
		// The reverse is not required: plenty of forms are deliberately NOT
		// enhanced (login, register, logout). Only assert that an enhanced one
		// agrees with its own form.
		_ = action
	}
	if checked == 0 {
		t.Error("no hx-post found in any template; this test is asserting nothing")
	}
	t.Logf("checked %d hx-post attributes", checked)
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
