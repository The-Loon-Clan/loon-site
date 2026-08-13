package site

import (
	"io/fs"
	"path"
	"strings"
	"testing"
)

// A malformed CSS comment does not fail loudly. It silently eats the
// declarations that follow it until the parser finds something it can
// resynchronise on, and everything else keeps working — so the symptom is one
// feature quietly missing, not an error.
//
// This test exists because that happened: a comment was extended by writing
// prose after the closing */ instead of before it, leaving thirteen lines of
// naked text and a stray */ in the middle of a theme's declaration block. The
// theme still loaded, every other token still applied, and the only visible
// effect was that --body-wash was gone and the page canvas went flat. It cost
// a build, a deploy, two screenshots and an isolated repro in a scratch file
// to find, and the CSS had been served correctly the whole time.
//
// Checking the whole embedded stylesheet set costs microseconds.
func TestCSSCommentsAreBalanced(t *testing.T) {
	var checked int
	err := fs.WalkDir(siteFS, "web/static/css", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".css") {
			return nil
		}
		// Vendored, minified, and not ours to reformat.
		if strings.Contains(path.Base(p), "bootstrap") {
			return nil
		}
		b, err := fs.ReadFile(siteFS, p)
		if err != nil {
			return err
		}
		checked++
		checkCSSComments(t, p, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walk css: %v", err)
	}
	if checked == 0 {
		t.Fatal("no stylesheets checked — the embed path is wrong, so this test proves nothing")
	}
	t.Logf("checked %d stylesheets", checked)
}

// TestCSSCommentCheckCatchesTheRealBug runs the detector against the exact
// shape of the mistake it was written for. A guard that has never been shown
// to fire is a guard nobody should trust — and this one passed on the first
// run against a tree that was, at that moment, still broken.
func TestCSSCommentCheckCatchesTheRealBug(t *testing.T) {
	cases := map[string]string{
		"stray close after a closed comment": `:root {
    /* a comment that closes here. */
       and then prose that is not in a comment at all,
       ending with a stray close. */
    --body-wash: linear-gradient(180deg, #fff, #000);
}`,
		"unclosed opener": `:root {
    /* this one never closes
    --body-wash: none;
}`,
	}
	for name, css := range cases {
		t.Run(name, func(t *testing.T) {
			var probe testing.T
			checkCSSComments(&probe, "probe.css", css)
			if !probe.Failed() {
				t.Errorf("detector did not fire on %q — it would not have caught "+
					"the bug it exists for", name)
			}
		})
	}
}

// checkCSSComments scans once, left to right, tracking whether it is inside a
// comment. Counting "/*" against "*/" is NOT equivalent and reports false
// failures: a "/*" written inside a comment body is ordinary text, and one of
// this site's stylesheets legitimately contains one.
func checkCSSComments(t *testing.T, name, s string) {
	t.Helper()
	line := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line++
			continue
		}
		if i+1 >= len(s) {
			break
		}
		switch s[i : i+2] {
		case "/*":
			// Find the close, counting newlines on the way so the reported
			// line number points at the opener.
			open := line
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				t.Errorf("%s:%d: unclosed /* — everything after it is swallowed", name, open)
				return
			}
			line += strings.Count(s[i+2:i+2+j], "\n")
			i += 2 + j + 1 // land on the '/' of "*/"; the loop's i++ moves past
		case "*/":
			t.Errorf("%s:%d: stray */ outside a comment — the text before it is "+
				"being parsed as CSS and the declarations after it are lost", name, line)
		}
	}
}
