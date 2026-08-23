#!/usr/bin/env python3
"""Contrast of the text the browser actually PAINTED, in every theme.

    python3 scripts/audit_paint.py              # every page, every theme
    python3 scripts/audit_paint.py /wiki        # these pages, every theme

WHY THIS EXISTS ALONGSIDE contrast.py, WHICH IS ALSO A CONTRAST CHECK.

contrast.py checks token PAIRS, and it says so plainly: "it knows the pairs
listed below... the alternative is parsing the CSS and guessing which pairs
ever meet, which would be confidently wrong rather than incomplete." That is
the right trade for a token check, and it has one failure mode it cannot fix
from the inside -- a hand-written list finds what somebody thought to list.

It missed --surface-3 entirely. Not one pair named it, in any theme, for the
whole life of the file, because the token calls itself "pressed" and nobody
reads a pressed state as a text ground. A dozen components rest on it: the
category tag, table headings in prose, the inbox rows, news and messages. In
nord, --muted on it was 3.54:1 and the message previews in everyone's inbox
had never been measured by anything. --muted-2 had been LIFTED for contrast
eight hours earlier with a note saying it "cleared all three grounds"; it
counted three and there are four.

This asks the other question. Render the page, walk every element that has its
own visible text, take the colour the browser computed and the ground it was
actually drawn on, and measure. No list, so nothing can be left off one -- and
a pair that never meets is never reported, because a pair that never meets is
never painted.

Neither check replaces the other. This one sees only what was rendered: the
pages in PAGES, in the states they happened to be in, so a tag that appears
only on an empty result set is invisible to it. contrast.py sees every theme's
tokens whether or not anything drew them. Run both; this one DISCOVERS pairs,
and the pairs it finds belong in contrast.py, where they get checked in every
theme forever after.

WHAT IT WILL NOT JUDGE, and why saying so matters. getComputedStyle reports a
gradient or an image background as transparent, so an element sitting on one
has no ground this can read. Walking past it to the next ancestor is what the
first version did, and it confidently reported the donate page's gold medal
badges as dark-on-dark: six findings, every one wrong, because the real ground
was the gradient it had skipped. Those elements are counted and reported as
unjudged rather than guessed at.
"""
import io
import json
import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import _site        # noqa: E402
import audit_depth  # noqa: E402

# Pages that are NOT one-per-section but are worth measuring anyway: each is a
# plugin surface with its own stylesheet, and plugin CSS carries 441 literal
# colours the token check cannot see by nature, because they are not tokens.
EXTRA = ["/", "/inbox", "/c", "/u/alice", "/help/donate", "/community/forums"]


def page_set():
    """One page per top-level section, plus the plugin surfaces above.

    DERIVED, not hand-written, and that is the same point the whole audit
    makes. A list is a thing somebody has to remember to add to: /staff was
    missing from the first hand-written version here and carried a failing
    admin-red username -- on the page whose entire job is listing the people
    whose names are that colour. Lighthouse found it because ITS list is
    derived from the crawl, so this borrows the same one.
    """
    try:
        import audit_lighthouse
        # It returns (sampled, not_sampled). Taking the tuple whole put two
        # LISTS in the page list, which saved_pages quietly skipped -- so this
        # measured six pages and said so in a number nobody would question.
        pages = list(audit_lighthouse.page_set()[0])
    except Exception as exc:  # noqa: BLE001 - degrade to the plugin surfaces
        print("  ! section list unavailable (%s); measuring the plugin pages only" % exc)
        pages = []
    for p in EXTRA:
        if p not in pages:
            pages.append(p)
    return pages

# WCAG AA. Large text is 3:1 and normal text 4.5:1; which one applies is
# decided per element from the size and weight the browser resolved, not
# guessed from the selector.
AA_NORMAL = 4.5
AA_LARGE = 3.0

# A measurement this far below the floor is not a contrast problem: it means
# the ground was misread. Nothing is exempt at this level today -- the gradient
# guard below removed the only cases -- and the constant exists so that if one
# comes back it is reported as a HARNESS fault rather than filed as a design
# failure somebody will spend an afternoon not finding.
IMPOSSIBLE = 1.5

