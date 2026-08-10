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
