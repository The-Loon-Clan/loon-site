# Design references

Drop a target screenshot here, then:

    python scripts/uimatch.py refs/<file>.png [page] [--band y0,y1]

It screenshots the live site, writes `refs/_match_<file>.png` as a
reference-beside-live sheet, and prints a per-band table of gutter vs content
colour — which answers "does this block have a background, and what colour"
without anyone squinting at it.

A reference and the live page never share content, so a pixel diff between them
is meaningless. What is comparable is structure: where surfaces are, what
colour they are, where edges fall — which is what design asks are usually
about ("no background", "rounded", "no padding").

## Pointing at what is wrong

The generated sheet is `refs/_match_<file>.png` — reference on the LEFT, the
real site on the RIGHT, with a labelled grid over both.

**The grid is a shared vocabulary.** Cells are the same size in both panels, so
`B3` is the same place on each. Open the sheet and say what is wrong by cell:

    "B3 the poster tray has a background, the reference does not"
    "G1 is missing the Trending pill"
    "A6 the footer should be one line"

That replaces "the bit under the heading on the left", which has cost several
rounds of guessing. Cells are ~110px, so one names a region rather than a pixel.

The CONTENT will never match — different releases, different posters. Only the
structure and colour are comparable, and those are what the printed table
measures.

`_match_*.png` and `*.png` here are ignored by git; these are working images,
not source.

## The focus loop

Prose has repeatedly sent work at the wrong element. This removes the
description step entirely.

1. `LOON_UI_INSPECT=1 docker compose up -d app`
2. Open **http://localhost:8090/dev/compare**
3. Pick the reference from the dropdown. **Zoom** it until it lines up with the
   live pane beside it.
4. **Box select: ON**, then drag a box round the same thing on EACH side.
5. Type **what is wrong** in the note box.
6. **Send to Claude** — writes `refs/_focus.json`.

There is no step 7. Claude watches that file with a background poll, so
pressing Send wakes it: no switching back to the editor, no describing where
to look. It reads the rectangles, the note, and runs
`python scripts/uimatch.py --focus` itself.

That crops both sides to the saved rectangles and prints the mean colour and
palette of each, plus `refs/_focus_compare.png` with the two crops side by side.

The rectangles are stored in each source's OWN pixels — the reference image's
natural size, the live page's CSS pixels — so they survive zooming, resizing
the window, and screenshots taken at any width.

The **Picker** is the other half: turn it on and click an element in the live
pane to get its selector, classes and the computed values that actually paint
it (background, border, radius, shadow, padding). Paste that and there is
nothing left to translate.
