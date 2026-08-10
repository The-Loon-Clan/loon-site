#!/usr/bin/env bash
# Screenshot the running demo so a UI change can be SEEN rather than inferred.
#
# Usage:  bash shot.sh <name> [path] [width] [height]
#         bash shot.sh home           /              1400 1000
#         bash shot.sh featured       /              1400 700
#
# Writes scratchpad/shots/<name>.png and prints the path, which can then be
# read back as an image.
#
# Anonymous by design: the home page, browse, search and a release page all
# render without a session, which covers layout work. A signed-in surface needs
# a cookie Chrome's CLI cannot set, so those are still verified from the HTML.
set -u
NAME="${1:?usage: shot.sh <name> [path] [w] [h]}"
PATH_="${2:-/}"
W="${3:-1400}"
H="${4:-1000}"

CHROME="/c/Program Files/Google/Chrome/Application/chrome.exe"
OUT_WIN="C:/Users/johnm/AppData/Local/Temp/claude/c--GitHub-loon-demo-site/376ebf67-236e-4e70-a798-6ef173bfba7e/scratchpad/shots/${NAME}.png"
OUT_POSIX="/c/Users/johnm/AppData/Local/Temp/claude/c--GitHub-loon-demo-site/376ebf67-236e-4e70-a798-6ef173bfba7e/scratchpad/shots/${NAME}.png"

mkdir -p "$(dirname "$OUT_POSIX")"
rm -f "$OUT_POSIX"

# --virtual-time-budget lets webfonts and images settle before the capture;
# without it a screenshot can catch the page mid-paint and "prove" a bug that
# is really just a race with the render.
"$CHROME" --headless=new --disable-gpu --hide-scrollbars \
  --window-size="${W},${H}" --virtual-time-budget=3000 \
  --screenshot="$OUT_WIN" "http://localhost:8090${PATH_}" >/dev/null 2>&1

if [ -s "$OUT_POSIX" ]; then
  echo "$OUT_POSIX"
else
  echo "SHOT FAILED for ${PATH_}" >&2
  exit 1
fi
