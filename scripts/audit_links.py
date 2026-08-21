"""Follow every internal link, signed in, and report the ones that lie.

Four failure modes, and the third is the reason this exists:

  404  a link to a page that is not there
  5xx  a page that errors
  200  a page whose HTML stops before </footer>
  a POST form with no _csrf field

That last one is this codebase's own documented trap: a template that fails at
EXECUTE time aborts mid-document and still returns 200, so the page looks
merely short. Nothing else in the stack notices, which is why a crawler that
only checks status codes would have missed it.

Found in earlier runs: /sitemap 404ing behind two nav links, /p/topics and
/p/posts linking threadS/<id>, /u//followers with an empty username, a 500 on
/c/usenet, and a broken link of my own an hour after shipping it.

THE FORM CHECK, and why it lives in a crawler. The host's CSRF middleware
refuses any POST without a valid token BEFORE any handler runs, so a form
whose template forgot the hidden field 403s on every submit, for every user,
forever. No other audit can see it: audit_access deliberately probes WITH a
valid token (it tests the gate, not the form), and the failure only exists in
RENDERED pages. Four plugins shipped exactly this — news, wiki, tickets,
reports — with every plugin-owned admin form dead on arrival. This crawler
already renders every reachable page signed in, so it is the place that can
count the tokens.

    python scripts/audit_links.py

Exits non-zero when anything is found, so deploy.sh can gate on it.
"""
import collections
import html
import re
import sys
import urllib.parse

sys.path.insert(0, __file__.rsplit("/", 1)[0].rsplit("\\", 1)[0])
import _site  # noqa: E402

MAX_PAGES = 250

# Paths that ACT rather than show. A crawler that follows them logs itself out,
# deletes things, and spends points. Anything destructive belongs here.
SKIP = re.compile(
    r"""^/logout$
      | ^/api\b | ^/rss\b | ^/sitemap\.xml$
      | /delete | /remove | /claim | /buy/ | /react | /follow$ | /report-avatar$
      | ^/nzb/ | ^/download
      | ^/moderation/vote$
      # NOT dead: this crawler strips query strings and the real link carries
      # ?name=. Skipped rather than reported, so the one line of output means
      # something -- a known false positive left in is how a clean run stops
      # being recognisable.
      | ^/admin/jobs/config$
    """,
    re.X,
)


def crawl():
    seen, queue, came_from = set(), collections.deque(["/"]), {}
    dead, errors, truncated, tokenless = [], [], [], []
    pages = 0

    while queue and len(seen) < MAX_PAGES:
        path = queue.popleft()
        if path in seen:
            continue
        seen.add(path)
        code, body = _site.get(path)
        pages += 1

        if code >= 500 or code == 0:
            errors.append((code, path, came_from.get(path, "-")))
            continue
        if code == 404:
            dead.append((code, path, came_from.get(path, "-")))
            continue
        if code != 200:
            # 3xx/401/403 are answers, not faults: a staff page refusing a
            # non-staff crawl is the gate working.
            continue

        # The silent-truncation check. Only for full documents -- a fragment
        # endpoint legitimately has no footer.
        if "<html" in body[:400] and "</footer>" not in body:
            truncated.append((path, len(body)))

        # Every POST form must carry the double-submit token, or csrf.go
        # answers 403 before any handler runs. Matched per form BODY rather
        # than counted per page, so one tokened form cannot vouch for a
        # tokenless one beside it.
        for m in re.finditer(
                r'<form\b[^>]*\bmethod=.?post[^>]*>(.*?)</form>',
                body, re.I | re.S):
            if 'name="_csrf"' not in m.group(0):
                action = re.search(r'action="([^"]*)"', m.group(0))
                tokenless.append((path, action.group(1) if action else "(self)"))

        for raw in re.findall(r'href="([^"]+)"', body):
            href = html.unescape(raw).strip()
            if not href.startswith("/") or href.startswith("//"):
                continue
            target = urllib.parse.urlsplit(href).path or "/"
            if target.startswith("/static/") or target.startswith("/uploads/"):
                continue
            if SKIP.search(target) or target in seen:
                continue
            came_from.setdefault(target, path)
            queue.append(target)

    return pages, dead, errors, truncated, tokenless


def main():
    _site.require_site()
    if not _site.login():
        raise SystemExit("audit: could not sign in as %s" % _site.USER)

    pages, dead, errors, truncated, tokenless = crawl()
    print("links: crawling %d pages" % pages)

    for label, rows in (("DEAD (404)", dead), ("ERROR (5xx)", errors)):
        print("  %s: %d" % (label, len(rows)))
        for code, path, src in rows:
            print("     %-4s %-42s linked from %s" % (code, path, src))

    print("  TRUNCATED (200 with no </footer>): %d" % len(truncated))
    for path, size in truncated:
        print("     %-42s %d bytes" % (path, size))

    print("  TOKENLESS POST FORMS (every submit 403s): %d" % len(tokenless))
    for path, action in tokenless:
        print("     %-42s form action=%s" % (path, action))

    # Summary LAST, so a caller showing only the final line gets the verdict.
    print("links: %d dead, %d error, %d truncated, %d tokenless across %d pages"
          % (len(dead), len(errors), len(truncated), len(tokenless), pages))
    return 1 if (dead or errors or truncated or tokenless) else 0


if __name__ == "__main__":
    sys.exit(main())
