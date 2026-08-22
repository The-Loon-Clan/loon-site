#!/usr/bin/env python3
"""W3C validation over every page a signed-out visitor can reach.

    python scripts/audit_html.py            # all of them
    python scripts/audit_html.py /wiki /news

Runs the official Nu validator image, so there is nothing to install and the
answer is the one the W3C service gives.

WHY THIS REPLACED A FIVE-PAGE LIST. htmlvalidate.sh checked /, /browse,
/search, /login and /register. Those five are clean and have been for a while,
which is what "make html: no errors" meant. Pointed at the 111 pages a visitor
can actually reach, the same site had 52 errors on 20 pages -- forum, profiles,
wiki, news, communities. The mojibake in docs/BACKLOG.md #9 was FOUND by a W3C
run and lived on release pages, which were not among the five either.

The page list comes from audit_links.discover(), the same crawl audit_a11y and
mobile.py use, so a page shape added tomorrow is validated tomorrow.

SIGNED OUT, deliberately. The validator container fetches the URLs itself and
has no session, so an admin page would validate a login redirect and report it
clean. Filtering to what returns 200 as HTML anonymously is the honest scope,
and it is stated here rather than left to be discovered.

WHY hx-* IS FILTERED. htmx's attributes are not in the HTML specification, so
every hx-post is reported as "not allowed on element" -- 82 of 98 errors when
this was first written. htmx supports data-hx-* for exactly this reason and
data-* IS valid HTML; switching would remove the need for the filter and touch
every converted control. The filter is narrow and stated. Two filters is the
point at which this stops being honest.
"""

import io
import json
import os
import re
import subprocess
import sys
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import _site  # noqa: E402
import audit_links  # noqa: E402

IMAGE = os.environ.get("VNU_IMAGE", "ghcr.io/validator/validator:latest")
# host.docker.internal reaches the host from inside the container on Docker
# Desktop. The container fetches the pages; nothing is mounted, because on
# Windows/MSYS a mktemp -d is a path Docker cannot mount -- /work came up EMPTY,
# vnu validated nothing, and the script exited 0 having printed nothing. A
# validator that reports success because it was given no input is the worst
# possible failure for a check like this, and it looked exactly like a pass.
INSIDE = os.environ.get("VNU_BASE", "http://host.docker.internal:8090")
BATCH = 20

def shape(path):
    """A path with its row ids blanked, so instances of one page collapse."""
    p = re.sub(r"/\d+", "/:id", path)
    return re.sub(r"/[0-9a-f]{16,}", "/:hash", p)


# Errors accepted for now, keyed by SHAPE. Not by path: the crawler picks one
# representative per shape and picks a different row each run, so
# /community/forums/category/1 one day and /category/3 the next would make a
# path-keyed baseline wrong every second run and teach whoever runs it to
# ignore the output.
BASELINE = {
    # Empty. It held 52 errors on 20 pages for one afternoon — the day this
    # check stopped looking at five pages — and they were six causes:
    #
    #   19  <style> inside a div or article. A plugin fragment ships its CSS
    #       in a <style> block because it has no other way to, and the host
    #       inserted it into the body. Hoisted into the head at the one seam
    #       where a fragment becomes a page — handlers/fragmentstyles.go.
    #   10  stray end tags on the profile pages: the self-controls box opened
    #       inside {{if .IsSelf}} and closed outside it, so every profile
    #       viewed by anybody else emitted three closes for nothing.
    #    9  a main element inside a main inside an article: four forum
    #       fragments still carried the landmark the host supplies.
    #    7  aria-label on a bare div or span, which cannot carry a name.
    #    3  title= on <svg>, which is an SVG title CHILD, not an attribute.
    #    2  an <a> and a <div> as a direct child of <ul>.
    #
    # Kept empty rather than deleted: the ratchet needs somewhere to put the
    # next one, and the guard below — a page that stops erroring must LEAVE
    # this list — is what emptied it.
}

GNU = re.compile(r'^"?(?P<url>[^"]+)"?:(?P<line>\d+)\.(?P<col>[\d.-]+):\s*error:\s*(?P<msg>.*)$')


