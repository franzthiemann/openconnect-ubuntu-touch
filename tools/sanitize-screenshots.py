"""Replace personal values in the store screenshots with realistic fakes.

Solid overwrite plus re-rendered text, not blur: blurred text can often be
recovered, and a grey box tells a store visitor nothing. The replacement is
drawn in the device's own Ubuntu font at a size matched to the text it covers.
"""
import collections
import pathlib
import sys

from PIL import Image, ImageDraw, ImageFont

SHOTS = pathlib.Path(sys.argv[1])
FONT = str(SHOTS.parent / "fonts" / "Ubuntu-R.ttf")
OUT = SHOTS.parent / "sanitized"
OUT.mkdir(exist_ok=True)

# (region x0,y0,x1,y1 in original pixels, replacement text[, ink threshold])
# The disabled profile row is drawn in a light grey, well above the default
# threshold that picks out ordinary text.
JOBS = {
    "screenshot20260914_165147292.png": [   # one-time setup page
        ((90, 1375, 1050, 1435), "ubt-x7k2m9", 140, 33),
        ((90, 1592, 1050, 1655), "q4n8vrt2wzp6k3h"),
    ],
    "screenshot20260914_165150191.png": [   # profile list, not connected
        ((40, 400, 1120, 462), "vpn.example.edu"),
        ((40, 462, 1120, 512), "vpn.example.edu · you@example.edu · 1-Standard"),
    ],
    "screenshot20260914_165217554.png": [   # tunnel ready, inline card
        ((380, 655, 1120, 712), "ubt-x7k2m9", 140, 33),
        ((380, 763, 1120, 820), "q4n8vrt2wzp6k3h"),
        ((40, 1350, 1120, 1406), "vpn.example.edu", 215),
        ((40, 1406, 1120, 1458), "vpn.example.edu · you@example.edu · 1-Standard", 215),
    ],
    "screenshot20260914_165246052.png": [   # system VPN editor
        ((60, 1530, 1150, 1596), "ubt-x7k2m9", 140, 40),
    ],
}


def analyse(img, box, threshold=140):
    """Find the ink in a region: its bounding box, colour, and the background."""
    x0, y0, x1, y1 = box
    crop = img.crop(box).convert("RGB")
    px = crop.load()
    w, h = crop.size
    counts = collections.Counter()
    dark = []
    for y in range(h):
        for x in range(w):
            r, g, b = px[x, y]
            counts[(r, g, b)] += 1
            if (r * 299 + g * 587 + b * 114) / 1000 < threshold:
                dark.append((x, y, (r, g, b)))
    bg = counts.most_common(1)[0][0]
    if not dark:
        return None, bg, None
    xs = [d[0] for d in dark]
    ys = [d[1] for d in dark]
    ink = (
        sum(d[2][0] for d in dark) // len(dark),
        sum(d[2][1] for d in dark) // len(dark),
        sum(d[2][2] for d in dark) // len(dark),
    )
    return (x0 + min(xs), y0 + min(ys), x0 + max(xs), y0 + max(ys)), bg, ink


def fit_font(text, target_h):
    """Pick the size whose rendered height matches what we are covering."""
    best, best_err = 30, 1e9
    for size in range(14, 64):
        f = ImageFont.truetype(FONT, size)
        bb = f.getbbox(text)
        err = abs((bb[3] - bb[1]) - target_h)
        if err < best_err:
            best, best_err = size, err
    return ImageFont.truetype(FONT, best)


for name, jobs in JOBS.items():
    src = SHOTS / name
    if not src.exists():
        print("missing", name)
        continue
    img = Image.open(src).convert("RGB")
    draw = ImageDraw.Draw(img)
    for job in jobs:
        box, replacement = job[0], job[1]
        threshold = job[2] if len(job) > 2 else 140
        forced_size = job[3] if len(job) > 3 else None
        ink_box, bg, ink = analyse(img, box, threshold)
        if ink_box is None:
            print(f"  {name}: no text found in {box} -- check the coordinates")
            continue
        # Solid overwrite of the whole region, then redraw.
        draw.rectangle(box, fill=bg)
        target_h = ink_box[3] - ink_box[1]
        font = (ImageFont.truetype(FONT, forced_size) if forced_size
                else fit_font(replacement, target_h))
        bb = font.getbbox(replacement)
        draw.text((ink_box[0] - bb[0], ink_box[1] - bb[1]), replacement,
                  font=font, fill=ink)
        print(f"  {name}: {box} -> {replacement!r} (h={target_h}, size={font.size})")
    img.save(OUT / name)

print("written to", OUT)
