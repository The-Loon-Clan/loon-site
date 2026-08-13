#!/usr/bin/env bash
# Run the Go toolchain in a container, never on the host.
#
#   scripts/go.sh build ./...
#   scripts/go.sh test ./... -count=1
#   scripts/go.sh vet ./...
#
# Why
# ---
# `go build` and `go test` write executables into the working tree and into a
# cache, and a Windows anti-virus treats freshly produced unsigned binaries as
# exactly what they look like. The symptoms are not obvious errors — they are
# a toolchain that reports `no such tool "compile"` because the compiler was
# quarantined between two commands.
#
# In a container nothing lands on the host filesystem except the source that
# was already there, so there is no binary to object to. It also pins the Go
# version to the one go.mod asks for rather than whatever the host happens to
# have, which is the same reason CI does it.
#
# GOWORK=off so a developer's go.work — which points at sibling checkouts that
# do not exist inside the container — cannot change what is built.
#
# The module cache lives in a named volume, so a second run does not
# re-download the graph.
set -euo pipefail

IMAGE="${GO_IMAGE:-golang:1.26}"
CACHE_VOL="loon-gomod"
BUILD_VOL="loon-gobuild"

docker volume create "$CACHE_VOL" >/dev/null
docker volume create "$BUILD_VOL" >/dev/null

# MSYS_NO_PATHCONV: git-bash on Windows rewrites /app into a Windows path
# before docker sees it, and the mount silently lands somewhere useless.
MSYS_NO_PATHCONV=1 exec docker run --rm \
  -v "$(pwd -W 2>/dev/null || pwd)":/app \
  -v "$CACHE_VOL":/go/pkg/mod \
  -v "$BUILD_VOL":/root/.cache/go-build \
  -w /app \
  -e GOWORK=off \
  -e GOFLAGS=-buildvcs=false \
  --network host \
  "$IMAGE" go "$@"
