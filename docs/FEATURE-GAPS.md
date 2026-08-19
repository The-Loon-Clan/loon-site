# Feature gaps

What comparable private trackers and Usenet indexers offer that this site does
not — sized by build cost against **this** codebase rather than in the abstract,
because several are cheap precisely for reasons nobody outside this repo could
guess.

Distinct from the two neighbouring docs:

| Doc | Axis |
|---|---|
| `BACKLOG.md` | things we built that are wrong or unfinished |
| `UNIT3D-PARITY.md` | UNIT3D's UI surface, page by page, for lifting markup |
| **this** | features the genre has that we have never had at all |

Researched Aug 2026 against UNIT3D and UNIT3D Community Edition's feature
lists, the nZEDb/newznab indexer lineage, autodl-irssi's tracker definitions,
and the public write-ups of closed sites. Sources at the bottom.

---

## The finding

**The gamification is ahead of the field; the table stakes are behind it.**

A typical tracker's entire reward system is bonus points and a shop. This site
has achievements, medals, ranks, perks, magic promotions, lootboxes, a daily
reward, a points pot, charity, donor tiers and a site-goal freeleech loop —
more moving parts than most sites will ever ship.

Meanwhile it has no comment box on a release, no personal freeleech tokens, no
invite tree, no "send to SABnzbd" button, and no way to say *tell me when this
lands*. Those are the five things a member notices in their first hour.

So the cheapest wins are all boring, and the temptation — build another reward
system, because that is the part we are good at — is the wrong instinct.

## Sizes

Build cost **against this codebase**. Read the "Here" note on each item before
trusting the size: most of the S's are S because a seam already exists and is
dormant.

- **S** — a day or two
- **M** — a week-ish
- **L** — a project

---

## Tracker side

### Comments on a release — S

Every Gazelle and UNIT3D site has a comment thread under each torrent, and it
is the most-used social surface on the whole site: *is this the good encode*,
*this one is missing subs*, *thanks*.

**Here:** the forum, communities and reports all exist; the release page has
nowhere to say anything. The forum plugin already owns threaded posting, so
this is a thread keyed to a release id rather than a new system.

**Watch for:** the release page is shared by the indexer and tracker sides, and
a comment belongs to the RELEASE, not to the torrent made from it — otherwise
mirroring a release later strands the conversation on the wrong object.

### Personal freeleech tokens — S

A token spends one torrent's download as free. Gazelle sites hand them out with
donations and rank-ups; they are the standard way to let somebody grab one huge
thing without wrecking their ratio.

**Here:** half of it is built and dormant. `hitrun/deps.go` already carries the
veto seam whose doc comment names freeleech tokens as its motivating case —
"a site that told somebody a download was free must not then punish them for
not seeding it" — and nothing mints or spends one. A new `store.itemtype.*`
kind plus a spend on the download path.

**Note:** this is a per-user, per-torrent grant. Site-wide freeleech already
exists as a STATE through the site-goal loop, and the two must not be conflated
— see the site-goal notes on why site rewards are states and grants are not.

### Invite tree — S

Who invited whom, as a walkable tree, so a bad recruiter's whole subtree is one
click for staff. Universal on invite-only sites, and the reason invites are
taken seriously anywhere.

**Here:** invites are issued and redeemed, but the edge is not kept as a
relationship anyone can query. One column and a staff page.

### IRC announce channel — M

The bot that announces every new upload into a channel, in the format
autodl-irssi and autobrr parse. This is how power users actually acquire:
filters fire within seconds of an upload.

**Here:** the `irc` plugin bridges *chat*, which is a different channel and a
different format. The bot connection exists; what is missing is the announce
line, the per-site tracker definition to publish, and the decision about
whether announce carries the passkey (it must not).

### Duplicate / trump detection on upload — M

Warn — or refuse — when an upload is the same release as one already indexed,
or supersedes it. Without it a catalogue silently accumulates five copies of
one thing and nobody can tell which to take.

**Here:** `uploads` takes the bytes and `predb` knows scene names; nothing
compares a new upload to what exists. The series parser (migration 042) now
gives a second axis to compare on — same show, same season, same episode.

### Seedbox / IP allowlist — S

Members register the IPs their seedbox announces from. Half convenience, half
anti-cheat: an announce from an unregistered address is a signal.

**Here:** cheat detection already samples announces, so the signal has
somewhere to go the day the list exists.

### Peer and snatch lists per torrent — S

Who is seeding this right now, and who has snatched it. Members use it to
decide whether a torrent is worth starting; staff use it constantly.

**Here:** the admin page has aggregates and the Redis peer store holds the live
swarm. It is a read, not a feature.

### Double upload / neutral leech — S

Per-torrent multipliers as first-class site states. UNIT3D ships "double upload
system" alongside freeleech as a headline feature; it is how a site pushes a
category it wants seeded.

