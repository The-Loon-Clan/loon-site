#!/usr/bin/env python3
"""Move inline style attributes onto the utility classes that already exist.

    python3 scripts/sweep_inline_styles.py            # report only
    python3 scripts/sweep_inline_styles.py --write    # rewrite the files

WHY THIS IS A SCRIPT AND NOT A MORNING'S EDITING. There are 1,715 of these and
they keep 'unsafe-inline' in style-src, which cannot be dropped while one
remains -- there is no nonce for a style ATTRIBUTE the way there is for a
<style> element. A third of them say nothing that theme.css does not already
have a class for, and that third is pure transcription: no naming, no judgment,
and no reason for a person to do it by hand and make a typo in the eight
hundredth one.

WHAT IT WILL NOT TOUCH, which matters more than what it will:

  - an attribute where ANY declaration has no class. All or nothing: half a
    style attribute is still a style attribute, still blocks the CSP, and now
    also has its rules in two places.
  - anything with a template action or a format verb in it. Those are computed
    per render -- a progress bar's width -- and a class cannot hold a number
    that is not known yet.
  - requests/, which belongs to another workstream.

FONT SIZES ARE SNAPPED, and that is a real change rather than a rename. The
inline values are 39 distinct sizes -- 0.72, 0.74, 0.78, 0.82, 0.88rem --
which is not a scale, it is 39 opinions. Each becomes its nearest --fs-* step.
Several were BELOW --fs-3xs, which tokens.css records as the floor the a11y
sweep set, so those get LARGER: 0.55rem is 8.8px and nobody meant that.
The largest single move is 0.03rem, about half a pixel, except for those.
"""
import io
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PLUGINS = os.path.join(os.path.dirname(ROOT), "loon-plugins")
SKIP = {".git", "node_modules", "vendor", "scratchpad", "examples", "testdata",
        ".claude", "deploy", ".github"}
# Another workstream's files. Named rather than guessed at: this repository is
# shared two ways and a sweep that rewrites 636 attributes across somebody
# else's in-flight work is a merge conflict with a script's name on it.
SKIP_PREFIX = ("requests/", "agent/", "ranks/")

# The --fs-* scale from tokens.css, in rem.
SCALE = [("3xs", 0.70), ("2xs", 0.75), ("xs", 0.80), ("sm", 0.85), ("md", 0.90),
         ("base", 0.94), ("lg", 1.00), ("xl", 1.10), ("2xl", 1.30), ("3xl", 1.50)]

# Everything below already exists in web/static/css/theme.css.
COLOURS = {
    "var(--text-muted)": "text-muted", "var(--muted)": "text-muted",
    "var(--text-primary)": "text-light", "var(--text)": "text-light",
    "var(--text-color)": "text-light",
    "var(--danger)": "text-danger", "var(--red)": "text-danger",
    "var(--success)": "text-success", "var(--green)": "text-success",
    "var(--warning)": "text-warning", "var(--warn)": "text-warning",
    "var(--info)": "text-info",
    "var(--blue)": "text-primary", "var(--primary-tint)": "text-primary",
}
DISPLAY = {"inline": "d-inline", "block": "d-block", "none": "d-none",
           "flex": "d-flex", "inline-block": "d-inline-block"}
WEIGHT = {"700": "fw-bold", "bold": "fw-bold", "600": "fw-semibold",
          "400": "fw-normal", "normal": "fw-normal"}

# SPACING IS MATCHED EXACTLY, never snapped. Font sizes could be rounded to the
# nearest step because half a pixel of type is imperceptible and the scale is
# the point; a margin is layout, and moving one by 0.05rem moves everything
# beside it. A value off the scale is left alone for a person to decide.
SPACE = {"0": "0", "0rem": "0", "0px": "0", ".25rem": "1", "0.25rem": "1",
         ".5rem": "2", "0.5rem": "2", "1rem": "3", "1.5rem": "4", "3rem": "5"}
SPACE_PREFIX = {"margin": "m", "margin-top": "mt", "margin-bottom": "mb",
                "margin-left": "ms", "margin-right": "me",
                "padding": "p", "padding-top": "pt", "padding-bottom": "pb",
                "padding-left": "ps", "padding-right": "pe"}
GAP = {".25rem": "gap-1", "0.25rem": "gap-1", ".5rem": "gap-2",
       "0.5rem": "gap-2", "1rem": "gap-3"}
