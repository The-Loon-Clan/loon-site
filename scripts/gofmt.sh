#!/usr/bin/env bash
# List unformatted files, in the container — see scripts/go.sh for why.
#
# A gofmt already on PATH is used directly: it is part of the Go toolchain, so
# anywhere `make check GO=go` makes sense this does too, and it keeps CI from
# starting a container for a formatting check.
#
# Whichever path runs, a FAILURE here must be a failure. When this script could
# not run at all, `make fmt` read its empty stdout as "nothing unformatted" and
# reported the check as passing — see the fmt target for that fix.
set -euo pipefail

if command -v gofmt >/dev/null 2>&1; then
	exec gofmt -l .
fi

MSYS_NO_PATHCONV=1 exec docker run --rm \
  -v "$(pwd -W 2>/dev/null || pwd)":/app -w /app \
  "${GO_IMAGE:-golang:1.26}" gofmt -l .
