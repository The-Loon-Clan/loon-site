#!/usr/bin/env python3
"""Measure a design's GEOMETRY out of a screenshot, so two of them can be
compared as numbers instead of as adjectives.

    python scripts/uispec.py refs/target_home.png
    python scripts/uispec.py refs/target_home.png refs/_live_now.png   # diff

Why this exists
---------------
The other tools in this folder answer "did anything change" (uidiff) and "show
me these two regions side by side" (uimatch). Neither answers the question that
actually keeps coming up, which is "what, specifically, is different" — and the
person at the keyboard has been left to translate a picture into prose, then me
to translate the prose back into CSS. Both translations lose, and the second
one has sent me at the wrong element repeatedly.

A screenshot already contains the answer. Card edges, poster widths, the gap
between them, the inset from the card, the heading's cap height and colour, are
all recoverable from pixels. Recovering them turns "not even close" into a
table of numbers where every row is either equal or is a specific value to
change.

What it measures
----------------
Given the featured-strip band it finds, for each image:

    card       the surface's box, and the page background it sits on
    inset      distance from the card edge to the first poster
    poster     width, height, and the gutter between adjacent posters
    heading    cap height, colour, x-origin, and whether it is INSIDE the
               card or outside it on the page background

The last one is the structural difference that prose kept failing to convey.

How it finds things
-------------------
Artwork has high per-column colour variance; a flat card surface has almost
none. So a column-variance profile across the band separates posters from
gutters without knowing anything about the markup. The card is then the
surrounding run of near-uniform colour, and the page background is what lies
beyond that.

This is deliberately independent of the DOM. The reference is a PNG somebody
pasted in — there is no DOM to ask — so the only measurement that can be
applied to BOTH sides is one taken from pixels.
"""
import sys
from PIL import Image

# A column of artwork varies a lot down its height; a column of flat card
# surface varies by a rounding error. The gap between those two populations is
# enormous, so the threshold is not delicate.
VARIANCE_POSTER = 300
# Runs narrower than this are antialiasing on a border, not a poster.
MIN_POSTER_W = 40
MIN_GUTTER_W = 3


def col_variance(im, x, y0, y1):
    """Mean per-channel variance of one column over a row range."""
    n = y1 - y0
    if n <= 1:
        return 0.0
    sums = [0, 0, 0]
    sqs = [0, 0, 0]
    px = im.load()
    for y in range(y0, y1):
        c = px[x, y]
        for i in range(3):
            sums[i] += c[i]
            sqs[i] += c[i] * c[i]
    var = 0.0
    for i in range(3):
        m = sums[i] / n
        var += sqs[i] / n - m * m
    return var / 3.0


def row_variance(im, y, x0, x1):
    n = x1 - x0
    if n <= 1:
        return 0.0
    sums = [0, 0, 0]
    sqs = [0, 0, 0]
    px = im.load()
    for x in range(x0, x1):
        c = px[x, y]
        for i in range(3):
            sums[i] += c[i]
            sqs[i] += c[i] * c[i]
    var = 0.0
    for i in range(3):
        m = sums[i] / n
        var += sqs[i] / n - m * m
    return var / 3.0


def runs(flags, lo=0):
    """Contiguous True runs as (start, end) in the flag list's own index."""
    out, start = [], None
    for i, on in enumerate(flags):
        if on and start is None:
            start = i
        elif not on and start is not None:
            out.append((start + lo, i + lo))
            start = None
    if start is not None:
        out.append((start + lo, len(flags) + lo))
    return out


def hexof(c):
    return "#%02x%02x%02x" % tuple(int(v) for v in c[:3])


