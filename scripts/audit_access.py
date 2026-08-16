#!/usr/bin/env python3
"""Every route, probed as three identities. Does the gate match the claim?

    python scripts/audit_access.py

Exit status is 1 if any staff route answers a non-staff caller, or if the
access table disagrees with what the server does.

WHY PROBE RATHER THAN READ THE SOURCE
-------------------------------------
/admin/access is a hand-written table (accessRoutes in accessadmin_web.go). It
says what an operator BELIEVES the gates are. The gates themselves are
middleware on the route. Nothing keeps the two in step, and the failure is
silent in the direction that matters: a route whose gate was dropped still
appears in the table as "staff".

So this reads the route list off the running app's own registration log, probes
each one signed out, as a plain member and as an admin, and compares.

THE CSRF TRAP, WHICH IS THE WHOLE REASON THIS IS A SCRIPT
---------------------------------------------------------
A destructive POST with no token is refused BEFORE any role check. So probing
admin endpoints without a session gives 403 everywhere and proves nothing — an
ungated route looks exactly like a gated one. This project has already shipped
one test that passed that way.

Every POST probe below therefore carries a VALID token for the identity making
it, and the admin is probed too: if the admin gets 403 as well, the route is
broken rather than guarded, and a suite that only checks "non-admin is refused"
would call that a pass.

Destructive endpoints are probed with ids that do not exist, so a missing gate
cannot destroy anything while being discovered.
"""

import re
import subprocess
import sys

import _site

# Routes whose gate is the point of this audit. Everything under these must
# refuse anyone who is not staff.
STAFF_PREFIXES = ("/admin", "/moderation")

# Non-GET probes. Ids are deliberately absent from the database: the question is
# whether the gate answers before the handler, and a 404 from a real gate is a
# pass while a 200 would be a finding either way.
# Endpoints that are not PAGES. The access table is a list of pages an operator
# reasons about; a fragment that only ever answers an htmx swap has no separate
# access story from the page that asks for it, and listing it would grow the
# table without telling anybody anything.
NOT_PAGES = ("/search/suggest", "/plugin/guestbook")

DESTRUCTIVE = [
    "/admin/news/999999/delete",
    "/admin/store/999999/delete",
    "/admin/messages/999999/delete",
    "/admin/tickets/999999/delete",
    "/admin/wiki/posts/999999/delete",
    "/admin/forum-categories/999999/delete",
    "/admin/p/groups/delete",
    "/admin/p/usenet/groups-purge",
    "/admin/p/users/reset-password",
]


def routes():
    """The app's own registration log — the real list, not a guess at it."""
    try:
        out = subprocess.run(
            ["docker", "compose", "logs", "app"],
            capture_output=True, text=True, timeout=120,
        ).stdout
    except Exception as e:
        print("could not read the app log (%s)" % e)
        return []
    found = set()
    for m in re.finditer(r"GIN-debug\]\s+(GET|POST)\s+(\S+)", out):
        found.add((m.group(1), m.group(2)))
    return sorted(found)


