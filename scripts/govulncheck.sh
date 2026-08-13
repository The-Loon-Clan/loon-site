#!/usr/bin/env bash
# Known vulnerabilities in code this project actually CALLS.
#
# govulncheck's distinction matters more than a plain dependency scan: it
# reports what is reachable from this code, not everything present in the
# module graph. The first run found seven that were reachable, four of them in
# golang.org/x/net/html — the parser internal/sanitize is built on, which is
# the site's defence against stored XSS. The same scan listed fifteen more in
# modules that are required but never called, and those are noise.
#
# In a container for the same reason as scripts/go.sh.
set -euo pipefail
MSYS_NO_PATHCONV=1 exec docker run --rm \
  -v "$(pwd -W 2>/dev/null || pwd)":/app \
  -v loon-gomod:/go/pkg/mod \
  -w /app -e GOWORK=off --network host \
  "${GO_IMAGE:-golang:1.26}" \
  sh -c 'go install golang.org/x/vuln/cmd/govulncheck@latest >/dev/null 2>&1 && govulncheck ./...'
