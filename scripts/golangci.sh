#!/usr/bin/env bash
# golangci-lint, in a container, built from this project's Go version.
#
# Not the published golangci/golangci-lint image: that is built with an older
# toolchain and refuses outright when go.mod targets a newer one —
#
#   the Go language version (go1.25) used to build golangci-lint is lower
#   than the targeted Go version (1.26.5)
#
# So it is installed into the same golang image the rest of scripts/ uses. The
# binary is cached in a named volume, making the first run slow (a few minutes)
# and every run after it fast. Same reasoning as scripts/go.sh: nothing
# unsigned is written to the host, where a Windows anti-virus quarantines fresh
# executables and the symptom looks like a broken toolchain.
#
# A golangci-lint already on PATH is used as-is. That is how CI runs it — the
# workflow installs the linter and then calls `make check` like everybody else,
# so the promise that CI runs no steps of its own survives a check whose normal
# form is a container.
set -euo pipefail

if command -v golangci-lint >/dev/null 2>&1; then
	exec golangci-lint run --timeout 15m "$@"
fi

IMAGE="golang:1.26"
BIN_VOLUME="loon-gobin"
MOD_VOLUME="loon-gomod"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# MSYS_NO_PATHCONV on the CHECK too, not only on the run below. Without it Git
# Bash rewrites /go/bin/golangci-lint into a Windows path before docker sees
# it, the test fails against a path that was never being asked about, and the
# script reinstalls every time while printing "first run only".
if ! MSYS_NO_PATHCONV=1 docker run --rm -v "${BIN_VOLUME}:/go/bin" "${IMAGE}" \
	test -x /go/bin/golangci-lint 2>/dev/null; then
	echo "installing golangci-lint into the ${BIN_VOLUME} volume (first run only)..." >&2
	docker run --rm \
		-v "${BIN_VOLUME}:/go/bin" \
		-v "${MOD_VOLUME}:/go/pkg/mod" \
		"${IMAGE}" \
		go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
fi

# MSYS_NO_PATHCONV keeps Git Bash on Windows from rewriting /app into a
# Windows path before docker ever sees it.
MSYS_NO_PATHCONV=1 exec docker run --rm \
	-v "${REPO}:/app" \
	-v "${MOD_VOLUME}:/go/pkg/mod" \
	-v "${BIN_VOLUME}:/go/bin" \
	-w /app \
	-e GOWORK=off \
	-e GOFLAGS=-buildvcs=false \
	"${IMAGE}" \
	/go/bin/golangci-lint run --timeout 15m "$@"