HARNESS_JS = r"""
window.addEventListener('load', function () {
  function rgb(s) {
    var p = (s || '').match(/[\d.]+/g);
    if (!p) return null;
    p = p.map(Number);
    if (p.length > 3 && p[3] < 0.5) return null;   /* see-through: not a ground */
    return p.slice(0, 3);
  }
  function lum(c) {
    var f = c.map(function (v) {
      v = v / 255;
      return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
    });
    return 0.2126 * f[0] + 0.7152 * f[1] + 0.0722 * f[2];
  }
  function ratio(a, b) {
    var l1 = lum(a), l2 = lum(b);
    if (l1 < l2) { var t = l1; l1 = l2; l2 = t; }
    return (l1 + 0.05) / (l2 + 0.05);
  }
  function hex(c) {
    return '#' + c.map(function (v) {
      return ('0' + Math.round(v).toString(16)).slice(-2);
    }).join('');
  }
  function sel(el) {
    var s = el.tagName.toLowerCase();
    var c = (el.className || '').toString().trim().split(/\s+/)[0];
    return c ? s + '.' + c : s;
  }
  var out = [];
  Array.prototype.slice.call(document.querySelectorAll('iframe')).forEach(function (f) {
    var res = {path: f.dataset.path, theme: f.dataset.theme, error: null,
               bad: [], judged: 0, art: 0};
    try {
      var d = f.contentDocument;
      if (!d || !d.body) { res.error = 'no document'; out.push(res); return; }
      var all = d.querySelectorAll('body *');
      var seen = {};
      for (var i = 0; i < all.length; i++) {
        var el = all[i];
        /* Only elements with their OWN text. An element whose text lives in a
           child is styled by that child, and counting both reports one string
           once per ancestor. */
        var own = '';
        for (var n = 0; n < el.childNodes.length; n++) {
          if (el.childNodes[n].nodeType === 3) own += el.childNodes[n].nodeValue;
        }
        if (!own.trim()) continue;
        var r = el.getBoundingClientRect();
        if (r.width < 4 || r.height < 4) continue;      /* not rendered */
        var cs = getComputedStyle(el);
        if (cs.visibility === 'hidden' || cs.opacity === '0') continue;
        var fg = rgb(cs.color);
        if (!fg) continue;
        var p = el, bg = null, art = false;
        while (p) {
          var ps = getComputedStyle(p);
          /* A gradient or image ground cannot be read: backgroundColor calls a
             linear-gradient transparent. Without this the walk sails past a
             gold medal badge and measures its dark ink against the page. */
          if (ps.backgroundImage && ps.backgroundImage !== 'none') { art = true; break; }
          var c = rgb(ps.backgroundColor);
          if (c) { bg = c; break; }
          p = p.parentElement;
        }
        if (art) { res.art++; continue; }
        if (!bg) continue;
        res.judged++;
        var size = parseFloat(cs.fontSize) || 16;
        var wt = parseInt(cs.fontWeight, 10) || 400;
        var need = (size >= 24 || (size >= 18.66 && wt >= 700)) ? LARGE : NORMAL;
        var got = ratio(fg, bg);
        if (got >= need) continue;
        var key = sel(el) + hex(fg) + hex(bg);
        if (seen[key]) continue;
        seen[key] = 1;
        res.bad.push({el: sel(el), fg: hex(fg), bg: hex(bg),
                      ratio: Math.round(got * 100) / 100, need: need,
                      text: own.trim().slice(0, 40)});
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


def measure(tmp, saved):
    """Render every saved page in one Chrome and read the result back.

    'load', NOT 'DOMContentLoaded'. The parent's DOM is parsed before its
    iframes have navigated, so a DOMContentLoaded handler measures eighteen
    copies of about:blank and reports a confident zero -- which is exactly what
    the first version of this did, and it survived because zero findings is
    what a passing check looks like. It was caught by asking for an impossible
    30:1 and still getting nothing back. Hence judged_total below.
    """
    frames = "\n".join(
        '<iframe data-path="%s" data-theme="%s" src="%s" width="1280" height="1400"></iframe>'
        % (p, t, n) for p, t, n in saved)
    js = HARNESS_JS.replace("NORMAL", repr(AA_NORMAL)).replace("LARGE", repr(AA_LARGE))
    harness = os.path.join(tmp, "_paint.html")
    io.open(harness, "w", encoding="utf-8").write(
        "<!doctype html><meta charset=utf-8><body>%s<script>%s</script></body>" % (frames, js))
    dom = subprocess.run(
        [_site.CHROME, "--headless=new", "--disable-gpu", "--hide-scrollbars",
         "--allow-file-access-from-files", "--window-size=1400,1200",
         "--virtual-time-budget=20000", "--dump-dom", harness.replace("/", os.sep)],
        capture_output=True, text=True, timeout=900,
        encoding="utf-8", errors="replace").stdout
    m = re.search(r'<pre id="RESULT">(.*?)</pre>', dom, re.S)
    if not m:
        return None
    return json.loads(m.group(1).replace("&quot;", '"').replace("&amp;", "&")
                      .replace("&lt;", "<").replace("&gt;", ">"))


def main():
    _site.require_site()
    args = [_site.unmangle(a) for a in sys.argv[1:] if not a.startswith("-")]
    # saved_pages is audit_depth's, deliberately: it signs in, walks the three
    # themes and rewrites /static and /pluginstyle so a saved page is not
    # measured unstyled. A second copy here would be a second thing to fix the
    # next time an asset prefix moves -- which has already happened once.
    tmp, saved = audit_depth.saved_pages(args or page_set())
    if not saved:
        print("paint: could not sign in or fetch anything -- nothing was checked.")
        return 1

    results = measure(tmp, saved)
    if results is None:
        print("paint: the harness produced no result. Chrome may not have run.")
        print("CHROME=%s" % _site.CHROME)
        return 1

    failed, renders, judged_total, art_total = False, 0, 0, 0
    worst = {}
    for r in results:
        if r.get("error"):
            _site.say("  !    %-12s %-24s %s" % (r.get("theme"), r.get("path"), r["error"]))
            failed = True
            continue
        renders += 1
        judged_total += r.get("judged", 0)
        art_total += r.get("art", 0)
        for b in r["bad"]:
            key = (b["el"], b["fg"], b["bg"])
            if key not in worst or b["ratio"] < worst[key][0]["ratio"]:
                worst[key] = (b, r["theme"], r["path"])

    # THE CHECK ON THE CHECK. A pass here means "every painted string cleared",
    # and that claim is only worth anything if strings were read. Ten per render
    # is far below any real page -- the smallest of these reads 290 -- so this
    # trips on a broken harness, not on a quiet day.
    if renders and judged_total < renders * 10:
        print("paint: %d element(s) judged across %d render(s) -- far too few.\n"
              "The harness measured almost nothing and would report a clean run\n"
              "whatever the site looked like. Fix that before trusting this."
              % (judged_total, renders))
        return 1

    for b, theme, path in sorted(worst.values(), key=lambda x: x[0]["ratio"]):
        failed = True
        note = ""
        if b["ratio"] < IMPOSSIBLE:
            note = "  <- below %.1f:1; suspect the GROUND was misread, not the design" % IMPOSSIBLE
        _site.say("  %5.2f (needs %.1f)  %-26s %s on %s  %-12s %-20s %s%s"
                  % (b["ratio"], b["need"], b["el"], b["fg"], b["bg"],
                     theme, path, b["text"], note))

    print()
    print("paint: %d element(s) judged across %d render(s); %d not judged "
          "(gradient or image ground)" % (judged_total, renders, art_total))
    if failed:
        print("paint: %d painted string(s) below WCAG AA.\n"
              "Each one is a pair contrast.py does not know about -- add it there\n"
              "too, so every theme is checked and not just the ones rendered here."
              % len(worst))
        return 1
    print("paint: every string the browser drew clears WCAG AA in every theme.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
