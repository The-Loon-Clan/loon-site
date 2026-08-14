# Async requests: the standard

How this site replaces part of a page instead of reloading all of it.

The short version: **htmx, server-rendered fragments, and every control still
works with JavaScript off.** The rules below are the ones that keep that true as
the surface grows.

## Why this document exists

The prod indexer (`Indexer/indexer-site`) answered the same question the other
way — hand-written `fetch()` per control, JSON responses, markup rebuilt on the
client — and its tree is the argument against doing that here. Measured Aug 2026:

| | count |
| --- | --- |
| `fetch(` calls | 85 |
| templates containing them | 31 of 99 |
| shared `.js` files between them | **1** |
| `.json()` — server sends data | 69 |
| `innerHTML =` — client rebuilds markup | 65 |
| `location.reload()` — gives up and refreshes anyway | **30** |
| `_csrf` in body vs `X-CSRF-Token` header | 57 vs 12 |

Two things fall out of that table.

**The 65 `innerHTML =` sites are a second view layer.** The server already knows
how to render a release row; sending JSON means writing that knowledge again in
JavaScript, where nothing type-checks it against the original and nothing tells
you when they drift.

**The 30 `location.reload()` calls are what giving up looks like.** The request
is async *and* the page still refreshes — strictly worse than a plain form post,
because you pay for the round trip twice.

And a third, which is the one that decided the design: at least one endpoint,
`/admin/duplicates/purge-all`, sends **neither** the form field nor the header.
The middleware answers 403, `.json()` throws on the HTML error body, and the
`.catch` writes "Failed" on the button. That button has never worked. Nobody
writes that on purpose — they write it by forgetting, once out of 85 times.

So the standard is built to make forgetting impossible rather than to remind
people not to forget.

## The rules

### 1. Every control is a real form or link

htmx attributes **add** behaviour to a working control. They never replace the
mechanism. Delete the `<script>` tag and the site still works — that is the
test, and it is not hypothetical: it is what a failed asset load, a strict
extension, or a slow connection produces.

```html
<form method="post" action="/release/{{.ID}}/bookmark"
      hx-post="/release/{{.ID}}/bookmark"
      hx-target="this" hx-swap="outerHTML">
```

`method`/`action` are the no-JS path. `hx-post` is the same request, made
without navigating. Both are present; they agree.

Enforced by `TestEveryHXPostIsOnARealForm` and
`TestASwappablePartialPostsWhereItsFormPosts` in `htmx_test.go`.

### 2. The server returns HTML, never JSON-for-rendering

The template exists. Use it. JSON is for the API (`/api`, Newznab) where a
machine is the consumer — never for something a browser is about to turn back
into markup.

### 2a. Pick the right swap shape

There are two, and choosing wrong is most of the difficulty in a conversion.

**Self-replacing** — `hx-target="this"`. The control *is* the whole of what
changes. A bookmark button becomes a different bookmark button.

```html
<form hx-post="…" hx-target="this" hx-swap="outerHTML">
```

Reference: `bookmark_button.html`, `follow_button.html`.

**Container** — `hx-target="closest li"` (or `.card`, or an id). The control
changes or removes something *around* it. Marking a wishlist row filled
restyles the whole row and adds a date; removing it deletes the row.

```html
<form hx-post="…" hx-target="closest li" hx-swap="outerHTML">
```

Reference: `wishlist_item.html`.

Two traps in the container shape:

- **Deleting is an empty `200`, never a `204`.** htmx does not swap on 204 at
  all, so the row stays on screen and invites a second click on something
  already gone.
- **The re-render may need a re-read.** Rule 7 says report state from the
  write, and that holds for a toggle whose entire state is the boolean the
  write returned. A row that renders a computed date or an aggregate count is
  different: those are known only to the list query, and reconstructing them
  by hand is inventing the row rather than reading it. Re-read through the
  *same query the page uses* so the swapped row is what a reload would draw.

### 3. A swappable region is a shared partial, used by both paths

One `{{define}}`, in its own file, registered in `sharedPartials`. The page
renders it and the handler re-renders it. They cannot drift because they are the
same bytes.

`web/templates/bookmark_button.html` is the reference implementation.

Partials are not pages, so add the filename to `shellTemplates` in
`templates_test.go` or `TestEveryPageTemplateIsParsed` will fail — which is the
test doing its job.

### 4. The handler branches once, on `isHTMX`

```go
if isHTMX(c) {
    w.renderFragment(c, "release.html", "bookmark-button", data)
    return
}
// ...the redirect that was always here, untouched
```

That single branch is the only difference between the two paths. Keeping the
redirect path literally unchanged is what makes rule 1 real rather than
aspirational.

