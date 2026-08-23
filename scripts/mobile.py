#!/usr/bin/env python3
"""Every page at phone width, checked for layout that does not fit.

    python scripts/mobile.py                  # every page the sitemap lists
    python scripts/mobile.py /search?q=a      # just these

Signs itself in (AUDIT_USER/AUDIT_PASS, default alice) so the account pages are
covered. Signed out it reaches 23 of 36.

Exit status is 1 if any page overflows, so it can gate a release.

WHY THIS EXISTS
---------------
A mobile pass by screenshot checks the pages somebody thought to look at. This
checks the ones the site says it has — the page list comes from /sitemap, which
has its own test keeping it honest, so a page added without being added here is
not possible unless it is missing from the sitemap too.

It measures two different things, and the second is the one that matters:

  document overflow   the page itself scrolls sideways. Loud, rare, and usually
                      caught by eye.
  element overhang    a single element sticks out past the viewport. This is
                      the one that hides: /search laid its results table out at
                      667px inside a 390px screen and the PAGE never scrolled,
                      because the table sat in a .data-table-wrapper with
                      overflow-x. Invisible to any check that looks at
                      document.scrollWidth, and invisible in a screenshot,
                      because the container clips it.

Overhang inside a deliberate scroller is not a bug — a carousel is supposed to
run off the edge — so those containers are named in SCROLLERS below and their
children are ignored. Anything else that sticks out is reported.

HOW IT WORKS, AND WHY IT LOOKS ODD
----------------------------------
Pages are FETCHED and SAVED, then loaded from disk in an iframe, rather than
pointed at directly. Two reasons, both discovered the hard way:

  headless Chrome will not go below a ~500px window on Windows, so
  --window-size=390 silently measures 500 — wide enough to miss exactly the
  breakpoints being tested. An iframe has no such floor.

  the site sends frame-ancestors 'none', so a live URL cannot be framed. A
  saved copy carries no headers.

Saving also buys the thing scripts/shot.sh says it cannot do: a signed-in page.
Chrome's CLI cannot set a session cookie; _site.Session can.
"""

import json
import os
import re
import subprocess
import sys
import tempfile
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import _site  # noqa: E402

BASE = os.environ.get("BASE", "http://localhost:8090")
WIDTH = int(os.environ.get("MOBILE_WIDTH", "390"))
HEIGHT = int(os.environ.get("MOBILE_HEIGHT", "844"))

# Both moved to _site.py when shot.py needed them too — one Chrome path for
# every script that drives a browser, rather than a copy per script.
winpath = _site.winpath
CHROME = _site.CHROME

# Containers whose children are MEANT to run past the edge. Named explicitly:
# the alternative is ignoring any element inside anything scrollable, which
# would also ignore the results table, which is the bug this was written for.
# .nav.tabs is theme.css's Bootstrap-shaped tab strip and has carried
# overflow-x: auto from the start. It was missing from this list, so five
# pages were reported for a tab row that scrolls exactly as intended --
# findings against a component doing its job.
SCROLLERS = [".carousel", ".stat-strip", ".data-table-wrapper", ".nav-tabsV2--scroll",
             ".nav.tabs",
             # The Bootstrap-shaped equivalent of .data-table-wrapper, which the
             # tracker plugin's templates use. Added only after checking that
             # this site actually implements it — theme.css:113 sets
             # overflow-x: auto — because naming a container here on the
             # assumption that it scrolls is how a real overflow gets silenced.
             # The tracker pages confirm it: their tables run 364px past the
             # edge and the DOCUMENT still does not scroll.
             ".table-responsive"]

# Pages the sitemap cannot name because they need a parameter. A release id and
# a thread id are looked up from the running site rather than hardcoded.
EXTRA = ["/search?q=a"]


# One signed-in session, shared with the other audits.
#
# It used to take a cookie from LOON_COOKIE, which meant a run without one
# silently checked 23 of 36 pages — and the 13 it skipped were the account area,
# where 14 of the first 15 failures were. A check that quietly tests a third
# less than you think is the failure this project keeps finding, so it signs
# itself in now and says so if it cannot.
_session = _site.Session()
_signed_in = _session.login(_site.USER, _site.PASS)


