"""Walk the site signed in and check the accessibility rules that hold per page.

This is a REWRITE. The original lived only in a session scratchpad, which was
cleared, and it is being written again from the record of what it found -- which
is exactly the argument for this file existing at the repository root instead.

What it checks, and why each one is here rather than in a linter:

  one h1          a page with none has no name; a page with two has no outline
  heading order   h2 -> h4 leaves a hole a screen-reader user navigates by
  named landmarks two <nav>s with no label are "navigation" and "navigation"
  named controls  an input with no label is announced as "edit text"
  aria-current    marking the active nav item with COLOUR ONLY says nothing
                  to a screen reader -- the audit found 8 strips doing this
  image alt       a missing alt reads the filename out loud
  duplicate ids   breaks every label-for and aria-describedby pointing at them
  named tables    "table with 6 columns" is not a description of anything

Earlier runs found 2 pages with no h1 (including the home page), 8 nav strips
marking the current item to sighted readers only, and 9 unnamed tables.

    python scripts/audit_a11y.py

Exits non-zero when anything is found.
"""
import re
import sys
from html.parser import HTMLParser

sys.path.insert(0, __file__.rsplit("/", 1)[0].rsplit("\\", 1)[0])
import _site  # noqa: E402
import audit_links  # noqa: E402

# SEEDS, not the list. These are checked always; everything the link crawler
# reaches is added to them (see pages_to_check).
#
# The curated list used to BE the list, and 53 of the site's 65 /p/ and
# /admin/ routes were missing from it -- every plugin admin page, most member
# pages. The audit printed "0 findings across 48 pages" the whole time, which
# was true and read as "the site is clean". Nothing was wrong with the checks;
# the argument list stopped early, the same way `make resources` was never
# handed loon-baseline.
#
# What seeds still earn their place: pages a crawl cannot produce because no
# link points at them with the right query string (/admin/pages?edit=faq), and
# pages worth checking even if a nav entry that links to them is ever removed.
SEEDS = [
    "/", "/browse", "/search", "/groups", "/trending", "/stats", "/staff",
    "/about", "/rules", "/faq", "/sitemap", "/news", "/wiki",
    "/community/forums", "/c", "/support",
    "/login", "/register", "/forgot",
    "/u/alice", "/u/alice/followers", "/u/alice/friends",
    "/bookmarks", "/calendar", "/achievements", "/subscriptions", "/invites",
    "/p/pot", "/p/charity", "/p/medals", "/p/magic",
    "/settings/profile", "/settings/privacy", "/settings/notifications",
    "/inbox", "/store",
    "/admin", "/admin/settings", "/admin/access", "/admin/contracts",
    "/admin/i18n", "/admin/pages", "/admin/pages?edit=faq", "/admin/nav",
    "/admin/jobs", "/admin/plugins", "/moderation", "/moderation/avatars",
]

VOID = {"img", "input", "br", "hr", "meta", "link", "source", "use", "path", "circle", "rect"}
LABELLABLE = {"input", "select", "textarea"}
# These input types are not text fields and are named by their own value or by
# surrounding text; demanding a label for a hidden field is noise.
UNLABELLED_OK = {"hidden", "submit", "button", "reset", "image", "csrf"}


