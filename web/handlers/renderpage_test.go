package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Every page name a handler renders must be a page that was parsed.
//
// renderStatus looks the name up in w.tmpls and, on a miss, answers 500 with
// "unknown page". Nothing else notices: the name is a string literal, so a typo
// compiles, vets, and passes every test in templates_test.go — which checks the
// templates themselves, not the names handlers call them by. The failure only
// appears when somebody opens that one page.
//
// The realistic version is not a typo but a rename. A page file renamed on disk
// and in pageTemplates leaves the call site pointing at the old name, and the
// two tests either side of this one both stay green: the file exists, the list
// matches the directory, and the only broken thing is the sentence joining them.

// pageNameWrappers are functions that take a page name and hand it to render.
// The key is the function name, the value the argument index holding the page.
//
// These exist because the scan below first reported them as "computed page
// name — not checked here", and the three prose pages behind sitePagePlain
// turned out to be checked by nothing at all: their names are literals at a
// sitePagePlain(...) call, which is not a render(...) call, so both this test
// and every other one walked straight past them.
//
// Any OTHER computed page name is a hard failure rather than a note, because
// that is the same hole reopening somewhere this list does not know about.
var pageNameWrappers = map[string]int{
	"sitePagePlain": 0,
}

func wrapper(name string) bool { _, ok := pageNameWrappers[name]; return ok }

// parseNonTestFiles parses this package's non-test sources.
//
// Written out rather than using parser.ParseDir, which is deprecated: it
// associates files with packages without considering build tags. That does not
// bite here — this package has no tagged files — but the tests that read the
// source are exactly the ones that must not quietly skip a file, so the
// deprecation is worth taking seriously rather than silencing.
func parseNonTestFiles(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("no source files parsed — the scan is broken, not the code")
	}
	return fset, files
}

func TestEveryRenderedPageNameIsParsed(t *testing.T) {
	parsed := map[string]bool{}
	for _, p := range pageTemplates {
		parsed[p] = true
	}

	fset, files := parseNonTestFiles(t)

	// check reports one page name, wherever it was found.
	var checked int
	check := func(name string, pos token.Position, via string) {
		checked++
		if parsed[name] {
			return
		}
		t.Errorf("%s:%d renders %q%s, which is not in pageTemplates:\n"+
			"    that request answers 500 \"unknown page\" — see renderStatus in views.go.\n"+
			"    add it to pageTemplates, or fix the name to match the page it means",
			filepath.Base(pos.Filename), pos.Line, name, via)
	}

	// literalArg pulls a string literal out of a call's Nth argument.
	literalArg := func(call *ast.CallExpr, i int) (string, bool) {
		if len(call.Args) <= i {
			return "", false
		}
		lit, ok := call.Args[i].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		return s, err == nil
	}

	for _, file := range files {
		// Walking FuncDecls rather than the whole file, because which function a
		// computed name sits in is what decides whether it is covered elsewhere
		// or is a new hole.
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
				if !ok {
					return true
				}
				// A wrapper's own call sites carry the literal.
				if arg, isWrapper := pageNameWrappers[sel.Sel.Name]; isWrapper {
					if name, ok := literalArg(call, arg); ok {
						check(name, fset.Position(call.Args[arg].Pos()),
							" via "+sel.Sel.Name)
					}
					return true
				}
				// render(c, page, data), renderStatus(c, status, page, data).
				var arg int
				switch sel.Sel.Name {
				case "render":
					arg = 1
				case "renderStatus":
					arg = 2
				default:
					return true
				}
				if name, ok := literalArg(call, arg); ok {
					check(name, fset.Position(call.Args[arg].Pos()), "")
					return true
				}
				// Not a literal. Allowed only where the name provably comes
				// from somewhere this test already checks.
				pos := fset.Position(call.Pos())
				switch {
				case fn.Name.Name == "render":
					// render forwarding to renderStatus — same argument.
				case wrapper(fn.Name.Name):
					// A wrapper; its call sites were checked above.
				default:
					t.Errorf("%s:%d in %s renders a computed page name that "+
						"nothing checks:\n    either pass a literal, or add %s to "+
						"pageNameWrappers with the argument index that holds the "+
						"page, so its call sites are checked instead",
						filepath.Base(pos.Filename), pos.Line, fn.Name.Name, fn.Name.Name)
				}
				return true
			})
			return true
		})
	}

	if checked == 0 {
		t.Fatal("no render call sites found at all — the scan is broken, not the code")
	}
	t.Logf("checked %d rendered page names against %d parsed pages", checked, len(pageTemplates))
}

// The other direction: a page parsed at boot that nothing renders.
//
// Not a failure — a page can be reached through a plugin, or through
// renderStatus with a computed name — so this reports rather than fails. It is
// here because the two ways a page dies are being renamed out from under its
// call site and being orphaned when the route that reached it was removed, and
// the second leaves no symptom at all.
func TestParsedPagesThatNothingRenders(t *testing.T) {
	rendered := map[string]bool{}

	_, files := parseNonTestFiles(t)
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(lit.Value); err == nil {
				rendered[s] = true
			}
			return true
		})
	}

	var orphans []string
	for _, p := range pageTemplates {
		if !rendered[p] {
			orphans = append(orphans, p)
		}
	}
	if len(orphans) > 0 {
		t.Logf("parsed at boot but named nowhere in the package: %v", orphans)
	}
}
