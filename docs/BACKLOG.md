# Backlog

Work that is understood but not done. Each entry says what the problem IS, what
the evidence for it is, and what makes it worth doing — because "we should
probably look at X" ages into nothing, while "here is the query that proves it"
survives.

Ordered by what I would pick up first. Nothing here is blocking; the site is
healthy as of Aug 2026 (125 pages crawled, no 5xx, no truncated pages).

---

## 1. Host↔plugin contracts fail silently

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

## 7. Dependency PRs waiting on a decision

**Six Dependabot PRs are open** and have been since automation was switched on
(Aug 2026). All six are green on `ci` and `codeql`.

| PR | bump |
| --- | --- |
| #2 | Go modules, grouped: sqlx 1.3.5→1.4.0, go-redis 9.21→9.22, x/crypto 0.51→0.54, x/net 0.55→0.56 |
| #1, #3, #4, #5, #6 | actions: checkout 4→7, upload-artifact 4→7, setup-buildx 3→4, setup-qemu 3→4, build-push 6→7 |

**#2 was verified beyond its CI run**, because sqlx is what the whole storage
layer is built on and a green workflow is not the same as a working site: the
branch was checked out, run against a real Postgres and Redis (263 tests pass),
and booted — `/`, `/browse`, `/login` and `/api?t=caps` all answer, no errors.

**Why it is here rather than done:** merging is the maintainer's call. The cost
of leaving them is that they rot — five action bumps and a grouped module bump
will start conflicting with each other and with the next week's PRs, and a
stale dependency PR gets closed rather than reviewed.

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

## 9. Mojibake in release titles (2,008 rows)

Found by the first W3C validation run — see docs/QUALITY.md.

2,008 rows in `usenet.nzbs` have titles containing C1 control characters
(the U+0080..U+009F range), which are forbidden in HTML and meaningless as
text. The cause is a double decode at ingest: UTF-8 bytes read as Latin-1 and
re-encoded, so a single mathematical operator becomes a six-character run of
accented Latin.

Count them with a regex over that range against `usenet.nzbs.title`.

Two halves, owned by different repositories:

- **The decode**, in the usenet plugin's subject handling. That is where new
  rows keep being written wrong, so nothing else matters until it is fixed.
- **The stored rows**, which need a one-off repair once the decode is right.
  Repairing first just re-breaks them on the next crawl.

The host could also strip C1 controls at render, which fixes the invalid HTML
without fixing the titles. Worth doing as a floor, not as the answer: a
stripped title still reads as mojibake to the member, just legally.

## 10. loon-baseline does not trim passwords

loon-site now trims leading/trailing whitespace from every password it accepts
— register, login, reset — so a stray space can never lock somebody out. See
`web/handlers/inputs.go`.

`loon-baseline` does not. `authflow` trims the username (authflow.go:47, :76)
and passes the password through untouched, and the `account` package's
change-password form does the same.

The gap that leaves: a member who changes their password through
`/p/account` **with** leading or trailing whitespace stores it padded, and
loon-site's login — which now trims — will never match it. That is a lockout
this repository created and cannot close on its own.

Unlikely (it needs somebody to deliberately pad a password on one specific
form) and worth closing anyway, because the fix is the same one line in
`authflow.ChangePassword` and in `account`'s handler, and because "trimmed
here, raw there" is exactly the inconsistency that made the original bug
invisible.

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

## 12. Two "Site stats" pages, and one of them is an estimate

Both are now in Other (`/stats` "Stats", `/p/stats` "Site snapshot") — see
`navPlacement` in admin_views.go. The placement is fixed; the numbers are not.

They disagree, and a member has no way to tell which to believe:

    host   /stats     Releases      160,692    <- exact, matches the table
    plugin /p/stats   NZBs indexed  160,980    <- 288 high

Neither is wrong. The host runs `COUNT(*)`. The plugin's figure comes from the
usenet plugin's `est()` (usenet/store.go), which reads `pg_class.reltuples` — a
PLANNER ESTIMATE — and only falls back to a bounded count when that is
unusable. That is a sound choice for a 5-second liveness poll on a table with
millions of rows; an exact count there would be a scan on every tick.

The problem is presentational: both pages are headed "Site stats" and print
their number the same way, so an estimate is displayed as a fact beside a fact.
Both figures are also plausible, which is worse than one being obviously
broken.

Fix, in the shared tree:

- `usenet/service.go:61` labels the metric "NZBs indexed". Saying "NZBs indexed
  (approx.)" costs one word and removes the whole confusion.
- Consider whether the snapshot page should show the exact count instead. It
  refreshes hourly, not every five seconds, so it can afford one.

Neither is the host's to change. Recorded here rather than worked around,
because the host relabelling its own page would not help — the misleading
number is on the other one.

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
