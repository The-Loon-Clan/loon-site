#!/usr/bin/env python3
"""Contrast ratios for the theme token pairs that carry text.

    python3 scripts/contrast.py

Lighthouse found this — `.panel__grouping` was 4.16:1 on cosmic-void and 2.75:1
on nord, both under the 4.5:1 that WCAG AA asks for normal text. But Lighthouse
only reports what was ON THE PAGE IT LOADED, in the theme that happened to be
active, so it can only ever catch one theme at a time and only where a given
element is rendered. This checks the tokens directly, so a theme nobody
screenshotted is checked too.

Not exhaustive: it knows the pairs listed below, which are the ones where a
token is deliberately dimmed against a known surface. Adding a token means
adding it here — the alternative is parsing the CSS and guessing which pairs
ever meet, which would be confidently wrong rather than incomplete.
"""

import re
import sys
from pathlib import Path

THEMES = Path("web/static/css/themes")

# (foreground token, background token, what it is)
PAIRS = [
    ("--panel-group-fg", "--panel-bg", "the count beside a panel heading"),
    ("--muted", "--bg", "secondary copy on the page canvas"),
    ("--muted", "--surface", "secondary copy on a panel"),
    ("--text", "--bg", "body text"),
]

AA_NORMAL = 4.5


def srgb_to_linear(c: float) -> float:
    c /= 255
    return c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4


def luminance(hexcolor: str) -> float:
    h = hexcolor.lstrip("#")
    r, g, b = (int(h[i:i + 2], 16) for i in (0, 2, 4))
    return (0.2126 * srgb_to_linear(r)
            + 0.7152 * srgb_to_linear(g)
            + 0.0722 * srgb_to_linear(b))


def ratio(fg: str, bg: str) -> float:
    a, b = luminance(fg), luminance(bg)
    hi, lo = max(a, b), min(a, b)
    return (hi + 0.05) / (lo + 0.05)


def tokens(path: Path) -> dict:
    """Read --name: value pairs, resolving one level of var() indirection.

    COMMENTS ARE STRIPPED FIRST, and that is not tidiness. The scan runs over
    the whole file, dict() keeps the LAST match, and nord.css contains

        --panel-head-bg: #2e3440;   /* = --bg: nord's header is the page showing through */

    so `--bg:` inside that comment produced a second, later entry for --bg whose
    value was prose. It failed the hex test, --bg vanished from the result, and
    the two pairs that use it — body text on the page canvas, and secondary copy
    on it — were skipped with "(token not resolvable to a hex value)" while the
    summary still read "every checked pair clears 4.5:1".

    A comment that MENTIONS a token silently switched that token off.
    """
    text = re.sub(r"/\*.*?\*/", " ", path.read_text(encoding="utf-8"), flags=re.S)
    raw = dict(re.findall(r"(--[a-z0-9-]+):\s*([^;]+);", text))
    out = {}
    for k, v in raw.items():
        v = v.split("/*")[0].strip()
        m = re.fullmatch(r"var\((--[a-z0-9-]+)\)", v)
        if m:
            v = raw.get(m.group(1), "").split("/*")[0].strip()
        if re.fullmatch(r"#[0-9a-fA-F]{6}", v):
            out[k] = v
    return out


def main() -> int:
    failures = 0
    for css in sorted(THEMES.glob("*.css")):
        tok = tokens(css)
        print(css.stem)
        for fg, bg, what in PAIRS:
            if fg not in tok or bg not in tok:
                print("  %-38s (token not resolvable to a hex value)" % what)
                continue
            r = ratio(tok[fg], tok[bg])
            ok = r >= AA_NORMAL
            if not ok:
                failures += 1
            print("  %-38s %s on %s  %.2f  %s"
                  % (what, tok[fg], tok[bg], r, "" if ok else "FAIL"))
    print()
    if failures:
        print("%d pair(s) below the %.1f:1 WCAG AA minimum for normal text" % (failures, AA_NORMAL))
        return 1
    print("every checked pair clears %.1f:1" % AA_NORMAL)
    return 0


if __name__ == "__main__":
    sys.exit(main())
