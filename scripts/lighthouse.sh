#!/usr/bin/env bash
# Lighthouse against the running site: accessibility, SEO, best practices.
#
#   bash scripts/lighthouse.sh              # /browse
#   bash scripts/lighthouse.sh /release/1   # any path
#
# Two flags are load-bearing and were both found the hard way:
#
#   --shm-size=1g   without it Chrome dies with "Browser tab has unexpectedly
#                   crashed" — the container's default 64MB of /dev/shm is not
#                   enough to render a page this size.
#   --add-host      the container has to reach the host's :8090.
#
# HTTPS audits will always fail against a local HTTP server. That is a property
# of where it is running, not of the site, so read Best Practices as "78 minus
# the two HTTPS items" here and only trust that number from a TLS deployment.
set -u

PATH_="${1:-/browse}"
BASE="${BASE:-http://host.docker.internal:8090}"

docker run --rm --shm-size=1g --add-host=host.docker.internal:host-gateway \
    --entrypoint lighthouse ghcr.io/femtopixel/google-lighthouse \
    "$BASE$PATH_" --quiet \
    --chrome-flags="--headless=new --no-sandbox --disable-gpu --disable-dev-shm-usage" \
    --only-categories=accessibility,seo,best-practices \
    --output=json --output-path=stdout 2>/dev/null \
  | python3 -c '
import json,sys,re
raw=sys.stdin.read()
m=re.search(r"\{.*\}", raw, re.S)
if not m:
    print("no JSON from lighthouse — is the site up?"); sys.exit(2)
o=json.loads(m.group(0))
bad=0
for k in ("accessibility","seo","best-practices"):
    c=o["categories"][k]
    print("%-16s %d" % (c["title"], round(c["score"]*100)))
for k in ("accessibility","seo","best-practices"):
    for ref in o["categories"][k]["auditRefs"]:
        a=o["audits"][ref["id"]]
        if a.get("score") is not None and a["score"]<1 and a.get("scoreDisplayMode")=="binary":
            print("  [%s] %s" % (k, a["title"])); bad+=1
sys.exit(0)
'
