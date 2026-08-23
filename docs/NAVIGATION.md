# Navigation: what goes where

Five places can hold a link, and each answers one question. Without the rule
things land wherever they were added — which is how "New avatars", a staff work
queue, ended up in the member-facing account dropdown between "My profile" and
"Log out".

| place | answers | holds |
|---|---|---|
| top nav | "where is the site?" | Browse, Search, Community, Store — the site's own sections |
| points pill | "how am I doing?" | a live figure worth seeing without clicking |
| **account dropdown** | **"where are MY things?"** | the viewer's own pages, and ONE staff door |
| account bar (`/u/`, `/settings/`, …) | "what else is in my account?" | the account area's pages, grouped |
| admin subnav | "what can staff do?" | every queue and tool, including the ones not under `/admin` |

## The rule

**The account dropdown carries the viewer's own things, plus at most one staff
door. Never a queue, never a tool, never a list.**

A work queue is not *yours*, it is the site's. Listing queues in a personal menu
gets two things wrong at once: it puts site administration in a member-facing
place, and it splits the staff area in half — some tools in the dropdown, the
rest behind the admin bar — so neither place can answer "where is everything I
can do".

One door, and everything behind it:

```
Admin  →  /admin          (admins: the subnav lists every queue and tool)
Moderation → /moderation/avatars   (moderators: the one queue they can reach)
```

A moderator needs their own door because `/admin/*` gates at `RoleAdmin` —
sending them to the admin dashboard would be a 403, and a door that 403s is
worse than no door. The pending-avatars badge travels with whichever door is
shown, because an unread count with no way to act on it is just an accusation.

Enforced by `TestAccountDropdownHasAtMostOneStaffDoor`, which counts the
`/admin` and `/moderation` links in the dropdown markup. That is a crude check
and deliberately so: the failure it guards against is somebody adding one more
link, and a crude check catches that on the line it happens.

## The rule applies to the TOP nav too, and nothing was enforcing it

The table says the top nav holds "the site's own sections". On 23 Aug 2026 it
also held **ACCOUNT**, a dropdown with Appearance and Download reports in it,
sitting between Other and Donate.

Nobody decided that. Both plugins declare `NavHint{Group: "Account"}`, and
`siteNav` collapses any group with two or more visible pages into a top-level
dropdown — so the second plugin to ask for that group is what created the tab.
One would have flattened to a plain link and looked like an oversight; two
looked like a feature.

The fix is the one `/p/medals` and `/p/api-key` already use: answer the hint
with no group (`navPlacement`), let `navPlacedByHost` stop the generic nav
placing a loose copy, and list the page by hand on the account BAR — which is
the row that answers "what else is in my account?" and appears across `/u/`,
`/settings/` and the rest of the area.

Two things that are easy to miss when moving a page this way:

- **add it to `accountAreaPrefixes`.** A page listed on the bar that does not
  itself show the bar strands whoever clicks it.
- **add it to the `served` map in `sectionnav_test.go`.** A plugin registers
  these as VIEWS, so `w.mount` never sees a route and the test cannot discover
  them; that list is the only thing that can say the page exists, and the
  entry is worth nothing unless somebody actually loaded the page first.

`accountPluginPages` exists for the same problem and catches nothing, because
it only sees pages that arrive ungrouped. A plugin that names a group skips
past it entirely.

## The second rule: one word, one destination

**No label may name two different pages.** A menu that offers "Store" twice is
not ambiguous to the person who built it — they know which is which — and is
unusable to everybody else, who has only the word to choose from.

It has happened three times here, and every time the same way: two plugins, or a
plugin and the host, each picked the obvious name for their own page without
being able to see the other one.

| the collision | fixed by |
|---|---|
| "Store" — the flair shop and the points shop | Store (`/p/store`) and Points store (`/store`) |
| "Stats" — the host's hub and the plugin's snapshot | Stats (`/stats`) and Site snapshot (`/p/stats`) |
| "Sitemap" — the page and the crawler feed | Sitemap (`/sitemap`) and XML sitemap (`/sitemap.xml`) |

The last one shipped a footer link labelled "Sitemap" that served raw XML to
anybody who clicked it. Nothing errored — that is the whole difficulty. A
collision produces a working link to the wrong place, so it is only ever found
by a reader who was confused enough to say so.

The host renames plugin pages in `navPlacement` (`admin_views.go`) rather than
asking the plugin to change: a plugin cannot know what else is on the menu of
the site running it, so the collision is the host's to resolve.

Enforced by `TestNoTwoDestinationsShareALabel`, which collects every label in
the chrome, the account menu and `navPlacement`, and fails when one leads to two
hrefs. The same label for the *same* page is fine — the nav and the footer
should agree.

## Corollary: every staff page must be on the admin subnav

The dropdown used to be the only door to `/moderation` and `/moderation/avatars`,
and `/admin/access` had no entry anywhere — reachable only by typing the URL.
Both are on the bar now. If a page is added under `/admin` or `/moderation` and
does not appear in `adminNav` (`admin_views.go`), it does not exist as far as
anyone using the site is concerned.
