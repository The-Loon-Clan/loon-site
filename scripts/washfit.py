#!/usr/bin/env python3
"""Fit a page-background wash to a reference, by measuring instead of eyeballing.

    python scripts/washfit.py

Why
---
The canvas wash is several overlapping gradients, and its effect is a broad
light/dark FIELD across the viewport rather than anything visible in any one
declaration. Reading the CSS tells you nothing about whether the field matches;
only a render does. And rebuilding the container to see each attempt costs a
minute, so a five-candidate comparison costs five minutes and nobody does it.

This renders candidate washes straight to a file:// page at the real viewport
size, samples each one's horizontal brightness profile across a band of pure
background, and scores it against the same profile taken from the reference
screenshot. Iteration drops from a minute to about a second.

The profile is the measurement that matters here. The reference varies in
SEVERAL zones across the width — light, dip, light again, then dark — while a
two-radial wash gives one bright corner and then nothing. That difference is
invisible in a colour picker and obvious in a profile.
"""
import subprocess
import sys
from pathlib import Path

from PIL import Image

CHROME = r"C:\Program Files\Google\Chrome\Application\chrome.exe"
SCRATCH = Path(
    r"C:\Users\johnm\AppData\Local\Temp\claude\c--GitHub-loon-demo-site"
    r"\376ebf67-236e-4e70-a798-6ef173bfba7e\scratchpad"
)
W, H = 1400, 1000
COLS = 12

# The page colour each wash is composited over — cosmic-void's --bg.
BASE = "#222222"


def profile(im, y0, y1, cols=COLS):
    """Mean brightness at `cols` sample points across a band, minus the band mean.

    Relative, not absolute: what is being compared is the SHAPE of the field,
    not its overall level, which the base colour already sets.
    """
    px = im.load()
    xs = [int(i * (im.width - 1) / (cols - 1)) for i in range(cols)]
    vals = []
    for x in xs:
        s = [sum(px[min(x, im.width - 1), y]) for y in range(y0, y1)]
        vals.append(sum(s) / len(s))
    m = sum(vals) / len(vals)
    return [v - m for v in vals]


def render(css, name):
    html = f"""<!doctype html><meta charset=utf-8><style>
html,body{{margin:0;height:2000px}}
body{{background:{css},{BASE};background-attachment:fixed;}}
</style>"""
    p = SCRATCH / f"{name}.html"
    p.write_text(html, encoding="utf-8")
    out = SCRATCH / f"{name}.png"
    subprocess.run(
        [CHROME, "--headless=new", "--disable-gpu", "--hide-scrollbars",
         f"--window-size={W},{H}", "--virtual-time-budget=1500",
         f"--screenshot={out}", p.as_uri()],
        capture_output=True,
    )
    return Image.open(out).convert("RGB")


def score(got, want):
    """Sum of absolute differences between the two profiles."""
    return sum(abs(a - b) for a, b in zip(got, want))


# The band is expressed as a FRACTION of viewport height, not pixels, because
# the reference screenshot and the candidate renders are different sizes and
# the wash is attachment:fixed with vh-sized stops — so the same fraction is
# the same place in the field, and the same pixel row is not.
#
# 0.1225..0.1495 is the widest run of pure background above the featured card.
# Measured by per-row spread across the full width: rows there vary by 32-38
# levels, and from y=124 the spread jumps to 90 and then 555 as the Trending
# pill and the section heading enter the band. Scoring against those rows was
# scoring against content — it ranked the wash we already ship as the best fit
# because its bright left corner happened to sit under the heading.
BAND_TOP, BAND_BOT = 0.1225, 0.1495