def find_poster_band(im):
    """The vertical band holding the poster artwork.

    Found by row variance: a row crossing several posters varies wildly, a row
    of card surface or body text does not vary nearly as much.
    """
    w, h = im.size
    step = 4
    prof = [(y, row_variance(im, y, 0, w)) for y in range(0, h, step)]
    if not prof:
        return None
    peak = max(v for _, v in prof)
    if peak <= 0:
        return None
    hot = [v > peak * 0.45 for _, v in prof]
    bands = runs(hot)
    if not bands:
        return None
    # The tallest such band is the poster strip.
    a, b = max(bands, key=lambda r: r[1] - r[0])
    return prof[a][0], prof[min(b, len(prof) - 1)][0]


def measure(path, band=None):
    im = Image.open(path).convert("RGB")
    w, h = im.size
    out = {"path": path, "size": (w, h)}

    # An explicit band beats the auto-detector and is worth passing whenever
    # the answer matters. Auto-detection picked the tallest high-variance run
    # on this very page and got a band spanning several sections, which then
    # reported a colour sampled from inside a poster as the "page background".
    # A wrong band does not fail loudly — it produces plausible numbers.
    if band is None:
        band = find_poster_band(im)
    if band is None:
        out["error"] = "no poster band found"
        return out
    y0, y1 = band
    # Sample the middle 60% of the band: the very top and bottom edges blend
    # into the card through the posters' own rounded corners.
    sy0 = y0 + (y1 - y0) // 5
    sy1 = y1 - (y1 - y0) // 5
    out["band"] = (y0, y1)

    # Find posters by finding what ISN'T one.
    #
    # Detecting artwork by column variance sounds right and is not: a poster
    # with dark edges (a night sky, a black border) has flat columns at its
    # own margins, so its run shrinks inward and the measured width comes out
    # short. The Mentalist's poster measured 50px wide that way, against a
    # true 120.
    #
    # The card surface, by contrast, is one exact flat colour covering every
    # gutter and both insets — so it is the modal colour of the band. Classify
    # each column by how much of it matches that colour, and the posters are
    # simply the runs that do not. Dark artwork stays dark artwork.
    px = im.load()
    counts = {}
    for y in range(sy0, sy1, 2):
        for x in range(0, w, 2):
            c = px[x, y]
            counts[c] = counts.get(c, 0) + 1
    ranked = [c for c, _ in sorted(counts.items(), key=lambda kv: -kv[1])]

    # The modal colour is USUALLY the card. It stops being so once the posters
    # grow enough to cover most of it, at which point the page background wins
    # the count and everything downstream measures the viewport instead of the
    # card — reported as a card 1305px wide starting at x=0, which is a wrong
    # answer wearing the same clothes as a right one.
    #
    # A card cannot reach both image edges, so try candidates in order of
    # frequency and take the first that does not. Rejecting a bad measurement
    # matters more here than producing one.
    def spans_whole_image(col):
        my_ = (sy0 + sy1) // 2
        return (all(abs(int(a) - int(b)) <= 12 for a, b in zip(px[2, my_], col))
                and all(abs(int(a) - int(b)) <= 12
                        for a, b in zip(px[w - 3, my_], col)))

    card_col = ranked[0]
    for cand in ranked[:6]:
        if not spans_whole_image(cand):
            card_col = cand
            break
    else:
        out["warning"] = "card colour indistinguishable from page background"

    def same(c, d, tol=12):
        return all(abs(int(a) - int(b)) <= tol for a, b in zip(c[:3], d[:3]))

    rows = list(range(sy0, sy1, 2))
    is_card = []
    for x in range(w):
        hit = sum(1 for y in rows if same(px[x, y], card_col))
        is_card.append(hit >= len(rows) * 0.85)
    is_art = [not v for v in is_card]
    posters = [r for r in runs(is_art) if r[1] - r[0] >= MIN_POSTER_W]
    # A run touching the image edge is a poster the screenshot cut in half; its
    # width is an artefact of the viewport, not of the design.
    posters = [r for r in posters if r[0] > 0 and r[1] < w]
    out["posters"] = posters
    out["card_colour"] = hexof(card_col)

    if not posters:
        out["error"] = "no posters found in band"
        return out

    widths = [b - a for a, b in posters]
    gaps = [posters[i + 1][0] - posters[i][1] for i in range(len(posters) - 1)]
    out["poster_w"] = widths
    out["gaps"] = [g for g in gaps if g >= MIN_GUTTER_W]

    # The card: walk out from the first poster while the surface still matches
    # the card colour. Where it stops is the card's edge, and the colour just
    # beyond it is the page background.
    my = (sy0 + sy1) // 2
    first_x = posters[0][0]
    lx = first_x
    while lx > 0 and same(px[lx - 1, my], card_col):
        lx -= 1
    rx = posters[-1][1]
    while rx < w - 1 and same(px[rx + 1, my], card_col):
        rx += 1
    out["card_x"] = (lx, rx)
    out["inset_left"] = first_x - lx
    # Sampled clear of the edge: right against it is the border's antialiasing,
    # which is neither colour and reads as a third one.
    out["page_bg"] = hexof(px[max(lx - 8, 0), my])

    # Poster height, measured down a column known to be inside the first
    # poster. Together with the width this gives the aspect the design uses.
    mx = (posters[0][0] + posters[0][1]) // 2
    ty = y0
    while ty > 0 and not same(px[mx, ty - 1], card_col):
        ty -= 1
    by = y1
    while by < h - 1 and not same(px[mx, by + 1], card_col):
        by += 1
    out["poster_h"] = by - ty
    out["poster_y"] = (ty, by)

    # The card's top edge, and therefore whether the heading is inside it.
    cy = ty
    while cy > 0 and same(px[lx + 3, cy - 1], card_col, tol=14):
        cy -= 1
    out["card_top"] = cy
    out["inset_top"] = ty - cy

    # The heading: the nearest band of bright pixels above the card's content.
    #
    # Scanned STRICTLY above the card top. Letting the window reach into the
    # card put poster artwork in the sample and reported a 157px "cap height"
    # for a line of 13px text — a wrong number that still looked like a
    # measurement.
    # The window runs from well above the card down to the first poster, and
    # covers BOTH possible placements — above the card on the page, or inside
    # it in the space over the posters. Which one it turns out to be is the
    # measurement, not an assumption baked into where we look.
    bright_rows = []
    for y in range(max(cy - 80, 0), ty):
        bright = 0
        for x in range(max(lx - 40, 0), min(lx + 420, w)):
            c = px[x, y]
            if c[0] + c[1] + c[2] > 420:
                bright += 1
        if bright >= 3:
            bright_rows.append(y)
    # Group into lines, then keep the one CLOSEST to the posters. The window
    # reaches past the heading into whatever sits above the section, and taking
    # first-to-last bright row spanned both — a 13px heading measured 58px tall
    # because the row above it was included.
    lines = []
    for y in bright_rows:
        if lines and y - lines[-1][-1] <= 3:
            lines[-1].append(y)
        else:
            lines.append([y])
    head_rows = lines[-1] if lines else []
    inside = bool(head_rows) and head_rows[0] >= cy
    if head_rows:
        hy0, hy1 = head_rows[0], head_rows[-1] + 1
        out["heading_y"] = (hy0, hy1)
        out["heading_cap_h"] = hy1 - hy0
        out["heading_inside_card"] = inside
        # x-origin and colour of the heading's own glyphs
        hx = None
        samples = []
        for y in range(hy0, hy1):
            for x in range(max(lx - 40, 0), min(lx + 420, w)):
                c = px[x, y]
                if c[0] + c[1] + c[2] > 420:
                    if hx is None or x < hx:
                        hx = x
                    samples.append(c)
        out["heading_x"] = hx
        if samples:
            n = len(samples)
            out["heading_colour"] = hexof(tuple(
                sum(s[i] for s in samples) // n for i in range(3)))
    return out


def show(m):
    print(f"\n=== {m['path']}  {m['size'][0]}x{m['size'][1]}")
    if "error" in m:
        print(f"    ! {m['error']}")
        return
    print(f"    page background   {m['page_bg']}")
    print(f"    card              x {m['card_x'][0]}..{m['card_x'][1]}"
          f"  (w {m['card_x'][1] - m['card_x'][0]})   top y {m['card_top']}"
          f"   {m['card_colour']}")
    print(f"    inset             left {m['inset_left']}px   top {m['inset_top']}px")
    print(f"    posters           {len(m['posters'])} found   "
          f"widths {m['poster_w'][:6]}")
    if "poster_h" in m:
        pw = max(set(m["poster_w"]), key=m["poster_w"].count)
        print(f"    poster size       {pw} x {m['poster_h']}"
              f"   (aspect {m['poster_h'] / pw:.2f})")
    print(f"    gutter            {m['gaps'][:6]}")
    if "heading_cap_h" in m:
        where = "INSIDE the card" if m["heading_inside_card"] else "outside, on the page"
        print(f"    heading           {where}   cap height {m['heading_cap_h']}px"
              f"   x {m['heading_x']}   {m.get('heading_colour', '?')}")


def diff(a, b):
    print("\n" + "=" * 64)
    print("DIFFERENCES  (reference -> live)")
    print("=" * 64)
    rows = []

    def cmp(label, va, vb, unit=""):
        same = va == vb
        rows.append((label, va, vb, unit, same))

    cmp("page background", a.get("page_bg"), b.get("page_bg"))
    cmp("card colour", a.get("card_colour"), b.get("card_colour"))
    cmp("card left x", a["card_x"][0], b["card_x"][0], "px")
    cmp("card width", a["card_x"][1] - a["card_x"][0],
        b["card_x"][1] - b["card_x"][0], "px")
    cmp("inset left", a.get("inset_left"), b.get("inset_left"), "px")
    cmp("inset top", a.get("inset_top"), b.get("inset_top"), "px")
    if a.get("poster_w") and b.get("poster_w"):
        cmp("poster width", max(set(a["poster_w"]), key=a["poster_w"].count),
            max(set(b["poster_w"]), key=b["poster_w"].count), "px")
    if "poster_h" in a and "poster_h" in b:
        cmp("poster height", a["poster_h"], b["poster_h"], "px")
    if a.get("gaps") and b.get("gaps"):
        cmp("gutter", min(a["gaps"]), min(b["gaps"]), "px")
    cmp("posters visible", len(a.get("posters", [])), len(b.get("posters", [])))
    if "heading_cap_h" in a and "heading_cap_h" in b:
        cmp("heading inside card", a["heading_inside_card"], b["heading_inside_card"])
        cmp("heading cap height", a["heading_cap_h"], b["heading_cap_h"], "px")
        cmp("heading colour", a.get("heading_colour"), b.get("heading_colour"))

    width = max(len(r[0]) for r in rows)
    for label, va, vb, unit, same in rows:
        mark = "  ok " if same else "  >> "
        sa = f"{va}{unit}"
        sb = f"{vb}{unit}"
        print(f"{mark}{label.ljust(width)}   {str(sa).rjust(9)}   {str(sb).rjust(9)}")
    bad = [r for r in rows if not r[4]]
    print(f"\n{len(bad)} of {len(rows)} differ.")


def main():
    argv = sys.argv[1:]
    args = [a for a in argv if not a.startswith("--")]
    if not args:
        print(__doc__)
        return 2
    # --band=y0,y1 once per image, in the same order as the images. The joined
    # form on purpose: as two tokens the value reads as a positional filename.
    bands = []
    for a in argv:
        if a.startswith("--band="):
            y0, y1 = (int(v) for v in a.split("=", 1)[1].split(","))
            bands.append((y0, y1))
    while len(bands) < len(args):
        bands.append(None)
    ms = [measure(p, b) for p, b in zip(args, bands)]
    for m in ms:
        show(m)
    if len(ms) == 2 and "error" not in ms[0] and "error" not in ms[1]:
        diff(ms[0], ms[1])
    return 0


if __name__ == "__main__":
    sys.exit(main())
