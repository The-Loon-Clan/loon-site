#!/usr/bin/env python3
"""Screenshot a page that needs a session.

    python scripts/shot.py tracker /tracker
    python scripts/shot.py tracker /tracker 1400 1200

Writes the PNG and prints its path, so a UI change can be SEEN rather than
inferred from the CSS.

WHY THIS EXISTS ALONGSIDE shot.sh
---------------------------------
shot.sh points Chrome at a live URL, which is the right thing for anything
anonymous — it captures exactly what a visitor gets, headers and all. But its
own comment names the limit: Chrome's CLI cannot set a session cookie, so half
the site is out of reach, and the tracker, the account area and every admin page
are all on that half.

The trick is mobile.py's: FETCH the page with a signed-in session, save it, and
screenshot the file. Assets are absolutised on the way out so /static still
resolves, and the saved copy carries no frame-ancestors header.

What is lost is worth stating: this is the HTML the server sent, rendered from
disk. Anything the page does with its own URL — an htmx swap, a redirect, a
canonical link — is not exercised here. For layout, which is what a screenshot
is for, that costs nothing.
"""

import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import _site  # noqa: E402

CHROME, winpath = _site.CHROME, _site.winpath

BASE = os.environ.get("BASE", "http://localhost:8090")
OUT = os.environ.get(
    "SHOT_DIR",
    os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scratchpad", "shots"),
)


def shot(name, path, width=1400, height=1000):
    session = _site.Session()
    if not session.login(_site.USER, _site.PASS):
        print("could not sign in as %s — set AUDIT_USER/AUDIT_PASS." % _site.USER)
        print("Refusing to screenshot the signed-out page under a name that says otherwise.")
        return 1
    status, html = session.get(path)
    if status != 200:
        print("%s answered HTTP %s" % (path, status))
        return 1

    os.makedirs(OUT, exist_ok=True)
    html = html.replace('href="/static', 'href="%s/static' % BASE)
    html = html.replace('src="/static', 'src="%s/static' % BASE)
    page = os.path.join(OUT, name + ".html")
    with open(page, "w", encoding="utf-8") as f:
        f.write(html)

    png = os.path.abspath(os.path.join(OUT, name + ".png"))
    if os.path.exists(png):
        os.remove(png)
    subprocess.run(
        [CHROME, "--headless=new", "--disable-gpu", "--hide-scrollbars",
         "--allow-file-access-from-files", "--window-size=%d,%d" % (width, height),
         "--virtual-time-budget=3000", "--screenshot=" + winpath(png),
         "file:///" + winpath(os.path.abspath(page))],
        capture_output=True, text=True, timeout=120,
    )
    if not os.path.exists(png) or os.path.getsize(png) == 0:
        print("Chrome produced no image. CHROME=%s" % CHROME)
        return 1
    print(png)
    return 0


if __name__ == "__main__":
    if len(sys.argv) < 3:
        print(__doc__)
        sys.exit(2)
    # unmangle, or a Git Bash prompt turns /tracker into a path under its own
    # install and the failure reads as the site being down. See _site.unmangle.
    args = [_site.unmangle(a) for a in sys.argv[1:]]
    sys.exit(shot(args[0], args[1],
                  int(args[2]) if len(args) > 2 else 1400,
                  int(args[3]) if len(args) > 3 else 1000))
