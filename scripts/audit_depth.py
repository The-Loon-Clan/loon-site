#!/usr/bin/env python3
"""A region inside a panel must not be darker than the page canvas.

    python scripts/audit_depth.py              # every theme
    python scripts/audit_depth.py /wiki        # these pages, every theme

WHAT THIS IS FOR. A design system with three surface tokens has an ordering:
the page canvas, a panel raised above it, and something raised again inside
that panel. Reach past them for the token that means "the deepest recess on
the page" -- a footer, a table gutter, a well -- and you get a region that
falls through the floor it is standing on.

That is not a contrast failure, so contrast.py cannot see it. It is valid
HTML, so audit_html cannot. It has no accessibility violation, so audit_a11y
cannot. On 22 Aug 2026 the forum poster column was doing exactly this and was
reported by a person looking at a screenshot -- in the one theme where the gap
is wide enough to notice. It was wrong in all three.

THE RULE, and it is deliberately one rule. For every element with a background
of its own: if an ancestor between it and the body has a LIGHTER background
than the canvas -- that ancestor is a panel, raised -- then this element must
not be DARKER than the canvas. Inside something raised, a region may sit level
with the page or above it. It may not sit in the basement.

WHY EVERY THEME. The tokens differ per theme and so does the gap. The midnight
sunken colour is #070a11 against a #111826 panel and reads as a hole; the
cosmic-void one is #161618 against #282828 and reads as merely dark. The bug
was identical and only one theme showed it.

WHAT IT CANNOT SEE. A background painted by an image, a gradient or a pseudo
element: only a resolved background-color is compared. The cosmetics grounds
are gradients on a ::before, so a member wearing one is invisible here, which
is correct -- those are meant to sit on top.
"""

import io
import json
import os
import re
import subprocess
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import _site  # noqa: E402

THEMES = ["cosmic-void", "midnight", "nord"]

# Pages with real nesting: a panel with something inside it. Small on purpose,
# because this is a STRUCTURAL rule and a shape is a shape wherever it is used.
PAGES = ["/", "/community/forums/thread/2", "/community/forums", "/browse",
         "/u/alice", "/wiki", "/help/donate"]

# Deliberate recesses, keyed "theme selector" -- NOT by page, because the
# exemption is about a COMPONENT and a component is the same wherever it is
# drawn. Keying by page would mean the same intentional thing failing the day
# it appears on a new one.
#
# cosmic-void is a port of UNIT3D and these two are faithful to it, each with
# its own token rather than borrowing a general one:
#
#   th                  --data-table-th-bg #19191b. The dark table header is
#                       the look; the theme comment at the top of the file
#                       names it as the thing being ported.
#   span.poster__frame  --poster-bg. The well a missing cover sits in, which
#                       is meant to read as a hole because that is what it is.
#
# Everything else this found was a real inversion and is fixed: cosmic-void
# defined --surface-2 DARKER than its own canvas, so thirty-one components
# read as raised in two themes and as a recess in the third.
BASELINE = {
    "cosmic-void th",
    "cosmic-void th.data-table__num",
    "cosmic-void th.data-table__nowrap",
    "cosmic-void th.data-table__shrink",
    "cosmic-void span.poster__frame",
}

HARNESS_JS = r"""
window.addEventListener('load', function () {
  function lum(c) {
    var m = /rgba?\(([^)]+)\)/.exec(c || '');
    if (!m) return null;
    var p = m[1].split(',').map(function (x) { return parseFloat(x); });
    if (p.length > 3 && p[3] < 0.5) return null;   /* see-through: not a ground */
    var f = p.slice(0, 3).map(function (v) {
      v = v / 255;
      return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
    });
    return 0.2126 * f[0] + 0.7152 * f[1] + 0.0722 * f[2];
  }
  function sel(el) {
    var s = el.tagName.toLowerCase();
    if (el.id) return s + '#' + el.id;
    var c = (el.className || '').toString().trim().split(/\s+/)[0];
    return c ? s + '.' + c : s;
  }
  var out = [];
  Array.prototype.slice.call(document.querySelectorAll('iframe')).forEach(function (f) {
    var res = {path: f.dataset.path, theme: f.dataset.theme, error: null, bad: []};
    try {
      var d = f.contentDocument;
      if (!d || !d.body) { res.error = 'no document'; out.push(res); return; }
      var canvas = lum(getComputedStyle(d.body).backgroundColor);
      if (canvas === null) canvas = lum(getComputedStyle(d.documentElement).backgroundColor);
      if (canvas === null) { res.error = 'no canvas colour'; out.push(res); return; }
      var all = d.querySelectorAll('body *');
      for (var i = 0; i < all.length; i++) {
        var el = all[i];
        var r = el.getBoundingClientRect();
        if (r.width < 32 || r.height < 24) continue;    /* too small to be a region */
        /* A form CONTROL is meant to look inset -- an input, a select and a
           textarea are recessed by convention in every dark theme there is,
           and a button has its own surface. This rule is about REGIONS: a
           column, a card, a well. Without this the theme switcher alone
           reported twenty-one times, which is the shape of a check nobody
           reads to the end. */
        if (/^(INPUT|SELECT|TEXTAREA|BUTTON|OPTION|PROGRESS|METER)$/.test(el.tagName)) continue;
        var mine = lum(getComputedStyle(el).backgroundColor);
        if (mine === null || mine >= canvas) continue;  /* level or raised: fine */
        var p = el.parentElement, raised = null;
        while (p && p !== d.body) {
          var pl = lum(getComputedStyle(p).backgroundColor);
          if (pl !== null && pl > canvas) { raised = p; break; }
          p = p.parentElement;
        }
        if (!raised) continue;                          /* not inside a panel */
        res.bad.push({el: sel(el), inside: sel(raised)});
      }
    } catch (e) { res.error = String(e); }
    out.push(res);
  });
  var pre = document.createElement('pre');
  pre.id = 'RESULT';
  pre.textContent = JSON.stringify(out);
  document.body.appendChild(pre);
});
"""


