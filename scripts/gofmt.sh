#!/usr/bin/env bash
# List unformatted files, in the container — see scripts/go.sh for why.
set -euo pipefail
MSYS_NO_PATHCONV=1 exec docker run --rm \
  -v "$(pwd -W 2>/dev/null || pwd)":/app -w /app \
  "${GO_IMAGE:-golang:1.26}" gofmt -l .
