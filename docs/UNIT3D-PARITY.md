# UNIT3D parity list

A working inventory of UNIT3D's UI surface against this demo, so porting is a
worklist rather than a memory exercise. Source of truth is the checkout at
`C:/GitHub/Indexer/example/UNIT3D-master` — **read it before building anything
here**, because the whole point of mirroring its class names is that its markup
can be lifted with classes intact.

Scale, for calibration:

| | UNIT3D | this demo |
|---|---|---|
| Blade / Go templates | 446 | 18 |
| of which staff/admin | 127 | 4 |
| Livewire components | 75 classes + 74 views | n/a (no JS framework) |
| Sass partials / CSS files | ~100 | 5 + 3 themes |

## How to read the status column

- **have** — built and rendering here
- **mock** — the page exists, the data does not. Every one is registered in
  `docs/MOCKS.md` with the seam that replaces it, marked `data-mock="1"` in the
  markup and chipped `MOCK` in the UI
- **partial** — exists but thinner than UNIT3D's
- **todo** — applies to us, not built yet
- **n/a** — no analogue in a Usenet indexer, do not build

The **n/a** rule matters. UNIT3D is a BitTorrent tracker. This is a Usenet
indexer. There are no peers, no seeding, no ratio, no announce, no upload/
download totals, and no grab counts anywhere in the stack. Anything resting on
those is n/a, not "todo later". Where a tracker concept has an honest indexer
analogue it is marked **partial** with the mapping spelled out.

---

## 1. Menus and dropdowns

`resources/views/partials/top-nav.blade.php` is the whole nav in one file. It
has three regions: left (branding + quick search), centre (dropdown menus), and
right (stat bar + icon bar + avatar menu).

### Main dropdowns

| UNIT3D menu | Items | Ours | Status |
|---|---|---|---|
| **Torrents** | Torrents, Pending, Upload, Requests, Reseed Requests, RSS, MediaHub | Browse, Search | partial |
| **Community** | Forums, Playlists, Polls, Extra Stats, News, Chat | Forums, Guestbook | partial |
| **Support** | Rules, FAQ, Wiki, Helpdesk, Staff | Rules, FAQ, Wiki, Helpdesk, Staff, Stats, About | have |
| **Other** | Events, Subtitles, Trending, Missing, Internal | — | mixed |
| **Donate** | Support site (with % progress bar), Support UNIT3D | — | todo |

The bar is now FIVE items — Home plus Releases / Community / Support / Other,
with Donate appearing only where donations are enabled. That matches what live
UNIT3D sites actually do: a handful of broad dropdowns up top, detail one level
down.

It had drifted to eight flat tabs while `/store`, `/c`, `/stats`, `/sitemap` and
`/api` were not in the nav at all — a bar that grows with every feature and
still misses things.

**The structural cause was plugin pages.** An ungrouped `SiteNav` entry was
promoted to a top-level tab, so every plugin that registered a site page widened
the bar. Ungrouped entries now fold into **Other**; a plugin that declares a
NavHint group still gets its own dropdown, because it asked to be a section.

**Grouping rule:** a destination belongs to the group a visitor would GUESS, not
the plugin that owns it. The points store is under Community because a member
thinks of it as part of the site, not as "the store plugin".

A nav dropdown menu is absolutely positioned, and a closed `<details>` still
lays its menu out, so an overflowing menu is real and measurable. `left: 0`
overflows for right-side items and `right: 0` for left-side ones, so there is no
universally safe anchor: the side is chosen per item via `.dropdown__menu--end`.
That modifier MUST stay after `.dropdown__menu` in the stylesheet — same
specificity, so source order is what makes it win.

The contextual SECOND bar now exists too (`sectionnav_web.go`): the main nav
answers "where can I go", the section bar answers "what else is here". Five
sections — Account, Releases, Community, Support, Site — matched by path prefix
so a page cannot forget to declare itself.

Absent by design on home, the auth pages and `/admin/*`. A bar with one tab is
furniture, and admin already has its own subnav built from the view registry;
two competing section bars would be worse than one.

Two matching subtleties, both tested: `/store/history` marks **Points** under
Account rather than Store (longest matching href wins, so a shorter prefix
cannot steal a deeper page), and section matching is per SEGMENT rather than a
bare prefix — otherwise the `/c` section would swallow `/community/forums`.

---

