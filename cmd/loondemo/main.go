// Command loondemo runs the demo Usenet indexer.
//
// The command is deliberately this thin. The production indexer keeps its
// binary under cmd/ and its handlers under web/handlers, and this tree now
// matches that — but with one structural difference forced by a design choice
// the demo makes and prod does not.
//
// Prod reads its templates off disk (LoadHTMLGlob("web/templates/*")), so its
// command can live anywhere. The demo EMBEDS web/templates and web/static and
// ships gcr.io/distroless/static-debian12: there is no web/ directory in the
// runtime image, only the binary. //go:embed cannot reference a parent
// directory, so the package holding those directives has to sit at the module
// root, above web/ — which means the root is a library package and the command
// is what moves.
//
// Everything real is in the root package. This file exists so `go build
// ./cmd/loondemo` produces the binary and nothing else has to know.
package main

import (
	"github.com/the-loon-clan/loon-demo-site/web/handlers"
)

func main() { handlers.Main() }
