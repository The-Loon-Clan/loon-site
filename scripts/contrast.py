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
    # Added 22 Aug 2026, after LIGHTHOUSE found two failures on the rendered
    # front page that this file passed. It checked --muted and not --muted-2,
    # so the tier of text actually used for timestamps and captions was never
    # measured; and it checked no username colour at all, though a username is
    # on nearly every row of the site.
    #
    # The lesson is the one this repo keeps relearning: a hand-written list
    # finds what somebody thought to list. Lighthouse measures what the browser
    # PAINTED, and is the check that can find a pair nobody wrote down — but it
    # runs one page at a time and cannot cover a theme nobody is viewing, which
    # is what this file is for. They answer different questions.
    ("--muted-2", "--bg", "timestamps and captions on the page canvas"),
    ("--muted-2", "--surface", "timestamps and captions on a panel"),
    # --surface-2 is a text ground too: avatars, badge tiles, dash tiles and
    # the forum poster column all sit on it, and in cosmic-void it is now the
    # LIGHTEST of the three, so it binds where --bg and --surface do not.
    ("--muted-2", "--surface-2", "timestamps and captions on a raised block"),
    ("--text", "--surface-2", "body text on a raised block"),
    # Every ROLE colour, not just member. A username is on nearly every row of
    # this site and the role decides its colour, so checking one of five was
    # checking the one that happens to be plain text. Lighthouse found the
    # admin red at 3.65:1 on a panel; the other three had never been measured.
    #
    # Against --surface-3, and the ground moved on 22 Aug 2026 because the old
    # reasoning had stopped being true. It said "a panel is the LIGHTER of the
    # two grounds in every theme, so it is the binding constraint" -- which was
    # sound when there were two grounds. There are four, --surface-3 is the
    # lightest in all three themes, and cosmic-void's scale was corrected so
    # that --surface is now its DARKEST raised ground: the exact inversion of
    # what the sentence assumed. Six role colours failed there and here, one of
    # them the admin red on /staff, which is the page that lists them all.
    #
    # A pair that clears --surface-3 clears every ground below it, so this is
    # one line per role rather than four.
    ("--user-tag-member-fg", "--surface-3", "a member's name on a raised row"),
    ("--user-tag-admin-fg", "--surface-3", "an admin's name on a raised row"),
    ("--user-tag-mod-fg", "--surface-3", "a moderator's name on a raised row"),
    ("--user-tag-contributor-fg", "--surface-3", "a contributor's name on a raised row"),
    ("--user-tag-banned-fg", "--surface-3", "a banned member's name on a raised row"),
    # The primary button: a filled control with a 14px label, so AA applies
    # at 4.5. No pair named it, which is how cosmic-void shipped 3.15:1 and
    # midnight 4.35:1 — the second never measured by anything at all.
    ("--primary-fg", "--primary-strong", "the label on a primary button"),
    # Added 22 Aug 2026 by MEASUREMENT rather than by thinking of them.
    #
    # --surface-3 was in this file's grounds exactly nowhere, in any pair, for
    # its whole life. The token above calls it "pressed", which is why nobody
    # listed it -- but a dozen components rest on it (.tag--cat, .prose th, the
    # inbox rows, two plugins), so it is a text ground like any other, and the
    # LIGHTEST one there is. --muted-2 was lifted on this same day with a note
    # saying it "clears all three grounds"; it counted three and there are four.
    #
    # Found by rendering plugin pages in every theme and reading the contrast of
    # what the browser actually PAINTED (scripts/audit_paint.py). That is the
    # answer to this file's own limitation: a hand-written list finds what
    # somebody thought to list, and no amount of care fixes that from the inside.
    ("--text", "--surface-3", "body text on a raised row"),
    ("--muted", "--surface-3", "secondary copy on a raised row"),
    ("--muted-2", "--surface-3", "timestamps and captions on a raised row"),
    # A tag is a filled chip with a small label, and it sits on both raised
    # grounds. Neither was measured against anything.
    ("--success", "--surface-2", "a success tag on a chip"),
    ("--success", "--surface-3", "a success tag on a raised row"),
    ("--primary-tint", "--surface-2", "avatar initials on a chip"),
    ("--primary-tint", "--surface-3", "avatar initials on a raised row"),
    # A table header has its OWN ground token, and two of the three themes set
    # it to something other than --surface-2 -- cosmic-void deliberately, nord
    # by writing --surface-2's value out a second time and then not following
    # it. Checked as the separate ground it is, which is what would have caught
    # that literal on the day it stopped matching.
    ("--text", "--data-table-th-bg", "a column heading"),
    ("--muted", "--data-table-th-bg", "secondary copy in a column heading"),
    ("--success", "--data-table-th-bg", "a success tag in a table header"),
    # The leaderboard's medal chips. Found by audit_paint.py on the FRONT PAGE,
    # which is as visited as a page gets -- the bronze read 3.33:1 in nord and
    # 3.92:1 in cosmic-void, and no pair here had ever named a rank colour, so
    # nothing had measured any of the three in any theme.
    ("--rank-1-fg", "--surface-3", "first place on the leaderboard"),
    ("--rank-2-fg", "--surface-3", "second place on the leaderboard"),
    ("--rank-3-fg", "--surface-3", "third place on the leaderboard"),
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