def clean_bands(im, n=4, max_spread=60, min_rows=8):
    """The widest content-free row-run in each of n horizontal slices.

    "Content-free" is judged by how much a row varies across the full width: a
    row of pure canvas varies by the wash alone (30-40 levels here), a row
    crossing a heading or a poster varies by hundreds. This is what stops a
    band that looks like background from quietly containing a section title —
    which happened, and ranked the wash already shipping as the best fit
    because its bright corner sat under that title.
    """
    px = im.load()
    out = []
    for s in range(n):
        lo = int(im.height * (s / n))
        hi = int(im.height * ((s + 1) / n))
        ok = []
        for y in range(lo, hi):
            v = [sum(px[x, y]) for x in range(0, im.width, 7)]
            if max(v) - min(v) < max_spread:
                ok.append(y)
        best, cur = [], []
        for y in ok:
            if cur and y == cur[-1] + 1:
                cur.append(y)
            else:
                if len(cur) > len(best):
                    best = cur
                cur = [y]
        if len(cur) > len(best):
            best = cur
        if len(best) >= min_rows:
            out.append((best[0], best[-1]))
    return out


def main():
    # A "foundation" reference — the design with its content removed, leaving
    # only the canvas and empty containers — is worth far more here than the
    # full screenshot, because the thing being fitted is the canvas and the
    # content is pure interference. With one present the fit runs over several
    # bands down the page instead of the single 22px strip the full screenshot
    # leaves uncovered.
    foundation = Path("refs/target_foundation.png")
    ref_path = foundation if foundation.exists() else Path("refs/target_home.png")
    ref = Image.open(ref_path).convert("RGB")
    bands = clean_bands(ref) if foundation.exists() else []
    if bands:
        print(f"reference: {ref_path.name} — {len(bands)} clean bands "
              f"{[f'y{a}..{b}' for a, b in bands]}")
        want = [v for a, b in bands
                for v in profile(ref.crop((0, a, ref.width, b)), 0, b - a)]
        frac = [(a / ref.height, b / ref.height) for a, b in bands]
    else:
        ry0, ry1 = int(ref.height * BAND_TOP), int(ref.height * BAND_BOT)
        print(f"reference: {ref_path.name} — single band y{ry0}..{ry1} "
              f"of {ref.height} (no foundation layer; drop one at "
              f"refs/target_foundation.png for a fuller fit)")
        want = profile(ref.crop((0, ry0, ref.width, ry1)), 0, ry1 - ry0)
        frac = [(BAND_TOP, BAND_BOT)]
    globals()["FRACS"] = frac
    print("reference profile (relative, luma-sum):")
    print("   " + " ".join(f"{v:+6.1f}" for v in want))
    print()

    candidates = {
        # what we ship today: one huge glow left, one weaker right
        "current": (
            "linear-gradient(180deg,rgba(255,255,255,0.055),rgba(255,255,255,0)),"
            "radial-gradient(92vw 130vh at 12% 14%,rgba(33,149,243,0.15),rgba(33,149,243,0) 62%),"
            "radial-gradient(84vw 118vh at 88% 6%,rgba(33,149,243,0.10),rgba(33,149,243,0) 58%)"
        ),
        # several smaller zones, plus dark ones to make the troughs
        "zones-a": (
            "linear-gradient(180deg,rgba(255,255,255,0.05),rgba(255,255,255,0)),"
            "radial-gradient(46vw 60vh at 14% 12%,rgba(33,149,243,0.14),rgba(33,149,243,0) 70%),"
            "radial-gradient(34vw 46vh at 52% 22%,rgba(33,149,243,0.10),rgba(33,149,243,0) 68%),"
            "radial-gradient(40vw 54vh at 86% 60%,rgba(33,149,243,0.07),rgba(33,149,243,0) 66%),"
            "radial-gradient(44vw 56vh at 84% 10%,rgba(0,0,0,0.16),rgba(0,0,0,0) 70%)"
        ),
        "zones-b": (
            "linear-gradient(180deg,rgba(255,255,255,0.05),rgba(255,255,255,0)),"
            "radial-gradient(40vw 55vh at 16% 14%,rgba(33,149,243,0.13),rgba(33,149,243,0) 72%),"
            "radial-gradient(30vw 42vh at 50% 18%,rgba(33,149,243,0.11),rgba(33,149,243,0) 70%),"
            "radial-gradient(38vw 50vh at 30% 70%,rgba(33,149,243,0.08),rgba(33,149,243,0) 68%),"
            "radial-gradient(46vw 60vh at 90% 16%,rgba(0,0,0,0.20),rgba(0,0,0,0) 72%),"
            "radial-gradient(36vw 48vh at 70% 88%,rgba(0,0,0,0.12),rgba(0,0,0,0) 70%)"
        ),
        "zones-c": (
            "linear-gradient(180deg,rgba(255,255,255,0.045),rgba(255,255,255,0)),"
            "radial-gradient(36vw 50vh at 14% 16%,rgba(33,149,243,0.14),rgba(33,149,243,0) 74%),"
            "radial-gradient(26vw 38vh at 48% 20%,rgba(33,149,243,0.12),rgba(33,149,243,0) 72%),"
            "radial-gradient(34vw 46vh at 26% 66%,rgba(33,149,243,0.09),rgba(33,149,243,0) 70%),"
            "radial-gradient(30vw 42vh at 62% 82%,rgba(33,149,243,0.07),rgba(33,149,243,0) 70%),"
            "radial-gradient(50vw 64vh at 94% 22%,rgba(0,0,0,0.22),rgba(0,0,0,0) 74%)"
        ),
        # zones-a had the right SHAPE at roughly 1.8x the amplitude, and its
        # dark zone curled back up at the far right where the reference keeps
        # falling. These scale the alphas down and push the dark centre out to
        # the edge so the corner stays the darkest point.
        "zones-d": (
            "linear-gradient(180deg,rgba(255,255,255,0.04),rgba(255,255,255,0)),"
            "radial-gradient(46vw 60vh at 14% 12%,rgba(33,149,243,0.085),rgba(33,149,243,0) 70%),"
            "radial-gradient(34vw 46vh at 52% 22%,rgba(33,149,243,0.060),rgba(33,149,243,0) 68%),"
            "radial-gradient(40vw 54vh at 86% 60%,rgba(33,149,243,0.040),rgba(33,149,243,0) 66%),"
            "radial-gradient(52vw 66vh at 100% 16%,rgba(0,0,0,0.13),rgba(0,0,0,0) 76%)"
        ),
        "zones-e": (
            "linear-gradient(180deg,rgba(255,255,255,0.04),rgba(255,255,255,0)),"
            "radial-gradient(42vw 56vh at 16% 12%,rgba(33,149,243,0.075),rgba(33,149,243,0) 72%),"
            "radial-gradient(30vw 42vh at 50% 24%,rgba(33,149,243,0.055),rgba(33,149,243,0) 70%),"
            "radial-gradient(38vw 50vh at 84% 64%,rgba(33,149,243,0.035),rgba(33,149,243,0) 68%),"
            "radial-gradient(58vw 72vh at 104% 20%,rgba(0,0,0,0.15),rgba(0,0,0,0) 78%)"
        ),
        "zones-f": (
            "linear-gradient(180deg,rgba(255,255,255,0.035),rgba(255,255,255,0)),"
            "radial-gradient(46vw 60vh at 13% 13%,rgba(33,149,243,0.070),rgba(33,149,243,0) 72%),"
            "radial-gradient(32vw 44vh at 47% 20%,rgba(33,149,243,0.050),rgba(33,149,243,0) 70%),"
            "radial-gradient(40vw 54vh at 80% 66%,rgba(33,149,243,0.038),rgba(33,149,243,0) 68%),"
            "radial-gradient(64vw 80vh at 108% 24%,rgba(0,0,0,0.17),rgba(0,0,0,0) 80%)"
        ),
    }

    results = []
    for name, css in candidates.items():
        im = render(css, f"wash_{name}")
        # Sampled at the same viewport FRACTIONS the reference bands sit at —
        # the two images are different sizes and the wash is vh-sized, so the
        # same fraction is the same place in the field and the same pixel row
        # is not.
        got = [v for f0, f1 in FRACS
               for v in profile(im, int(H * f0), int(H * f1))]
        s = score(got, want)
        results.append((s, name))
        print(f"{name:10} score {s:6.1f}")
        print("   " + " ".join(f"{v:+6.1f}" for v in got))
    print()
    for s, name in sorted(results):
        print(f"  {s:7.1f}  {name}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
