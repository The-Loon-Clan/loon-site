#!/usr/bin/env python3
"""Compare two screenshots of the site, so a UI change can be judged instead of
guessed at.

    python scripts/uidiff.py before.png after.png            -> writes uidiff.png
    python scripts/uidiff.py before.png after.png out.png
    python scripts/uidiff.py a.png b.png out.png --crop 0,150,1400,650

Why this exists
---------------
A run of UI changes on this site were each verified by confirming the CSS was
correct and served, and were each wrong anyway — because a NEIGHBOURING element
undid the one being changed. That seam is invisible from either file and
obvious in a picture. Screenshots made it visible; this makes the difference
between two screenshots visible, which is the part a person still had to do by
eye.

What it produces
----------------
One image, three panels: before, after, and a difference map with changed
regions boxed. Plus a printed summary — percentage of pixels changed, and the
bounding box of the change — so "did that do anything at all" has a numeric
answer before anyone squints at it.

The summary matters as much as the picture. A change that reports 0.0% did
NOTHING, whatever the stylesheet says, and that has been the actual situation
more than once here.
"""
import sys
from PIL import Image, ImageChops, ImageDraw

# Below this, a pixel difference is compression noise or antialiasing rather
# than a change anyone asked for. Screenshots of the same page are not
# bit-identical between runs — fonts hint differently, images decode slightly
# differently — so a threshold of 0 reports every capture as "changed".
#
# But a threshold hides SUBTLE changes, and this tool nearly lied because of
# it: restoring a card surface moved the pixels by 6 (#222222 -> #282828), the
# floor was 12, and the summary reported 1% and "regions: 7" for a change that
# covered half the page. A surface one step above the page is a perfectly
# ordinary design change and must not read as noise.
#
# So the floor is lower, overridable with --threshold, and the summary ALWAYS
# reports the mean channel difference — which is threshold-independent and
# catches exactly the uniform, low-amplitude change a mask misses.
NOISE = 4


def load(path):
    return Image.open(path).convert("RGB")


def diff_mask(a, b):
    """A black/white mask of what actually changed, ignoring noise."""
    d = ImageChops.difference(a, b).convert("L")
    return d.point(lambda v: 255 if v > NOISE else 0)


def changed_boxes(mask, min_area=400):
    """Bounding boxes of the changed regions.

    Deliberately coarse — a row of scanline-sized boxes is not more useful than
    one box round the area that moved, and this is read by eye.
    """
    w, h = mask.size
    px = mask.load()
    # Column/row profiles: cheap, and enough to bound the change without a
    # full connected-component pass over a 1400x1000 image in pure Python.
    cols = [any(px[x, y] for y in range(0, h, 2)) for x in range(0, w, 2)]
    rows = [any(px[x, y] for x in range(0, w, 2)) for y in range(0, h, 2)]

    def runs(flags, scale):
        out, start = [], None
        for i, on in enumerate(flags):
            if on and start is None:
                start = i
            elif not on and start is not None:
                out.append((start * scale, i * scale))
                start = None
        if start is not None:
            out.append((start * scale, len(flags) * scale))
        return out

    xs, ys = runs(cols, 2), runs(rows, 2)
    if not xs or not ys:
        return []
    boxes = [(x0, y0, x1, y1) for (x0, x1) in xs for (y0, y1) in ys]
    return [b for b in boxes if (b[2] - b[0]) * (b[3] - b[1]) >= min_area]


def main():
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    if len(args) < 2:
        print(__doc__)
        return 2
    before, after = load(args[0]), load(args[1])
    out_path = args[2] if len(args) > 2 else "uidiff.png"

    crop = None
    for a in sys.argv[1:]:
        if a.startswith("--crop"):
            crop = tuple(int(v) for v in a.split("=", 1)[-1].split(",")) \
                if "=" in a else None
    if crop is None and "--crop" in sys.argv:
        i = sys.argv.index("--crop")
        crop = tuple(int(v) for v in sys.argv[i + 1].split(","))
    if crop:
        before, after = before.crop(crop), after.crop(crop)

    # Different sizes are a real answer, not an error: a layout change that
    # alters page height is exactly the kind of thing worth reporting.
    if before.size != after.size:
        print(f"SIZE CHANGED  before {before.size}  after {after.size}")
        w = min(before.width, after.width)
        h = min(before.height, after.height)
        before, after = before.crop((0, 0, w, h)), after.crop((0, 0, w, h))

    for a in sys.argv[1:]:
        if a.startswith("--threshold"):
            global NOISE
            NOISE = int(a.split("=", 1)[1]) if "=" in a else NOISE
    if "--threshold" in sys.argv:
        NOISE = int(sys.argv[sys.argv.index("--threshold") + 1])

    mask = diff_mask(before, after)
    changed = sum(mask.point(lambda v: 1 if v else 0)
                  .getdata())
    total = mask.width * mask.height
    pct = 100.0 * changed / total

    boxes = changed_boxes(mask)
    heat = Image.merge("RGB", (mask, Image.new("L", mask.size, 0), Image.new("L", mask.size, 0)))
    marked = after.copy()
    d = ImageDraw.Draw(marked)
    for b in boxes:
        d.rectangle(b, outline=(255, 60, 60), width=3)

    gap = 12
    sheet = Image.new("RGB", (before.width * 3 + gap * 2, before.height), (18, 18, 20))
    sheet.paste(before, (0, 0))
    sheet.paste(marked, (before.width + gap, 0))
    sheet.paste(heat, (before.width * 2 + gap * 2, 0))
    sheet.save(out_path)

    # Threshold-independent, so a uniform low-amplitude shift (a surface one
    # step above the page) cannot hide under the noise floor.
    grey = ImageChops.difference(before, after).convert("L")
    hist = grey.histogram()
    mean = sum(i * n for i, n in enumerate(hist)) / float(total)
    peak = max(i for i, n in enumerate(hist) if n) if any(hist) else 0

    print(f"changed pixels: {pct:.2f}% (threshold {NOISE})   regions: {len(boxes)}")
    print(f"mean channel delta: {mean:.2f}   peak: {peak}")
    if mean == 0:
        print("IDENTICAL — whatever was edited did not reach the page")
    elif pct < 1 and mean > 0.5:
        print("SUBTLE, WIDE change — low amplitude over a large area "
              "(a surface or tint shift). Trust the mean, not the mask.")
    for b in boxes[:8]:
        print(f"  box x{b[0]}..{b[2]}  y{b[1]}..{b[3]}")
    print(f"wrote {out_path}  (before | after+boxes | difference)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
