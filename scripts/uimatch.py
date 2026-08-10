#!/usr/bin/env python3
"""Compare a REFERENCE design against the live page, and score how close it is.

    python scripts/uimatch.py refs/featured.png            # whole home page
    python scripts/uimatch.py refs/featured.png /browse
    python scripts/uimatch.py refs/featured.png / --band 150,620

Workflow
--------
1. Drop the target screenshot in refs/ (any name).
2. Run this. It screenshots the live site, scales both to one width, writes
   refs/_match_<name>.png as a side-by-side, and prints a comparison.
3. Read that image, change something, run it again, watch the numbers move.

Why a SCORE and not just a picture
----------------------------------
A reference and a live page never share content — different releases, different
posters, different text — so a pixel diff between them is meaningless. What IS
comparable is structure: where the surfaces are, what colour they are, and
where the edges fall. Those are the things a design ask is usually about
("no background", "rounded", "no padding"), and they survive the content being
different.

So this reports, per horizontal band:

  * the dominant colour of the page GUTTER (x near the edges) — the canvas
  * the dominant colour of the CONTENT column (x in the middle) — surfaces
  * whether the two differ, which is what "does this block have a background"
    actually means in pixels

and a row-profile of horizontal edges, which is where cards start and stop.

Read the printed table first. It answers "is there a surface here and what
colour" without any squinting, and squinting is what has been wrong.
"""
import os
import subprocess
import sys
from collections import Counter

from PIL import Image

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)

# Chrome is invoked DIRECTLY rather than through shot.sh. This script is run by
# Windows Python, which has no `bash` on PATH — spawning the shell wrapper
# failed with a message that read like the site was down when it was serving
# 200s. One less layer, and one less thing to misdiagnose.
CHROME_CANDIDATES = [
    r"C:\Program Files\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
    r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe",
    r"C:\Program Files\Microsoft\Edge\Application\msedge.exe",
]
SHOTS_DIR = ("C:/Users/johnm/AppData/Local/Temp/claude/"
             "c--GitHub-loon-demo-site/376ebf67-236e-4e70-a798-6ef173bfba7e/"
             "scratchpad/shots")


def browser():
    for c in CHROME_CANDIDATES:
        if os.path.exists(c):
            return c
    return None


def shoot(path, out_name, w=1400, h=1000):
    """Screenshot the live site and return the PNG path, or None."""
    exe = browser()
    if exe is None:
        print("no Chrome or Edge found")
        return None
    os.makedirs(SHOTS_DIR, exist_ok=True)
    out = f"{SHOTS_DIR}/{out_name}.png"
    if os.path.exists(out):
        os.remove(out)
    subprocess.run([
        exe, "--headless=new", "--disable-gpu", "--hide-scrollbars",
        f"--window-size={w},{h}", "--virtual-time-budget=3000",
        f"--screenshot={out}", f"http://localhost:8090{path}",
    ], capture_output=True, text=True)
    return out if os.path.exists(out) else None


def dominant(im, box):
    """The most common colour in a region, quantised so near-identical shades
    (gradients, antialiasing) count as one."""
    crop = im.crop(box).convert("RGB")
    crop = crop.resize((max(1, crop.width // 4), max(1, crop.height // 4)))
    c = Counter((r // 6 * 6, g // 6 * 6, b // 6 * 6) for r, g, b in crop.getdata())
    return c.most_common(1)[0][0]


def hexs(rgb):
    return "#%02x%02x%02x" % rgb


def profile(im, label, bands):
    """Per-band gutter vs content colour — 'is there a surface here'."""
    w, h = im.size
    rows = []
    for (y0, y1) in bands:
        if y1 > h:
            y1 = h
        if y0 >= y1:
            continue
        gutter = dominant(im, (2, y0, max(3, int(w * 0.03)), y1))
        content = dominant(im, (int(w * 0.35), y0, int(w * 0.65), y1))
        delta = max(abs(a - b) for a, b in zip(gutter, content))
        rows.append((y0, y1, gutter, content, delta))
    print(f"\n{label}")
    print("   band        gutter    content   diff  surface?")
    for y0, y1, g, c, d in rows:
        print(f"   {y0:>4}-{y1:<5} {hexs(g)}   {hexs(c)}  {d:>3}  "
              f"{'YES' if d > 6 else 'no'}")
    return rows


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    ref_path = sys.argv[1]
    page = sys.argv[2] if len(sys.argv) > 2 and not sys.argv[2].startswith("--") else "/"
    # MSYS rewrites a bare "/" argument into the Git install path before the
    # process ever sees it, so "/" arrives as "C:/Program Files/Git/". Left
    # alone that builds a nonsense URL and screenshots an error page, which
    # then reads as "the design does not match" — a wrong answer with no hint
    # that the input was mangled.
    if ":" in page or "\\" in page or page.startswith("C:/"):
        page = "/"
    if not page.startswith("/"):
        page = "/" + page
    if not os.path.exists(ref_path):
        print(f"reference not found: {ref_path}")
        print("Drop the target screenshot in refs/ and pass its path.")
        return 2

    ref = Image.open(ref_path).convert("RGB")
    live_path = shoot(page, "_uimatch_live", 1400, 1000)
    if not live_path:
        print("could not screenshot the live site — is it up on :8090?")
        return 1
    live = Image.open(live_path).convert("RGB")

    # One width, so bands line up and a side-by-side is readable. The
    # reference is usually a crop at a different scale; matching WIDTH keeps
    # horizontal proportions comparable, which is what layout questions are
    # about.
    tw = 900
    ref_s = ref.resize((tw, max(1, int(ref.height * tw / ref.width))))
    live_s = live.resize((tw, max(1, int(live.height * tw / live.width))))

    band_arg = None
    if "--band" in sys.argv:
        band_arg = sys.argv[sys.argv.index("--band") + 1]
    if band_arg:
        y0, y1 = (int(v) for v in band_arg.split(","))
        bands = [(y0, y1)]
    else:
        step = 80
        bands = [(y, y + step) for y in range(0, min(ref_s.height, live_s.height), step)]

    profile(ref_s, f"REFERENCE  {os.path.basename(ref_path)}", bands)
    profile(live_s, f"LIVE       {page}", bands)

    gap = 16
    H = max(ref_s.height, live_s.height)
    sheet = Image.new("RGB", (tw * 2 + gap, H), (18, 18, 20))
    sheet.paste(ref_s, (0, 0))
    sheet.paste(live_s, (tw + gap, 0))
    out = os.path.join(os.path.dirname(ref_path) or ".",
                       "_match_" + os.path.basename(ref_path))
    sheet.save(out)
    print(f"\nwrote {out}   (reference | live)")
    print("Read that image, then compare the two tables above band by band:")
    print("a band where the reference says 'no' and live says 'YES' is a "
          "surface the design does not have.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