# ── the palette must be the only place a ground colour is written ───────────
#
# Every ground in PAIRS is a palette token, and a component token that repeats
# one of their VALUES as a literal is not checked by anything -- it is a second
# copy that no longer moves when the first does.
#
# That is not hypothetical. nord's --surface-2 was darkened for contrast on
# 22 Aug 2026 and eight component tokens stayed behind on #434c5e, so a table
# header, a chip and a poster went on failing after every pair in this file
# cleared. The painted audit still saw it; this file could not, because the
# colour it was checking was no longer the colour being drawn.
PALETTE = ("--bg", "--surface", "--surface-2", "--surface-3")

# A background token whose literal is DELIBERATELY not a palette value needs no
# entry here -- only one that repeats a palette value is a copy. cosmic-void's
# --data-table-th-bg (#19191b) is the case that matters: it is UNIT3D's dark
# table header, chosen to differ from --surface-2, and it stays a literal.
BG_TOKEN = re.compile(r"^\s*(--[a-z0-9-]*bg[a-z0-9-]*):\s*(#[0-9a-fA-F]{6});", re.M | re.I)


def literal_copies(path: Path) -> list:
    """Background tokens that write a palette value out a second time."""
    tk = tokens(path)
    by_value = {}
    for name in PALETTE:
        if name in tk:
            by_value.setdefault(tk[name].lower(), name)
    text = path.read_text(encoding="utf-8")
    text = re.sub(r"/\*.*?\*/", " ", text, flags=re.S)
    out = []
    for m in BG_TOKEN.finditer(text):
        token, value = m.group(1), m.group(2).lower()
        owner = by_value.get(value)
        if owner and token != owner:
            out.append((token, value, owner))
    return out


def main() -> int:
    failures = 0
    copies = 0
    for css in sorted(THEMES.glob("*.css")):
        tok = tokens(css)
        print(css.stem)
        for token, value, owner in literal_copies(css):
            copies += 1
            print("  %-38s %s repeats %s -- write var(%s)"
                  % ("a ground written twice", token, owner, owner))
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
    if copies:
        print("%d background token(s) repeat a palette colour as a literal. "
              "A copy does not move when the palette does: that is how nord's "
              "--surface-2 was darkened for contrast and eight components kept "
              "drawing the old colour. Write var(--token) instead." % copies)
    if failures:
        print("%d pair(s) below the %.1f:1 WCAG AA minimum for normal text" % (failures, AA_NORMAL))
    if failures or copies:
        return 1
    print("every checked pair clears %.1f:1, and no ground is written twice" % AA_NORMAL)
    return 0


if __name__ == "__main__":
    sys.exit(main())