**Here:** `magic` already casts per-torrent multipliers and `ResolveMultiplier`
combines them under the rules in the multiplier contract. This is a site-level
state on machinery that runs — and it must go through `ResolveMultiplier`
rather than beside it, or the combining rules exist in two places.

---

## Indexer side

### Send to SABnzbd / NZBGet — S

One click puts the NZB in the downloader's queue instead of the browser's
downloads folder. Every serious indexer has it; the SABconnect++ browser
extension exists purely because people expect it on sites that lack it.

**Here:** SABnzbd and NZBGet appear in this codebase only as correctness notes
about NZB generation (`usenet/assemble.go` on segment byte ceilings and
`JoinGroup`). A per-user saved endpoint and API key, a POST, a toast.

**This is the single cheapest visible win on the list.**

### Saved searches that notify — M

*Tell me when anything matching this lands.* Gazelle calls them notification
filters, indexers call them watches. It converts a site from somewhere you
visit into somewhere that reaches you.

**Here:** `lists` and the wishlist store what you *want*, and nothing matches
new releases against them. The crawler has the hook point, and the notify seam
and the inbox are both built. Pairs naturally with an RSS feed per saved search
— the Newznab `/api` + `/rss` plumbing already exists.

### Failed-download reporting, fed into health — M

The newznab lineage lets a downloader report back that a release failed, and
folds it into a health score. It is the only signal that catches a release that
*looks* complete and is not.

**Here:** `reports` is member-filed and manual. An API endpoint the downloader
calls, feeding the health the crawler already computes, would be better than
most indexers manage — and it lines up with the structural-junk problem already
on the books (releases no title rule or health check can catch).

### Cart / bulk grab — S

Tick ten rows, get one zip of NZBs, or push them all to the downloader. nZEDb
has had "send to cart" and "send to my queue" for over a decade.

**Here:** `lists` already downloads a whole list; what is missing is selection
on a listing page.

### MediaInfo and screenshots — M

Trackers require both on upload: resolution, bitrate, audio tracks, subtitle
languages, and a handful of frames. It is how a member picks between six copies
of one episode.

**Here:** the new series page puts six copies of one episode in front of a
reader with only the filename tags to choose from — which is exactly the page
that makes this worth having. `img` and `uploads` are the seams.

---

## Community and economy

### Polls — S

UNIT3D ships a polls system as a headline feature. Staff use them for rule
changes and category decisions; members use them for arguments.

**Here:** `roadmap` has voting on ideas, which is a different thing. The page
widget shortcode (`[widget slug config]`) makes a poll placeable on any page
the day it exists.

### Thanks / kudos between members — S

A one-click thanks on an upload or a post, where both parties get a little
something. The cheapest engagement mechanic there is, and it feeds the economy
already built.

**Here:** the points ledger and the multiplier system are done; this is a
button and an economy rule.

### General leaderboards — S

Top uploaders, top seeders, top posters, this week and all time. Trackers run
on this.

**Here:** there is a donations leaderboard, a trending page and a stats page —
the data is all sitting there unranked.

### Collectible sets with trading — M

The natural next step from lootboxes, and the closest confirmable analogue to
what AnimeBytes-style sites do with their currency: drops belong to *sets*,
duplicates are tradeable between members, completing a set pays out.

**Here:** lootboxes already draw weighted rewards, and `gifts` already moves
things between members. This is a set definition, a duplicate rule and a
completion payout — three tables on top of two working systems.

### Cosmetics as store items — S

Name colours, avatar frames, profile backgrounds, custom titles. Perceived
value is wildly out of proportion to build cost, and it is the classic sink for
a currency that otherwise inflates.

**Here:** the `store.itemtype.*` seam was opened for charity and takes new
kinds from any plugin. Ranks and medals already prove the display side, and
medals' icon picker already reads `icons.catalogue`.

### Applications / interviews — M

Open-signup windows with a written application and a staff queue — how
AnimeBytes and most closed sites actually recruit, rather than pure invites.

**Here:** registration is invite-or-open. `tickets` is a staff queue that
already works; an application is a ticket with a form and a decision.

---

## The AnimeBytes mini-game

