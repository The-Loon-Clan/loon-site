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

**Convert — toggles (~26 forms).** One boolean, no navigation: bookmark, follow,
theme, wishlist add/remove, moderation votes, admin widget reorder, avatar
approve/reject, resend verification. Highest value, lowest risk. *(Bookmark is
done; the rest follow the same shape.)*

**Convert — browse/search/paginate.** The biggest win for an indexer: `/browse`
is 48 KB and carries 42 category links, so changing one filter currently
re-downloads the nav, the featured strip, the footer and all 42 facets to
replace one table. Params in play: `page`, `cat`, `sort`, `q`, `group`, `days`.

**Convert — settings (11 forms).** `settings_security.html` alone has six;
saving any one of them reloads the other five.

**Do not convert — session transitions (7 forms).** `login`, `register`,
`logout`, `reset`, `forgot`, `login_2fa`. These change who you are, and that
should be a real navigation: the URL, the history entry and the session all
move together, which is exactly what you want when it goes wrong.

## Measured effect

The bookmark toggle, before and after, on the same release page:

```
full page reload   ~48,000 bytes
htmx fragment           554 bytes      87x smaller
```

Both paths verified against the running site: `HX-Request: true` returns 200
with a bare fragment (no `<html>`, no `<nav>`); the same POST without the header
returns 303 to the release page, exactly as it did before htmx existed.

## Vendoring

htmx 2.0.7, self-hosted at `web/static/js/htmx.min.js` (51 KB). Not a CDN:
`/static` is the only origin these pages fetch from, and a script that silently
fails to load would take every async control with it. `assetVersion` already
hashes `.js`, so it is cache-busted with everything else.
