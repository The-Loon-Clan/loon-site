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

`_match_*.png` and `*.png` here are ignored by git; these are working images,
not source.
