# Backlog

Work that is understood but not done. Each entry says what the problem IS, what
the evidence for it is, and what makes it worth doing — because "we should
probably look at X" ages into nothing, while "here is the query that proves it"
survives.

Ordered by what I would pick up first. Nothing here is blocking; the site is
healthy as of Aug 2026 (125 pages crawled, no 5xx, no truncated pages).

Everything here is about work already STARTED. Features the genre has that this
site has never had at all live in [FEATURE-GAPS.md](FEATURE-GAPS.md) instead —
two checklists, indexer and tracker — because "wrong" and "absent" are different
problems and mixing them buries the first.

---

## 1. Host↔plugin contracts fail silently — DONE

**The pattern, hit four times in one day:**

| Contract | What was missing | How it presented |
|---|---|---|
| `rewards.metrics.<key>` | host registered no counters | achievements stuck at 0 progress |
| `rewards.payout.achievement` | host registered no handler | claim button redirected, changed nothing |
| `Process == "worker"` gate | plugin never ran on `Process: "all"` | scoring, expiry and windows all absent |
| `user_display` view | communities joined `users` instead | 500 on `/c/<slug>` |

Every one degraded quietly, and every one looked like "nothing has happened
yet" rather than "this is broken". The plugin side is RIGHT to degrade — the
rewards migration says so explicitly, "a metric with no registered source is
INERT, never an error … so a half-configured site degrades rather than failing
to boot". The cost of that correctness lands on the host, which has no way to
discover what it has not wired.

**Built** — `contracts_web.go`, `/admin/contracts`, plus a WARN per finding at
boot. Two more instances turned up after this item was written, both in the
identity view: `user_display` stubbed `avatar_path` AND `reputation_tier` to
constants, so both were real, host-populated, and discarded on the way out to
every plugin. Six total.

It is DATA-DRIVEN rather than a list of the six: it reads the payout kinds
enabled rewards actually promise, the metrics enabled achievements actually
count, and the columns the identity view actually exposes, then checks the
other half is present. A hardcoded list goes stale the day somebody adds the
seventh.

It reports and does not fail boot. Most of these seams are legitimately
optional, and an operator who has deliberately not wired one should not have to
argue with the binary.

Verified by reintroducing each failure and watching it come back — a stubbed
view column, an achievement on an uncounted metric, a reward promising a
handler-less payout kind.

Not covered, and worth knowing:

* **The plugin side.** A plugin joining `users` instead of `user_display` is
  static analysis, not a runtime check. That instance would still get through.
* **Capabilities — now covered, by a different tool.** A seventh instance turned
  up: `events` was split out of `rewards`, rewards kept consuming
  `events.scheduled` through an optional lookup, and this host never imported
  the new plugin, so rewards ran without event gating. `contracts_web.go` could
  not have caught it — the registry knows a name is absent but not whether
  anybody wanted it, and "absent and unwanted" is the normal case. That half of
  the question lives in source, so `scripts/audit_capabilities.py` answers it
  statically: consumed by a wired plugin + provided only by an unwired one.
* **Process gates.** A plugin skipping itself because `Process` does not match
  is invisible from outside — it registers nothing and claims nothing.
* **Scope is the page.** `/admin/contracts` lists what it checks even when it
  finds nothing, because "no findings" from an audit whose scope nobody can see
  is indistinguishable from an audit that checks nothing.
* **CSS classes are the same bug.** `.button--danger` and `.text-danger` were
  used across three plugins' templates and defined in no stylesheet, so every
  destructive button rendered like a safe one. A sweep for classes referenced
  in templates but never defined is the same audit in another medium.
  `scripts/audit_css.py` is that sweep, and it has since caught two more —
  `.cart-pill` and `.contents__fact`, both shipped as hooks with no rule behind
  them. Note its scope: it reads HOST templates only, so a class used in a
  plugin's template and defined in the host's stylesheet is invisible to it in
  both directions. The cosmetics `fx-*` classes are exactly that shape, and
  they needed a test of their own (`TestEveryEffectHasCSS`) rather than this.

**Marked done 20 Aug 2026.** `/admin/contracts` and
`scripts/audit_capabilities.py` both exist and run; the entry described the fix
as built and then sat unticked for four days, which is its own small version of
the problem it is about. The three "not covered" items above are still not
covered and are the reason this is worth reading rather than deleting.

