"""Refuse Bootstrap behaviour attributes this site cannot honour.

`data-bs-toggle` and friends are inert markup unless Bootstrap's JavaScript is
on the page. It is not. web/static/css/bootstrap.min.css is 790 bytes and says
so in its own first line -- "Not real Bootstrap - a tiny reset + base" -- and the
only Bootstrap object that exists at runtime is the ~10-line window.bootstrap.Tab
shim at the foot of site_chrome.html.

So every other data-bs-* attribute in this codebase has been decoration. Not
degraded, not partially working: a "+ New Item" button that opened nothing, a
"Claim ownership" button that opened nothing, a modal that rendered as a plain
block stacked at the foot of the page with a Cancel button that cancelled
nothing, a dropdown menu permanently splayed open under its own toggle. Twelve
of them, across five plugins, none of which raised an error anywhere.

Worse than inert, in two cases. flow.html guarded its modal with

    if (modalEl && window.bootstrap) { bootstrap.Modal.getOrCreateInstance(...) }

and `window.bootstrap` IS truthy -- the Tab shim defines it. `bootstrap.Modal`
is not, so the guard passed and the next line threw. The button was dead on
click, which is the one failure mode the guard was written to prevent.

CHECKLIST section 8 predicted the whole thing: "a CDN framework arrives as dead
toggles with a lying aria-expanded. Prefer <details> and CSS over scripts."
The rule existed. Nothing enforced it, so the markup kept arriving.

What is allowed: toggle="tab" and toggle="pill", because the shim really does
drive those, plus the data-bs-target that pairs with them.

    python scripts/audit_bootstrap.py

Exits non-zero above the baseline.
"""
import io
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SKIP_DIRS = {".git", "node_modules", "__pycache__", "vendor", "clonecheck",
             "examples", "docs"}

TREES = [
    ("host", os.path.join(ROOT, "web")),
    ("plugins", os.path.join(os.path.dirname(ROOT), "loon-plugins")),
]

# The shim in site_chrome.html implements exactly one component. A target that
# pairs with a tab toggle rides along; a target without one is caught by the
# toggle it belongs to.
SHIMMED = {"tab", "pill"}

ATTR = re.compile(r'data-bs-(toggle|dismiss|target|slide|slide-to|ride|spy|parent)\s*=\s*"([^"]*)"')
# Go template comments hold the explanations of why a conversion happened, and
# those name the attribute they replaced. Stripping them first is what keeps a
# comment from reading as a finding.
GOCOMMENT = re.compile(r"\{\{/\*.*?\*/\}\}", re.S)
HTMLCOMMENT = re.compile(r"<!--.*?-->", re.S)

# Every remaining hit lives in one file owned by the other workstream, so this
# lands as a ratchet rather than a red build. The list is the handover: six
# controls in community_requests.html, five dismiss buttons and one collapse.
BASELINE = {"requests/templates/community_requests.html": 6}

# CI checks out this repo alone, so the plugin tree is usually absent there and
# every baseline file counts zero. Reading that as "fixed, lower the baseline"
# would make the check fail hardest exactly where it can see least, which is how
# a ratchet gets deleted. Resolve each entry and judge only the ones on disk --
# the same guard audit_css.py puts around its own plugin baseline.
def baseline_path(rel):
    return os.path.join(os.path.dirname(ROOT), "loon-plugins", rel)


def findings():
    out = []
    for label, tree in TREES:
        if not os.path.isdir(tree):
            continue
        for dirpath, dirnames, filenames in os.walk(tree):
            dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
            for fn in sorted(filenames):
                if not fn.endswith(".html"):
                    continue
                path = os.path.join(dirpath, fn)
                src = io.open(path, encoding="utf-8", errors="replace").read()
                src = HTMLCOMMENT.sub("", GOCOMMENT.sub("", src))
                rel = os.path.relpath(path, os.path.dirname(tree)
                                      if label == "plugins" else ROOT)
                rel = rel.replace("\\", "/")
                if label == "plugins":
                    rel = rel.split("loon-plugins/", 1)[-1]
                for m in ATTR.finditer(src):
                    kind, value = m.group(1), m.group(2)
                    if kind == "toggle" and value in SHIMMED:
                        continue
                    if kind == "target":
                        continue  # judged by the toggle it pairs with
                    out.append((rel, m.group(0)))
    return out


def main():
    found = findings()
    counts = {}
    for rel, _ in found:
        counts[rel] = counts.get(rel, 0) + 1

    over = {f: n for f, n in counts.items() if n > BASELINE.get(f, 0)}
    if over:
        print("bootstrap: %d inert behaviour attribute(s) with no JS to honour them\n"
              % sum(over.values()))
        for rel, _ in found:
            if rel in over:
                print("   %-58s %s" % (rel, _))
        print("\n   No Bootstrap JS is loaded. Use <details> for disclosure,")
        print("   <dialog> for a real modal, or a link for navigation.")
        return 1

    stale = {f: n for f, n in BASELINE.items()
             if os.path.isfile(baseline_path(f)) and counts.get(f, 0) < n}
    if stale:
        print("bootstrap: baseline stale (fixed, so lower it in this commit)\n")
        for f, n in sorted(stale.items()):
            print("   %-58s %d, baseline %d" % (f, counts.get(f, 0), n))
        return 1

    reachable = sum(n for f, n in BASELINE.items() if os.path.isfile(baseline_path(f)))
    print("bootstrap: 0 inert behaviours outside the baseline (%d there%s)"
          % (reachable, "" if reachable == sum(BASELINE.values())
             else ", plugin tree not checked out"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
