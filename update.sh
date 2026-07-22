#!/usr/bin/env bash
# Local-dev refresh for the loon demo site.
#
# Rebuilds the app image from the CURRENT local checkouts and restarts it, then
# reclaims disk by dropping the now-dangling old image layers. There is NO git
# pull on purpose: the sibling repos (loon / loon-baseline / loon-plugins) are
# wired as BuildKit named build-contexts in docker-compose.yml, so whatever is
# on disk right now is exactly what gets built -- which is what you want while
# editing them. (For a deployed instance you'd pull first; this script is for
# the local edit -> rebuild -> look loop.)
#
# Usage:  ./update.sh          # rebuild + restart + prune (app on :8090)
#         ./update.sh --logs   # ...then tail the app logs
set -euo pipefail

cd "$(cd "$(dirname "$0")" && pwd)"

echo "==> Rebuilding + restarting (app -> http://localhost:8090) ..."
# --build is essential: a plain 'up' reuses the stale image. The named
# build-contexts pull in the current loon / loon-baseline / loon-plugins.
docker compose up --build -d

echo "==> Pruning dangling images left by previous builds ..."
# Dangling only (-f, no -a): removes the old untagged app-image layers the
# rebuild just orphaned, without touching pulled base images (postgres, redis)
# or anything still tagged.
docker image prune -f

echo "==> Status:"
docker compose ps

if [ "${1:-}" = "--logs" ]; then
    echo "==> Tailing app logs (Ctrl-C to stop) ..."
    docker compose logs -f app
else
    echo "==> Done. Tail logs with:  docker compose logs -f app"
fi
