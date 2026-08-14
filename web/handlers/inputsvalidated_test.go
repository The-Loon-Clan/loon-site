package handlers

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"
)

// A handler that binds an input with RULES must run them.
//
// This is the gap the other input test cannot see. TestEveryInputTypeStatesItsRules
// checks that a …Input type HAS a Validate method; request.Validate's type
// parameter makes calling it with a rule-less struct a compile error. Neither
// notices the third case, which is the one that actually happened here: twenty
// structs with carefully written rules, and only four handlers that ever called
// request.Validate. Rules that exist and are enforced by nothing are worse than
// no rules, because the file reads as though the endpoint is checked.
//
// Middleware would be the other way to guarantee this. It was considered and
// not taken: the five validating endpoints have five different failure
// responses — three re-render a form with its values refilled, two redirect
// with a query — and two of those rebuild their destination FROM the bound
// input. A middleware would need a per-route failure function taking the
// context and the typed value, which is the code that is in the handlers now,
// moved and named, plus a type assertion to get the value back. This gets the
// same guarantee for no runtime cost and no lost type safety.

// noRuleInputs are the input types whose Validate deliberately returns nil.
//
// Each entry is a claim that the endpoint's checks live somewhere better, and
// the comment beside its Validate says where — usually inside the transaction
// or the store call that acts on the value, because a rule checked in a handler
// is a rule that can be stale by the time it is used.
//
// Listing them by hand rather than detecting "returns nil" is the point: adding
// a name here is a decision, and it is the moment to ask whether the rule
// really does live downstream.
var noRuleInputs = map[string]bool{
	"settingsPrivacyInput":       true, // a checkbox cannot be malformed
	"settingsNotificationsInput": true, // kinds come from notifiableKinds, not the form
	"securityActionInput":        true, // each action's own step checks what it needs
	"twoFactorInput":             true, // an empty code and a wrong code get one answer
	"profileBioInput":            true, // truncated by runes where it is stored
	"resetInput":                 true, // authtoken judges the token and the password
	"themeInput":                 true, // an unknown theme resolves to the default
	"avatarSaveInput":            true, // a submit button
	"wishActionInput":            true, // a closed set the switch already handles
	"nextInput":                  true, // checked by sameOriginPath at the call site
	"newznabQueryInput":          true, // clamped, because a downloader wants results
	"jobControlInput":            true, // the switch's default is the check
	"accessSaveInput":            true, // saveAccessSettings refuses an unknown mode
	"coverModeInput":             true, // saveCoverMode refuses an unknown mode
	"widgetActionInput":          true, // region and slug are checked against the registry
	"avatarModInput":             true, // an id of zero means it did not come from the page
	"cheatFlagInput":             true, // same
	"communityVoteInput":         true, // same
	"reportAvatarInput":          true, // reportAvatar caps the reason where it stores it
	"undoInput":                  true, // performUndo judges a single-use token
}

func TestAHandlerThatBindsRulesRunsThem(t *testing.T) {
	fset, files := parseNonTestFiles(t)

	var checked int
	for _, file := range files {
		name := filepath.Base(fset.Position(file.Pos()).Filename)
		// The readers themselves live here; they bind and do not validate,
		// which is their whole job.
		if strings.HasPrefix(name, "inputs") {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}

			// What this function binds, and whether it validates.
			var bound []string
			validates := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := call.Fun.(type) {
				case *ast.Ident: // readXInput(c)
					if in, found := strings.CutPrefix(f.Name, "read"); found {
						if strings.HasSuffix(in, "Input") {
							bound = append(bound, lowerFirst(in))
						}
					}
				case *ast.SelectorExpr: // request.Validate(in)
					if id, ok := f.X.(*ast.Ident); ok && id.Name == "request" && f.Sel.Name == "Validate" {
						validates = true
					}
				}
				return true
			})

			for _, in := range bound {
				checked++
				if noRuleInputs[in] || validates {
					continue
				}
				pos := fset.Position(fn.Pos())
				t.Errorf("%s:%d %s binds %s but never calls request.Validate.\n"+
					"    Either run the rules, or — if the check really belongs "+
					"downstream, beside the state it acts on — make %s.Validate "+
					"return nil, say where the rule lives, and add it to "+
					"noRuleInputs in this test.",
					name, pos.Line, fn.Name.Name, in, in)
			}
			return true
		})
	}

	if checked == 0 {
		t.Fatal("no handlers bind an input at all — the scan is broken, not the code")
	}
	t.Logf("checked %d handler/input pairs; %d input types state their rules live downstream",
		checked, len(noRuleInputs))
}

func TestNoRuleInputsAreAllReal(t *testing.T) {
	// A name in that list that is not an input type any more is an exemption
	// for something that no longer exists — and it would silently excuse a NEW
	// type that happened to be given the same name.
	_, files := parseNonTestFiles(t)

	real := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok && strings.HasSuffix(ts.Name.Name, "Input") {
				real[ts.Name.Name] = true
			}
			return true
		})
	}
	for name := range noRuleInputs {
		if !real[name] {
			t.Errorf("noRuleInputs lists %s, which is not an input type in this package", name)
		}
	}
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