Use `renderFragment`, not `renderStatus` — the latter executes `base.html` and
would swap an entire second page, navbar and all, into the middle of the first.

### 5. CSRF is automatic, never per call site

One `htmx:configRequest` listener in `site_chrome.html` attaches the token to
every htmx request, including controls added to the page later by a swap. Do not
attach it by hand. Do not add a `_csrf` to an `hx-post`'s data.

This is rule 5 because it is the one prod got wrong 85 different times.

### 6. No `location.reload()`

If a control reloads the page after its request, it was not converted. Either
swap the region that changed or leave it as a plain form post.

### 7. Report state from the write, not a re-read

```go
on, err := w.data.ToggleBookmark(ctx, u.ID, id)   // it already knows
```

Not a follow-up `IsBookmarked`. A re-read answers "what is true now", which
after a concurrent press is a different question from "what did this click do",
and the button is reporting the second one.

### 8. Guard toggles against double-submission

`hx-disabled-elt="find button"` disables the control for the flight of the
request. This matters most for endpoints that **toggle** rather than set: two
clicks land back where they started and look like nothing happened.

## What to convert, and what not to

Derived from the audit of all 52 templates / 49 forms, and cross-checked against
UNIT3D, which spent ~40 of its 70 Livewire components on exactly one category.

**Converted so far.** Bookmark, follow, resend-verification and undo
(self-replacing); wishlist fill/reopen/remove (container); browse and search
filtering and sorting.

Resend and undo were worth more than the others: each previously answered with
a redirect that moved the reader somewhere else to read one sentence — resend
to `/`, off whatever page the banner was on, and undo to `?undone=1` to say one
word.

**Not an htmx candidate — the theme switcher.** It swaps a stylesheet `<link>`,
so there is no region to replace and no fragment to render. The right technique
is the client-side preview `theme.go` already anticipates in its own comment,
reading the deliberately non-HttpOnly cookie. Listed here because it looks like
a toggle and will otherwise be picked up as one.

**Convert — browse/search filtering and sorting.** *(Done.)* Every facet pill
and sortable column header now swaps `#results` instead of reloading. Note there
is no pagination to convert: `/browse` caps at 50 rows with no page parameter,
by an existing decision recorded at the top of `browse.html`.

**Convert — settings (11 forms).** `settings_security.html` alone has six;
saving any one of them reloads the other five.

**Blocked, not merely unconverted — the moderation surfaces.** These look like
the container shape and are not safely convertible yet, for a reason worth
writing down before somebody tries it and half-succeeds.

htmx follows redirects on its own request and swaps whatever comes back. A
handler that answers *some* paths with a fragment and the rest with a redirect
will therefore paste an entire rendered page inside a `<li>` on those paths.
The conversion is only complete when **every** branch has an htmx answer:

| handler | `c.Redirect` branches |
| --- | --- |
| `communityModVote` | **10** |
| avatar moderation | 4 |

Most of those branches carry a message — `?err=you+cannot+vote+on+your+own+avatar`,
`?done=removed` — and the page renders it as a notice. A fragment swap has
nowhere to put that: the target is one card, and the notice belongs outside it.

**The missing piece is an error/notice convention**, and it does not exist in
this document yet. htmx offers the machinery (an out-of-band swap at a notice
region, or `HX-Retarget`); what is missing is the decision about which, applied
once, so twenty handlers do not each invent it. Design that first, then
moderation is mechanical.

Until then these stay full page posts, which is correct behaviour rather than
missing work.

**Do not convert — session transitions (7 forms).** `login`, `register`,
`logout`, `reset`, `forgot`, `login_2fa`. These change who you are, and that
should be a real navigation: the URL, the history entry and the session all
move together, which is exactly what you want when it goes wrong.

## Measured effect

Measured against the running site, not estimated. The two conversions have
very different profiles, and it is worth being straight about that.

**Bookmark toggle** — the whole page was chrome around one button:

```
full page reload   47,000 bytes
htmx fragment           554 bytes      85x smaller
```

**Browse and search** — the saving is the chrome, which is a CONSTANT, so it
shrinks as a percentage the more results there are:

```
/browse?cat=2000    174,553  ->  133,009      42 KB saved   (24%)
/search?q=1080p     559,610  ->  516,621      42 KB saved   ( 8%)
/search (0 results) 559,610  ->      245
```

So the honest case for converting a listing is not mainly bandwidth: it is ~42
KB per click plus no full document re-parse, no scroll-position loss, and no
blank flash between pages. The bandwidth argument is the toggle's.

Paths verified for both:

| request | response |
| --- | --- |
| `HX-Request: true` | the fragment — no `<html>`, no `<nav>` |
| no header (JS off) | the full page, or the 303 it always sent |
| `HX-History-Restore-Request: true` | the **full page** — see below |

