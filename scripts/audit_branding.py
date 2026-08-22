"""Refuse one site's identity hardcoded into a shared plugin.

A plugin in loon-plugins is installed by whoever wants it. A sentence in one
that names a particular site renders that name on every host that installs it,
and there is no configuration that can take it back out.

This has now been found by hand three times in one day, each time by looking at
a rendered page rather than by any check:

    donations/help_donate.html   "ameNZB is 100% community-funded", "Support
                                 ameNZB", "Total annual cost to run ameNZB",
                                 "keeping ameNZB fast, secure, and online",
                                 "ameNZB exists because of you" — five
                                 sentences, plus a Rain/Storm/Monsoon/Typhoon
                                 donor ladder built on the same site's motif
    roadmap/flow_proposals.html  "Have an idea to make amenzb better?"
    releasegroups                a claim snippet labelled "ameNZB 💧" that a
                                 group owner pastes into their own bio

The remedy already exists and is already used: wiki and donations both take a
SiteName seam, with the comment "a plugin cannot know the name and must not
guess it, and there is exactly one right answer per deployment, so the host
says." What was missing is anything that notices when a new one arrives.

COMMENTS ARE EXEMPT, deliberately. "It exists because this page said ameNZB
five times" is the explanation of a fix; refusing it would delete the reason
along with the bug, and the next person would reintroduce both.

    python scripts/audit_branding.py

Exits non-zero above the baseline.
"""
import io
import os
import re
import sys


def say(line):
    """print, surviving a console that cannot encode the finding.

    The releasegroups snippet this check exists to catch contains an emoji, and
    a cp1252 console raised UnicodeEncodeError while printing it — a checker
    that crashes on its own worst finding is worse than no checker.
    """
    enc = getattr(sys.stdout, "encoding", None) or "utf-8"
    sys.stdout.write(line.encode(enc, "replace").decode(enc, "replace") + os.linesep)


ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SKIP_DIRS = {".git", "node_modules", "__pycache__", "vendor", "clonecheck",
             "examples", "docs", "migrations"}

# The site identities a SHARED plugin must not assert. This is a denylist and
# not a heuristic on purpose: "which words are a site's name" cannot be
# inferred, and a check that guesses would either miss the next one or refuse
# ordinary prose.
#
# ameNZB is the site most of these plugins were lifted out of, which is why it
# is the one that keeps turning up.
IDENTITIES = [
    re.compile(r"amenzb", re.I),
]

TREES = [
    ("plugins", os.path.join(os.path.dirname(ROOT), "loon-plugins")),
]

GOCOMMENT = re.compile(r"\{\{/\*.*?\*/\}\}", re.S)
HTMLCOMMENT = re.compile(r"<!--.*?-->", re.S)
BLOCKCOMMENT = re.compile(r"/\*.*?\*/", re.S)
LINECOMMENT = re.compile(r"^[ \t]*//.*$", re.M)
TRAILCOMMENT = re.compile(r"(?<![:\"'/])//[^\"'\n]*$", re.M)

# Test files name the thing they are refusing — that is what a regression test
# for this IS. donations/views_test.go asserts the page contains no "ameNZB".
SKIP_SUFFIX = ("_test.go",)

# Everything still here is a deliberate, reasoned exception. Each entry is a
# file and the count it is allowed, so a NEW one in the same file still fails.
BASELINE = {}


def _blank(m):
    """Replace a comment with blanks, keeping its newlines.

    Deleting comments outright shifts every line after them, so the line
    numbers this reports stopped matching the file — wiki/wiki.html:503 pointed
    at a JavaScript function forty lines from the finding. A check that sends
    somebody to the wrong line is worse than one that says nothing.
    """
    return re.sub(r"[^\n]", " ", m.group(0))


def strip_comments(path, text):
    if path.endswith(".html"):
        # /* */ too: a template's <style> and <script> blocks hold ordinary CSS
        # and JS comments, and donations/help_donate.html explains its own
        # layout inside one. Stripping only the template-comment form reported
        # a CSS comment as branding.
        for pat in (GOCOMMENT, HTMLCOMMENT, BLOCKCOMMENT):
            text = pat.sub(_blank, text)
        return text
    for pat in (BLOCKCOMMENT, LINECOMMENT, TRAILCOMMENT):
        text = pat.sub(_blank, text)
    return text


def findings():
    out = []
    for label, tree in TREES:
        if not os.path.isdir(tree):
            continue
        for dirpath, dirs, files in os.walk(tree):
            dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS)
            for fn in sorted(files):
                if not fn.endswith((".html", ".go")) or fn.endswith(SKIP_SUFFIX):
                    continue
                path = os.path.join(dirpath, fn)
                raw = io.open(path, encoding="utf-8", errors="replace").read()
                body = strip_comments(path, raw)
                rel = os.path.relpath(path, tree).replace(os.sep, "/")
                for pat in IDENTITIES:
                    for m in pat.finditer(body):
                        line = body[:m.start()].count("\n") + 1
                        ctx = body.splitlines()[line - 1].strip()
                        out.append((rel, line, ctx[:88]))
    return out


def main():
    found = findings()
    counts = {}
    for rel, _, _ in found:
        counts[rel] = counts.get(rel, 0) + 1

    over = {f: n for f, n in counts.items() if n > BASELINE.get(f, 0)}
    if over:
        print("branding: %d place(s) where a shared plugin names one site\n"
              % sum(over.values()))
        for rel, line, ctx in found:
            if rel in over:
                say("   %s:%d\n       %s" % (rel, line, ctx))
        print("")
        print("A plugin cannot know the deployment's name and must not guess it.")
        print("wiki and donations both take a SiteName seam for this; the host")
        print("answers it, and the copy reads \"this site\" where none is wired.")
        print("Comments are exempt — the explanation of a fix is not the bug.")
        return 1

    stale = {f: n for f, n in BASELINE.items() if counts.get(f, 0) < n}
    if stale:
        print("branding: baseline stale (fixed, so lower it in this commit)\n")
        for f, n in sorted(stale.items()):
            print("   %-58s %d, baseline %d" % (f, counts.get(f, 0), n))
        return 1

    print("branding: no shared plugin names a particular site (%d exempted)"
          % sum(BASELINE.values()))
    return 0


if __name__ == "__main__":
    sys.exit(main())