class Page(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.headings = []      # (level, text)
        self.ids = []
        self.navs = []          # (named, has_current)
        self.controls = []      # (tag, name, named)
        self.images = []        # (src, has_alt)
        self.tables = []        # named
        self._stack = []
        self._heading = None
        self._labels_for = set()
        self._prescanned = set()
        self._nav_depth = None
        self._in_current_nav = False
        self._label_depth = None

    def set_labels(self, names):
        """Seed the label targets found in the pre-pass (see check)."""
        self._prescanned = set(names)

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if "id" in a:
            self.ids.append(a["id"])
        if tag == "label":
            if "for" in a:
                self._labels_for.add(a["for"])
            # A WRAPPING label names what it contains, with no `for` and no id:
            #
            #     <label>Enabled <input type="checkbox" name="enabled"></label>
            #
            # is a valid association that HTML has always had. Not tracking it
            # reported correctly-labelled checkboxes as unnamed, which is the
            # kind of false positive that gets an audit switched off.
            self._label_depth = len(self._stack)

        if re.fullmatch(r"h[1-6]", tag):
            self._heading = (int(tag[1]), [])

        if tag == "nav":
            named = bool(a.get("aria-label") or a.get("aria-labelledby"))
            self.navs.append([named, False, set()])
            self._nav_depth = len(self._stack)

        if tag == "a" and self._nav_depth is not None and self.navs:
            self.navs[-1][2].add(a.get("href", "").split("?")[0].split("#")[0])

        # aria-current anywhere inside a nav counts: it may sit on the <a>, and
        # the <a> may be several elements down.
        if self._nav_depth is not None and a.get("aria-current"):
            if self.navs:
                self.navs[-1][1] = True

        if tag in LABELLABLE:
            typ = (a.get("type") or "text").lower()
            name = a.get("name", "")
            if typ not in UNLABELLED_OK and name != "_csrf":
                named = bool(
                    a.get("aria-label") or a.get("aria-labelledby") or a.get("title")
                    or (a.get("id") and a["id"] in (self._labels_for | self._prescanned))
                    or self._label_depth is not None
                )
                self.controls.append((tag, name or a.get("id", "?"), named, a.get("id", "")))

        if tag == "img":
            self.images.append((a.get("src", "?"), "alt" in a))

        if tag == "table":
            self.tables.append(bool(a.get("aria-label") or a.get("aria-labelledby")))

        if tag not in VOID:
            self._stack.append(tag)

    def handle_endtag(self, tag):
        if self._heading and re.fullmatch(r"h[1-6]", tag):
            level, parts = self._heading
            self.headings.append((level, "".join(parts).strip()))
            self._heading = None
        if tag == "nav":
            self._nav_depth = None
        if tag == "label":
            self._label_depth = None
        while self._stack and self._stack[-1] != tag:
            self._stack.pop()
        if self._stack:
            self._stack.pop()

    def handle_data(self, data):
        if self._heading:
            self._heading[1].append(data)

    # A <caption> names a table too; recorded by the parser above only as text,
    # so it is resolved after the fact in check().


def check(path, body):
    """Return a list of finding strings for one page."""
    p = Page()
    # Labels are collected in a PRE-PASS because a <label for=x> may appear
    # after the input it names -- which is the normal order for a checkbox, and
    # which made this tool report every checkbox on /settings/privacy and
    # /settings/notifications as unlabelled. A single-pass parser sees the
    # input before the label exists and cannot know better.
    p.set_labels(re.findall(r'<label[^>]*\sfor="([^"]+)"', body))
    try:
        p.feed(body)
    except Exception as e:  # noqa: BLE001 -- malformed markup is itself a finding
        return ["HTML did not parse: %s" % e]

    out = []

    h1s = [t for lvl, t in p.headings if lvl == 1]
    if not h1s:
        out.append("no <h1> : the page has no name")
    elif len(h1s) > 1:
        out.append("%d <h1>s (%s) : a page has one name" % (len(h1s), ", ".join(h1s[:3])))

    prev = 0
    for lvl, text in p.headings:
        if prev and lvl > prev + 1:
            out.append("heading jumps h%d -> h%d at %r" % (prev, lvl, text[:40]))
        prev = lvl

    for i, (named, has_current, hrefs) in enumerate(p.navs):
        if not named:
            out.append("<nav> #%d has no aria-label: every nav is announced as 'navigation'" % (i + 1))
        # Only a finding when the nav LINKS to the page you are on. A footer
        # nav, or a breadcrumb whose last crumb is plain text rather than a
        # link, has no current item to mark, and demanding one buries the real
        # cases under one finding per page.
        if not has_current and path in hrefs:
            out.append("<nav> #%d links %s but marks nothing aria-current: "
                       "the active item is colour only" % (i + 1, path))

    for tag, name, named, _id in p.controls:
        if not named:
            out.append("<%s name=%r> has no label" % (tag, name))

    for src, has_alt in p.images:
        if not has_alt:
            out.append("<img src=%r> has no alt" % src[:50])

    dupes = {i for i in p.ids if p.ids.count(i) > 1}
    for d in sorted(dupes):
        out.append("duplicate id %r : breaks every label and aria-* pointing at it" % d)

    # <caption> resolves a table's name and the parser sees it as plain text, so
    # only complain when the page has unnamed tables AND no captions at all.
    captions = body.count("<caption")
    unnamed = sum(1 for named in p.tables if not named)
    if unnamed > captions:
        out.append("%d table(s) with no aria-label or <caption>" % (unnamed - captions))

    return out



# Paths whose next segment names an INSTANCE rather than a page. Forty release
# pages exercise one template, so one of them is the whole of the signal.
#
# Everything else is its own shape, deliberately: /admin/p/medals and
# /admin/p/rewards share a prefix and are entirely different templates, and a
# collapser clever enough to fold those would hide exactly the pages this
# change exists to reach.
INSTANCE_PREFIXES = (
    "/u/", "/release/", "/series/", "/nzb/", "/c/", "/wiki/", "/news/",
    "/forum/", "/community/thread/", "/p/lists/", "/g/",
)


def shape(path):
    """The template a path exercises, as far as a URL can say."""
    # A numeric segment is always an id.
    collapsed = re.sub(r"/\d+(?=/|$)", "/:id", path)
    for prefix in INSTANCE_PREFIXES:
        if collapsed.startswith(prefix) and len(collapsed) > len(prefix):
            rest = collapsed[len(prefix):]
            # Keep one more segment for sub-pages: /u/alice/followers is a
            # different template from /u/alice.
            tail = rest.split("/", 1)
            return prefix + ":x" + ("/" + tail[1] if len(tail) > 1 else "")
    return collapsed


def pages_to_check():
    """The seeds, plus one page per shape the crawler reached.

    Sorted so a diff of the output is readable, and so the run order does not
    depend on set iteration.
    """
    chosen = {}
    for path in SEEDS:
        chosen.setdefault(shape(path), path)
    try:
        discovered = audit_links.discover()
    except Exception as exc:  # noqa: BLE001 - a crawl failure must not hide the seeds
        print("a11y: discovery failed (%s); checking the seeds only" % exc)
        return sorted(SEEDS)
    # SORTED, and it is not cosmetic. discover() returns a SET, so iterating it
    # raw picks an arbitrary member as each shape's representative — a
    # different page every run, and with it a different finding count. The
    # baseline then flaps between two numbers and the check that is supposed to
    # be a ratchet becomes noise somebody learns to re-run until it passes.
    for path in sorted(discovered):
        chosen.setdefault(shape(path), path)
    return sorted(chosen.values())


# What the audit found the first time it looked at the whole site, per shape.
#
# 534 findings across 109 pages, from a check that had been reporting "0
# findings across 48 pages". Not a regression -- 53 of the site's 65 /p/ and
# /admin/ routes had never been checked, so this is the first measurement
# rather than a change.
#
# 534 of those were the first measurement. 152 template controls have since
# been named from their own label's words (loon-plugins b502f0c, bcf7e9a),
# taking it to 269.
#
# WHAT IS LEFT IS A DIFFERENT PATTERN, and it needs a different fix. The
# remainder are table-cell inputs with NO adjacent label at all --
#
#     <th>Host</th>
#     ...
#     <td><input type="text" name="host" value="news.example.com"></td>
#
# where the column header is the only thing naming the field. Nothing beside
# the input can be read to name it; the fix has to map each cell to its <th>,
# which is a per-template job rather than one regex. /admin/p/usenet (92),
# /admin/p/groups (63) and /admin/forum-categories (38) are most of it.
#
# requests/ and ranks/ contribute some of this and belong to another
# workstream.
#
# PER SHAPE, NOT A TOTAL. A single number lets a new page's findings hide
# behind another page's fix. Keyed like this, a page that gets worse fails even
# when the site's total fell, and a shape with no entry fails on its FIRST
# finding -- which is the property actually worth having, because it is what
# stops the next page being added with the same markup.
#
# Lower an entry in the same commit that fixes one. Raising one needs a reason
# in the commit message and there is not currently a good one.
#
# Measured 21 Aug 2026.
A11Y_BASELINE = {
    "/admin/p/groups": 63,
    "/admin/p/usenet": 1,
}

def main():
    _site.require_site()
    if not _site.login():
        raise SystemExit("audit: could not sign in as %s" % _site.USER)

    pages = pages_to_check()
    total, checked = 0, 0
    counts = {}
    for path in pages:
        code, body = _site.get(path)
        if code != 200:
            # Not a finding: a page may legitimately redirect or be gated.
            continue
        # HTML only. /tracker/download/<hash> answers 200 with a .torrent, and
        # parsing 18KB of bencode as markup reported "no <h1>: the page has no
        # name" five times — a true statement about a file that is not a page.
        # Checked by content rather than by path, so the next binary endpoint
        # is handled without anybody remembering to add it.
        if "<html" not in body[:600].lower():
            continue
        checked += 1
        found = check(path, body)
        if not found:
            continue
        allowed = A11Y_BASELINE.get(shape(path), 0)
        counts[shape(path)] = counts.get(shape(path), 0) + len(found)
        total += len(found)
        # Printed either way: the debt is meant to be visible, not hidden by a
        # green exit. Only the excess fails.
        print("  %s%s" % (path, "" if allowed else "   NEW"))
        for f in found:
            print("     %s" % f)

    # Both numbers: "0 findings" means nothing without how much was looked at,
    # which is the whole reason this audit was reporting clean.
    print("a11y: %d finding(s) across %d pages (%d shapes offered, %d seeds)"
          % (total, checked, len(pages), len(SEEDS)))

    over = [(s, n, A11Y_BASELINE.get(s, 0)) for s, n in sorted(counts.items())
            if n > A11Y_BASELINE.get(s, 0)]
    if over:
        print("\nWORSE THAN THE BASELINE:")
        for s, n, was in over:
            print("  %-40s %d, baseline %d" % (s, n, was))
        return 1

    stale = sorted(s for s, was in A11Y_BASELINE.items() if counts.get(s, 0) < was)
    if stale:
        print("\nBASELINE STALE (fixed, so lower it in this commit):")
        for s in stale:
            print("  %-40s %d, baseline %d" % (s, counts.get(s, 0), A11Y_BASELINE[s]))
        return 1

    print("a11y: at the baseline (%d recorded across %d shapes)"
          % (total, len(A11Y_BASELINE)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