def fetch(path):
    status, body = _session.get(path)
    return body, status


BASELINE = {
    # Empty, and it got there the same day it was written. The eleven it
    # held were two causes -- a table in neither scrolling wrapper, and a
    # nowrap toolbar held right by ms-auto -- plus five that were never
    # broken at all: .nav.tabs has carried overflow-x: auto from the
    # start and was simply missing from SCROLLERS, so the check was
    # reporting a component for doing its job.
    #
    # Kept rather than deleted because the ratchet needs somewhere to put
    # the next one, and because both guards below are load-bearing: a
    # page that starts fitting must LEAVE, and an entry nothing checks
    # any more must be noticed. Both were probed, and the first of them
    # is what emptied this list.
}


def _crawled():
    """One page per shape the link crawler reached."""
    try:
        import audit_links
        seen = {}
        for p in audit_links.discover():
            k = re.sub(r'/\d+', '/:id', p)
            k = re.sub(r'/[0-9a-f]{16,}', '/:hash', k)
            seen.setdefault(k, p)
        return sorted(seen.values())
    except Exception as exc:  # noqa: BLE001 - degrade to the sitemap
        print("  ! crawl unavailable (%s); checking the sitemap only" % exc)
        return []


def _admin_routes():
    """The admin pages, which the sitemap will never list."""
    try:
        import audit_adminnav
        return sorted(r for r in audit_adminnav.served_routes()
                      if ':' not in r and not r.endswith('.json'))
    except Exception as exc:  # noqa: BLE001 - degrade to the sitemap
        print("  ! admin routes unavailable (%s); checking the sitemap only" % exc)
        return []


def discover():
    """Every page the site has, not every page the sitemap names.

    The sitemap lists public CONTENT. It does not list the admin area, and
    it does not list a community -- so 76 pages had never been loaded at
    phone width, and 12 of them did not fit. One, /c/usenet, is a page
    members visit.

    Same two sources audit_a11y already combines, for the same reason it
    combines them: a route added tomorrow is checked tomorrow. Additive, so
    a crawl or a rotated boot log degrades to the sitemap rather than
    emptying the list.
    """
    html, _ = fetch("/sitemap")
    paths = []
    for href in re.findall(r'class="sitemap-list__link" href="([^"]+)"', html):
        if href.startswith("/") and not href.startswith("/static"):
            # .xml and the API are not pages; they have no layout to check.
            if href.endswith(".xml") or href.startswith("/api") or href.startswith("/rss"):
                continue
            paths.append(href)
    # A release and a thread, because a detail page is a different layout from
    # any index and neither is in the sitemap by nature.
    # The crawler's shapes and the admin route table, both additive.
    for extra in (_crawled(), _admin_routes()):
        paths.extend(extra)

    for pat, page in ((r'href="(/release/\d+)"', "/browse"),
                      (r'href="(/community/forums/thread/\d+)"', "/community/forums")):
        try:
            body, _ = fetch(page)
            m = re.search(pat, body)
            if m:
                paths.append(m.group(1))
        except Exception:
            pass
    return sorted(set(paths + EXTRA))


HARNESS_JS = r"""
window.addEventListener('load', function () {
  var frames = Array.prototype.slice.call(document.querySelectorAll('iframe'));
  var SCROLLERS = __SCROLLERS__;
  var out = [];
  frames.forEach(function (f) {
    var res = {path: f.dataset.path, error: null, doc: 0, vw: 0, over: []};
    try {
      var d = f.contentDocument;
      if (!d || !d.documentElement) { res.error = 'no document'; out.push(res); return; }
      var vw = d.documentElement.clientWidth;
      res.vw = vw;
      res.doc = d.documentElement.scrollWidth;
      var seen = {};
      var all = d.querySelectorAll('body *');
      for (var i = 0; i < all.length; i++) {
        var el = all[i];
        var r = el.getBoundingClientRect();
        if (r.width <= 0 || r.right <= vw + 1) continue;
        var inScroller = false;
        for (var s = 0; s < SCROLLERS.length; s++) {
          if (el.closest(SCROLLERS[s])) { inScroller = true; break; }
        }
        if (inScroller) continue;
        var cls = (typeof el.className === 'string' && el.className)
          ? '.' + el.className.trim().split(/\s+/)[0] : '';
        var id = el.tagName.toLowerCase() + cls;
        if (seen[id]) continue;
        seen[id] = 1;
        res.over.push({el: id, px: Math.round(r.right - vw)});
      }
    } catch (e) {
      res.error = String(e);
    }
    out.push(res);
  });
  var pre = document.createElement('pre');
  pre.id = 'RESULT';
  pre.textContent = JSON.stringify(out);
  document.body.appendChild(pre);
});
"""


