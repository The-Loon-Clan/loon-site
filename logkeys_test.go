package site

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The structured log keys this project uses, and what each one means.
//
// A log line is only searchable if the same thing is called the same thing
// everywhere. This project had three keys for one concept: a moderation action
// recorded the staff member's id under "by" and their username under "actor",
// while the member acted upon was "user" in one file and "subject" in another.
// Nothing was broken and nothing failed — you simply could not ask "what has
// this moderator done" and get an answer, which is the whole reason a
// moderation action is logged.
//
// So the vocabulary is a list with meanings, and a key that is not on it fails
// this test. The fix is then a choice made deliberately: reuse the existing key
// or add a line here saying what the new one means.
//
// This test lives at the module root because it is about the whole binary's
// output. A per-package version would let two packages disagree, which is
// exactly the failure it exists to prevent.
var logKeys = map[string]string{
	// who
	"user":       "the member the entry is ABOUT, as an id",
	"actor":      "the staff member who performed it, as an id",
	"actor_name": "that staff member's username, alongside actor",
	"followee":   "the other end of a follow edge, as an id",
	"name":       "a username, where the entry is about the name itself",

	// what
	"release":      "a release id",
	"hash":         "a torrent's info hash",
	"item":         "a moderation queue item id",
	"flag":         "a cheat-flag id",
	"slug":         "a plugin, widget, page or community slug",
	"job":          "a scheduled job's name",
	"list":         "a named list (wishlist, bookmarks)",
	"key":          "a settings or cache key",
	"title":        "a release or thread title",
	"action":       "which edit a multi-action endpoint was asked to perform",
	"fragment":     "a named template swapped in by htmx, within a page's set",
	"region":       "a widget region key (site.home, header-bar, …)",
	"page":         "a template page name",
	"path":         "a URL path or file path",
	"url":          "an absolute URL",
	"link":         "a generated one-time link (email confirmation, reset)",
	"login":        "the demo credentials, printed once at boot",
	"file":         "a file on disk",
	"priority":     "a metadata source's precedence",
	"subject":      "an email subject line",
	"body":         "an email body, in the demo mailer that logs instead of sending",
	"anchor":       "a fragment target",
	"kind":         "a variant of the thing being logged",
	"mode":         "which of several modes a subsystem is in",
	"flavour":      "the site flavour: indexer, torrent or both",
	"href":         "the path or URL a nav entry links to",
	"icon":         "a sprite id (#name) — logged when one is named and the sheet has no such symbol",
	"area":         "which part of the site",
	"source":       "a metadata source's name",
	"provider":     "an external service",
	"domain":       "a hostname",
	"addr":         "a network address",
	"redis":        "the Redis address in play",
	"http":         "an HTTP status",
	"line":         "a line number",
	"version":      "this build's version and revision, from BuildInfo",
	"notification": "a notification id or type",
	"registration": "a plugin registration",
	"resolution":   "how a moderation item was settled",
	"reason":       "why, in words",
	"meaning":      "what a parsed token was taken to mean",
	"symptom":      "what went wrong, observably",
	"fix":          "what to do about it",
	"from":         "the previous value or origin",
	"to":           "the new value or destination",
	"new_releases": "how many releases an import added",
	"categories":   "how many categories",
	"groups":       "how many groups",
	"posts":        "how many posts",
	"threads":      "how many threads",
	"ids":          "a set of ids",
	"files":        "how many files",
	"metrics":      "how many metrics",
	"history":      "how many history rows",
	"warnings":     "how many warnings",
	"torrents":     "how many torrents",
	// Deliberately NOT "count": the tracker seed logs two figures in one entry
	// — torrents made, and accounting rows written against them — and a pair
	// where one is "count" reads as though the other were the incidental one.
	"stats":       "how many per-member accounting rows",
	"count":       "how many, where nothing more specific fits",
	"amount":      "a quantity of points or currency",
	"left_remote": "rows left on the remote side",

	// outcome
	"err":                 "the error. ALWAYS this spelling, never \"error\"",
	"ran":                 "whether something ran",
	"runs_jobs":           "whether this process is the one running scheduled work",
	"enabled":             "whether a feature is on",
	"opened":              "whether something was opened",
	"repeated":            "how many times an entry was suppressed",
	"partial":             "the result was incomplete",
	"unfilled":            "how many were left unfilled",
	"localised":           "whether a local copy was made",
	"render":              "a render outcome",
	"browsing":            "the browsing context",
	"mine":                "the viewer's own, rather than everyone's",
	"donations":           "the donations feature's state",
	"files_removed":       "how many files were deleted",
	"undo_records_purged": "how many undo records were purged",
}

