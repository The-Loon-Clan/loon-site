#!/usr/bin/env python3
"""The /admin/contracts page, read by something other than a person.

    python scripts/audit_contracts.py

The host already computes two things on that page and neither gated anything:

  UNFILLED CONTRACTS   a seam with two halves where one half is missing --
                       "a reward promises a lootbox payout that cannot be
                       delivered". The page's own words: every one of these
                       fails silently by nature, the symptom is a feature that
                       does nothing rather than an error.

  ORPHAN SUBSCRIPTIONS a plugin listening for an event nothing declares.
                       Either the name is a typo or the emitter is not
                       installed here, and an orphan is INDISTINGUISHABLE FROM
                       WORKING: a listener for an event that never fires is
                       silent, which is what it would look like if it were
                       fine.

Both were rendered and watched by nobody. On 22 Aug 2026 the page had been
reporting one unfilled contract -- every lootbox reward on the site,
undeliverable -- and it turned out the finding itself was wrong, which is the
second-worst state for a report nobody reads. It is fixed; this is what stops
either kind of silence coming back.

WHY IT PARSES HTML. There is no JSON view of this page. Parsing markup is the
weaker half of this check, so it FAILS when the markers it needs are absent
rather than concluding that a page it could not read was clean.
"""

import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import _site  # noqa: E402

# The page always renders this heading, findings or none -- deliberately, per
# its own comment: "a page that only appears when something is wrong cannot be
# used to check that nothing is."
SCOPE = "What is checked"
CLEAN = "Nothing unfilled."
ORPHANS = "Listening for events nothing declares"
FINDING = re.compile(r'<li class="contract-finding">(.*?)</li>', re.S)
TAGS = re.compile(r"<[^>]+>")


def text_of(html):
    return re.sub(r"\s+", " ", TAGS.sub(" ", html)).strip()


def main():
    _site.require_site()
    admin = _site.Session()
    if not admin.login(_site.USER, _site.PASS):
        print("contracts: could not sign in as %s -- nothing was checked" % _site.USER)
        return 1

    code, body = admin.get("/admin/contracts")
    if code != 200:
        print("contracts: /admin/contracts returned %s" % code)
        return 1

    # The scope block is the proof the page rendered its audit at all. Without
    # it, "no findings" means the template changed, not that the site is well.
    if SCOPE not in body:
        print("contracts: the page rendered without its %r block. The markup "
              "changed and this check can no longer read it -- fix the check "
              "rather than trusting the silence." % SCOPE)
        return 1

    failed = False
    flat = text_of(body)

    if CLEAN not in body:
        m = re.search(r"(\d+) unfilled contract", flat)
        n = m.group(1) if m else "some"
        print("  UNFILLED: %s contract(s) with a missing half." % n)
        for f in FINDING.findall(body):
            print("     " + text_of(f)[:200])
        if not FINDING.search(body):
            i = flat.find("unfilled contract")
            print("     " + flat[max(0, i - 60):i + 400])
        print("     Each one is a feature that appears to work and does not.")
        failed = True

    if ORPHANS in body:
        i = body.find(ORPHANS)
        block = body[i:body.find("</ul>", i)]
        print("  ORPHANS: a plugin listens for an event nothing declares.")
        for name in re.findall(r"<code>([^<]+)</code>", block):
            print("     " + name)
        print("     A typo, or an emitter this site does not install. Both look "
              "exactly like an event that has not happened yet.")
        failed = True

    print()
    if failed:
        print("contracts: findings above, on a page that reports them to nobody.")
        return 1
    print("contracts: nothing unfilled, and no event listener without a declarer.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
