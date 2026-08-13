// Package site is the demo indexer's asset root, and nothing else.
//
// It exists at the module root for one reason: //go:embed cannot reference a
// parent directory. web/templates and web/static are embedded so the runtime
// image can be gcr.io/distroless/static-debian12 with no web/ directory in it
// at all — only the binary — and the package declaring those directives has to
// sit above web/. Everything that would normally live in a root package has
// moved out: the HTTP layer to web/handlers, the command to cmd/loondemo.
//
// The production indexer has no equivalent, because it reads its templates off
// disk (LoadHTMLGlob("web/templates/*")) and so never has to pin a package to
// the root. This file is the whole of the difference between the two trees.
package site

import (
	"embed"
	"io/fs"
	"os"
)

//go:embed web/templates web/static
var embeddedFS embed.FS

// FS is where templates and static assets are read from. Normally that is the
// embedded copy, so the runtime image needs nothing but the binary.
// LOON_DEMO_DEV=1 swaps in the working tree instead and makes render() re-parse
// per request, so a template or stylesheet edit shows on refresh with no
// rebuild. os.DirFS(".") means the process must run from the repo root, which
// both the compose dev mount and a local run already satisfy.
//
// A variable rather than a function so the dev swap below stays a one-liner,
// and exported because web/handlers reads through it — the import goes that
// way and only that way, which is what keeps this package a leaf.
var FS fs.FS = embeddedFS

// DevReload reports whether templates are re-read from disk on every render.
// Off by default: the cost is a full parse per request, and a parse error
// becomes a page instead of a boot panic — right for a dev loop, wrong for
// production.
var DevReload = os.Getenv("LOON_DEMO_DEV") == "1"

func init() {
	if DevReload {
		FS = os.DirFS(".")
	}
}