## 2. Pages

### Core browse / content

**Listing facets are filtered in Go, not SQL.** The `UsenetIndex` read path
takes a limit and no predicates, so "1080p only" means "1080p within the most
recent 200", not across the index. Facet counts are computed from that same
window so they always agree with what filtering produces — a facet promising 40
results that yields 3 is worse than a smaller honest number. Pushing this down
needs a capability that accepts predicates; until then the bound is real and is
stated in `filters_web.go`.

| UNIT3D | Ours | Status | Notes |
|---|---|---|---|
| `torrents/index` | `browse.html`, `search.html` | have | Facets: quality, source, newsgroup, 6 sorts. See the bound below |
| `torrent/show` | `release.html` | partial | See §3 for what a detail page carries |
| `torrent/create` (upload) | — | n/a | Releases come from crawling, not uploads |
| `torrents/pending` | — | n/a | No upload moderation queue |
| `requests/*` (12 views) | — | todo | Wants a request board; needs schema |
| `trending/index` | home "Most grabbed this week" | partial | Grabs are recorded now; a dedicated /trending page is still todo |
| `missing/index` | — | todo | Honest analogue: groups with coverage gaps |
| `mediahub/*` (8 views) | — | todo | Browse by genre/network/company/person; TMDB data lands this |
| `torrent-reseed` | — | n/a | Peer concept |
| `subtitle/*` | — | todo | Would need a subtitle store |
| `playlist/*` (4) | — | todo | Curated release collections |
| `rss/*` (4) | — | partial | Newznab RSS exists; no management UI |

### Community

| UNIT3D | Ours | Status |
|---|---|---|
| `forum/*` (11 views) | 5 forum templates | partial |
| `poll/*` (4) | — | todo |
| `article/*` (news) | `/news`, `/news/:slug`, `/admin/news` | have |
| `contact` | — | todo |
| `page/*` (static pages) | `/rules`, `/faq`, `/about`, `/staff` | have (no CMS — see below) |
| `wiki/*` | `/wiki`, `/wiki/:topic/:post`, `/admin/wiki` | have |
| `ticket/*` (helpdesk) | `/support`, `/support/public`, `/admin/tickets` | have |
| `event/*` | — | todo |
| `donation/*` | `/help/donate`, `/admin/donate` | have (**dev-only, flag-gated**) |

### User

39 views upstream. Ours is one `profile.html` plus plugin pages
(`/p/account`, `/p/api-key`, `/p/sign-ins`, `/p/inbox`, `/p/stats`, `/p/store`).

| UNIT3D area | Status | Notes |
|---|---|---|
| Profile | partial | user-tag heading, subject points + post count, mocked panels (see `docs/MOCKS.md`) |
| Privacy settings | have | `/settings/privacy` — enforced, not cosmetic |
| Notification settings | have | `/settings/notifications` — enforced in the DELIVERY path |
| General settings | n/a | Locale/timezone/per-page unsupported; the theme is already a header switcher |
| Email, password, 2FA, passkeys, API keys, RSS keys | partial | `/p/account`, `/p/api-key` |
| Conversations (PM) | have | `/inbox` — threaded DMs + announcements (messages plugin). `/p/inbox` remains the separate NOTIFICATION inbox |
| Notifications | have | |
| Achievements, wishlist, bookmarks, followers/following, gifts, invites + invite tree | todo | Each needs schema |
| History, peers, seedboxes, resurrections, earnings, transactions | n/a | Peer/ratio economy |
| Warnings, unregistered info hashes | n/a | |

### Stats

24 views upstream (`stats`, clients, uploaded, downloaded, seeders, leechers,
uploaders, bankers, seedtime, seedsize, upload_snatches, messages, seeded,
leeched, completed, dying, dead, bountied, groups, groups_requirements,
languages, themes).

Most are **n/a** — they rank peers and ratios. `/stats` is now a real hub of
what the capabilities actually answer: indexed releases, active groups,
categories, members, forum posts, and the most-indexed groups. Sub-pages are
deliberately not stubbed — a page that ranks nothing is worse than no page.

### Staff / admin

**127 views — the single largest area, and the least explored.** Ours is five
templates (`admin_dashboard`, `admin_settings`, `admin_jobs`, `admin_plugins`,
`admin_view`) plus loon's own admin surface and plugin `AdminView` fragments.