# Chrome renders every page as an IFRAME in one harness document, and a
# harness with 184 of them dies without producing output -- the failure is
# an IndexError deep in subprocess, not a message, which is why the page
# list could not simply be widened. Batched, the same run is 13 harnesses
# that each behave like the 36-page one this was written against.
BATCH = 15


def _measure(paths):
    tmp = tempfile.mkdtemp(prefix="mobilecheck-")
    saved = []
    for p in paths:
        try:
            html, status = fetch(p)
        except Exception as e:
            print("  ! %-38s fetch failed: %s" % (p, e))
            continue
        if status != 200:
            print("  ! %-38s HTTP %s" % (p, status))
            continue
        # Not every crawled path is a PAGE. /tracker/download/<hash> answers
        # with a .torrent, and measuring a file for layout produces a finding
        # nobody can act on. Checked by content rather than by a path list, so
        # the next download route added does not have to be remembered.
        if "<html" not in html[:2000].lower():
            continue
        # Assets have to resolve from a file:// page, so they become absolute.
        # /pluginstyle/ as well as /static/. A plugin stylesheet is served from its
        # own prefix (handlers/pluginstyles_web.go), and a saved page that does not
        # rewrite it loads nothing for that link -- every plugin page then measures
        # and photographs as UNSTYLED, which reads as the site being broken rather
        # than the harness missing an asset.
        html = html.replace('href="/static', 'href="%s/static' % BASE)
        html = html.replace('href="/pluginstyle', 'href="%s/pluginstyle' % BASE)
        html = html.replace('src="/static', 'src="%s/static' % BASE)
        # Uploads too — avatars and community banners; see shot.py.
        html = html.replace('src="/uploads', 'src="%s/uploads' % BASE)
        html = html.replace("url('/uploads", "url('%s/uploads" % BASE)
        name = re.sub(r"[^a-z0-9]+", "_", p.lower()).strip("_") or "root"
        fn = os.path.join(tmp, name + ".html")
        with open(fn, "w", encoding="utf-8") as f:
            f.write(html)
        saved.append((p, name + ".html"))

    if not saved:
        print("no pages fetched — is the site running at %s?" % BASE)
        return None

    frames = "\n".join(
        '<iframe data-path="%s" src="%s" width="%d" height="%d"></iframe>'
        % (p.replace('"', "&quot;"), fn, WIDTH, HEIGHT)
        for p, fn in saved
    )
    js = HARNESS_JS.replace("__SCROLLERS__", json.dumps(SCROLLERS))
    harness = os.path.join(tmp, "_harness.html")
    with open(harness, "w", encoding="utf-8") as f:
        f.write("<!doctype html><meta charset=utf-8><body>%s<script>%s</script></body>"
                % (frames, js))

    win = winpath(harness)
    # --allow-file-access-from-files is what lets the harness READ each frame.
    # Without it every file:// document is its own opaque origin and the
    # measurements come back as a cross-origin error rather than as numbers.
    cmd = [CHROME, "--headless=new", "--disable-gpu", "--hide-scrollbars",
           "--allow-file-access-from-files", "--window-size=1200,900",
           "--virtual-time-budget=%d" % (4000 + 500 * len(saved)),
           "--dump-dom", "file:///" + win]
    # encoding= is not optional. text=True decodes with the CONSOLE codepage,
    # which here is cp1252, and Chrome dumps the page DOM -- so one page
    # containing a character cp1252 cannot represent killed the reader
    # thread, left stdout empty, and surfaced as "IndexError: list index out
    # of range" from inside subprocess. It reads as a Chrome crash and it is
    # a mojibake bug. errors="replace" because a replacement character in a
    # DOM dump costs nothing: the measurements are numbers.
    dom = subprocess.run(cmd, capture_output=True, text=True, timeout=600,
                         encoding="utf-8", errors="replace").stdout
    m = re.search(r'<pre id="RESULT">(.*?)</pre>', dom, re.S)
    if not m:
        print("the harness produced no result — Chrome may not have run.")
        print("CHROME=%s" % CHROME)
        return None
    return json.loads(m.group(1).replace("&quot;", '"').replace("&amp;", "&")
                      .replace("&lt;", "<").replace("&gt;", ">"))


