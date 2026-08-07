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

## 2. The rar-split root cause was never found

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

## 5. Demo-data honesty

Achievements looked dead but were merely unseeded. Ranks was the same earlier;
so were store items and the flair shop. On a site whose PURPOSE is showing what
the framework does, "unseeded" and "broken" are indistinguishable to a visitor —
and both read as "this feature does not work".

Worth a deliberate pass over every wired plugin asking "does this show life on
first boot?", in the shape `forumSeed`/`achievementsSeed` already use: seed only
when the table is empty, so an operator's own data is never touched.

---

## 6. Workflow: the shared working tree

Three outages in one day, all the same shape — a change needing both `loon/core`
and a plugin, with one half committed and the other sitting uncommitted in a
tree two agents share. Once, an uncommitted fix of mine was discarded by the
other agent's recovery and the site stopped compiling.

`deploy.sh` now catches the symptom, and committing promptly helps. Separate
worktrees per agent would remove the cause. This is a workflow call, not a code
one.

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