### The back button

htmx sends `HX-Request: true` on history-restore requests too, so a handler
checking only that header answers the back button with a fragment, and the
browser paints a bare results table where the site used to be.

Guarded twice: `historyRestoreAsHxRequest:false` in the meta config, and
`isHTMX` refusing any request carrying `HX-History-Restore-Request`. The second
does not depend on the client honouring the first.

### Fragments must carry their own attributes

The returned fragment contains the `hx-get` attributes again, because it is
rendered from the same template. That is what makes the *second* filter click
work, and it is the concrete payoff of rule 3 — a hand-written fragment that
omitted them would work exactly once.

Verified: the `/browse` fragment carries all 13 `hx-get` attributes the full
page does.

### A swap target must survive its own empty state

`search-results` wraps all three shapes of the region — results, no results,
nothing searched yet — in one `<div id="results">`. With the id on the
`<section>` instead, filtering down to zero would replace the section with a
notice carrying no id, and the filter bar would work once and then go dead.
Verified: a zero-result search returns 245 bytes and still contains `#results`.

## Security

Checked against htmx's own guidance, which this section follows.

### Config, set in a meta tag

`site_chrome.html` carries `<meta name="htmx-config">`. It has to be a meta tag
rather than a script: `htmx.min.js` is `defer`red, so a script assigning
`htmx.config` would run too late for the first request.

| flag | value | why |
| --- | --- | --- |
| `allowEval` | `false` | no `hx-on:`, no event filters, no dynamic values |
| `allowScriptTags` | `false` | no fragment here contains a `<script>` |
| `disableSelector` | `…, .prose` | htmx will not process attributes inside user prose |
| `historyCacheSize` | `0` | keeps page snapshots out of `localStorage` |

`selfRequestsOnly` is left at its default of `true`.

`historyCacheSize: 0` is the one with a cost — a back-navigation re-requests
instead of restoring from cache. Taken because this site has private profiles,
members-only pages and an admin area. Revisit deliberately if `hx-push-url`
lands on a hot public path like `/browse`.

### Injected attributes

htmx changes what an injected attribute is worth. Before it, an `hx-post` that
survived sanitisation was inert markup; now it is an instruction the browser
carries out, on the reader's session, when the page loads.

**The defence is `internal/sanitize`, which allowlists attributes** — `hx-post`
never survives markdown, and `TestHTMXAttributesDoNotSurvive` proves it, mutation
included.

`disableSelector` adding `.prose` is defence in depth on top of that, and unlike
the sanitizer it cannot be bypassed by injected content. **It does not cover
everything:** plugin templates render prose without a consistent `.prose`
container, so they rely on the sanitizer alone. Giving plugin prose a shared
container class would close that, and is not done.

### Content-Security-Policy

`internal/middleware/csp.go`, applied to every response including `/static` and
gin's own 404s.

`script-src` carries `'unsafe-inline'`. That is a measured concession, not an
oversight: there are 4 inline `<script>` blocks in host templates and **35 more
across plugin templates**, and plugins are a published contract rendered into
these pages by hosts this repo does not control. A nonce-based policy would
silently blank every one of them.

So this policy does not stop XSS — the sanitizer and `html/template`'s
contextual escaping do. What it stops is the class of attack that works with no
script execution at all:

| directive | stops |
| --- | --- |
| `frame-ancestors 'none'` | clickjacking |
| `form-action 'self'` | a rewritten form action posting a password off-site |
| `base-uri 'self'` | an injected `<base>` re-pointing every relative URL |
| `object-src 'none'` | `<object>`/`<embed>` execution paths |
| `default-src 'self'` | external script, font, frame and connect origins |

`img-src` stays permissive (`'self' data: https:`) because user prose may
legitimately embed a remote image and an image cannot execute. Every image the
host itself renders is local — covers are proxied.

**To remove `'unsafe-inline'`:** move the host's four blocks into files under
`/static`, and give plugins a way to declare a nonce. Real work, not yet done.

`TestUnsafeInlineIsConfinedToScriptAndStyle` exists because `'unsafe-inline'`
reads like a general fix for a blocked resource, and the next person to hit a
CSP error is one paste away from putting it in `default-src`, where it would
silence the whole policy.

Verified on the running site: the JS-created password meter and both reveal
buttons are present in the rendered DOM (so inline scripts still execute), and
the bookmark toggle still returns its 554-byte fragment under `connect-src
'self'`.

## Vendoring

htmx 2.0.7, self-hosted at `web/static/js/htmx.min.js` (51 KB). Not a CDN:
`/static` is the only origin these pages fetch from, and a script that silently
fails to load would take every async control with it. `assetVersion` already
hashes `.js`, so it is cache-busted with everything else.
