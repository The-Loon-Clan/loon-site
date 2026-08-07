# Fragments: what a plugin renders, and what the host renders around it

A plugin page or widget arrives as an HTML fragment and the host drops it into
a page it already owns. That seam has rules. They were never written down, so
every plugin guessed, and the accessibility audit found the same three mistakes
across six of them.

This is the contract. It is short on purpose.

## 1. The page's name is the host's `<h1>`. A fragment starts at `<h2>`.

`site_page.html` renders the page title as the one `<h1>`. A fragment that adds
its own `<h1>` gives the page two names, and a page with two names has no
outline — a screen reader user navigating by heading cannot tell which one is
the page and which is a section of it.

```
h1  News                 <- host, from the page title
  h2  All Posts          <- fragment
    h3  …
```

Found in: `wiki` (`ameNZB Wiki`), `news` (`All Posts`). Both now `h2`.

## 2. Heading levels are an outline, not a size.

`<h5>` and `<h6>` were being chosen because they looked right. They are not a
type scale; they are the document's structure, and skipping from `h2` to `h6`
leaves a hole a keyboard user falls through.

Set the level from where the heading sits, and the size from CSS. Bootstrap's
`.card-title` sets no `font-size`, so a level change needs one adding — that is
a one-line style, not a reason to keep the wrong level.

Found in: `store` (`h1 → h5`), `dailyreward` (`h2 → h5`, `h2 → h6`).

## 3. A self-titled fragment does not get a titled wrapper.

If a widget renders its own heading, the host must not wrap it in a panel whose
header says the same thing. `/calendar` read:

```
DAILY REWARD          <- host panel header, h2
  Daily reward        <- widget's own title, h5
```

The name twice, and a level skip between them. **The host supplies the box; the
fragment supplies its own name.** The profile page already worked this way — it
drops widgets into a bare card and lets them speak for themselves.

Fixed on both sides: the widgets moved to `h2`, and `calendar.html` dropped its
panel header.

## 4. A `<label>` with no `for=` names nothing.

Both ticket fields had a perfectly good visible `<label>` and no `for`, and the
inputs had no `id`. Clicking the label does not focus the field and a screen
reader announces "edit text".

Use `for`/`id` when there is a visible label. Use `aria-label` only when the
design has no room for one — a magnifier icon and a placeholder, say. **A
placeholder is not a label**: it disappears on the first keystroke, which is
exactly when someone might want to check what the field was.

Found in: `tickets` (subject, priority), `wiki` (search), `messages` (search).

## 5. Name every table.

`<caption class="visually-hidden">` or `aria-label`. "Table with 6 columns" is
not a description of anything, and it is worse when a page has nine of them —
`/admin/jobs` renders one per job group, and all nine announced identically
until each took its group's name.

## Checking

```sh
python scripts/audit_a11y.py
```

It walks a representative page per shape and reports every one of the above.
See `scripts/README.md`.

## Why the host cannot just fix this

It can fix its own half — the wrapper, the panel header, the tables it renders.
It cannot fix a fragment: by the time the host has it, the markup is an opaque
`template.HTML` string, and rewriting somebody else's HTML with string surgery
is how you get a host that breaks a plugin every time the plugin changes.

So the fragment side belongs to the plugin, which is why this file exists rather
than a workaround in `views.go`.
