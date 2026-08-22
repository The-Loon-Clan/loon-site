#!/usr/bin/env python3
"""Screenshot a local HTML file.

    python scripts/shotfile.py scratchpad/forum-preview/community_forums.preview.html
    python scripts/shotfile.py <file> 1400 2400

shot.py needs a running site and a session; this needs neither, because some
markup cannot be reached through a URL at all. The forum plugin's five
templates are the case that prompted it: they render only on the RenderPage
contract, the one host that exists still wires the legacy BaseData one and
serves its own copies instead, so those files execute nowhere a person can
look at them. That is how 144 undefined class names accumulated in there --
nothing rendered them, so nothing showed the gap.

Same Chrome invocation as shot.py, same reason: reading a stylesheet is not
looking at a page.
"""

import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import _site  # noqa: E402

CHROME, winpath = _site.CHROME, _site.winpath


def shot(path, width=1400, height=1000):
    page = os.path.abspath(path)
    if not os.path.exists(page):
        print("no such file: %s" % page)
        return 1
    png = os.path.splitext(page)[0] + ".png"
    if os.path.exists(png):
        os.remove(png)
    subprocess.run(
        [CHROME, "--headless=new", "--disable-gpu", "--hide-scrollbars",
         "--allow-file-access-from-files", "--window-size=%d,%d" % (width, height),
         "--virtual-time-budget=3000", "--screenshot=" + winpath(png),
         "file:///" + winpath(page)],
        capture_output=True, text=True, timeout=120,
        encoding="utf-8", errors="replace",
    )
    if not os.path.exists(png) or os.path.getsize(png) == 0:
        print("Chrome produced no image. CHROME=%s" % CHROME)
        return 1
    print(png)
    return 0


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(2)
    args = [_site.unmangle(a) for a in sys.argv[1:]]
    sys.exit(shot(args[0],
                  int(args[1]) if len(args) > 1 else 1400,
                  int(args[2]) if len(args) > 2 else 1000))