def saved_pages(paths):
    """Fetch each page in each theme, signed in, and write it to disk."""
    tmp = tempfile.mkdtemp(prefix="depthcheck-")
    s = _site.Session()
    if not s.login(_site.USER, _site.PASS):
        return tmp, []
    saved = []
    for theme in THEMES:
        s.post("/settings/theme", {"theme": theme})
        for p in paths:
            try:
                code, html = s.get(p)
            except Exception:  # noqa: BLE001
                continue
            if code != 200 or "<html" not in html[:2000].lower():
                continue
            for a, b in (('href="/static', 'href="%s/static' % _site.BASE),
                         ('src="/static', 'src="%s/static' % _site.BASE),
                         ('src="/uploads', 'src="%s/uploads' % _site.BASE),
                         ("url('/uploads", "url('%s/uploads" % _site.BASE)):
                html = html.replace(a, b)
            name = re.sub(r"[^a-z0-9]+", "_", (theme + p).lower()).strip("_") + ".html"
            io.open(os.path.join(tmp, name), "w", encoding="utf-8").write(html)
            saved.append((p, theme, name))
    return tmp, saved


def measure(tmp, saved):
    frames = "\n".join(
        '<iframe data-path="%s" data-theme="%s" src="%s" width="1280" height="900"></iframe>'
        % (p, t, n) for p, t, n in saved)
    harness = os.path.join(tmp, "_harness.html")
    io.open(harness, "w", encoding="utf-8").write(
        "<!doctype html><meta charset=utf-8><body>%s<script>%s</script></body>"
        % (frames, HARNESS_JS))
    cmd = [_site.CHROME, "--headless=new", "--disable-gpu", "--hide-scrollbars",
           "--allow-file-access-from-files", "--window-size=1400,1000",
           "--virtual-time-budget=15000", "--dump-dom", harness.replace("/", os.sep)]
    dom = subprocess.run(cmd, capture_output=True, text=True, timeout=900,
                         encoding="utf-8", errors="replace").stdout
    m = re.search(r'<pre id="RESULT">(.*?)</pre>', dom, re.S)
    if not m:
        return None
    return json.loads(m.group(1).replace("&quot;", '"').replace("&amp;", "&")
                      .replace("&lt;", "<").replace("&gt;", ">"))


def main():
    _site.require_site()
    args = [_site.unmangle(a) for a in sys.argv[1:] if not a.startswith("-")]
    tmp, saved = saved_pages(args or PAGES)
    if not saved:
        print("depth: could not sign in or fetch anything -- nothing was checked.")
        return 1

    results = measure(tmp, saved)
    if results is None:
        print("depth: the harness produced no result. Chrome may not have run.")
        print("CHROME=%s" % _site.CHROME)
        return 1

    failed, seen = False, 0
    for r in results:
        if r.get("error"):
            _site.say("  !    %-12s %-30s %s" % (r.get("theme"), r.get("path"), r["error"]))
            failed = True
            continue
        seen += 1
        # One line per distinct selector PAIR, not per element: a repeated row
        # is one mistake, and printing it forty times buries the next one.
        uniq = {}
        for b in r["bad"]:
            uniq[(b["el"], b["inside"])] = True
        for el, inside in sorted(uniq):
            if "%s %s" % (r["theme"], el) in BASELINE:
                continue
            failed = True
            _site.say("  BELOW THE CANVAS  %-12s %s" % (r["theme"], r["path"]))
            _site.say("      %s inside %s" % (el, inside))
            _site.say("      darker than the page it sits on, while %s is raised above it"
                      % inside)

    print()
    print("depth: %d page-and-theme combination(s) checked across %d theme(s)"
          % (seen, len(THEMES)))
    if failed:
        return 1
    print("depth: nothing inside a panel is darker than the page behind it.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
