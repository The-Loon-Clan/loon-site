package handlers

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

// A form key that has a name must be used by that name.
//
// Naming the keys is only worth anything if the names are what handlers
// actually use. One `c.PostForm("action")` left behind is a string nobody can
// find when the field is renamed, and the failure it produces is the quiet
// kind: an unmatched key reads as empty, so the handler carries on with a zero
// value and the member's input is dropped without an error anywhere.
//
// So this reads the package's own source and fails on a literal that duplicates
// a constant. It is the same device as the log-key vocabulary: the strings
// crossing a boundary are an interface, and this is what keeps the interface in
// one place.

// namedKeys maps a literal to the constant that should be used instead.
//
// Deliberately NOT derived by reflection over the const block: the point is to
// state which strings are spoken for, and a list that builds itself would also
// silently stop covering a constant somebody renamed.
// Note: `checked` is not here. It is a VALUE a checkbox posts, not a key a
// handler looks up, so it never appears as an accessor argument.
var namedKeys = map[string]string{
	"action":          "fieldAction",
	"id":              "fieldID",
	"next":            "fieldNext",
	"private_profile": "fieldPrivateProfile",
	"code":            "fieldCode",
	"err":             "queryErr",
	"saved":           "querySaved",
	"done":            "queryDone",
}

// accessorCalls are the calls whose FIRST argument is a form or query key.
var accessorCalls = map[string]bool{
	"PostForm": true, "Query": true, "DefaultQuery": true,
	"DefaultPostForm": true, "GetPostForm": true, "GetQuery": true,
	"PostFormArray": true, "QueryArray": true,
}

func TestNamedKeysAreNotWrittenAsLiterals(t *testing.T) {
	fset, files := parseNonTestFiles(t)

	var checked int
	for _, file := range files {
		name := filepath.Base(fset.Position(file.Pos()).Filename)
		// formkeys.go is where the names are DEFINED; the literals there are
		// the definitions.
		if name == "formkeys.go" {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !accessorCalls[sel.Sel.Name] {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true // already a constant, or computed — both fine
			}
			key, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			checked++
			if constName, named := namedKeys[key]; named {
				pos := fset.Position(lit.Pos())
				t.Errorf("%s:%d reads %s(%q) — use %s.\n"+
					"    A renamed field leaves a literal like this behind, and an "+
					"unmatched key reads as empty rather than as an error: the "+
					"handler carries on with a zero value and the input is dropped.",
					name, pos.Line, sel.Sel.Name, key, constName)
			}
			return true
		})
	}

	if checked == 0 {
		t.Fatal("no form/query accessors found at all — the scan is broken, not the code")
	}
	t.Logf("checked %d literal form and query keys against %d named ones", checked, len(namedKeys))
}

func TestEveryNamedKeyIsActuallyUsed(t *testing.T) {
	// The other direction. A constant nothing reads is a name for a field that
	// no longer exists, and it makes the vocabulary above look more complete
	// than it is.
	_, files := parseNonTestFiles(t)

	refs := map[string]int{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				refs[id.Name]++
			}
			return true
		})
	}

	for literal, constName := range namedKeys {
		// One reference is the declaration in formkeys.go, so a constant that
		// is only declared counts once and no more.
		if refs[constName] <= 1 {
			t.Errorf("%s (for %q) is declared and never read — either a handler "+
				"should be using it, or the field is gone and so should the constant be",
				constName, literal)
		}
	}
}