The staff dashboard at `/admin` is built. That URL was a **404** — twenty-odd
routes hung beneath a root nothing served, and the usenet settings page's own
"Back" button pointed at it. It now answers "is anything wrong right now?" from
the live scheduler and real tables: job health, plugin count, members, open
tickets, releases, newsgroups, forum threads, and failed sign-ins in the last
24h, plus an index of every admin page built from the same `.AdminNav` the
subnav renders (so it cannot list a page that does not exist).

It is deliberately absent from `docs/MOCKS.md` and must stay that way — a staff
dashboard is the one surface where an invented figure gets acted on. A tile
whose source is unwired renders `—`, never `0`: no open tickets and no ticket
system are different facts, and `dashboard_test.go` fails if that degrades.

Worth mining for structure even where the subject matter is n/a: the staff
dashboard layout, the moderation queue table, the audit log, the bans/warnings
UI, and mass-action patterns are all directly reusable shapes.

---

## 3. Page details — the components worth lifting

This is where UNIT3D is richest and where a "page exists" checkmark hides the
most work.

| Component | Where | Status |
|---|---|---|
| `panelV2` + header/heading/actions/body | everywhere | have |
| `data-table` | listings | have |
| `torrent-card` | listings, cards | have (as release-row) |
| `meta` / `key-value` | detail pages | have |
| `user-tag` (coloured by role, icon) | everywhere a username appears | have |
| `comment` / `comments` | ticket replies, DMs | have |
| `bbcode-input` + `bbcode-rendered` | every prose input | have (markdown) |
| `mediainfo` | torrent detail | partial |
| `comparison` (image compare) | torrent detail | n/a-ish |
| `person` / `collection-card` / `mediahub-card` | mediahub | todo (with TMDB) |
| `pagination` | everywhere | have |
| `achievement`, `event`, `donation-package` | features not built | todo |
| `chatbox` | shoutbox | todo (needs websockets) |
| `dialog`, `swal` | confirmations | partial (native `<details>`/forms) |
| `user-stat-card`, `user-card` | profile | have |
| `article-preview` | news | have (unused) |
| `quick_search` / `compact-search` | nav | have |

**One prose pipeline, and it is markdown rather than BBCode.** UNIT3D runs
every user-typed body through one BBCode parser and gives every input the same
`bbcode-input` toolbar. This stack had already committed to markdown for the
wiki and communities, so a third syntax would have been the wrong kind of
parity — `markdown_web.go` is the single renderer (goldmark GFM, raw HTML
refused, then the news allowlist sanitizer) and every prose body now goes
through it, including the forum, which previously got escape-and-wrap-in-`<p>`
and nothing else. Tickets, DMs and announcements got NO renderer at all, so a
multi-paragraph body arrived as one run-on block; they render through the same
pipeline now, via the `{{prose}}` helper for the plugin pages whose bodies
arrive as plain strings. `.prose` was referenced by six templates and defined by
no stylesheet — it exists now.

The `bbcode-input` equivalent is a markdown toolbar site-scripts attaches to any
`textarea[data-prose]`: bold, italic, inline code, link, quote, bulleted and
numbered lists, with Ctrl+B/I/K, selection-aware wrapping, toggle-off, and
line-oriented prefixes that renumber an ordered list. Twelve textareas across
four plugins carry the attribute. News is deliberately NOT one of them: its
Sanitize seam takes raw HTML, so a markdown button there would produce text the
renderer never interprets.

**`user-tag` was the highest value-per-line item and is now built.** UNIT3D
colours every username by group, with an icon and an optional CSS gradient
"effect". Ours colours by role, with a role icon and the role name as the
title, defined once in `site_chrome.html` so both parse sets render it
identically. Not ported: donor sparkle backgrounds, per-user icon uploads and
group gradient effects — no data source here, and inventing one would be
fabrication.

All wired. The last gap was `community_category.html`'s thread rows, whose
model already carried `.Role` and `.LastPostRole` for exactly this — the comment
in the plugin's model says so — and simply never read them.

Two of the three items previously listed here were wrong. The profile header had
already been using `user-tag` for some time. And the release detail page must
NOT: its `.Poster` is the raw NNTP `From` header, not a site account, so a
`user-tag` there would link every release to a `/u/` profile that does not
exist. It is plain text, bounded, with the full header on hover — obfuscated
posters run past seventy characters of punctuation. The breadcrumb takes the
same treatment for the same reason.

