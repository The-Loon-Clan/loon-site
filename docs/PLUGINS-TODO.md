# Plugins to create

What is wired, what exists but is not wired, and what does not exist at all.

The third list is the interesting one: UNIT3D feature areas with **no plugin
behind them anywhere in `loon-plugins`**. Those are the ones that would have to
be written.

---

## 1. Wired (18)

`backups` · `catalog` · `communities`¹ · `dailyreward` · `donations`² ·
`forum` · `messages` · `news` · `pointstore` · `ranks` · `rewards` ·
`scraper` · `stats` · `store` · `tickets` · `usenet` · `wiki`

¹ wired but BLOCKED at runtime — see `UNIT3D-PARITY.md` §5d
² dev-only, behind `LOON_DONATIONS=1`

## 2. Exists, not wired (4)

| plugin | why not |
|---|---|
| `anidbscraper` | 4 host interfaces (AnimeCatalog, NzbTagSink, TitleMatcher, CoverStore). Real work, narrow payoff — it enriches anime only. |
| `backup` | 7 seams (PGConn, ConfigStore, FreeDisk, DBSize, AssetClass, Root, DBDumpDir). Distinct from the wired `backups`. Ops surface, no parity value. |
| `dbmaint` | 7 seams (Diagnostics, StatCache, Nzbs, Maintenance, ConfigStore, FreeDisk, RepackConn). Same. |
| `economy` | **Domain mismatch, not missing data.** Its job is a per-grab UPLOADER bonus. Grabs are recorded now, but releases here come from crawling, so there is nobody to pay. Wiring it would credit no one. |

Also present and unexamined: `agent` (fleet dispatch surfaces — belongs to a
different product), `img`, `scripts` (not `core.Plugin`s).

---

## 3. Does not exist — would need writing

Ordered by parity value. View counts are UNIT3D's, as a size signal.

### 3.1 `requests` — 12 views · **largest gap**

A request board: members ask for a release, others fill it, bounties in points.
UNIT3D's second-biggest content area after torrents.

Everything it needs already exists here: points with a real ledger, an
entitlements baseline for gating, notifications, and the forum's thread/reply
shape to copy. **Needs:** `requests`, `request_fills`, `request_bounties`,
`request_claims`. Seams: BaseData, Markdown, Pagination, Points, Notify.

The one design question is what "filling" a request means for an indexer.
UNIT3D's filler uploads a torrent; here it would be pointing at an indexed
release, or naming one that should be crawled.

### 3.2 `mediahub` — 8 views

Browse by genre / network / company / person, and collection pages. **Blocked
on TMDB**: the `scraper`'s tmdb source is written but never runs without
`TMDB_API_KEY`, so there is no metadata to browse. Wire the key first — this is
the payoff for that work, not a separate feature.

`catalog_entry` already stores `Genres`, `Year` and `CoverURL`, so a good part
of the schema is in place.

### 3.3 `playlists` / `lists` — DUPLICATES, and `lists` is unwired

Resolved into one question rather than two entries. `lists` exists in
loon-plugins, is the richer surface (public lists, follows, copy, bulk-ZIP,
discovery grid) and is designed for this host — it owns no tables and takes
everything through `Deps`. It is **not wired**: zero routes at boot.
`playlists` is wired and is the narrower duplicate.

Options and their costs are in `docs/PAGES.md`. Needs a decision, not more
code: `lists` is maintained in the other repo.

### 3.4 `polls` — 4 views

Site polls with one vote per user. Genuinely small: `polls`, `poll_options`,
`poll_votes`. Worth doing mainly because UNIT3D puts a poll block on the home
page, which our block stack has a slot for.

### 3.5 `comments`

Not a UNIT3D "area" but a component used on torrents and articles. Would let
releases and news posts take replies. Depends on a BBCode or markdown input —
the markdown pipeline exists (`wikiMarkdown`), so this is mostly schema plus a
render partial.

### 3.6 `subtitles` — 2 views · `events` — 2 views

Both small, both niche. Subtitles needs a file store (exists). Events is a
seasonal-promo mechanic that only makes sense on a site with an economy people
care about.

### 3.7 Small features with no plugin, from the mock register

`docs/MOCKS.md` M1–M4 are all in this category and each is smaller than a
plugin:

- **last seen** — a `last_seen_at` column touched by the session middleware
- **achievements** — `rewards` owns the domain and is WIRED; it needs a
  per-user read, not a new plugin
- **followers / bookmarks** — one table and two routes each

---

## 4. Deliberately not on this list

- **Staff/admin (127 UNIT3D views).** Mostly framework surface, not plugins:
  loon's own admin, the view registry, and each plugin's `AdminView`. The
  reusable part is SHAPES — moderation queue, audit log, mass actions — not a
  plugin to write.
- **Anything resting on peers, ratio, seeding, upload credit.** n/a for a
  Usenet indexer. See the parity doc's n/a rule.
- **`trending` / `missing` as plugins.** Both are queries over data the host
  already has. Trending is a page over `release_grab` (which now exists);
  missing is a query over catalog coverage. Pages, not plugins.

---

## 5. On mock pages

The register (`docs/MOCKS.md`) has four entries, all component-sized, all with a
named replacement.

**Mocking a whole feature area would be a mistake**, and the rules in that file
say why: a mock has to be inert and clearly marked. A fake `requests` board with
fake bounties would be a large, convincing surface that does nothing — the
opposite of a demo that proves the framework works. The current mocks are four
tiles on one profile page, which is about the limit of what a reader can be
expected to keep straight.

If a feature is worth showing, it is worth the plugin. §3.3 and §3.4 are both
small enough to build outright.