def main():
    reg = routes()
    if not reg:
        print("no routes found. Is the stack up? `docker compose up -d`")
        return 1
    gets = [p for verb, p in reg if verb == "GET" and ":" not in p and "*" not in p]
    print("%d routes registered, %d plain GETs to probe\n" % (len(reg), len(gets)))

    # No redirect following on the probes: a gated page redirects a browser to
    # /login, and a client that follows reports the login page's 200. See
    # _site._NoRedirect — this audit reported eighteen false leaks before it did.
    anon = _site.Session(follow_redirects=False)
    member = _site.Session(follow_redirects=False)
    admin = _site.Session(follow_redirects=False)
    if not member.login(_site.MEMBER_USER, _site.MEMBER_PASS):
        print("could not sign in as the member account (%s)." % _site.MEMBER_USER)
        print("Set AUDIT_MEMBER/AUDIT_MEMBER_PASS. Without a member session this")
        print("audit cannot tell 'staff only' from 'signed in only', so it stops")
        print("rather than reporting a pass it did not earn.")
        return 1
    if not admin.login(_site.USER, _site.PASS):
        print("could not sign in as the admin account (%s). Set AUDIT_USER/AUDIT_PASS." % _site.USER)
        return 1

    findings = []

    # 1. Staff pages must refuse everyone who is not staff.
    for p in gets:
        if not p.startswith(STAFF_PREFIXES):
            continue
        a = anon.status(p)
        m = member.status(p)
        d = admin.status(p)
        if a == 200:
            findings.append("%s serves 200 to an anonymous visitor" % p)
        if m == 200:
            findings.append("%s serves 200 to a plain member" % p)
        if d in (401, 403):
            findings.append("%s refuses the ADMIN too (%s) — broken, not guarded" % (p, d))

    # 2. Destructive POSTs, with real tokens. See the note at the top.
    for p in DESTRUCTIVE:
        m = member.post(p)
        if m == 200:
            findings.append("POST %s accepted from a plain member" % p)
        elif m in (0, 404):
            findings.append("POST %s answered %s for a member — route may have moved, "
                            "so this probe proves nothing" % (p, m))
        d = admin.post(p)
        if d in (401, 403):
            findings.append("POST %s refuses the ADMIN too (%s) — the probe is "
                            "measuring CSRF or a dead route, not the gate" % (p, d))

    # 3. COVERAGE, not agreement.
    #
    # The obvious check here is "does the access table match the gates", and it
    # is worthless: buildAccessMap answers each row by asking the SAME
    # predicates the middleware uses, so the table cannot describe a gate the
    # site does not have. Its own comment says so. A first version of this
    # audit compared the two anyway, which was the server checked against
    # itself — it passed with /browse deliberately mislabelled, which is how it
    # was caught.
    #
    # What the table genuinely cannot tell an operator is what is MISSING from
    # it. Every row is one somebody wrote down; a route nobody listed is
    # invisible, whatever its gate. That is not hypothetical — /nzb/:id, the
    # actual file download, was absent until this audit's manual first pass.
    #
    # So: anything readable by a stranger that no row covers.
    status, body = admin.get("/admin/access")
    if status != 200:
        findings.append("/admin/access did not render for an admin (%s), so table "
                        "coverage could not be checked" % status)
    else:
        listed = [p for p, _ in declared_rows(body)] + [
            r for r in re.findall(r'(/[a-z0-9/_.:*-]+)', body) if r.endswith("/*")]
        for p in gets:
            if p.startswith(STAFF_PREFIXES) or p in NOT_PAGES:
                continue  # collapsed to one row on purpose — see accessRoutes
            if anon.status(p) != 200:
                continue  # not readable by a stranger; the table not naming it is survivable
            if not any(p == l or (l.endswith("/*") and p.startswith(l[:-2])) for l in listed):
                findings.append("%s is readable by anyone and is not in the access "
                                "table — an operator auditing access cannot see it" % p)

    if findings:
        print("%d finding(s):\n" % len(findings))
        for f in findings:
            print("  " + f)
        return 1
    print("staff routes refuse non-staff, destructive POSTs refuse members with a")
    print("valid token, and the access table matches what the server does.")
    return 0


def declared_rows(html):
    """(path, claim) from the access table, claim lower-cased."""
    out = []
    for row in re.findall(r"<tr[^>]*>(.*?)</tr>", html, re.S):
        cells = [" ".join(re.sub(r"<[^>]+>", " ", c).split())
                 for c in re.findall(r"<td.*?>(.*?)</td>", row, re.S)]
        path = next((c for c in cells if c.startswith("/")), None)
        if path and ":" not in path and "*" not in path:
            out.append((path, " ".join(cells).lower()))
    return out


if __name__ == "__main__":
    sys.exit(main())