Wiring those rows surfaced two silent bugs worth recording, because both produce
output that looks right:

  - `{{print "/u/" .Name}}` on a `*string` emits the POINTER —
    `/u/0x1129d1d30910` — while `{{.Name}}` on its own still prints `bob`,
    because html/template auto-indirects a lone value but not one inside a
    multi-argument print. Nullable plugin columns are `*string`, so the row read
    perfectly and every link on it was garbage.
  - `roleSlug` matched `core.Role` and `string` but not `*string`, so a nullable
    role fell through every case and painted the whole column "member".

Both are fixed in the helper rather than at the call sites, and pinned by
`usertag_test.go` — a component this widely rendered cannot rely on each caller
passing the right shape.

Not ported from `bbcode-input`: a live preview. Rendering markdown in the
browser needs a client-side parser, and shipping one whose output could drift
from the server's sanitizer is how a preview becomes a lie about what will be
stored. A server-rendered preview endpoint is the honest version if it is ever
wanted.

---

## 4. Do we need a site dump?

Short answer: **the source is enough for structure; a dump would help for
look.**

What the source fully gives us:

- Every page's markup — all 446 Blade views, including the 74 Livewire views,
  whose templates are plain Blade in `resources/views/livewire/`
- Every route and its shape
- Every style — 17 themes and ~100 Sass partials, all on CSS custom properties
- Every component's expected markup contract

What the source does **not** give:

1. **Populated pages.** Blade shows the loop; it doesn't show what 50 real rows
   look like, where text wraps, or which columns dominate. Most of our layout
   bugs so far have been density and sizing problems that only appeared with
   real content.
2. **Post-Livewire HTML.** The Blade is there, but the composed result after a
   component re-renders is not.
3. **The actual look.** No screenshots. We are reconstructing visual intent
   from token values.
4. **Font Awesome Pro glyphs.** Unlicensed — we substitute our own SVG sprite,
   so icon choice is guesswork against `fa-*` class names.

So a dump is most useful for **(1) and (3)** — a populated torrents index, a
torrent detail page, a forum thread, a user profile, and the staff dashboard,
ideally in one dark theme. That is maybe five pages, not a whole site.

Saved HTML alone is of limited use without its CSS; a full page archive (or
screenshots plus the HTML) is worth more than either.

---

## 5. Nine plugins already exist and are not wired

The biggest finding of this survey: **most of the "todo" rows above are a
wiring exercise, not a build exercise.** `loon-plugins` already ships these,
and none is referenced by the demo:

| plugin | UNIT3D equivalent | needs |
|---|---|---|
| `news` | `article/*` | **wired** — see below |
| `wiki` | `wiki/*` | **wired** |
| `tickets` | `ticket/*` (helpdesk) | BaseData + PageOffset + Pagination + Viewer |
| `messages` | `users.conversations` (PM) | Store + BaseData |
| `donations` | `donation/*` | BaseData |
| `ranks` | groups / paid ranks | ships its own `groups.html` |
| `store` | `bon_exchanges` | points capability (we have it) |
| `rewards` | achievements | — |
| `communities` | *(no UNIT3D equivalent)* | user-owned sub-forums |

The cost per plugin is **not** the Go wiring — that is a `SetDeps` call and a
migration. It is the **host templates**: these plugins render `c.HTML("name")`
against gin's set and ship no templates of their own (except `ranks`). So each
costs N templates in our design system. `news` was four.

`pluginTemplates()` (views.go) parses `site_chrome.html` + `forum/*.html` +
`plugin/*.html` into ONE flat namespace keyed by base filename, so a second
plugin shipping an `index.html` would collide. Give plugin templates
distinct names.

Order of effort, cheapest first: `wiki` (5) · `donations` · `messages` ·
`tickets`. `ranks`/`store`/`rewards` need the points economy thinking through
first.

## 5b. Plugin wiring status, and what each still costs

Wired so far: `news`, `wiki`, `ranks`, `rewards` (plus the pre-existing
`backups`, `catalog`, `dailyreward`, `forum`, `pointstore`, `scraper`, `stats`,
`usenet`).

**The cost of a plugin is its host templates, not its Go wiring.** Plugins split
cleanly into two kinds, and the difference is an order of magnitude:

