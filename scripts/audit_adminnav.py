"""Every admin page must be reachable from the admin nav.

An admin route that is served and named nowhere is a page an operator finds by
knowing the URL, which in practice means they do not find it. On 20 Aug 2026
seventeen were in that state: six plugins whose admin surface is a route group
rather than a single view, and two of the host's own pages that shipped without
a link the same week.

WHAT IT COMPARES. The nav is read from the RENDERED /admin page as a signed-in
admin, not from the source, so feature gates and role filtering are accounted
for. The routes are read from the running server's route table via /admin/access
if it exposes one, and otherwise from the boot log.

WHY A PREFIX COUNTS. A sub-page like /admin/wiki/topics/new is reachable if
/admin/wiki is listed, because that is the page it is linked from. The bare
/admin is excluded from that rule — it is the dashboard, not a parent, and
treating it as one marks every admin route reachable and the audit passes while
proving nothing. That bug was in the first version of this script.
"""

import re
import subprocess
import sys

sys.path.insert(0, "scripts")
import _site  # noqa: E402

# Routes that are deliberately not in any nav.
ALLOWED_UNLISTED = {
    # Sub-pages of a listed parent are handled by the prefix rule; these are
    # the ones with no listed parent and a reason.
    "/admin/plugin/usenet/status": "a fragment endpoint the usenet admin page "
                                   "fetches, not a page a human opens",
}


def served_routes():
    """Admin GET routes, from the running container's boot log."""
    out = subprocess.run(
        ["docker", "compose", "logs", "app"],
        capture_output=True, text=True, timeout=120,
        encoding="utf-8", errors="replace",
    ).stdout
    found = set()
    for m in re.finditer(r"GET\s+(/admin/[A-Za-z0-9:/_-]+)", out):
        found.add(m.group(1).rstrip("/"))
    return found


def listed_hrefs(html):
    out = set()
    for href in re.findall(r'href="(/admin[^"#?]*|/moderation[^"#?]*)"', html):
        href = href.rstrip("/")
        if href:
            out.add(href)
    return out


def main():
    # Fail on "could not check" rather than passing quietly. Probed 22 Aug 2026
    # against three known-bad inputs: dropping /admin/access from the nav was
    # caught, but an empty route list and a failed sign-in BOTH returned 0 --
    # printing a note and reporting success. An audit that cannot see the site
    # has not found nothing; it has found nothing OUT, and the difference is
    # the whole value of running it in `make check`. Same defect and same fix
    # as the missing SENTENCE_BASELINE in audit_resources.
    _site.require_site()

    admin = _site.Session()
    if not admin.login(_site.USER, _site.PASS):
        print("adminnav: could not sign in as %s -- cannot read the admin nav,"
              " so this proves nothing" % _site.USER)
        return 1

    code, html = admin.get("/admin")
    if code != 200:
        print("adminnav: /admin returned %s" % code)
        return 1

    listed = listed_hrefs(html)
    served = served_routes()
    if not served:
        print("adminnav: no admin routes found in the container log. The stack"
              " is answering, so this is the log source, not the site --"
              " nothing was compared.")
        return 1

    def covered(route):
        for l in listed:
            if l == "/admin":
                continue  # the dashboard is not a parent — see the module docstring
            if route == l or route.startswith(l + "/"):
                return True
        return False

    missing = sorted(r for r in served if not covered(r) and r not in ALLOWED_UNLISTED)

    print("adminnav: %d admin routes, %d nav links, %d unreachable"
          % (len(served), len(listed), len(missing)))
    if ALLOWED_UNLISTED:
        print("  (%d deliberately unlisted)" % len(ALLOWED_UNLISTED))
    if missing:
        print()
        print("  SERVED BUT IN NO NAV:")
        for r in missing:
            print("    " + r)
        print()
        print("  A plugin whose admin surface is one page should register a")
        print("  SlotAdminPage view. One that is a route GROUP should call")
        print("  pluginapi.RegisterAdminNav beside the routes it mounts.")
        print("  A host page belongs in the adminNav list in admin_views.go.")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
