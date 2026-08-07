#!/usr/bin/env bash
# Rebuild the site and refuse to leave it broken.
#
# Three outages in one day had the same shape: a change that compiled, an image
# that built, a container that started, and a site that was down. `docker
# compose up -d --build` reports success for all four of those and says nothing
# about the fifth.
#
#     rewards    a doc placeholder inside a namespace the plugin also scans by
#                prefix — Provision returned an error and web + worker
#                crash-looped
#     forum      events declared with no Kind, rejected by the new validation in
#                core; the fix was on disk, uncommitted, and not in the image
#     redis      the container simply was not started; the session store
#                panicked at boot rather than degrading
#
# Every one was caught by a human loading the page. This script is that human.
#
# Usage:
#     ./deploy.sh              rebuild, verify, keep the new image
#     ./deploy.sh --dev        same, with the dev overlay (templates from disk)
#
# On failure it prints the boot log and exits non-zero, leaving the old image
# tagged so `docker compose up -d` puts the previous build back.
set -uo pipefail

# The audit sweeps (scripts/README.md). ADVISORY: they report and never change
# the exit status. Each script exits non-zero on findings, so CI can gate on one
# of them individually once its findings are at zero -- but a deploy that fails
# because a plugin template is missing a table caption is a deploy people learn
# to work around.
run_audits() {
    command -v python >/dev/null 2>&1 || return 0
    echo
    echo "  audits (advisory -- see scripts/README.md)"
    for a in audit_css audit_links audit_a11y; do
        [[ -f "scripts/$a.py" ]] || continue
        python "scripts/$a.py" 2>&1 | tail -1 | sed 's/^/     /'
    done
}

cd "$(dirname "$0")"

FILES=(-f docker-compose.yml)
[[ "${1:-}" == "--dev" ]] && FILES+=(-f compose.dev.yml)

# Health is asked of /healthz, not /. The root page reads the database and the
# cache, so it can fail for reasons that have nothing to do with whether the
# process came up — and it can SUCCEED slowly enough to look like a failure.
# /healthz is registered before any middleware precisely so it answers "is the
# process up", which is the question here.
HEALTH="http://localhost:8090/healthz"
DEADLINE=90

say() { printf '\n\033[1m%s\033[0m\n' "$*"; }

say "1/4  Tests"
if ! go test ./... >/tmp/deploy-test.log 2>&1; then
    tail -25 /tmp/deploy-test.log
    echo "FAILED: tests. Nothing was rebuilt." >&2
    exit 1
fi
echo "     ok"

say "2/4  Build image"
if ! docker compose "${FILES[@]}" build >/tmp/deploy-build.log 2>&1; then
    tail -25 /tmp/deploy-build.log
    echo "FAILED: image build. The running container is untouched." >&2
    exit 1
fi
echo "     ok"

say "3/4  Start"
# Every service, not just app: a missing redis is one of the three failures
# above, and `up -d app` alone is how it went missing.
if ! docker compose "${FILES[@]}" up -d >/tmp/deploy-up.log 2>&1; then
    tail -25 /tmp/deploy-up.log
    echo "FAILED: compose up." >&2
    exit 1
fi
echo "     ok"

say "4/4  Wait for the site to actually answer"
for ((i = 1; i <= DEADLINE; i++)); do
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$HEALTH" 2>/dev/null)
    if [[ "$code" == "200" ]]; then
        echo "     healthy after ${i}s"
        # Up is necessary and not sufficient: a boot error that a plugin logs
        # and swallows leaves the process running with that feature missing.
        # Worth surfacing, not worth failing on — the site IS serving.
        if docker compose "${FILES[@]}" logs app --tail 200 2>/dev/null \
             | grep -iE 'level=ERROR|panic:' | grep -v 'page cache read' | head -5 | grep .; then
            echo
            echo "     ^ the site is up, but it logged the above during boot." >&2
        fi
        run_audits
        say "Deployed."
        exit 0
    fi
    # A container that is restarting will never answer, so stop waiting for it.
    if [[ "$(docker compose "${FILES[@]}" ps --format '{{.State}}' app 2>/dev/null)" == "restarting" ]]; then
        break
    fi
    sleep 1
done

say "FAILED: the site never became healthy."
docker compose "${FILES[@]}" logs app --tail 40 2>/dev/null | tail -40
cat >&2 <<'MSG'

The new image is built and running, and it is not serving. To put the previous
build back:

    docker compose down app && docker compose up -d

MSG
exit 1
