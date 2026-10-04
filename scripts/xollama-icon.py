#!/usr/bin/env python3
# Makes app/assets/{tray,tray_upgrade,app}.ico: ollama's icons with a red X painted on the
# llama's chest. Run from app/assets on upstream's icons (git show upstream/main:app/assets/tray.ico):
#   python3 scripts/xollama-icon.py tray.upstream.ico tray.ico dc2626
import math, random, sys
from PIL import Image, ImageDraw, ImageFilter

def stroke(draw, p0, p1, w0, w1, k, rnd, fill):
    # a tapered brush stroke from p0 to p1 with slightly rough edges
    (x0, y0), (x1, y1) = p0, p1
    dx, dy = x1 - x0, y1 - y0
    L = math.hypot(dx, dy); nx, ny = -dy / L, dx / L
    n = 40
    left, right = [], []
    for i in range(n + 1):
        t = i / n
        w = (w0 + (w1 - w0) * t) / 2 * (1 + 0.06 * math.sin(t * 9 + rnd.random()))
        j = rnd.uniform(-0.04, 0.04) * w
        cx, cy = x0 + dx * t, y0 + dy * t
        left.append(((cx + nx * (w + j)) * k, (cy + ny * (w + j)) * k))
        right.append(((cx - nx * (w - j)) * k, (cy - ny * (w - j)) * k))
    # rounded start cap (brush touch-down)
    draw.ellipse([(x0 - w0 / 2) * k, (y0 - w0 / 2) * k, (x0 + w0 / 2) * k, (y0 + w0 / 2) * k], fill=fill)
    draw.polygon(left + right[::-1], fill=fill)

def paint(frame, color, seed=7):
    frame = frame.convert("RGBA")
    s = frame.width
    k = 1024 // s if s < 1024 else 1
    k = max(k, 4)
    f = s / 256.0
    rnd = random.Random(seed)
    lay = Image.new("RGBA", (s * k, s * k), (0, 0, 0, 0))
    d = ImageDraw.Draw(lay)
    cx, cy, h = 128 * f, 226 * f, 46 * f
    w0, w1 = 24 * f, 17 * f
    # top-left to bottom-right, then top-right to bottom-left; both run past the edge
    stroke(d, (cx - h, cy - h), (cx + h * 1.12, cy + h * 1.12), w0, w1, k, rnd, color)
    stroke(d, (cx + h * 1.02, cy - h * 0.98), (cx - h * 1.1, cy + h * 1.14), w0 * 0.95, w1, k, rnd, color)
    lay = lay.resize((s, s), Image.LANCZOS)
    # painted on the fur: keep the X only where the llama is light (white fill)
    mask = Image.new("L", (s, s), 0)
    fp, mp = frame.load(), mask.load()
    for y in range(s):
        for x in range(s):
            r, g, b, a = fp[x, y]
            lum = (r + g + b) / 765
            mp[x, y] = int(255 * a / 255 * max(0.0, min(1.0, (lum - 0.35) / 0.4)))
    if s >= 64:
        mask = mask.filter(ImageFilter.MinFilter(3))
    lay.putalpha(Image.composite(lay.getchannel("A"), Image.new("L", (s, s), 0), mask))
    return Image.alpha_composite(frame, lay)

if __name__ == "__main__":
    src, dst, rgb = sys.argv[1], sys.argv[2], tuple(int(sys.argv[3][i:i + 2], 16) for i in (0, 2, 4))
    ico = Image.open(src)
    sizes = sorted(ico.info.get("sizes") or [ico.size])
    frames = []
    for sz in sizes:
        ico.size = sz
        frames.append(paint(ico.copy(), rgb + (255,)))
    frames[-1].save(dst, format="ICO", sizes=sizes, append_images=frames[:-1])