**Unconfirmed, deliberately left that way.** AnimeBytes is closed, so its
feature set is not indexed. The public write-ups (opentrackers, TrackerVerse,
InviteHawk's 2024 review) cover ratio rules, freeleech and seeding requirements
and nothing else. No description of a game appears anywhere findable.

The one confirmable fact: their bonus currency is **Yen**, earned by seeding
and spent in a shop — an invite-forum giveaway lists "250,000 Yen
(BonusPoints)" as a prize. So whatever the game is, it is almost certainly a
Yen sink: wager or spend the currency you earn by seeding, get cosmetics,
tokens or bragging rights back.

That is the shape worth copying, and this site is already built for it — the
points ledger, the `store.itemtype.*` seam and the lootbox payout kind all
exist. **Do not build a guess.** Get a screenshot or the page name first.

---

## The Habbo-style room

**Verdict: not now, and the 80% version is a different feature.**

A persistent shared social space is expensive in a way that is easy to
underestimate, and the graphics are the cheap part. What costs is everything
around it:

- real-time presence and movement
- a whole second moderation problem, in a medium where harassment looks nothing
  like a bad forum post
- and the one that kills these — **something to do once you are in the room**

An empty room is worse than no room, and rooms are empty by default on a site
whose members show up to grab an episode and leave.

It also composes with nothing. Every system here earns its keep by feeding
another one; a chat room in a plaza feeds nothing, and the site already has
`chat`, `forum`, `communities`, `messages`, an IRC bridge and a Discord bridge.
That is six places to talk. The gap is not a seventh.

### The 80%: make the profile a room

A member's page becomes a space they decorate, and the things they already earn
become the furniture — medals, achievement badges, lootbox drops, rank
insignia, store cosmetics — arranged on a grid they control and visible to
visitors.

- **Same loop.** Collect, display, visit others, envy, collect. Which is what a
  Habbo room is actually for.
- **No real-time layer.** No presence, no movement, no live moderation surface.
  It is a page.
- **It composes.** Every existing reward instantly becomes placeable, which
  makes lootboxes, medals and the store all more valuable without touching any
  of them.
- **It has an obvious next step.** Sets and trading (above) turn decoration
  into a hobby, and that is where a mini-game actually belongs.

If the room still calls after that, it will call with a decoration system, a
trading economy and members who already own things — a far better place to
start a shared space from than an empty plaza.

---

## Already covered

For the record, so the comparison above is grounded. Features other sites list
as headline items that this one already ships:

**Community** — forums, communities, wiki, news, chat, IRC bridge, Discord
bridge, DMs + inbox, helpdesk tickets, reports queue

**Operations** — staff dashboard, backups, error-log search, i18n, 2FA, access
policy, page + nav editors, placeable widgets, definition editors, contracts
page

**Index** — Newznab/Torznab API, API key + quota, RSS, sitemap, requests +
bounties, offers, lists (Gazelle's *collages*), playlists, wishlist, bookmarks,
follows, gifts, invites, calendar, trending, series pages, release groups,
catalog + cover art, predb, uploads, feeds

**Tracker** — announce + scrape, passkeys, hit & run, seedlock, cheat
detection, tracker↔index mirroring, site-goal freeleech

**Economy** — points store (UNIT3D's *BON store*), economy rules, ranks +
automatic promotion, perks, medals, achievements, lootboxes, daily reward,
magic promotions, points pot + charity, donations + donor tiers, events,
roadmap

Mapping, where the genre uses a different word: collages are `lists` and
`playlists`; bounties are `requests` and `offers`; the BON store is `store` and
`pointstore`.

---

## Suggested order

Nothing here is blocking. If picking by value per day of work:

1. **Send to SABnzbd / NZBGet** — cheapest visible win in the whole document
2. **Comments on a release** — the biggest hole in the social surface
3. **Cosmetics as store items** — absurd perceived value for the seam that exists
4. **Freeleech tokens** — finishes a seam that is already written and dormant
5. **Saved searches that notify** — the one that changes how members use the site
6. **MediaInfo** — newly worth it now that the series page shows six copies side by side

---

## Sources

- [UNIT3D Community Edition](https://github.com/Baine/UNIT3D-Community-Edition) — the feature list quoted for polls, double upload, BON store, request bounties
- [UNIT3D](https://github.com/HDInnovations/UNIT3D) — current upstream
- [AnimeBytes on opentrackers](https://opentrackers.org/animebytes/), [TrackerVerse](https://bttrackers.com/trackers/animebytes), [InviteHawk 2024 review](https://www.invitehawk.com/topic/170400-animebytes-ab-anime-2024-review/) — ratio, freeleech and seeding rules; no game documented anywhere public
- [Yen (BonusPoints) giveaway](https://www.invitehawk.com/topic/122652-250000-yen-bonuspoints-on-animebytes/) — the currency name
- [autodl-trackers](https://github.com/autodl-community/autodl-trackers), [autodl-irssi](https://github.com/autodl-irssi-community/autodl-irssi) — the announce-channel format
- [nZEDb](https://github.com/nZEDb/nZEDb), [newznab-tmux](https://github.com/NNTmux/newznab-tmux) — cart, my-queue, predb import
- [SABconnect++](https://github.com/jeremybergen/sabconnectplusplus) — one-click send-to-downloader
- [Best Usenet indexers 2026](https://datahoarder.io/best-usenet-indexers/), [NZBGeek review](https://datahoarder.io/nzbgeek-review/) — what indexer users compare on
- [Torrent ratio, freeleech and seeding](https://www.rapidseedbox.com/blog/torrent-ratio-tips) — freeleech token conventions
