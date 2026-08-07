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

**Worth building:** a boot-time report of declared-but-unfilled contracts. The
pieces exist — `core.ExtensionNames()`, `rewards.StockSources()`, the
`PayoutKind` list — so this is assembly, not invention. One log line, or a panel
on `/admin/plugins`, reading:

    declared but unfilled: rewards.metrics.uploads.created, rewards.payout.medal

Four debugging sessions would have been four glances.

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

## 3. Avatars are stubbed at every layer

`users.avatar_path` exists as a column, nothing reads it, and loon-baseline's
`user_display` view hardcodes `''::text AS avatar_path` — with a comment saying
it changes "when the corresponding facet packages land". So every avatar on the
site is an initials tile, and three layers each believe another one will supply
the picture.

Gravatar-by-email-hash is a cheap real step and needs no storage decision.
Uploads need one (local disk vs object store) and a moderation answer.

---

## 4. Committing the audit tools

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

Both belong next to `shellcheck/`, and `deploy.sh` could run them.

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
