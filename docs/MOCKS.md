# Mock register

Every place this demo shows data it does not really have.

The site is a reference host for loon, and several UNIT3D parity pages exist
before the plugin that would back them. Rather than leave those pages out (and
lose the layout work) or fill them in silently (and lose the ability to tell
real from fake), each stub is listed here with the seam that would replace it.

**Rules for anything on this list:**

1. It is marked in the UI. Mocked panels carry `data-mock="1"` on their
   container and render a `MOCK` chip in the panel header. A reader can always
   tell. Never let a mock look like a measurement.
2. It is marked in the template, in a comment naming this file.
3. It is **inert**. A mock never writes, never affects points, entitlements or
   permissions, and never feeds another calculation.
4. It has a named replacement below. "Some plugin eventually" is not a plan.

**Not on this list, deliberately:** empty states, gradient poster fallbacks, and
the honest label swaps (`Top posters` rather than `Top contributors`). Those are
real behaviour for a site with no data yet, not fabrication.

---

## Register

| # | Where | What is mocked | Replaced by |
|---|---|---|---|
| ~~M1~~ | ~~`/u/<name>` — Activity~~ | ~~Last seen~~ | **RETIRED** — `users.last_seen_at`, written from the session resolve |
| M2 | `/u/<name>` — Achievements | The achievement list and progress | The `rewards` plugin (wired) once it exposes a per-user read; it currently only registers admin views |
| ~~M3~~ | ~~`/u/<name>` — Community~~ | ~~Follower / following~~ | **RETIRED** — `user_follow`, plus a Follow button |
| ~~M4~~ | ~~`/u/<name>` — Collection~~ | ~~Bookmark count~~ | **RETIRED** — `release_bookmark` is real; see below |

## Status of each

### M1 — Last seen — RETIRED

`users.last_seen_at`, written from the session resolve (presence_web.go) — the
one hook that runs for every authenticated request.

Throttled to one write per user per five minutes, in memory. Unthrottled it
would put an UPDATE on the users row in front of every page load and every
sub-resource, which is a lot of writes to answer "roughly when".

NOT a "who is online" list: that needs a presence window and an opinion about
what counts as online. This column answers the smaller question the profile
actually asks.

### M2 — Achievements

The `rewards` plugin IS wired and owns exactly this domain, but it registers
admin views plus a `rewards-claim` member widget — no "achievements for user X"
read. So the panel is a mock of a feature the site genuinely has, which makes it
the most likely of the four to become real.

**Split of work (agreed Aug 2026):** the plugin half belongs in `loon-plugins`
(the live-site repo); the page is host-side and waits on it. The seam is one
more extension in the shape rewards already uses for `rewards.trigger` /
`rewards.admin` / `rewards.validator`, so the host looks it up after Boot the
way it looks up `news.home`:

```go
// rewards.achievements — per-user read for the account Achievements page.
const AchievementsExtension = "rewards.achievements"

type AchievementsFunc func(ctx context.Context, userID int64) (Achievements, error)

type Achievements struct {
    Unlocked []Achievement // earned, newest first
    Pending  []Achievement // defined, not yet earned
}

type Achievement struct {
    Key, Name, Description string
    Progress, Target       int       // drives the pending progress bar
    EarnedAt               time.Time // zero while pending
}
```

Nothing else is needed from the plugin: Unlocked/Pending/Statistics are the
three tabs a live UNIT3D serves (§5e), and the Statistics tab is just
`len(Unlocked)` against `len(Unlocked)+len(Pending)` — the counts it shows are
"Unlocked Achievements: N / Locked Achievements: M" and nothing more.

### M3 — Followers / following — RETIRED

`user_follow (follower_id, followee_id)` plus a Follow button on the profile
(follows_web.go).

DIRECTIONAL, not mutual — following is a subscription, not a friendship, so
there is no request/accept flow and no status column. Reciprocity is a
coincidence, not a state. The pair is the primary key, so following twice is a
no-op in the database; a CHECK stops self-follows without any handler
remembering to.

### M4 — Bookmarks — RETIRED

Real as of Aug 2026: `release_bookmark` (bookmarks_web.go), a Save button on
the release page, `/bookmarks`, and a profile count that reads the table.

Kept on the HOST rather than in the usenet plugin, which this entry previously
suggested. It is a relation between a USER and a release id — the users are the
host's and the plugin owns neither side of it — which is exactly where
`release_grab` already lives.

The tile still degrades to an em dash when the table is unreachable, because
"nobody saved anything" and "cannot measure" are different claims.

---

## Related, but NOT mocks

Recorded here so nobody adds them to the list by mistake:

- **Cover art** — real, via TMDB. Renders a CSS gradient fallback when a
  release has no matched artwork, which is a genuine state, not a stand-in.
  See `docs/UNIT3D-PARITY.md` §5c.
- **Grab counts** — genuinely absent, and deliberately NOT mocked. Faking a
  download count would corrupt the trending and economy features that will read
  it later. It stays missing until `/nzb/:id` records one.
- **`users.reputation_tier`** — a column that exists ONLY so the communities
  plugin's `COALESCE(u.reputation_tier, 0)` join resolves. Nothing in this stack
  computes reputation, so it is always 0. Not a mock because nothing displays it
  as a measurement; if reputation ever becomes real it gets a plugin, not an
  UPDATE. Contrast `users.points`, which was deliberately made REAL rather than
  added as a zero column — a number on a page that is confidently wrong is worse
  than no number.
- **Ratio, buffer, seeding, peers** — n/a for a Usenet indexer. Not mocked, not
  planned, not missing. See the parity doc's n/a rule.
