package site

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The module root exists to hold the embed, and nothing else.
//
// Not tidiness — a constraint. //go:embed cannot reference a parent directory,
// and the runtime image is distroless with no web/ directory in it, so the
// package declaring those directives has to sit above web/. That forces the
// root to be a library package and the command to move to cmd/loonsite.
//
// The failure mode this guards is drift: the root is the most convenient place
// to put a helper "just for now", and every one that lands there is code
// outside internal/, importable by anything, in a package whose whole purpose
// is a handful of embed directives. See docs/adr/0003.

func TestTheRootHoldsOnlyTheEmbed(t *testing.T) {
	var files []string
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		files = append(files, e.Name())
	}

	if len(files) != 1 || files[0] != "assets.go" {
		t.Fatalf("the module root holds %v, want exactly [assets.go].\n"+
			"    Code here sits outside internal/ and is importable by anything.\n"+
			"    It belongs in internal/ or web/handlers — see docs/adr/0003.", files)
	}
}

func TestTheRootDeclaresNothingButTheEmbeddedFilesystem(t *testing.T) {
	// A single file can still accumulate. What is allowed here is the embed
	// variables and whatever they need — not functions, not types.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "assets.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	var funcs, types []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.FuncDecl:
			funcs = append(funcs, d.Name.Name)
		case *ast.TypeSpec:
			types = append(types, d.Name.Name)
		}
		return true
	})

	// init is allowed, and only init: it performs the LOON_DEV swap that points
	// FS at the working tree instead of the embedded copy. That is part of
	// serving the assets, not behaviour that wandered in.
	for _, name := range funcs {
		if name != "init" {
			t.Errorf("the root package declares %s() — it is the embed and nothing "+
				"else; behaviour belongs in internal/ or web/handlers", name)
		}
	}
	if len(types) > 0 {
		t.Errorf("the root package declares types %v — same reason", types)
	}
}

func TestTheEmbeddedTreeActuallyContainsTheSite(t *testing.T) {
	// FS is the working tree, not the embed, when LOON_DEV is set — see the
	// init in assets.go. Walking it then measures the checkout rather than the
	// binary, so these two assert nothing useful and fail for a reason that is
	// not a bug.
	if DevReload {
		t.Skip("LOON_DEV swaps FS for the working tree; nothing to say about the embed")
	}

	// The embed is the one thing here, so it is worth checking it embedded
	// something. A pattern that matches nothing is a build error, but a pattern
	// that matches the wrong subtree is not: the binary ships, and the pages
	// 500 at request time.
	//
	// The paths keep their web/ prefix: //go:embed roots the filesystem at the
	// package directory, so the tree inside is web/templates/… and not
	// templates/…. Worth pinning, because every caller has to spell it the same
	// way and getting it wrong is a 500 at request time rather than a build
	// error.
	want := []string{
		"web/templates/base.html",
		"web/templates/home.html",
		"web/static/css/theme.css",
	}
	for _, name := range want {
		if _, err := fs.Stat(FS, name); err != nil {
			t.Errorf("%s is not in the embedded filesystem: %v", name, err)
		}
	}

	// And that it is not embedding the whole repository by accident — which
	// would ship the refs/ screenshots, the docs and the examples inside the
	// binary, quietly multiplying the image size.
	for _, unwanted := range []string{"web/handlers", "web/handlers/main.go", "docs", "refs"} {
		if _, err := fs.Stat(FS, unwanted); err == nil {
			t.Errorf("%s is embedded; the pattern is wider than intended", unwanted)
		}
	}
}

func TestEveryEmbeddedFileIsUnderWeb(t *testing.T) {
	// FS is the working tree, not the embed, when LOON_DEV is set — see the
	// init in assets.go. Walking it then measures the checkout rather than the
	// binary, so these two assert nothing useful and fail for a reason that is
	// not a bug.
	if DevReload {
		t.Skip("LOON_DEV swaps FS for the working tree; nothing to say about the embed")
	}

	// Walk what actually shipped. The templates and static assets are the whole
	// of it; anything else means a pattern picked up a neighbour.
	var strays []string
	err := fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || p == "." {
			return nil
		}
		q := filepath.ToSlash(p)
		if !strings.HasPrefix(q, "web/templates/") && !strings.HasPrefix(q, "web/static/") {
			strays = append(strays, q)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(strays) > 0 {
		show := strays
		if len(show) > 10 {
			show = show[:10]
		}
		t.Errorf("%d embedded files are outside web/templates/ and web/static/: %v",
			len(strays), show)
	}
}
