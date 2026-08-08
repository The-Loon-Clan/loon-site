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

## Corollary: every staff page must be on the admin subnav

The dropdown used to be the only door to `/moderation` and `/moderation/avatars`,
and `/admin/access` had no entry anywhere — reachable only by typing the URL.
Both are on the bar now. If a page is added under `/admin` or `/moderation` and
does not appear in `adminNav` (`admin_views.go`), it does not exist as far as
anyone using the site is concerned.
