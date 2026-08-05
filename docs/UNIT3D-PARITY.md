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
| **Support** | Rules, FAQ, Wiki, Helpdesk, Staff | — | todo |
| **Other** | Events, Subtitles, Trending, Missing, Internal | — | mixed |
| **Donate** | Support site (with % progress bar), Support UNIT3D | — | todo |

Our nav is currently flat (HOME, BROWSE, GROUPS, SEARCH, FORUMS, GUESTBOOK)
plus plugin `SiteNav` entries. UNIT3D groups into five dropdowns because it has
far more destinations. **Adopt grouping when we have the destinations to
justify it — not before.** A dropdown holding one item is worse than a link.

### Right-hand stat bar

UNIT3D shows uploaded, downloaded, seeding, leeching, buffer, bonus points,
ratio, freeleech tokens. Seven of those eight are **n/a** here. We show
Releases / Active groups / Categories plus Rank / Unread / Points, which is the
honest equivalent and is already built.

### Icon bar and avatar menu

| UNIT3D | Ours | Status |
|---|---|---|
| Staff dashboard (mod only), Torrent moderation (torrent-mod only) | Admin link | partial |
| Conversations / PM, Notifications | `/p/inbox` bell | partial |
| Avatar → Profile, Settings, Privacy, Achievements, Uploads, Downloads, Requested, Bookmarks, Playlists, Wishlist, Logout | Profile, Inbox, Admin, Logout, plugin pages | partial |

**Dropdown mechanics differ deliberately.** UNIT3D uses Alpine plus a
hover/`tabindex` pattern. We use native `<details>`, which works on touch with
no JS. Do not port `top-nav__dropdown--nontouch` / `--touch`; that pair exists
only to work around hover on touch devices.

---

## 2. Pages

### Core browse / content

| UNIT3D | Ours | Status | Notes |
|---|---|---|---|
| `torrents/index` | `browse.html`, `search.html` | have | Facet filtering is far richer upstream |
| `torrent/show` | `release.html` | partial | See §3 for what a detail page carries |
| `torrent/create` (upload) | — | n/a | Releases come from crawling, not uploads |
| `torrents/pending` | — | n/a | No upload moderation queue |
| `requests/*` (12 views) | — | todo | Wants a request board; needs schema |
| `trending/index` | — | todo | Needs a grab/view counter that doesn't exist yet |
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
| `article/*` (news) | — | todo |
| `contact` | — | todo |
| `page/*` (static pages: rules, FAQ, about, staff, internal, client blacklist) | `site_page.html` | partial |
| `wiki/*` | — | todo |
| `ticket/*` (helpdesk) | — | todo |
| `event/*` | — | todo |
| `donation/*` | — | todo |

### User

39 views upstream. Ours is one `profile.html` plus plugin pages
(`/p/account`, `/p/api-key`, `/p/sign-ins`, `/p/inbox`, `/p/stats`, `/p/store`).

| UNIT3D area | Status | Notes |
|---|---|---|
| Profile, general/privacy/notification settings | partial | Split across plugin pages |
| Email, password, 2FA, passkeys, API keys, RSS keys | partial | `/p/account`, `/p/api-key` |
| Conversations (PM) | partial | `/p/inbox` is notifications, not threaded PM |
| Notifications | have | |
| Achievements, wishlist, bookmarks, followers/following, gifts, invites + invite tree | todo | Each needs schema |
| History, peers, seedboxes, resurrections, earnings, transactions | n/a | Peer/ratio economy |
| Warnings, unregistered info hashes | n/a | |

### Stats

24 views upstream (`stats`, clients, uploaded, downloaded, seeders, leechers,
uploaders, bankers, seedtime, seedsize, upload_snatches, messages, seeded,
leeched, completed, dying, dead, bountied, groups, groups_requirements,
languages, themes).

Most are **n/a** — they rank peers and ratios. The ones that carry over:
`groups`, `languages`, `themes`, and a release-count equivalent of `uploaders`.
We have `/p/stats`. **todo:** a real stats index page.

### Staff / admin

**127 views — the single largest area, and the least explored.** Ours is four
templates (`admin_settings`, `admin_jobs`, `admin_plugins`, `admin_view`) plus
loon's own admin surface and plugin `AdminView` fragments.

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
| `comment` / `comments` | torrent + article pages | todo |
| `bbcode-input` + `bbcode-rendered` | every text input | todo |
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

**`user-tag` was the highest value-per-line item and is now built.** UNIT3D
colours every username by group, with an icon and an optional CSS gradient
"effect". Ours colours by role, with a role icon and the role name as the
title, defined once in `site_chrome.html` so both parse sets render it
identically. Not ported: donor sparkle backgrounds, per-user icon uploads and
group gradient effects — no data source here, and inventing one would be
fabrication.

Still to wire: the release detail page, the profile header, and
`community_category.html`'s thread rows.

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

## 5. Suggested order

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
