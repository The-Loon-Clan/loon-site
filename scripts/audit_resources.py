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
    #
    # The path is stylesheet.go and not the template because the RegisterCSS
    # migration moved the rules that draw them: a plugin's CSS is a Go constant
    # now, served from a URL with a hash, so script-src could drop
    # 'unsafe-inline'. Same two images, same open to-do, one file along -- and
    # the entry has to follow the code or the audit reports a to-do it was
    # already told about, which is how a list like this starts being ignored.
    "donations/stylesheet.go",  # hero-rain.png, mascot-thumb.png
}
# wiki/templates/wiki_topic.html was here for /static/posters/ame.png and left
# on 22 Aug 2026 — not because the registry landed, but because the image was
# chosen by `{{if eq .Topic.Slug "amenzb"}}`: one site's mascot, drawn for a
# topic named after that site, on every host that installed the plugin. The
# branding sweep took the whole branch out and the hardcoded path went with it.
# Two images left, both decorative art on the donate page.


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


# The field is USUALLY the literal _csrf, but a framework page that must work
# under a host spelling it differently renders the NAME from its data too:
# loon/schedule/config_admin.go emits name="{{.CSRFField}}" beside the value,
# guarded by {{if .CSRFToken}}, and was reported as tokenless for it. Matching
# only the literal made this audit wrong about the one page in the tree that
# handles the general case, which is the worst place for it to be wrong.
#
# A template action counts as a field NAME only -- a hidden input must still
# be there. What the action RENDERS to is not knowable from the source, and
# saying so is better than a check that looks stronger than it is.
CSRF_FIELD = re.compile(
    'name\s*=\s*["\']'
    '(?:_csrf|\{\{[^}]*\}\})'
    '["\']')

def csrf_findings(files):
    """Every POST form with no hidden _csrf input."""
    out = []
    for path, rel, body in files:
        for m in POST_FORM.finditer(body):
            form = m.group(0)
            if CSRF_FIELD.search(form):
                continue
            action = re.search(r'action\s*=\s*"([^"]*)"', form)
            out.append((rel, line_of(body, m.start()),
                        action.group(1) if action else "(posts to itself)"))
    return out


# ── E. discarded template errors ────────────────────────────────────────────

# `_ = tmpl.Execute(w, data)`.
#
# html/template aborts at the FIRST error and writes nothing further. When the
# writer is a response whose status is already out, that is a page which stops
# mid-document and still reads as 200 to everything upstream -- no error, no
# 500, nothing in a log. This project has met it twice:
#
#   loon/schedule/admin.go hid a $-scoping bug for as long as the bug existed.
#   The control form rendered, the CSRF token inside it did not, and the page
#   simply ended. audit_links catches the truncation from OUTSIDE, on a live
#   host, which is a slow way to learn about a template that cannot execute.
#
#   loon-baseline/maintenance returned a truncated 503 -- the one page every
#   visitor is guaranteed to see, rendered while the site is already down.
#
# Writing to a BUFFER is the case worth keeping separate: nothing has reached
# the client, so the handler still has a choice, and discarding the error
# throws that choice away. Both spellings are flagged; the fix differs.
GO_DISCARDED_EXECUTE = re.compile(
    r"^\s*_\s*=\s*[\w.]*\.Execute(?:Template)?\s*\(", re.M)


def execute_findings(files):
    """Every template execution whose error is thrown away."""
    out = []
    for path, rel, body in files:
        if not rel.endswith(".go") or rel.endswith("_test.go"):
            continue
        for m in GO_DISCARDED_EXECUTE.finditer(body):
            out.append((rel, line_of(body, m.start()),
                        body[m.start():m.end()].strip()))
    return out


# ── D. member-facing sentences built in Go ──────────────────────────────────

# The sinks that put words in front of a person: a redirect carrying a message,
# and a bare string response. Twelve characters or more, so "ok", "1" and slugs
# do not count -- this is looking for SENTENCES.
GO_SENTENCE = re.compile(
    # The status may be a named constant OR a bare literal. It was only the
    # named form for a long time, so c.String(500, "this page failed to render")
    # went uncounted while the identical line written
    # c.String(http.StatusInternalServerError, ...) was caught — the same
    # sentence, in three plugins, counted in one of them.
    r'(?:QueryEscape|c\.String|gc\.String)\('
    r'\s*(?:(?:http\.Status\w+|\d{3})\s*,\s*)?"([^"]{12,})"')