def public_pages():
    """Every crawled page shape that answers 200 with HTML, signed out."""
    seen = {}
    for p in audit_links.discover():
        k = re.sub(r"/\d+", "/:id", p)
        k = re.sub(r"/[0-9a-f]{16,}", "/:hash", k)
        seen.setdefault(k, p)
    out = []
    for p in sorted(seen.values()):
        try:
            req = urllib.request.Request(_site.BASE + p,
                                         headers={"User-Agent": "audit_html"})
            with urllib.request.urlopen(req, timeout=20) as r:
                if r.status == 200 and "html" in (r.headers.get("Content-Type") or "").lower():
                    out.append(p)
        except Exception:  # noqa: BLE001 - a page that will not answer is not ours to validate
            continue
    return out


def validate(paths):
    """vnu over a batch of paths, returning [(path, line, message)]."""
    urls = [INSIDE + p for p in paths]
    cmd = ["docker", "run", "--rm",
           "--add-host=host.docker.internal:host-gateway", IMAGE,
           "vnu", "--errors-only", "--filterpattern", ".*hx-[a-z-]+.*",
           "--format", "gnu"] + urls
    env = dict(os.environ, MSYS_NO_PATHCONV="1")
    r = subprocess.run(cmd, capture_output=True, text=True, timeout=900,
                       encoding="utf-8", errors="replace", env=env)
    out = []
    for line in (r.stdout + r.stderr).split("\n"):
        m = GNU.match(line.strip())
        if not m:
            continue
        url = m.group("url")
        path = url[len(INSIDE):] if url.startswith(INSIDE) else url
        out.append((path, int(m.group("line")), m.group("msg").strip()))
    return out


def main():
    _site.require_site()
    # _site.unmangle, because git-bash on Windows rewrites a /path argument
    # into a Windows path before Python sees it -- "/u/bob" arrives as
    # "C:/Program Files/Git/u/bob" and validates nothing while reporting
    # zero errors. shot.py and mobile.py both hit this first.
    args = [_site.unmangle(a) for a in sys.argv[1:] if not a.startswith("-")]
    pages = args or public_pages()
    if not pages:
        print("html: no public pages found -- the crawl produced nothing, so "
              "this proves nothing.")
        return 1

    findings = []
    for i in range(0, len(pages), BATCH):
        findings.extend(validate(pages[i:i + BATCH]))

    by_page = {}
    for path, line, msg in findings:
        by_page.setdefault(shape(path), []).append((line, msg))

    failed = False
    for path in sorted(by_page):
        rows = by_page[path]
        allowed = BASELINE.get(path, 0)  # path IS the shape here
        if len(rows) <= allowed:
            print("  ~    %-44s %d (at the baseline)" % (path, len(rows)))
            continue
        failed = True
        _site.say("  FAIL %s  %d error(s)%s"
                  % (path, len(rows), "" if not allowed else ", baseline %d" % allowed))
        for line, msg in sorted(rows)[:6]:
            _site.say("       %d: %s" % (line, msg[:120]))
        if len(rows) > 6:
            print("       ... and %d more" % (len(rows) - 6))

    # A page that stops erroring must LEAVE the baseline, or the list becomes a
    # record of what used to be broken.
    #
    # Only among the shapes THIS RUN looked at. Passing a page on the command
    # line made every other baselined page look fixed, which is a false alarm
    # that teaches whoever runs it to skim the output -- the exact failure the
    # baseline exists to prevent.
    looked_at = {shape(p) for p in pages}
    fixed = sorted(p for p in BASELINE if p in looked_at and p not in by_page)
    if fixed:
        failed = True
        print()
        print("  FIXED, and still in BASELINE -- remove them:")
        for p in fixed:
            print("    " + p)

    baselined = sum(min(len(v), BASELINE.get(k, 0)) for k, v in by_page.items())
    print()
    print("html: %d error(s) on %d of %d public page(s)"
          % (len(findings), len(by_page), len(pages)))
    if failed:
        return 1
    if baselined:
        # NOT "every page is valid". It is not, and a green line that says so
        # is how a baseline stops being a debt and becomes a habit.
        print("html: nothing new — %d error(s) still at the baseline on %d page(s)."
              % (baselined, len(by_page)))
        return 0
    print("html: every public page is valid HTML.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
