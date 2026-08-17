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

# A representative page per shape rather than the whole site: these checks are
# about TEMPLATES, and crawling 200 pages to test the same listing template
# forty times buys nothing but noise.
PAGES = [
    "/", "/browse", "/search", "/groups", "/trending", "/stats", "/staff",
    "/about", "/rules", "/faq", "/sitemap", "/news", "/wiki",
    "/community/forums", "/c", "/support",
    "/login", "/register", "/forgot",
    "/u/alice", "/u/alice/followers", "/u/alice/friends",
    "/bookmarks", "/calendar", "/achievements", "/subscriptions", "/invites",
    "/p/pot", "/p/charity", "/p/medals",
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

    def set_labels(self, names):
        """Seed the label targets found in the pre-pass (see check)."""
        self._prescanned = set(names)

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if "id" in a:
            self.ids.append(a["id"])
        if tag == "label" and "for" in a:
            self._labels_for.add(a["for"])

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


def main():
    _site.require_site()
    if not _site.login():
        raise SystemExit("audit: could not sign in as %s" % _site.USER)

    total, checked = 0, 0
    for path in PAGES:
        code, body = _site.get(path)
        if code != 200:
            # Not a finding: a page may legitimately redirect or be gated.
            continue
        checked += 1
        found = check(path, body)
        if found:
            print("  %s" % path)
            for f in found:
                print("     %s" % f)
            total += len(found)

    print("a11y: %d finding(s) across %d pages" % (total, checked))
    return 1 if total else 0


if __name__ == "__main__":
    sys.exit(main())