# THE SAME SINKS, REACHED THE LONG WAY. The pattern above only sees a literal
# sitting inside the call, so for a long time these did not count:
#
#     msg := "Your achievements are shown."   // assigned, then passed
#     return "That did not save."             // returned from a helper
#     QueryEscape(fmt.Sprintf("%d points", n))  // built, not literal
#
# achievements read as ONE sentence and held three; games read as NONE and held
# eleven, behind a named error type and a helper. Every one reached a member.
#
# The first two are found precisely, by anchoring to a WHOLE statement -- the
# closing quote must end the line. Without that anchor the pattern spans two
# different literals in `"?msg=" + url.QueryEscape(msg)` and reports URL
# plumbing as prose, which is what the first attempt did.
GO_SENTENCE_ASSIGNED = re.compile(
    r'^[ \t]*([a-z][A-Za-z0-9_]*)[ \t]*:?=[ \t]*"([^"\\]{12,})"[ \t]*$', re.M)
GO_SENTENCE_RETURNED = re.compile(
    r'^[ \t]*return[ \t]+"([^"\\]{12,})"[ \t]*$', re.M)

# Which of those flow to a person: a variable that reaches a sink in the same
# file, or a return in a file that has a sink or a redirect at all.
GO_SENTENCE_SINK_VAR = re.compile(
    r'(?:QueryEscape|c\.String|gc\.String)\([ \t]*(?:http\.Status\w+,[ \t]*)?'
    r'([a-z][A-Za-z0-9_]*)[ \t]*\)')

# A sentence has a space and no format verb. This skips SQL, slugs, registry
# keys, content types and log formats, which is most of what a Go file holds.
# fmt.Sprintf INTO a sink is a real fourth route and is deliberately NOT
# counted: finding it needs dataflow, and a check that guesses is worse than
# one whose blind spot is written down. It is written down here.
GO_SENTENCE_SKIP = ("select ", "insert ", "update ", "delete ", "create ",
                    "alter ", "http://", "https://", "application/", "text/")

# Sentences that CANNOT live in a template, matched whole rather than by
# prefix. Same kind of exception as /readyz's "database unreachable" below, and
# listed with the reason for the same reason: an exemption without one is where
# findings go to die.
#
# Every one of these is the fallback for a TEMPLATE that failed. There is no
# template left to render the apology in — that is the condition being reported
# — so Go is the only place the words can be. Fourteen plugins carry the first
# one, and tracker's two are the same case with its own wording.
GO_SENTENCE_TEMPLATE_FALLBACK = (
    "this page failed to render",
    "tracker: setdeps was not called with a full deps — wire it in main() before core.boot",
    "tracker: templates were not parsed",
)