---

## 2. The rar-split root cause — FOUND, and the guess below was wrong

New splits stopped after the par fix (`6914cc5`, `de55b40`) and the 419
historical rows were deleted, so nothing is currently wrong. But the mechanism
for whole rar volumes each becoming their own release was never explained.

**What is known:** `parseSubject` derives the CORRECT base for those subjects —
`"Ratatouille…DVDR-COX.part001.rar" yEnc (81/137)` → `Ratatouille…DVDR-COX` —
so the split happens downstream of subject parsing. Each junk row was a
one-file NZB holding a SINGLE segment while claiming the whole volume's size,
and two rows thirteen days apart both held `part001.rar` with different segment
numbers. The staging key is `formatFieldKey(fileNum, partNum)`, and in the
single-file form `fileNum` is always 0 — so 42 volumes numbering their segments
1..137 compete for the same 137 keys.

**Why it still matters:** if it recurs there is nothing to start from but this
paragraph. The evidence is still in the staging tables today.

**Superseded by `docs/SUBJECT-PARSING.md`**, which measures this against ~10M
real staged articles rather than reasoning about it. The short version: the
mechanism below is real but is NOT what was doing the damage. The posts that
produced the junk releases carry TWO counters, and the parser reads the FILE
counter as the segment counter — 98.1% loss across 32,777 articles. A second and
larger problem turned up beside it: 1.7M obfuscated posts sharing
`base_subject = 'payload'`.

The paragraph below is kept because it is a correct description of a real (if
minor) collision, and because the difference between it and the measurement is
the point — it was written from reasoning, and reasoning got the cause wrong.

Confirmed by test, not inferred —
`loon-plugins/usenet/subject_rarsplit_test.go` (`ae87468`).

A multi-volume post with no `[i/j]` file counter shares ONE base subject across
every volume — correctly, because the volume number belongs to the filename and
not to the release — and with no file counter `fileNum` stays 0 for all of them.
So `formatFieldKey(0, 81)` is `"0:81"` for volume 1 and for volume 42 alike:
42 volumes numbering 1..137 compete for 137 fields, each overwriting the last,
and what survives per key is a single segment claiming a whole volume's size.
That is exactly the shape the junk rows had.

Two corrections to the note above:

* The staging tables were empty when this was written, and **they are not any
  more**: 4,674,261 staged articles came back with the database (see below).
  The pinned tests stay -- they are cheap and they fail loudly if somebody
  changes the parser -- but the original evidence is available again, so the
  mechanism can now be confirmed against real rows rather than synthesised ones.

  Worth recording why they were empty, because the note above was wrong about
  it. They were not lost to some earlier cleanup: a `docker compose down` run
  on 7 Aug at 20:05 recreated the database, because the `db` service had no
  NAMED volume and postgres's own anonymous one was orphaned by that command.
  56,331 releases, the 4.6M staged articles and the configured NNTP server went
  with it, and the absence was then written up here an hour later as a
  discovered fact. Recovered from the orphaned volume; `db` now has a named
  volume so the same command cannot do it again.
* **The par fix did not repair this.** It changed which subjects are split
  apart, not which ones collide; a `.partNN.par2` volume still parses to the
  same base and the same `fileNum` 0 as its `.rar` siblings. New splits stopping
  is consistent with the trigger going away, not the mechanism being fixed — so
  a recurrence is possible and would look identical.

**Not fixed, deliberately.** A fix means teaching `parseSubject` to read
`.partNNN` as a file counter, which changes staging for every multi-volume post
on a live indexer, and nothing is currently wrong. The tests pin the two facts
such a fix must change — shared base, colliding key — and fail loudly with an
instruction when it does.

---

## 3. Avatars are stubbed at every layer — DONE

`users.avatar_path` exists as a column, nothing reads it, and loon-baseline's
`user_display` view hardcodes `''::text AS avatar_path` — with a comment saying
it changes "when the corresponding facet packages land". So every avatar on the
site is an initials tile, and three layers each believe another one will supply
the picture.

