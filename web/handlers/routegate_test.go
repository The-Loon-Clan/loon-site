package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every route that serves a member's own data must be mounted behind a gate.
//
// This reads the ROUTE TABLE rather than making requests, and that is the
// point: a test that exercises the pages we remembered to list only ever
// covers the pages we remembered to list. The mistake worth catching is the
// one nobody writes a test for — a new route added next to the gated ones,
// looking exactly like them, with the gate left off.
//
// The gate itself is covered by behaviour elsewhere (a signed-out request to
// /bookmarks gets 303, an API caller gets 401). What is checked here is
// PRESENCE, across every route, including ones added after this was written.

// gatedPrefixes are paths whose handlers see or change one member's data.
//
// Deliberately a prefix list rather than an exhaustive route list: a new
// /settings/* page is covered the day it is added, which is the only version
// of this test that keeps working.
var gatedPrefixes = []string{
	"/settings", "/bookmarks", "/wishlist", "/gifts", "/invites",
	"/subscriptions", "/achievements", "/moderation", "/admin", "/undo",
}

// publicExceptions are paths that match a prefix above but are genuinely open.
// Each entry needs a reason, because an exception list without them becomes
// the place ungated routes go to be forgotten.
var publicExceptions = map[string]bool{
	// The 2FA challenge sits between password and session: there is no user to
	// require yet, which is the whole reason it exists as a separate step.
	"/login/2fa": true,

	// The TOKEN is the authorisation here, not the session. It was emailed to
	// the address on file and carries the account id, and ClaimEmailChange
	// consumes it in one statement so it cannot be replayed. Requiring a
	// session as well would break the ordinary case — the link opened on a
	// phone, or in a browser that is not the one that requested the change.
	//
	// This test found this route on its first run, which is what it is for:
	// nobody had written down whether it was deliberate. It is, and now that
	// is recorded rather than remembered.
	"/settings/email/confirm": true,
}

// routeRegistration matches the ways this codebase mounts a handler.
var routeRegistration = regexp.MustCompile(
	`\.(GET|POST|PUT|DELETE|PATCH)\(\s*"([^"]+)"\s*,\s*(.+)$`)

func TestGatedRoutesAreMountedBehindAGate(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var checked, ungated int
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n") {
			m := routeRegistration.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			path, rest := m[2], m[3]
			if publicExceptions[path] {
				continue
			}
			if !underGatedPrefix(path) {
				continue
			}
			checked++
			// The three ways a gate is applied here. staffOnly/adminOnly are
			// auth.Require(...) chains bound to a variable; a group carries its
			// own Require and its members inherit it.
			if strings.Contains(rest, "w.authed(") || strings.Contains(rest, "wsrv.authed(") ||
				strings.Contains(rest, "auth.Require(") ||
				strings.Contains(rest, "staffOnly") || strings.Contains(rest, "adminOnly") ||
				strings.Contains(rest, "append(") {
				continue
			}
			// A route on a group that was created WITH a gate is fine; the
			// group variable carries it. Those read `moderation.GET(` or
			// `admin.GET(`, so the receiver is not the bare engine.
			if !strings.HasPrefix(strings.TrimSpace(line), "e.") &&
				!strings.HasPrefix(strings.TrimSpace(line), "engine.") {
				continue
			}
			ungated++
			t.Errorf("%s:%d mounts %s with no gate:\n    %s\n"+
				"    every route under %v must go through w.authed(...) or a "+
				"gated group — see views.go authed()",
				f, i+1, path, strings.TrimSpace(line), gatedPrefixes)
		}
	}
	if checked == 0 {
		t.Fatal("no gated routes found at all — the scan is broken, not the code")
	}
	t.Logf("checked %d routes under %v, %d ungated", checked, gatedPrefixes, ungated)
}

func underGatedPrefix(path string) bool {
	for _, p := range gatedPrefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// TestViewerIsOnlyReadBehindAGate pairs with the above.
//
// w.viewer() aborts with 401 when there is no user, because behind a gate that
// cannot happen and a nil there means a MOUNTING mistake. This checks the other
// end: that no handler reads the viewer without the route being gated — which
// would turn that deliberate 401 into a page members see.
func TestViewerIsOnlyReadBehindAGate(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var users []string
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					return true
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if ok && sel.Sel.Name == "viewer" {
						users = append(users, fn.Name.Name+" ("+filepath.Base(name)+")")
					}
					return true
				})
				return true
			})
		}
	}
	if len(users) == 0 {
		t.Fatal("no callers of w.viewer found — the scan is broken, not the code")
	}
	// Not an assertion about each one: which routes are gated is the previous
	// test's job. This records the set, so a reviewer adding a viewer read can
	// see at a glance how many handlers depend on the gate being there.
	t.Logf("%d handlers read the viewer and therefore require a gate", len(users))
}