def _is_prose(lit):
    if " " not in lit or "%" in lit or lit.upper() == lit:
        return False
    low = lit.lower()
    if low in GO_SENTENCE_TEMPLATE_FALLBACK:
        return False
    return not any(low.startswith(b) or b in low[:20] for b in GO_SENTENCE_SKIP)

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

    # 0, and it should stay 0. loon is the FRAMEWORK: it has no members,
    # only hosts, so a sentence built in Go here is one every host that
    # embeds it inherits and none of them can translate or reword. This
    # entry existed nowhere until 22 Aug 2026, which meant the tree was
    # not being checked at all -- see the Makefile note on `resources`.
    "loon": 0,
    # 41 as of 21 Aug 2026, down from 111 — communities (20), medals (17),
    # games (11), forum (7), wiki (6), uploads (6), news (3), tickets (3),
    # magic (3), achievements (3), seedlock (2) and rewards (1) moved into
    # templates.
    #
    # IT WENT 40 -> 41 WHEN THE CHECK GOT BETTER, NOT WHEN THE CODE GOT WORSE.
    # For most of its life this counted only a literal sitting inside the sink
    # call, so three other routes to the same sink were invisible:
    #
    #     msg := "Your achievements are shown."   assigned, then passed
    #     return "That did not save."             returned from a helper
    #     errBadInput("pick an amount")           carried by an error type
    #
    # achievements read as ONE sentence and held three. games read as NONE and
    # held eleven, behind errBadInput and memberErr. uploads read as ONE and
    # held six, behind its own p.redirect helper. Every one reached a member.
    # The first two routes are now counted; the third cannot be, and neither
    # can QueryEscape(fmt.Sprintf(...)), because finding those needs dataflow
    # and a check that guesses is worse than one whose limits are written down.
    #
    # THE FLOOR IS NOT ZERO. Roughly six of what remains are not member-facing
    # sentences at all and should stay in Go: "<plugin>: no page renderer
    # wired" in seedlock, perks and hitrun, "tracker: no user on a gated
    # route", "seedlock: template failed", and downloads' two, which answer a
    # script a download client fetches rather than a person. They are counted
    # anyway, on the same principle that kept /readyz's probe body in the host
    # count with its reason written down: an exemption list is how a check
    # quietly stops checking.
    #
    # What is left is concentrated and each piece has a reason: releasegroups
    # (18) is not wired on this host so a conversion cannot be verified here,
    # requests (9) belongs to another workstream, and playlists (9) needs a
    # decision first — its templates live in the host, so it needs either a
    # host-side error page or the shared error seam three plugins already
    # declare privately.
    #
    # THE +1 IS communities/handlers.go's joinRequirementError, and it is
    # deliberately left. It is one line of a coherent set: communities puts its
    # messages in a SESSION FLASH rather than a query parameter — twelve
    # redirectWithFlash calls, nine carrying sentences, plus this helper's
    # three — and they have to move together. It is also the plugin whose
    # templates this host resolves from its OWN set, so the mapping ships in
    # two places; that is a pass of its own, not a straggler to sweep up.
    #
    # It was found at 115, ABOVE the recorded baseline: four had crept past and
    # nothing reported it, because this file only reads a plugin tree when one
    # is passed, and `make resources` is what passes it. Running the script
    # bare checks the host alone and says nothing about the other 876 files.
    "loon-plugins": 26,

    # 4 as of 21 Aug 2026, measured the first time this tree was scanned at all.
    # It had never been passed to this script — the scope gap that hid eight
    # tokenless POST forms in it, including change-password.
    #
    # 5 as of 22 Aug 2026, and the one that moved it is ratelimit's
    # c.String(429, "rate limit exceeded"). Same exception as /readyz above:
    # it is the DEFAULT a limiter writes when the host supplies no OnLimit
    # hook, read by a client that is being told to back off rather than by a
    # person reading a page. The seam for words already exists — cfg.OnLimit —
    # and a host that wants a sentence supplies one.
    "loon-baseline": 5,
}


def sentence_count(files):
    """Sentences a member can read, by all three routes to a sink."""
    n = 0
    for path, rel, body in files:
        if rel.endswith("_test.go") or not rel.endswith(".go"):
            continue
        # NOT _is_prose here, and that asymmetry is deliberate. That helper
        # guesses whether an arbitrary literal is prose, because the assigned
        # and returned routes below find it anywhere in the file. On THIS
        # route the sink is already known: the literal is the body of a
        # c.String response, which is a sentence a member reads whatever it
        # looks like. Section 10's exemplar is exactly such a line —
        # c.String(500, "failed to load posts").
        #
        # Applying the guess here cost two real findings: "Downloads are
        # disabled: you have %d active hit-and-run warnings." went for having
        # a format verb, and "failed to create request" for starting with a
        # word the SQL prefix list holds.
        n += sum(1 for lit in GO_SENTENCE.findall(body)
                 if lit.lower() not in GO_SENTENCE_TEMPLATE_FALLBACK)
        sunk = set(m.group(1) for m in GO_SENTENCE_SINK_VAR.finditer(body))
        if sunk:
            for m in GO_SENTENCE_ASSIGNED.finditer(body):
                if m.group(1) in sunk and _is_prose(m.group(2)):
                    n += 1
        # A helper returning prose only counts where this file also puts words
        # in front of somebody; elsewhere a returned string is a log line or an
        # error for another handler to wrap.
        if sunk or "Redirect(" in body:
            for m in GO_SENTENCE_RETURNED.finditer(body):
                if _is_prose(m.group(1)):
                    n += 1
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
        discarded = execute_findings(files)
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
        for rel, line, call in discarded:
            failed = True
            print("  SILENT TEMPLATE  %s:%d  %s"
                  % (rel, line, call))
            print("                   a failure here truncates the page and still "
                  "returns 200")
        if baseline is None:
            # A FAILURE, not a note. The baseline is keyed by the tree's
            # directory NAME, so a checkout named anything else silently turns
            # the ratchet off — this printed "34 sentences (no baseline
            # recorded)" and exited 0 when the repo was mounted at /src, which
            # is a check that stops checking because somebody renamed a folder.
            failed = True
            print("  NO BASELINE      %d member-facing sentences and no entry for "
                  "%r in SENTENCE_BASELINE. Add one, or the ratchet is off for "
                  "this tree." % (sentences, name))
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
          "carries a token, no template renders its errors away, and no new "
          "member-facing text in Go.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