# One value, one class. Anything not here has no class and stops the attribute.
EXACT = {
    "flex-wrap:wrap": "flex-wrap",
    "flex-direction:column": "flex-column",
    "flex-grow:1": "flex-grow-1",
    "flex:1": "flex-1",
    "align-items:center": "align-items-center",
    "align-items:baseline": "align-items-baseline",
    "align-items:flex-end": "align-items-end",
    "align-items:flex-start": "align-items-start",
    "align-self:center": "align-self-center",
    "justify-content:space-between": "justify-content-between",
    "justify-content:center": "justify-content-center",
    "justify-content:flex-end": "justify-content-end",
    "white-space:nowrap": "text-nowrap",
    "vertical-align:middle": "align-middle",
    "text-transform:uppercase": "text-uppercase",
    "text-align:left": "text-start",
    "text-align:center": "text-center",
    "text-align:right": "text-end",
    "width:100%": "w-100",
    "margin-left:auto": "ms-auto",
    "margin-right:auto": "me-auto",
    "margin-top:auto": "mt-auto",
    "background:var(--bg-elevated)": "bg-elevated",
    "background:var(--bg-surface)": "bg-surface",
    "background:var(--surface)": "bg-surface",
    "background:var(--bg)": "bg-dark",
    "background:var(--surface-3)": "bg-secondary",
}

STYLE_ATTR = re.compile(r'\sstyle\s*=\s*"([^"]*)"', re.I)
CLASS_ATTR = re.compile(r'\sclass\s*=\s*"([^"]*)"', re.I)
REM = re.compile(r"^([0-9]*\.?[0-9]+)rem$")


def fs_class(value):
    m = REM.match(value)
    if not m:
        return None
    v = float(m.group(1))
    name, _ = min(SCALE, key=lambda s: abs(s[1] - v))
    if v < SCALE[0][1]:
        name = SCALE[0][0]  # never snap below the floor
    return "fs-" + name


def classes_for(decl_text):
    """Every declaration mapped, or None if any one of them is not."""
    out = []
    for d in decl_text.split(";"):
        d = d.strip()
        if not d:
            continue
        if ":" not in d:
            return None
        k, v = d.split(":", 1)
        k, v = k.strip().lower(), v.strip().lower().rstrip(";")
        v = re.sub(r"\s+", "", v)
        cls = None
        if k == "font-size":
            cls = fs_class(v)
        elif k == "color":
            cls = COLOURS.get(v)
        elif k == "display":
            cls = DISPLAY.get(v)
        elif k == "font-weight":
            cls = WEIGHT.get(v)
        elif k == "text-decoration" and v == "none":
            cls = "text-decoration-none"
        elif k == "gap":
            cls = GAP.get(v)
        elif k in SPACE_PREFIX and v in SPACE:
            cls = SPACE_PREFIX[k] + "-" + SPACE[v]
        if cls is None:
            cls = EXACT.get(k + ":" + v)
        if cls is None:
            return None
        out.append(cls)
    return out


def rewrite(src):
    """Returns (new_src, converted, left). Idempotent."""
    converted = left = 0
    out, pos = [], 0
    for m in STYLE_ATTR.finditer(src):
        decls = m.group(1)
        if "{{" in decls or "%s" in decls or "%d" in decls or "%v" in decls:
            left += 1
            continue
        cls = classes_for(decls)
        if not cls:
            left += 1
            continue
        # The attribute goes. Its classes join the element's existing class
        # attribute if it has one, so an element does not end up with two.
        tag_start = src.rfind("<", 0, m.start())
        tag_end = src.find(">", m.end())
        if tag_start < 0 or tag_end < 0:
            left += 1
            continue
        tag = src[tag_start:tag_end + 1]
        without = tag[:m.start() - tag_start] + tag[m.end() - tag_start:]
        cm = CLASS_ATTR.search(without)
        if cm:
            have = cm.group(1).split()
            merged = have + [c for c in cls if c not in have]
            new_tag = (without[:cm.start()] + ' class="' + " ".join(merged) + '"'
                       + without[cm.end():])
        else:
            insert = without.find(" ")
            if insert < 0 or insert > without.find(">"):
                insert = without.find(">")
            new_tag = without[:insert] + ' class="' + " ".join(cls) + '"' + without[insert:]
        out.append((tag_start, tag_end + 1, new_tag))
        converted += 1
    if not out:
        return src, 0, left
    buf, pos = [], 0
    for a, b, t in out:
        buf.append(src[pos:a])
        buf.append(t)
        pos = b
    buf.append(src[pos:])
    return "".join(buf), converted, left


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
                rel = os.path.relpath(p, root).replace(os.sep, "/")
                if rel.startswith(SKIP_PREFIX):
                    continue
                yield label, rel, p


def main():
    write = "--write" in sys.argv
    tot_c = tot_l = files = 0
    for label, rel, path in sources():
        src = io.open(path, encoding="utf-8", errors="replace").read()
        if "style=" not in src:
            continue
        new, c, l = rewrite(src)
        tot_c += c
        tot_l += l
        if c and write:
            io.open(path, "w", encoding="utf-8", newline="").write(new)
        if c:
            files += 1
            print("  %4d converted, %4d left   %s %s" % (c, l, label, rel))
    print()
    print("%s %d attribute(s) across %d file(s); %d left, which say something "
          "no class covers." % ("Converted" if write else "WOULD convert",
                                tot_c, files, tot_l))
    if not write:
        print("Nothing was written. Pass --write.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
