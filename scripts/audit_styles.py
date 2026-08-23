#!/usr/bin/env python3
"""Styling that no other check can see: dead tokens, and inline attributes.

    python3 scripts/audit_styles.py

TWO THINGS, and they are related by what looks at them rather than by what
they are. `audit_css.py` reads CLASSES and cannot see a style attribute.
`contrast.py` reads TOKENS and cannot see a literal. `audit_paint.py` measures
what was PAINTED and cannot see a declaration the browser threw away. Between
the three there is a hole, and both of these live in it.

1. A DEAD var(). `var(--name)` with no fallback, where nothing defines --name,
   is not a fallback to anything -- the declaration is invalid at computed-value
   time and the browser drops it whole, so the element inherits instead. It
   degrades gracefully, which is exactly why it survives: an inherited colour
   usually looks close enough to right.

   There were 352 of these on 23 Aug 2026 -- 219 in plugin stylesheets and 133
   in inline attributes, across 27 names. --text-primary alone accounted for 88,
   which is to say the main text colour of ten plugins was doing nothing at all.
   Eight names are aliased in the themes now and the rest are counted here.

   Fixing them is not free and the baseline says why: giving 199 declarations
   their intended value made eighteen strings fail WCAG AA at once. The colours
   had always been that dark; what changed is that they became measurable.

2. AN INLINE style ATTRIBUTE. 1,715 of them, and the reason to count is the
   Content-Security-Policy: `style-src` still carries 'unsafe-inline' purely
   for these, and it cannot be dropped while one remains. There is no nonce for
   a style attribute the way there is for a <style> element, so this is not a
   thing that can be worked around -- it is a number that has to reach zero.

   A ratchet rather than a rule, because 1,715 is a body of work and a check
   that fails on all of it is a check somebody deletes. It may go down.
"""
import collections
import glob
import io
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PLUGINS = os.path.join(os.path.dirname(ROOT), "loon-plugins")
SKIP = {".git", "node_modules", "vendor", "scratchpad", "examples", "testdata",
        ".claude", "deploy", ".github"}

# Where it stands. Both may fall and neither may rise.
#
#   23 Aug 2026, first measurement:  host 44, plugins 1671  (1,715)
#   23 Aug 2026, after the sweep:    host 41, plugins 1054  (1,095)
#   23 Aug 2026, second pass:        host 34, plugins  946  (  980)
#   23 Aug 2026, opacity -> tiers:    host 34, plugins  905  (  939)
#
# 620 went in one commit because they said something theme.css already had a
# class for -- see scripts/sweep_inline_styles.py. What is left says things no
# class covers: widths, gaps, paddings, backgrounds, one-off accent colours.
# Those need NAMING, which is judgment rather than transcription, and 302 of
# them are in requests/ and belong to another workstream.
BASELINE_INLINE = {"host": 34, "plugins": 905}
BASELINE_DEAD = {"host": 0, "plugins": 32}

STYLE_ATTR = re.compile(r'style\s*=\s*"([^"]*)"', re.I)
# var(--x) with NO comma: a fallback makes it valid, whatever --x is.
BARE_VAR = re.compile(r"var\(\s*(--[a-zA-Z0-9-]+)\s*\)")
DEFINES = re.compile(r"(--[a-zA-Z0-9-]+)\s*:")


def defined_properties():
    """Every custom property the host or any plugin defines.

    Plugin stylesheets are Go constants (see loon-plugins/*/stylesheet.go), so
    this reads .go as well as .css -- the same blind spot that made audit_css
    invent seven findings the day the RegisterCSS migration landed.
    """
    out = set()
    for p in (glob.glob(os.path.join(ROOT, "web", "static", "css", "**", "*.css"), recursive=True)
              + glob.glob(os.path.join(PLUGINS, "*", "stylesheet.go"))):
        out |= set(DEFINES.findall(io.open(p, encoding="utf-8", errors="replace").read()))
    return out


def sources():
    for label, root in (("host", ROOT), ("plugins", PLUGINS)):
        if not os.path.isdir(root):
            continue
        for base, dirs, names in os.walk(root):
            dirs[:] = [d for d in dirs if d not in SKIP]
            for fn in sorted(names):
                if not (fn.endswith(".html") or fn.endswith(".go")):
                    continue
                if fn.endswith("_test.go"):
                    continue
                p = os.path.join(base, fn)
                yield label, os.path.relpath(p, root).replace(os.sep, "/"), p


def main():
    defined = defined_properties()
    if not defined:
        print("styles: no custom properties found anywhere -- the CSS moved, and\n"
              "this would report every var() as dead. Refusing to guess.")
        return 1

    inline = collections.Counter()
    dead = collections.Counter()
    dead_names = collections.Counter()
    worst = collections.Counter()

    for label, rel, path in sources():
        src = io.open(path, encoding="utf-8", errors="replace").read()
        n = len(STYLE_ATTR.findall(src))
        if n:
            inline[label] += n
            worst[label + " " + rel] += n
        for m in BARE_VAR.finditer(src):
            if m.group(1) not in defined:
                dead[label] += 1
                dead_names[m.group(1)] += 1

    failed = False
    print("DEAD var() -- the declaration is dropped and the element inherits")
    for label in ("host", "plugins"):
        was, now = BASELINE_DEAD.get(label, 0), dead[label]
        mark = ""
        if now > was:
            mark, failed = "  WORSE", True
        elif now < was:
            mark = "  (baseline is stale -- lower it in this commit)"
            failed = True
        print("  %-8s %4d   baseline %4d%s" % (label, now, was, mark))
    if dead_names:
        print("  names:", ", ".join("%s x%d" % (k, v) for k, v in dead_names.most_common(6)))

    print()
    print("INLINE style attributes -- what keeps 'unsafe-inline' in style-src")
    for label in ("host", "plugins"):
        was, now = BASELINE_INLINE.get(label, 0), inline[label]
        mark = ""
        if now > was:
            mark, failed = "  WORSE", True
        elif now < was:
            mark = "  (baseline is stale -- lower it in this commit)"
            failed = True
        print("  %-8s %4d   baseline %4d%s" % (label, now, was, mark))
    print("  heaviest files:")
    for k, v in worst.most_common(5):
        print("    %4d  %s" % (v, k))

    print()
    if failed:
        print("styles: a count moved. Raising either one is a regression; lowering\n"
              "one is progress that has to be recorded here, or the ratchet stops\n"
              "ratcheting and the next rise goes unnoticed.")
        return 1
    print("styles: %d dead var() and %d inline attribute(s), both at the baseline.\n"
          "style-src cannot drop 'unsafe-inline' until the second reaches zero."
          % (sum(dead.values()), sum(inline.values())))
    return 0


if __name__ == "__main__":
    sys.exit(main())