def run(paths):
    results = []
    for i in range(0, len(paths), BATCH):
        got = _measure(paths[i:i + BATCH])
        if got is None:
            return 1
        results.extend(got)

    bad = 0
    known, fixed = [], []
    print("%d pages at %dx%d" % (len(results), WIDTH, HEIGHT))
    # Said before the results, not after, because it changes what they MEAN.
    # Signed out this reaches 23 of 36 pages, and the 13 it cannot see are the
    # account area — which is where 14 of the first 15 failures were. A run
    # without a session reported a clean site while a third of it was broken,
    # and nothing about the output said so.
    if not _signed_in:
        print("  ! not signed in as %s, so every page behind a login is UNCHECKED." % _site.USER)
        print("    Set AUDIT_USER/AUDIT_PASS. This reaches 23 of 36 pages signed out.")
    print()
    for r in results:
        issues = []
        if r["error"]:
            issues.append("could not measure: " + r["error"])
        if r["doc"] > r["vw"]:
            issues.append("PAGE SCROLLS SIDEWAYS (%d > %d)" % (r["doc"], r["vw"]))
        for o in r["over"][:4]:
            issues.append("%s sticks out %dpx" % (o["el"], o["px"]))
        if issues:
            if r["path"] in BASELINE:
                known.append(r["path"])
                print("  ~    %-38s (at the baseline)" % r["path"])
                continue
            bad += 1
            _site.say("  FAIL %s" % r["path"])
            for i in issues:
                _site.say("       %s" % i)
        else:
            if r["path"] in BASELINE:
                fixed.append(r["path"])
            print("  ok   %s" % r["path"])

    print()
    if known:
        print("%d page(s) at the baseline — two causes, both named in BASELINE."
              % len(known))
    # An entry that is no longer CHECKED is the quieter half of the same
    # problem: it reads as "known bad" forever while nothing looks at it. This
    # is how /admin/metrics sat in the list -- a JSON endpoint measured as a
    # page -- and it would have stayed there unnoticed.
    checked = {r["path"] for r in results}
    stale = sorted(p for p in BASELINE if p not in checked)
    if stale and len(results) > len(BASELINE):
        print()
        print("  IN BASELINE but not checked at all — is it still a page?")
        for p in stale:
            print("    " + p)
        return 1

    # A page that starts fitting must LEAVE the baseline, or the list becomes a
    # record of what used to be broken and stops being a ratchet.
    if fixed:
        print()
        print("  FIXED, and still listed in BASELINE — remove them:")
        for p in fixed:
            print("    " + p)
        return 1
    if bad:
        print("%d of %d pages have layout that does not fit a %dpx screen."
              % (bad, len(results), WIDTH))
        print("Overflow inside %s is ignored — those scroll on purpose."
              % ", ".join(SCROLLERS))
        return 1
    print("every page fits a %dpx screen (%d at the baseline)." % (WIDTH, len(known)))
    return 0


if __name__ == "__main__":
    # _site.unmangle, moved there when shot.py hit the same trap and reported
    # the site as down rather than the argument as mangled.
    args = [_site.unmangle(a) for a in sys.argv[1:] if not a.startswith("-")]
    sys.exit(run(args or discover()))
