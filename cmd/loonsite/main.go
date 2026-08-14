// Command loonsite runs the demo Usenet indexer.
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
// ./cmd/loonsite` produces the binary and nothing else has to know.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/the-loon-clan/loon-site/web/handlers"
)

// healthcheck asks the running server whether it is serving, and is how the
// container health check works at all.
//
// The runtime image is distroless: no shell, no curl, no wget. The usual
// `test: ["CMD", "curl", "-f", ...]` cannot run, and a health check that
// cannot run is reported as unhealthy — so the binary checks itself. One
// process, one request, no dependencies.
//
// It talks to localhost rather than to the published address deliberately:
// this answers "is THIS container serving", which is the question a health
// check is for. Reaching the site through a load balancer would report a
// sibling's health as its own.
func healthcheck(addr string) int {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}

func main() {
	// A flag rather than a subcommand, so the ordinary invocation stays
	// `/loonsite` with no arguments and nothing about the normal path changes.
	check := flag.Bool("healthcheck", false,
		"ask the local server for /healthz and exit 0 when it answers; for container health checks")
	addr := flag.String("healthcheck-addr", "localhost:8090", "address the local server listens on")
	flag.Parse()

	if *check {
		os.Exit(healthcheck(*addr))
	}
	handlers.Main()
}
