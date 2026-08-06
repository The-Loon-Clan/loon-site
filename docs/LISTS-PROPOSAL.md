# Proposal: `lists` as the one collection surface

A request to `loon-plugins/lists`, written from the host side. The goal is
**less code in loon-demo-site**: every list-shaped page should be the plugin's,
with the host contributing data, not markup and not a second implementation.

Today that goal is inverted. `lists` owns no tables and runs no migrations, so
wiring it costs the host **three tables, their migrations and 26 `Deps`
functions — 15 of them persistence**. `playlists`, which duplicates it, costs
70 lines because it owns its own schema. Reusing the better plugin currently
means writing more host code, not less.

Two changes fix that, and a third unlocks the wider idea.

---

## 1. Own the persistence (the blocker)

`lists` should self-migrate the way `playlists`, `store` and `catalog` already
do in this same repo:

```go
//go:embed migrations/*.sql
var listMigrations embed.FS

func (p *Plugin) Metadata() core.Metadata {
    return core.Metadata{Name: "lists", Migrations: listMigrations, …}
}

func (p *Plugin) Provision(c *core.Core) error {
    db := c.Storage.SchemaDB("lists")   // unqualified names resolve in "lists"
    …
}
```

That deletes 15 `Deps` functions and the host's three tables at a stroke. What
should stay in `Deps` is what the plugin genuinely cannot know: `RenderPage`,
`RenderError`, `Viewer`, `DownloadAllowed` (host IP policy), `Gunzip`,
`NzbData`, `NzbCardCSS`, `ReportModal`, the JSON helpers, `NotifyFollow`.

The README's reason for host-owned tables — *"the account Following tab and the
release-page widgets read them too"* — is right for a host that already has
those surfaces. This one does not, and a plugin that owns its schema can still
expose reads for them later through an extension, the way `news` publishes
`news.home`.

## 2. Types, and a system owner

Enough to carry the pages below:

```sql
CREATE TABLE list (
    id          BIGSERIAL PRIMARY KEY,
    kind        TEXT   NOT NULL DEFAULT 'collection',  -- see below
    owner_id    BIGINT,                                -- NULL = system-owned
    slug        TEXT   NOT NULL,
    name        TEXT   NOT NULL,
    description TEXT   NOT NULL DEFAULT '',
    public      BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_id, slug)
);

CREATE TABLE list_item (
    list_id    BIGINT NOT NULL REFERENCES list(id) ON DELETE CASCADE,
    release_id BIGINT NOT NULL,
    position   INT,
    added_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (list_id, release_id)
);

CREATE TABLE list_follow (
    list_id BIGINT NOT NULL REFERENCES list(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (list_id, user_id)
);
```

`owner_id NULL` is the system owner. `kind` is what lets one page render a
member's collection and a site-wide list without the template asking which.

## 3. Derived lists need no rows — `Items` is already a function

This is the part worth not over-building. `Deps.Items(ctx, listID) []Item` is a
FUNCTION, so a list whose contents are a query needs no `list_item` rows at
all. A "virtual" list is a `list` row with a `kind` the host resolves itself.

That matters because **curated and derived lists fail differently**:

- **Curated** — someone inserted the rows. The table is the truth.
- **Derived** — a query over other data. Materialising it into rows means a job
  to refresh them and a window where the page is confidently wrong.

`/trending` is derived: it is `GROUP BY release_id ORDER BY count DESC` over
`release_grab`, always current, no job. Storing it as list rows would trade a
correct page for a stale one to gain uniformity. Keep the query, render it
through the list page.

---

## The pages, assessed

| Idea | Fits? | Why |
|---|---|---|
| **Collections** (member watchlists) | **Yes — this is the plugin** | Curated references, an owner, sharing. Retires this repo's `playlists`. |
| **Bookmarks** | **Yes** | A list of one implicit `kind='saved'` list per member. Retires ~175 host lines and MOCKS M4 stays retired. |
| **Grab list** ("what I downloaded") | **Yes, derived** | `release_grab` is ALREADY `(user_id, release_id, created_at)` — a list-entry table in all but name. No new schema; `Items` runs the query. |
| **Trending** | **Yes, derived + system-owned** | `owner_id IS NULL`, `kind='trending'`, items from the grab query. Do NOT materialise. |
| **Uploaded list** | **n/a here** | This is an indexer: releases are crawled, not uploaded. There is no uploader to attribute. Real on a tracker, empty here. |
| **Friends / blocked** | **No** | Graph edges, not ordered collections — directional, sometimes mutual, and blocking changes DM delivery rather than displaying anything. `dm_blocks` already exists and does its job. Agreed with your read. |
| **News** | **No** | `news_posts` carries `body`. A list holds REFERENCES; news rows hold CONTENT, with an editor, slugs and RSS over them. Folding it in means the list row is the post, at which point it is a CMS wearing a list's name. |

The line that holds: **a list points at things; it does not contain them.**
Everything above the line is a set of release ids with an owner. Everything
below either has no ordering (the social graph) or has a body (news).

---

## What the host does after this

- Wire `lists` (~70 lines, the shape `playlists` uses today).
- Delete `playlists` and its wiring; redirect `/playlists*` → `/lists*`.
- Delete `bookmarks_web.go`; `/bookmarks` becomes the `kind='saved'` list, and
  the release page's Save button becomes the plugin's add/remove.
- Keep `/trending` as a host route if simpler, or hand its query to the plugin
  as a derived system list — the page is the same either way.

Net: roughly **245 lines of host code removed**, one collection surface instead
of three, and `/trending` gains an owner without gaining a staleness bug.
