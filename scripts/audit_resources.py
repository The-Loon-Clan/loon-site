"""Refuse hardcoded images, unresolvable icons, and member-facing text built in Go.

Three checks, each with a different enforcement level, because they are at
three different stages and pretending otherwise is how a lint becomes noise:

  A. IMAGES        enforced at ZERO. A literal image path in a template, a Go
                   file or a stylesheet. The host has none today, so this
                   fails on the first regression rather than reporting a
                   backlog nobody reads.

  B. ICONS         enforced at ZERO. Every <use href="#id"> must resolve to a
                   <symbol id> in the sprite sheet. A missing symbol renders
                   an EMPTY BOX -- no error, no console warning -- and the
                   host already had a Go test for its own templates. Plugins
                   draw sprites now too (medals, the ranks groups widget, the
                   store's cards) and nothing was checking those at all.

  C. CSRF          enforced at ZERO. Every POST form must carry a hidden
                   `_csrf` input. A host mounts CSRF middleware over the whole
                   engine, so one without it answers 403 to every human who
                   clicks it -- and audit_access.py CANNOT see this, because it
                   probes destructive POSTs WITH a valid token by design (it
                   tests the gate, not the form). A sweep on 18 Aug 2026 found
                   58 tokenless forms across nine plugins: every admin action
                   in usenet, ranks, events, achievements, messages and lists,
                   plus the rewards page's own toggle and create. All of them
                   had been refusing every operator who tried.

  D. GO SENTENCES  a RATCHET, not a zero. "Every user-visible string lives in
                   templates, not in Go" is already a MUST in
                   loon-plugins/CHECKLIST.md section 10, and there are 33 of
                   them here. A check that failed on all 33 today would be
                   switched off today, so the count is recorded and may only
                   go DOWN. Lower the baseline in the same commit that
                   converts one.

WHAT THIS DELIBERATELY DOES NOT CHECK: English in TEMPLATES. There are 76
host templates and 83 plugin ones, and the message-lookup seam for them does
not exist (internal/i18n says so in its own package comment: it resolves the
locale and formats dates, and does not do message lookup yet). Counting them
would produce a number in the thousands with no mechanism to act on it. When
the seam lands, that becomes a fourth check and this comment is what says so.

Static: reads files, needs no running site, so it gates a pull request rather
than a release.

    python scripts/audit_resources.py                    # the host
    python scripts/audit_resources.py ../loon-plugins    # and a plugin tree

Exits non-zero when anything is found.
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# Directories that are not this codebase's own source.
#
# `examples` holds saved pages from OTHER sites, kept as design reference. They
# are full of hardcoded images by their nature -- they are somebody else's
# markup -- and 196 findings from them would bury the two that matter.
SKIP_DIRS = {".git", "node_modules", "vendor", "scratchpad", ".claude",
             "examples", "testdata", "deploy"}

SOURCE_EXT = (".html", ".go", ".css")

# ── A. images ───────────────────────────────────────────────────────────────

IMAGE_REF = re.compile(
    r'(?:src|href)="([^"{}]*\.(?:png|jpg|jpeg|gif|svg|webp|ico))"'
    # url("x.png"), url('x.png') and url(x.png) are all one thing to CSS, and
    # only one of the three was matched until the donate page's two background
    # images sat here unreported.
    r"""|url\(\s*["']?([^)"'{}]*\.(?:png|jpg|jpeg|gif|svg|webp))["']?\s*\)""", re.I)

# Literal paths that are NOT a resource decision, each with its reason. An
# ignore list without them is where findings go to die.
IMAGE_ALLOW = {
    # Sanitizer fixtures: the whole point is to feed the allowlist a URL and
    # see what survives, so the path is test DATA, not a picture the site draws.
    "internal/sanitize/sanitize_test.go",

    # ── the resource registry's first three customers ────────────────────────
    #
    # These are real findings, not exemptions. Each is a plugin naming a file
    # under the HOST's /static, which is the coupling this check exists to
    # surface: a host that does not ship that file gets a broken image, and
    # nothing anywhere says so.
    #
    # They are listed rather than fixed because the fix does not exist yet. A
    # decorative asset wants a resource DEF — a slug an operator points at
    # whatever art their site has — and the registry for that is designed and
    # unbuilt (see the icons.catalogue seam, which is its first brick). Fixing
    # them any other way today means inventing a second mechanism to delete
    # when the first arrives.
    #
    # Delete these three entries in the commit that lands the registry. That
    # is what makes this list a to-do rather than a graveyard.
    "donations/templates/help_donate.html",  # hero-rain.png, mascot-thumb.png
    "wiki/templates/wiki_topic.html",        # /static/posters/ame.png
}


def image_findings(root, files):
    out = []
    for path, rel, body in files:
        if rel in IMAGE_ALLOW or rel.endswith("_test.go"):
            continue
        for m in IMAGE_REF.finditer(body):
            val = m.group(1) or m.group(2)
            out.append((rel, line_of(body, m.start()), val))
    return out


# ── B. icons ────────────────────────────────────────────────────────────────

SYMBOL_ID = re.compile(r'<symbol id="([^"]+)"')
# Both dialects: a template's <use href="#id"> and a plugin building the same
# markup in Go with fmt.Fprintf(..., `<use href="#%s">`, id).
USE_REF = re.compile(r'<use href="#([^"]+)"')


def icon_findings(symbols, files):
    out = []
    for path, rel, body in files:
        if rel.endswith("_test.go"):
            continue
        for m in USE_REF.finditer(body):
            ref = m.group(1)
            # Decided at render time -- a template action or a format verb.
            # Those are the ones a human has to check; this catches the
            # literals, which is where the typos are.
            if "{{" in ref or "%" in ref or not ref:
                continue
            if ref not in symbols:
                out.append((rel, line_of(body, m.start()), ref))
    return out


# ── C. CSRF tokens ──────────────────────────────────────────────────────────

# A POST form and its body. Non-greedy to </form>, so nested markup between two
# forms cannot make one swallow the other's token.
POST_FORM = re.compile(
    r'(?is)<form(?=[\s>])[^>]*method\s*=\s*["\']post["\'][^>]*>.*?</form>')


def csrf_findings(files):
    """Every POST form with no hidden _csrf input."""
    out = []
    for path, rel, body in files:
        for m in POST_FORM.finditer(body):
            form = m.group(0)
            if 'name="_csrf"' in form or "name='_csrf'" in form:
                continue
            action = re.search(r'action\s*=\s*"([^"]*)"', form)
            out.append((rel, line_of(body, m.start()),
                        action.group(1) if action else "(posts to itself)"))
    return out


# ── D. member-facing sentences built in Go ──────────────────────────────────

# The sinks that put words in front of a person: a redirect carrying a message,
# and a bare string response. Twelve characters or more, so "ok", "1" and slugs
# do not count -- this is looking for SENTENCES.
GO_SENTENCE = re.compile(
    r'(?:QueryEscape|c\.String|gc\.String)\(\s*(?:http\.Status\w+,\s*)?"([^"]{12,})"')

# The count as it stands, per tree. Lower it in the same commit that converts
# one; raising it needs a reason in the commit message, and there is not
# currently a good one.
#
# Measured 18 Aug 2026.
SENTENCE_BASELINE = {
    # 34 as of 20 Aug 2026. The one that moved it is /readyz's
    # "database unreachable", and it is the exception this rule did not
    # anticipate rather than a slip: a readiness probe's body is a protocol
    # response read by a load balancer, never rendered to anybody, and putting
    # it in a template would mean a probe that renders HTML.
    #
    # Raising this needs a reason in the commit message and that is the whole
    # of the one available. Everything a MEMBER reads still belongs in a
    # template.
    "loon-demo-site": 34,
    "loon-plugins": 111,
}


def sentence_count(files):
    n = 0
    for path, rel, body in files:
        if rel.endswith("_test.go") or not rel.endswith(".go"):
            continue
        n += len(GO_SENTENCE.findall(body))
    return n


# ── plumbing ────────────────────────────────────────────────────────────────

def line_of(body, offset):
    return body.count("\n", 0, offset) + 1


# Comment syntaxes in the three file kinds scanned.
COMMENTS = [
    re.compile(r"\{\{/\*.*?\*/\}\}", re.S),  # template
    re.compile(r"<!--.*?-->", re.S),         # html
    re.compile(r"/\*.*?\*/", re.S),          # go, css
    re.compile(r"(?m)^\s*//.*$"),            # go line comment
]


def strip_comments(body):
    """Blank out comments, KEEPING newlines so reported line numbers stay true.

    Necessary rather than tidy: this file's own prose explains the rules by
    quoting them — `<use href="#name">` appears in a comment three files away
    from any markup — and a scanner that reads its own documentation as code
    reports the explanation as the bug.

    Only a Go line comment is anchored to the start of a line: `//` inside a
    string literal is common (an http:// URL), and blanking from there would
    swallow the rest of the line including real findings.
    """
    for pat in COMMENTS:
        body = pat.sub(lambda m: re.sub(r"[^\n]", " ", m.group(0)), body)
    return body


def read_tree(root):
    """Every source file under root, as (path, repo-relative path, contents)."""
    out = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for name in filenames:
            if not name.endswith(SOURCE_EXT):
                continue
            path = os.path.join(dirpath, name)
            try:
                with open(path, encoding="utf-8") as fh:
                    body = fh.read()
            except (OSError, UnicodeDecodeError):
                continue
            out.append((path, os.path.relpath(path, root).replace(os.sep, "/"),
                        strip_comments(body)))
    return out


def sprite_symbols(files):
    """Every <symbol id> the sprite sheet defines."""
    ids = set()
    for path, rel, body in files:
        for m in SYMBOL_ID.finditer(body):
            ids.add(m.group(1))
    return ids


def main():
    roots = [ROOT] + [os.path.abspath(a) for a in sys.argv[1:]]
    trees = [(os.path.basename(r.rstrip(os.sep)), r, read_tree(r)) for r in roots]

    # The sprite sheet is the HOST's, and a plugin's <use> resolves against it —
    # which is the coupling being checked, so symbols are pooled across trees.
    symbols = set()
    for _, _, files in trees:
        symbols |= sprite_symbols(files)
    if not symbols:
        print("resources: no <symbol id> found anywhere — the sprite sheet moved, "
              "and the icon check would pass by finding nothing to check")
        return 1

    failed = False
    for name, root, files in trees:
        images = image_findings(root, files)
        icons = icon_findings(symbols, files)
        tokenless = csrf_findings(files)
        sentences = sentence_count(files)
        baseline = SENTENCE_BASELINE.get(name)

        print("%s: %d files" % (name, len(files)))
        for rel, line, val in images:
            failed = True
            print("  HARDCODED IMAGE  %s:%d  %s" % (rel, line, val))
        for rel, line, ref in icons:
            failed = True
            print("  UNKNOWN ICON     %s:%d  #%s  (renders as an empty box)" % (rel, line, ref))
        for rel, line, action in tokenless:
            failed = True
            print("  NO CSRF TOKEN    %s:%d  posts to %s  (403s for every human)" % (rel, line, action))
        if baseline is None:
            print("  %d member-facing sentences built in Go (no baseline recorded "
                  "for this tree — add one to SENTENCE_BASELINE)" % sentences)
        elif sentences > baseline:
            failed = True
            print("  MORE TEXT IN GO  %d member-facing sentences, baseline %d. "
                  "A user-visible string belongs in a template "
                  "(loon-plugins/CHECKLIST.md section 10)." % (sentences, baseline))
        elif sentences < baseline:
            failed = True
            print("  BASELINE STALE   %d member-facing sentences, baseline %d — "
                  "lower it in this commit, or the next one back is free." % (sentences, baseline))
        else:
            print("  %d member-facing sentences built in Go (at the baseline)" % sentences)

    if failed:
        print("\nresources: findings above.")
        return 1
    print()
    print("resources: no hardcoded images, every icon resolves, every POST form "
          "carries a token, and no new member-facing text in Go.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
