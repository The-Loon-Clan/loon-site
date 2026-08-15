#!/usr/bin/env bash
# W3C Nu validator over the running site.
#
#   bash scripts/htmlvalidate.sh          # against http://localhost:8090
#   BASE=http://host:port bash scripts/...
#
# Runs the official validator image, so there is nothing to install and the
# result is the same one the W3C service gives.
#
# WHY hx-* IS FILTERED
#
# htmx's attributes are not in the HTML specification, so every hx-post and
# hx-target is reported as "not allowed on element". On this site that was 82
# of 98 errors — noise that would bury the 16 real ones and, worse, would train
# whoever runs this to skim the output.
#
# The alternative is real: htmx supports data-hx-* for exactly this reason, and
# data-* IS valid HTML. Switching would make this run clean without a filter.
# It is not done because it would touch every converted control and the tests
# that assert on them, and because the filter here is narrow and stated rather
# than a blanket exclusion. Revisit if anything else ever needs filtering — two
# filters is the point at which this stops being honest.
set -u

# The container fetches the URLs itself. The first version of this script wrote
# the pages to a mktemp -d and mounted it, which on Windows/MSYS hands Docker a
# path it cannot mount — so /work was EMPTY, vnu validated nothing, and the
# script exited 0 and printed nothing. A validator that reports success because
# it was given no input is the worst possible failure for a check like this, and
# it looked exactly like a pass.
#
# host.docker.internal reaches the host from inside the container on Docker
# Desktop; override BASE for anything else.
BASE="${BASE:-http://host.docker.internal:8090}"
PAGES=(/ /browse "/search?q=x" /login /register)

urls=()
for p in "${PAGES[@]}"; do urls+=("$BASE$p"); done

# --errors-only: warnings here are mostly stylistic and this is a gate.
# --filterpattern drops the htmx attribute reports; see the note above.
# --format gnu: one line per problem, greppable.
docker run --rm --add-host=host.docker.internal:host-gateway     ghcr.io/validator/validator:latest     vnu --errors-only     --filterpattern '.*hx-[a-z-]+.*'     --format gnu "${urls[@]}"