- **View-system plugins** draw through `core.RegisterView`, so loon renders them
  inside the host's own admin/site page chrome. Host templates needed: **zero**.
  `ranks` and `rewards` were a blank import each — no `SetDeps`, no migration,
  no markup. They landed at `/admin/p/groups`, `/admin/p/rewards`,
  `/admin/p/rewards-events` and a `rewards-claim` member widget.
- **gin-template plugins** call `c.HTML("name")` and expect the HOST to own that
  file. Host templates needed: one per view. `news` cost 4, `wiki` cost 6.

Remaining, cheapest first:

| plugin | host templates | other seams | notes |
|---|---|---|---|
| `messages` | **wired** | — | `/inbox`, `/admin/messages` |
| `store` | **wired** | — | `/store`, `/store/history`, `/admin/store` |
| `donations` | 3 | BaseData, Settings, IsDonateEnabled, LookupUsername/UserID | Needs BTCPay — see §5c |
| `tickets` | **wired** | — | `/support`, `/admin/tickets` |
| `communities` | **wired, BLOCKED** | — | Schema mismatch on `users.role` — see §5d |
| `anidbscraper` | 0 | Catalog, Nzbs, Matcher, Covers | We have Catalog + Covers already |
| `backup` | 0 (ships `backup.html`) | DB, Config, FreeDisk, DBSize, Classes, Root, DBDumpDir | Distinct from the wired `backups` |
| `dbmaint` | 0 | Diag, StatCache, Nzbs, Maintenance, ConfigStore, FreeDisk, Repack | Usenet ops surface |
| `economy` | 0 | PointsPerGrab, UploaderGrabTotals, GrabsAlreadyCredited | **Blocked — see §5c** |

## 5d. BLOCKED: communities and the users.role shape

`communities` is fully wired — 8 tables migrated, 5 seams supplied, 7 host
templates written and passing the execute sweep. `/c` and `/c/new` render, and
creating a community works. **`/c/:slug` 500s**, and it is not a bug in any of
that work:

```
pq: invalid input syntax for type integer: "user"
```

Four of the plugin's queries do `COALESCE(u.role, 'user')` — they assume the
HOST's `users.role` is TEXT holding a role name. This host stores
`role INTEGER`, the `core.Role` enum, and comparing the two is a type error in
Postgres.

This is a shape mismatch between the plugin's origin host and this one, not
something either side got wrong. The options, none of which is a small edit:

1. **Plugin-side seam.** The cleanest: the plugin takes a role resolver in
   `Deps` (it already takes five others) and stops assuming a column shape.
   Everything else it needs from `users` it reads through plain columns.
2. **Plugin-side column.** Select `role_name` and let each host provide it.
   Same idea, less flexible.
3. **Host-side schema change.** Move this host to text roles. Rejected:
   `core.Role` is an ordered enum and `AtLeast` compares it numerically, so a
   text column would break every permission check in the stack for one plugin.

It runs on a live site whose users table already satisfies the assumption, so
**do not "fix" the plugin without checking there first** — a change here could
break production there.

Everything else about the wiring is done and correct, so this becomes a
one-line change the day the seam exists.

## 5c. Missing features — what blocks a plugin we cannot simply wire

These are gaps in the HOST, not in the plugins. Each is a real feature, not a
template.