**Resolved.** The diagnosis was half right: plugins *did* read the column (the
forum, communities, messages and chat all select it) — the view was throwing it
away, and `reputation_tier` with it. `migrateUserDisplay` replaces the view with
one that reads both; `avatar_web.go` adds upload, crop, re-encode and removal,
storing through the existing `blob.Store` mount; one `{{template "avatar"}}`
partial serves every render site in the host and the forum set.

Two things left behind it:

* **Moderation.** Anyone may upload anything. There is no report path for an
  avatar and no staff control to clear one — `clearAvatar` is wired only to the
  member's own settings page. On a site with open registration that is the gap
  that matters.
* **Orphan sweep.** Files are deleted on replace and on removal, but a delete
  that fails is only logged, and an account deleted directly in the database
  leaves its file behind. Nothing walks `avatars/` against `users.avatar_path`.

---

## 4. Committing the audit tools — DONE

Two sweeps live only in the session scratchpad, which was cleared once:

* **accessibility audit** — walks N pages checking one `h1`, heading order,
  named nav landmarks, named controls, `aria-current`, alt text, duplicate ids,
  unnamed tables. Found 2 pages with no `h1` (including home), 8 marking the
  current nav item to sighted readers only, and 9 unnamed tables.
* **link crawler** — follows every internal href signed in, reporting 404s,
  5xx, and 200s whose HTML stops before `</footer>` (the silent-truncation mode
  this codebase's own notes warn about). Found `/sitemap` 404ing behind two nav
  links, `/p/topics` and `/p/posts` linking `threadS/<id>`, `/u//followers` with
  an empty username, a 500 on `/c/usenet`, and one bad link of MY OWN an hour
  after I shipped it.

**Built** — `scripts/`, with a README explaining why each exists. The
accessibility audit had ALREADY been lost by the time this was picked up and was
rewritten from the description above; the crawler survived. `deploy.sh` runs all
three after a successful deploy, advisory, printing one summary line each.

A third was added: **`audit_css.py`**, which finds classes used in a template and
defined in no stylesheet — the contract audit (#1) in another medium. It found
`.button--primary` on the main action of FOURTEEN forms, defined nowhere, so
every Save, Upload and Create rendered identically to the Cancel beside it.

Fixed as a result: `.button--primary`, `.tag--danger`, `.tag--info`, `.chip--ok`,
`.chip--warn`, `.panelV2--overflow`; `/staff` had its `<h1>` only in the EMPTY
branch, so every real site rendered a page with no name; the shared prose editor
gave every textarea on the site no accessible name at all.

**Still open, deliberately not fixed here:**

* **23 undefined CSS classes remain**, mostly structural BEM names in plugin
  markup (`.blocks__*`, `.meta__line`, `.form__checkbox`). Each needs a design
  decision — define it or delete it — rather than an invented rule. This is the
  closest of the three to being gateable in CI once resolved.
* **12 a11y findings remain**, nearly all in plugin markup: unlabelled search
  and ticket fields (wiki, messages, tickets), `h2 -> h6` from dailyreward,
  `h1 -> h5` from store, two `<h1>`s on `/news` and `/wiki`, and unnamed tables
  on `/admin/jobs`. Host pages are otherwise clean.

The tools are advisory on purpose, for the same reason the contract audit does
not fail boot: a check that stops the build is a check people learn to skip.

---

## 5. Demo-data honesty — DONE

Achievements looked dead but were merely unseeded. Ranks was the same earlier;
so were store items and the flair shop. On a site whose PURPOSE is showing what
the framework does, "unseeded" and "broken" are indistinguishable to a visitor —
and both read as "this feature does not work".

Worth a deliberate pass over every wired plugin asking "does this show life on
first boot?", in the shape `forumSeed`/`achievementsSeed` already use: seed only
when the table is empty, so an operator's own data is never touched.

**Done** — `demoseed_web.go`. The pass found a clean line to draw, and drawing
it was the actual work:

* **Seeded** — the rank ladder (six groups across all three kinds), the points
  shop (three items, every one resolving through a reward type the store plugin
  really implements, against ranks that really exist), and two news posts. All
  of it is content an OPERATOR curates; an empty ranks table is not "no data
  yet", it is a feature nobody has set up.
* **Deliberately not seeded** — `usenet.nzbs`, playlists, bookmarks, grabs,
  tickets, DMs. Releases are the one thing the site is about, and inventing them
  makes every figure, listing and stat a fabrication. The rest either point at
  releases or are records of people talking to each other, and fabricating a
  support conversation puts words in a member's mouth.

The seeded news posts say outright that this is a demonstration and that the
index is empty because no news server is configured — a seeded announcement
written as though it were real is the same dishonesty from the other side.

Two schema facts the first attempt got wrong, both worth keeping:
`ranks.groups.color` is a Bootstrap NAME (the template renders
`class="badge bg-{{.Color}}"`, so a hex string produces no colour at all), and
`duration_days` is NOT NULL with `CHECK (>= 1)` — there is no way to say
"permanent", and the admin form clamps anything lower to 30.

---

## 6. Workflow: the shared working tree — WRITTEN UP, decision open

Three outages in one day, all the same shape — a change needing both `loon/core`
and a plugin, with one half committed and the other sitting uncommitted in a
tree two agents share. Once, an uncommitted fix of mine was discarded by the
other agent's recovery and the site stopped compiling.

`deploy.sh` now catches the symptom, and committing promptly helps. Separate
worktrees per agent would remove the cause. This is a workflow call, not a code
one.

**Written up in `docs/SHARED-TREE.md`** with the three options and their costs.
One thing was code and is done: `deploy.sh` now names the CAUSE as well as the
symptom, listing any uncommitted files in the three shared checkouts before each
successful deploy — because the build reads working trees, not commits, so those
files are exactly what just went into the image. Advisory, never blocking.

It caught a real case on its first run: `loon-plugins` had two untracked paths
from the other agent, both baked into that image.

The decision itself is open and is the user's. The recommendation is a worktree
per agent for `loon` and `loon-plugins` only — `loon-baseline` has never been
the site of an incident.

---

## 7. Dependency PRs — the module bump is IN, five action bumps remain

**The Go module bump landed on 22 Aug 2026** in `3030ef5`, not by merging PR
#2 but by applying its bumps to the current tree — its own CI had run against a
base 74 commits old, and a green workflow on a stale base is not the same
question as "does this work here". Dependabot closes the PR as superseded.

| bump | |
| --- | --- |
| sqlx 1.3.5 → 1.4.0 | the storage layer is built on it |
| go-redis 9.21 → 9.22 | |
| x/crypto 0.51 → 0.55 | |
| x/net 0.55 → **0.57** | this entry said 0.56; dependabot updated the PR since |

Verified beyond the build: full suite passes, the site boots, `/`, `/browse`,
`/login` and `/api?t=caps` answer 200 with a clean log, and links, contracts,
adminnav and access all pass against it. `go.sum` lost 162 lines — sqlx 1.4.0
drops transitive drivers this site never used.

**Five action bumps are still open** (#1, #3, #4, #5, #6): checkout 4→7,
upload-artifact 4→7, setup-buildx 3→4, setup-qemu 3→4, build-push 6→7. All five
are pure `uses:` version changes in workflow YAML — the diffs contain nothing
else — and all five still merge cleanly despite being 238 commits behind.

They are left open deliberately rather than for want of attention: a MAJOR
action bump only proves out when the workflow runs, and nothing local can
exercise a GitHub Action. Merging them is a decision to find out on the next
push, which is the maintainer's to make. The cost of leaving them is the one
this entry always recorded — they rot, and a stale dependency PR gets closed
rather than reviewed.

---

## 8. Requests and bounties

**The largest feature gap against UNIT3D**, which has `Request`, `RequestFill`,
`ApprovedRequestFill`, `Bounty`, `Claim` and `ClaimedPrize` controllers.

A member asks for content nobody has indexed, other members put points behind
the asking, and whoever fills it takes the pot. It is the social loop that makes
a points economy mean something — without it points accumulate and buy nothing
except the store's cosmetics.

**Most of what it needs already exists.** The points ledger, the transfer
mechanics (`storage.TransferPoints`, checked inside its transaction), gifting,
and the wishlist — which is the single-member version of the same idea and would
likely be subsumed by it.

**What is genuinely new:** a request row with a bounty pot, contributions from
several members, a fill claim pointing at a release, and staff approval before
the pot pays out. The approval step is the part worth designing carefully — an
unapproved auto-payout on "somebody said they filled it" is the obvious abuse.

**Why it is here rather than done:** it is a feature, and the session it came up
in was about engineering quality. It should be picked up deliberately rather
than squeezed in.

---

## Smaller, known, not urgent

* The profile's plugin "Profile" widget shows Role and Joined, which the
  sidebar now also shows. Plugin-owned markup.
* Heading-level skips and three unlabelled inputs remain in plugin markup
  (dailyreward, store, account) — Bootstrap-era `h5`/`h6` and unlabelled fields.
  Host pages are clean.
* `/admin/jobs/config` shows as a 404 in the crawler. That is the CRAWLER: it
  strips query strings and the real link carries `?name=`. Recorded so nobody
  chases it twice.

## 9. Mojibake in release titles (2,008 rows) — DONE

Fixed 22 Aug 2026. Both halves, in the order this entry insisted on.

**The decode**, in loon-plugins `c5c216a`. The cause was narrower than "a
double decode at ingest": an encoded-word that LIED about its charset.
`=?ISO-8859-1?Q?Espa=C3=B1ol?=` carries UTF-8 under a Latin-1 label, and
`mime.WordDecoder` does what it was told — widens each byte to the code point
of the same value. A right single quote, E2 80 99, becomes `â` + U+0080 +
U+0099: the two C1 controls the W3C run found. `decodeSubject` now reverses
that when, and only when, every rune fits in a byte, the narrowed bytes are
valid UTF-8, and the result differs — which leaves an honest UTF-8 subject and
an honest Latin-1 subject both untouched, verified by test rather than argued.
Only what that path decoded: a raw subject is the poster's own bytes.

**The stored rows**, in `scripts/repair_mojibake.sql`, run once. It was not one
column. The entry said 2,008 titles, and that was true and not the same as
finished:

| column | rows | |
|---|---|---|
| `usenet.nzbs.title` | 2,008 | |
| `usenet.nzbs.filename` | 2,001 | taken from the title |
| `usenet.nzbs.series_name` | 245 | **what /series GROUPS BY** — mojibake here splits one show into two |
| `usenet.set_resolutions.base_subject` | 719 | diagnostic samples |
| `usenet.subject_corpus.subject` | 138 | diagnostic samples |

All five are 0 now, and the script is idempotent: re-running it reported
`UPDATE 0` for the column already done.

**What could not be fixed.** Ten titles had lost the LEAD byte of a two-byte
sequence before they were stored — all ten Bleach episodes where `ō` is an `o`
followed by an orphan U+008D. The character is gone and no reversal invents it
back, so the orphan is stripped: `Tosen`, `Kyoraku`, `Zanpakuto`. Ordinary
macron-less romanisation, and valid HTML. Recorded here rather than hidden by
the count reaching zero.

The render-time strip this entry suggested as a floor was not needed and would
have been the wrong answer anyway, for the reason given: a stripped title still
reads as mojibake to the member, just legally.

## 10. loon-baseline does not trim passwords — DONE

Closed 22 Aug 2026 in loon-baseline `13487b3`. Register, Authenticate and
ChangePassword all normalise through `authflow.NormalisePassword`.

Fixed in ONE place rather than the two this entry suggested, because trimming
only where the hash is STORED makes it worse: the verify would then compare a
raw string against a trimmed hash, and lock out anybody who habitually types
the padding.

Two things this entry did not have:

- **A legacy padded account** would have been locked out by the fix meant to
  prevent lockouts — its hash was made from the padded string. Authenticate
  tries the normalised form, then the raw one if they differ, and rehashes
  from the normalised form on success: the account opens, migrates on first
  login, and the fallback is needed once.
- **The length check counted the padding.** `len()` on the raw string meant an
  eight-character policy accepted `"  abcd  "` — four real characters, and a
  password weaker than the rule it passed.

`NormalisePassword` is exported because a host has to ask the same question:
`account`'s form compared a new password against its confirmation RAW, so
`"abc"` and `"abc "` were reported as not matching when they are about to be
the same password. loon-site can drop its own trim onto this rather than
keeping a second answer.

Four tests, run against the unfixed code first — all four fail there.

---

## 11. Flair store takes USD/crypto (design done, implementation in loon-plugins)

Design: [FLAIR-PAYMENTS.md](FLAIR-PAYMENTS.md).

Three surfaces, two currencies, one money rail. Donate and a USD store are the
SAME mechanism — both raise an invoice, both wait for the same HMAC-verified
webhook, both grant on settlement, and the only difference is what the
settlement handler does with the money. A donation is a purchase whose item is
points. The points store is untouched and keeps ranks and invites.

The work is in `loon-plugins`, which is shared and runs a live site:

- `pluginapi/checkout.go` — a new `Checkout` capability, additive, breaks
  nothing. Third of its kind after `RankGranter` and `InviteGranter`, and
  `RankGranter`'s own doc already anticipates external money as a payment path.
- `donations` — publish it. All the machinery exists; this exposes it.
- `pointstore` — consume it, price flair in fiat, declare the dependency in
  `Metadata.Requires` so the shop degrades to unavailable when donations is off
  (which is this host's default, behind `LOON_DONATIONS`).

Four properties to test, each with a specific failure behind it: grant on
settlement and never on checkout; idempotent per `Ref`, because webhooks retry;
verify the AMOUNT against the price recorded at invoice time, since crypto
underpayment is normal; and degrade when the rail is absent rather than
rendering as free.

The host needs no change, which is the test of whether the seam is in the right
place.

## 12. Two "Site stats" pages, and one of them is an estimate — DONE

Closed 22 Aug 2026 in loon-plugins `9412cbc`. Both pages read 160,673, which
is `COUNT(*)` on the table.

This entry offered two fixes and recommended the cheap one: relabel the metric
"NZBs indexed (approx.)". The other one was taken instead, because the reason
the estimate was there did not apply to the caller that was publishing it.

`statsTotals` reads `pg_class.reltuples` and is right to: it answers a
5-second liveness poll, where an exact count is a table scan on every tick,
and its own comment records the 2026-07-24 production timeout that shape
caused at 33M staged rows.

But the SNAPSHOT is a different question asked by a different caller.
`stats/plugin.go` collects it from a background job on a one-hour interval,
and the page says "refreshed hourly" on its face. A scan an hour is affordable
where a scan every five seconds is not, so the hook uses `statsTotalsExact`
and the two numbers agree rather than one being labelled.

`Active newsgroups` was already exact — `COUNT(*)` on newsgroups — so only the
two row counts changed.

---

## 13. Plugin CSS ships as `<style>` inside the page body

`vnu` on `/news`:

    Element "style" not allowed as child of element "div" in this context.

A plugin renders a fragment, the host wraps it in chrome, and any CSS the
fragment needs travels with it — inside a `<div>`, where `<style>` is metadata
content that does not belong. Every browser honours it, so nothing looks broken;
the page is simply not valid HTML.

Not one template's problem. **37 plugin templates and 3 host templates** carry an
in-body `<style>` block today, so `make html` reports it on most plugin pages.

It is also the other half of the `'unsafe-inline'` concession in the CSP (item
in `docs/QUALITY.md`): the host cannot drop `style-src 'unsafe-inline'` while
every plugin page needs it.

The fix is a seam, not an edit. Something like a `Deps.RegisterCSS(name, css)`
the host serves from `/static/plugin/<name>.css` and links in `<head>` — one
request per plugin, cacheable, valid, and nonce-free. Until then a plugin that
wants styling has no other way to ask for it, which is why this is a design task
rather than a cleanup.

Found while cleaning up the news feed, which is also where the shape of the
problem is clearest: 90 lines of CSS in a template, re-sent on every page view,
duplicated across the plugin's four pages.

## 14. Spotnet import (spike done, design in SPOTNET.md)

Design and live findings: [SPOTNET.md](SPOTNET.md).

Spotnet is a decentralised index carried on Usenet itself: a human spots an
upload and posts a signed description to `free.pt`, and every client builds its
own copy of the index by reading the group. The provider this host is configured
against carries it — 4.4M articles, live.

It fits better than it looks. A spot points at a finished NZB in
`alt.binaries.ftd`, and `usenet.nzbs.nzb_data` stores NZBs inline, so an
importer skips the expensive half of the existing pipeline: no article table, no
set resolution, no build outcomes. `loon/nntp` already has the transport.

Blocked on nothing, but two decisions want making first — provenance (nzbs.source
is the MEDIA source, so a spotted release has nowhere to say it was spotted) and
moderation (regex-over-titles here vs key-and-reports there). One unknown needs
Spotweb's source rather than a guess: which bytes the signature covers. That is
the only part where being wrong is dangerous rather than broken — a verifier
that accepts everything looks exactly like one that works, in front of a group
anyone can post to.

## 15. A donation tier with no stock LIMIT reads as having no stock LEFT

`DonationPackageView.Recompute` (loon-plugins/donations/models.go):

    v.StockRemaining = v.StockTotal - stockUsed
    v.Funded         = v.StockRemaining == 0

With `stock_total = 0` — which is what an admin sets for an unlimited tier, and
what the column defaults to — that is `0 - 0 = 0`, so the tier is Funded before
anybody has claimed it. The handler then files it under FundedPackages and the
public page shows it as taken.

Two lines further down the same function guards the other derived field:

    if v.StockTotal > 0 { … PercentRound … }

so the zero case was clearly considered for the percentage and not for the
flag. That asymmetry is why this reads as an oversight rather than a decision.

Found by configuring three tiers and seeing two.

NOT fixed here, deliberately. It is a change to what a money-facing flag means
in a plugin shared with a live site, and the fix depends on an answer this host
cannot give: does `stock_total = 0` mean "unlimited" or "none"? If unlimited,
Funded should be `StockTotal > 0 && StockRemaining == 0`. If none, the admin
form should refuse 0 rather than silently creating a tier nobody can buy.

The host side IS fixed: help_donate.html renders FundedPackages as well as
Packages, so a genuinely sold-out tier shows as claimed instead of vanishing.

---

## 16. Everything built this week is verified by hand, not by CI

**Mostly paid off, 20 Aug 2026.** The plugin half is done; what remains is
recorded at the bottom.

**The evidence, as it stood:** `loon-plugins/GRADES.md` carried a section
listing ten plugins missing from its grading table. Three of them —
`cosmetics`, `polls`, `applications` — had **no test file at all**, and this
host was the same story in the other direction: the cart, the derived contents
panel and the image intake all landed with tests, and the cosmetics slots, the
poll widget and the comments thanks toggle did not.

**Why it mattered more than a coverage number.** Hand-verification against a
running site is genuinely good — it caught things no unit test would have, three
CSS faults among them, each of which had its class applied, its rule matching
and its test passing. What it does not do is survive the next refactor, run in
CI, or run at all for a contributor who has not got a database and two seeded
accounts.

**What was written.** Every plugin package that had no test file now has one:
`cosmetics`, `polls`, `applications`, and then the rest of the sweep —
`stats`, `backups`, `playlists`, `scraper/sources/theporndb`, and
`scripts/lint-sql`. Only `anidbscraper` is still bare, deliberately: its bodies
are stubs awaiting extraction, so a test would pin a placeholder.

The tests were aimed at decisions rather than lines:

- `cosmetics.cleanTitle` — the eleven bidi overrides each get a case, because
  those characters reorder what is drawn *around* them, which on this site is
  somebody else's username.
- `applications.submit` — the enumeration rule: a known address and an unknown
  one must answer identically, and the only case that differs is a duplicate,
  which reveals an application to the person who made it.
- `polls.showResults` — the three-policy × voted × closed matrix, including
  that an unrecognised policy falls back to *withholding* rather than to
  publishing.
- `stats` and `backups` — "every early return after `SetRunning` must end the
  run". Five plugins state that rule in a comment and nothing enforced it.

**Two faults the tests found**, which is the part worth keeping:

- `playlists.owned()` compared the stored owner against a viewer id that is
  **0 for anonymous**, so a `user_id = 0` row would have been owned by every
  signed-out visitor. Unreachable today — every write route sits behind
  `RequireUser` — but a check that claims to stand on its own was leaning on
  the middleware.
- `lint-sql`, the guard that keeps SQL in that repo constant-only, could be
  stepped around by **naming your string**: it skips bare identifier arguments,
  so `q := fmt.Sprintf(...)` then `tx.ExecContext(ctx, q)` passed in silence.
  Now closed, narrowed to strings that are both built dynamically and open with
  a SQL verb so a Sprint'ed value bound as `$1` is not mistaken for a query.

**The store-level rules are done too (20 Aug).** The line above this one used
to say they needed "a test harness this repo does not have". That was wrong.
`loon-plugins` already held **thirty-one** integration tests — and every one of
them read its own environment variable (`ACHIEVEMENTS_TEST_DSN`,
`NEWS_TEST_DSN`, `RANKS_TEST_DSN`, eight more), not one of which was set by
anything. They all skipped, on every run, and the suite reported green. A
harness that nothing invokes is indistinguishable from no harness, which is how
it came to be described as absent.

So: one `pluginapi/pgtest` (scratch schema, the plugin's own embedded
migrations, one variable — `LOON_TEST_DSN`, the same name this repo's
`make itest` already exported), `scripts/itest.sh` + a Makefile in
`loon-plugins`, and a CI job with a Postgres service. `itest.sh` also exports
the ten legacy names, so the thirty-one existing tests run now without editing
thirty-one files that several people own. 61 packages, green.

Written on it: `cosmetics.Equip` (ownership and expiry, both of which live
inside the statement), the `comments` delete/edit rules, and `mediainfo`'s
`SummariesFor` / `RemoveReport`.

**The `0` sentinel sweep (20 Aug).** Three of these turned up by accident in
two days, so the fourth was looked for deliberately — and the framing was
wrong in a way worth recording. "The routes are gated" was the reason each one
was called defence-in-depth; the routes are behind `Authenticate()`, which
**lets anonymous requests through in the site's public access mode**. So the
comparisons really are reached with a viewer id of 0.

What holds them up instead is that no row is owned by 0 — measured, not
assumed: 63 identity columns across every schema in the live database, and the
only 0 is a `login_logs` row for a failed sign-in, which is correct. Nothing
enforces it.

`pluginapi.OwnedBy` / `VisibleTo` now state the rule once, applied at every
site that decides whether to show something private, and
`scripts/audit-sentinels` (baselined, in `make check` and CI) finds new ones.
Ten baselined with reasons: seven in-memory store doubles, and three in
`requests/`, which are the same shape and are left for that workstream —
one line each, and the baseline says which line.

**And a third fault, found the moment it ran.** `comments.Delete` expressed
"staff" by passing **0** in place of the caller's id, with the clause reading
`($3 = 0 OR user_id = $3)` — so the sentinel meaning *staff* and the id meaning
*nobody is signed in* were the same value, and a non-staff call with user id 0
removed anybody's comment. The routes were the only thing preventing it. It is
now a boolean parameter, matching `mediainfo.RemoveReport`, which had it right.

**What is still open.** This host's own suite is in decent shape — 59 test
files under `web/handlers`, and only `cmd/loonsite` (a main) and
`plugins/guestbook` (the demo plugin) have none. Two things remain in the
plugins repo: the `store` adoption-importer tests skip because the SQL file
they read (`deploy/import/store_from_profile.sql`) was never carried over from
the indexer repo this plugin came from, and `anidbscraper` still has no tests
because its bodies are stubs.

---

## 17. A cart can hold release ids that resolve to nothing

**What happens:** `/cart/add` takes ids from a POST and stores them without
asking the index whether they exist. A made-up id is stored, never renders, and
the cart page reports "N are no longer on the index" — which is the correct
message for a release that aged out and a misleading one for an id nobody ever
had.

**Why it was left:** checking costs a lookup per id on a path whose whole point
is arriving with fifty of them, and `pluginapi.UsenetIndex` has no batch
existence call. The bound is on the cart's TOTAL instead (`cartCap`, 500), so
the abuse is capped rather than prevented.

**What would fix it:** a batch `ExistingIDs(ctx, []int64) []int64` on the index
contract. One query, and the cart could then refuse silently rather than store
and explain.

---

## 18. `mediainfo.SummariesFor` exists and nothing consumes it

The mediainfo plugin can answer "HEVC at 10.4 Mb/s · E-AC-3 JOC 6 channels" for
a batch of releases, which is exactly the line the **series page** wants — six
copies of one episode, and currently only filename tags to choose between them.
That page is named in FEATURE-GAPS as the reason the feature was worth building,
and it is the one page that still cannot show it.

It is a store method with no contract, deliberately: inventing one before there
is a second side is how SEAMS.md's bare-string tier grows. The consumer comes
first, then the contract.

---