// logCall finds a slog-style call and captures its arguments.
var logCall = regexp.MustCompile(`\.(Error|Warn|Info|Debug)\(([^()]*(?:\([^()]*\)[^()]*)*)\)`)

// keyLiteral matches a bare string literal used as an argument.
var keyLiteral = regexp.MustCompile(`"([a-z_]+)"\s*,`)

func TestEveryLogKeyIsInTheVocabulary(t *testing.T) {
	var checked int
	unknown := map[string][]string{}

	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Vendored or example trees are not ours to hold to this.
			switch d.Name() {
			case ".git", "vendor", "examples", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range logCall.FindAllStringSubmatch(string(src), -1) {
			args := m[2]
			// Drop the message, which is the first argument and is prose
			// rather than a key.
			if i := strings.Index(args, ","); i >= 0 {
				args = args[i+1:]
			} else {
				continue
			}
			for _, k := range keyLiteral.FindAllStringSubmatch(args, -1) {
				key := k[1]
				checked++
				if _, ok := logKeys[key]; !ok {
					unknown[key] = append(unknown[key], path)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if checked == 0 {
		t.Fatal("no log keys found at all — the scan is broken, not the code")
	}

	var names []string
	for k := range unknown {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		t.Errorf("log key %q is not in the vocabulary (%s)\n"+
			"    either reuse an existing key or add %q to logKeys in "+
			"logkeys_test.go with a line saying what it means — a log schema "+
			"nobody reviews is one nobody can query",
			k, strings.Join(dedupe(unknown[k]), ", "), k)
	}
	t.Logf("checked %d log keys against a %d-word vocabulary", checked, len(logKeys))
}

// Every log call must pass its keys and values in pairs.
//
// slog takes key, value, key, value after the message. Miscount, and it does
// not fail — it emits the stray argument under "!BADKEY" and carries on, so the
// line is still written, still looks approximately right, and has silently lost
// the field somebody will one day try to filter on.
//
// This is here because unifying the moderation keys above meant rewriting
// argument lists by hand, which is exactly how the count goes wrong.
func TestLogCallsPassKeysAndValuesInPairs(t *testing.T) {
	var checked int
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "examples", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || call.Ellipsis != token.NoPos {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Error", "Warn", "Info", "Debug":
			default:
				return true
			}
			// Only calls on something named like a logger. Info/Error are
			// common method names, and this must not start policing types it
			// knows nothing about.
			recv, ok := sel.X.(*ast.Ident)
			if !ok {
				if s, ok := sel.X.(*ast.SelectorExpr); ok {
					recv = s.Sel
				} else {
					return true
				}
			}
			if !strings.Contains(strings.ToLower(recv.Name), "log") {
				return true
			}
			checked++
			if rest := len(call.Args) - 1; rest%2 != 0 {
				pos := fset.Position(call.Pos())
				t.Errorf("%s:%d has %d arguments after the message, which is odd:\n"+
					"    slog reads them as key/value pairs and files the leftover "+
					"under \"!BADKEY\" without failing",
					filepath.ToSlash(path), pos.Line, rest)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no log calls found at all — the scan is broken, not the code")
	}
	t.Logf("checked %d log calls for balanced key/value arguments", checked)
}

// TestTheErrorKeyHasOneSpelling guards the vocabulary itself.
//
// "err" and "error" both read naturally, both work, and a codebase that uses
// each half the time cannot filter its own failures. The test above already
// rejects "error" at a call site, since it is not a listed key — what it cannot
// see is somebody resolving that failure by adding "error" to the list, which
// is the path of least resistance when the test is the thing in your way.
//
// This started out grepping the tree as well. That half was redundant with the
// vocabulary check and quietly wrong: it required the line to contain "log.",
// so every call through a logger named `logger` went unexamined. Deleted rather
// than repaired — a second check that agrees with the first when it works and
// says nothing when it does not is worse than one check.
func TestTheErrorKeyHasOneSpelling(t *testing.T) {
	if _, ok := logKeys["error"]; ok {
		t.Error(`"error" was added to the vocabulary — the spelling here is "err", ` +
			`and 145 existing call sites use it`)
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