1. ~~Grab / download counter~~ — **done**. `release_grab` records one row per
   NZB download (a row, not a counter column: a counter cannot answer "this
   week", which is what trending asks). `user_id` is nullable on purpose —
   `/nzb/:id` is reachable anonymously and by API key, and NOT NULL would drop
   exactly the traffic a public indexer sees most.

   Unblocked by it: the "Grabs" column on every listing, the home "Most grabbed
   this week" block, and `uploaderGrabTotals` — the read the `economy` plugin's
   seam wants.

   `economy` still stays unwired, but for a DIFFERENT and more honest reason
   than "no data": its job is a per-grab **uploader** bonus, and releases here
   come from crawling rather than uploads, so there is nobody to pay. That is a
   domain mismatch, not a missing table.

2. ~~Threaded private messages~~ — **done**, `messages` is wired. Two host
   gaps surfaced while wiring it, both now fixed and worth knowing about:
   - **Entitlements had no baseline.** `messages` gates "may this user start a
     DM" purely on `ents.Has("dm.initiate")` — its error text mentions roles,
     but the code delegates the entire decision to the host. With no baseline
     every send failed closed, including for an admin. The host now maps
     `RoleMod ⇒ dm.initiate` via `EntitlementsConfig.Baseline`.
   - **`users.avatar_path` did not exist.** The plugin's thread-list query
     selects `COALESCE(u.avatar_path, '')` from the HOST's users table, and
     loon-baseline's table has no such column. The handler discards the error
     (`threads, _ = ...`), so the inbox rendered empty while the rows sat in
     the database. Added host-side.

3. **A payment gateway.** `donations` is wired but **DEV-ONLY**, gated on
   `LOON_DEMO_DONATIONS=1`. The gate is the ENV VAR, not the admin toggle:
   `IsDonateEnabled` ANDs the two, so a deployment without the flag reports
   disabled even with `donate_enabled=1` persisted — verified. `SetDonateEnabled`
   refuses with an explanation rather than silently no-opping, since an admin
   who clicks "enable" and sees nothing happen would reasonably assume the
   feature is broken rather than deliberately gated.

   Two things still missing for a real deployment: BTCPay credentials
   (`btcpay_*` keys in `site_settings`), and the wallet/package admin surface —
   the template covers the toggle, cost lines and donation log, not payment
   configuration. The plugin's own `/admin/donate/btcpay` routes remain
   available for anyone who needs it.

   NOTE the webhook at `POST /api/btcpay/webhook` registers regardless of the
   flag — routes bind at Provision and core has no per-plugin disable. That is
   safe because the plugin authenticates it by HMAC-SHA256 over the raw body
   ("the HMAC verification IS the auth"), so with no secret configured nothing
   can validate. Verified: an unsigned callback gets 403.

4. **A user directory.** `messages.ListUsers` is optional precisely because
   core has no "list every user" method. The composer degrades to a username
   field without it. Fine for a demo; a real host wants a paged directory.

5. ~~Notification fan-out for tickets~~ — **done**, wired through
   `core.Notifications`. One limit worth recording: `Notify` addresses ONE
   user, and a new ticket has no single recipient — it is for whoever is on
   duty. So the host notifies the ticket's AUTHOR that it was received rather
   than fanning out to staff, because a staff-list query does not exist here
   and inventing one would be the same unbounded query `ListUsers` exists to
   avoid. **A staff broadcast needs either a staff-list seam or a
   notify-group capability.**

6. **Cover art.** Still unexercised: no `TMDB_API_KEY` is set, so every poster
   is a gradient fallback. Blocks `mediahub` parity entirely.

7. ~~An InviteGranter~~ — **done**. Published on the extension registry; an
   invite purchase now credits `users.invites` instead of producing a
   spend/refund pair. A BALANCE, not a table of codes: UNIT3D issues individual
   invite rows with codes and an invite tree, but this host has no registration
   gate for a code to unlock, and the granter contract only asks for "credit N
   invites". The counter is real spendable state, not a display number.

### A pattern worth naming

Every plugin wired so far has surfaced at least one HOST seam left nil or
half-wired, and **every one of them failed silently**:

| plugin | gap | symptom |
|---|---|---|
| `news` | — | (plugin-side: list path skipped Sanitize) |
| `wiki` | — | (plugin-side: form handlers skipped BaseData) |
| `messages` | Entitlements had no `Baseline` | every DM send failed closed, incl. admins |
| `messages` | `users.avatar_path` missing | inbox rendered empty, error discarded |
| `store` | PointsAdapter had no `HistoryFn` | ledger page rendered its error branch |

None was a crash. The lesson for wiring the rest: **exercise the feature, do
not just load the page.** Four of those five look fine until you send a
message, open a ledger, or create an item.

## 6. Suggested order

1. **`user-tag`** — small, touches every page, biggest consistency win
2. **Static pages** (rules, FAQ, about, staff) — real content, trivial to build
3. **Stats index** — we have the data, no page
4. **Nav grouping** — once 1–3 add destinations worth grouping
5. **BBCode input/render** — unlocks comments, news, wiki, tickets
6. **MediaHub** — only after TMDB is wired and covers are real
7. **Staff dashboard shapes** — mine the 127 views for reusable structure

Deliberately **not** on this list: anything resting on peers, ratio, seeding,
upload credit or grab counts. Those are n/a and should stay that way rather
than being faked.
