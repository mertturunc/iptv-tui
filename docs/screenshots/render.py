#!/usr/bin/env python3
"""Render docs/screenshots/*.ansi (raw TUI frames) to high-def PNG + demo GIF.

Uses only Pillow. Run from the repo root:

    python3 docs/screenshots/render.py

Inputs : docs/screenshots/01-live.ansi, 02-movies.ansi, 03-epg.ansi,
         04-series.ansi, 05-help.ansi (via IPTV_TUI_GEN_SHOTS=1 go test)
Outputs: docs/screenshots/*.png + docs/screenshots/demo.gif
"""

import os
import re
import sys

try:
    from PIL import Image  # noqa: F401
except ImportError:
    print(
        "Pillow is required. Install it with: "
        "python3 -m pip install -r docs/screenshots/requirements.txt",
        file=sys.stderr,
    )
    sys.exit(2)

HERE = os.path.dirname(os.path.abspath(__file__))

GROUND = (0x1D, 0x20, 0x21)  # colGround #1d2021
INK = (0xEB, 0xDB, 0xB2)  # colInk #ebdbb2

ANSI_RE = re.compile(r"\x1b\[([0-9;?]*)?([a-zA-Z])")

# Standard 16 colors mapped onto the headend palette where sensible.
BASE16 = {
    30: (0x28, 0x28, 0x28), 31: (0xFB, 0x49, 0x34), 32: (0xB8, 0xBB, 0x26),
    33: (0xFA, 0xBD, 0x2F), 34: (0x83, 0xA5, 0x98), 35: (0xD3, 0x86, 0x9B),
    36: (0x8E, 0xC0, 0x7C), 37: (0xEB, 0xDB, 0xB2),
    90: (0xA8, 0x99, 0x84), 91: (0xFB, 0x49, 0x34), 92: (0xB8, 0xBB, 0x26),
    93: (0xFA, 0xBD, 0x2F), 94: (0x83, 0xA5, 0x98), 95: (0xD3, 0x86, 0x9B),
    96: (0x8E, 0xC0, 0x7C), 97: (0xEB, 0xDB, 0xB2),
}
for _k in range(30, 38):
    BASE16[_k + 60] = BASE16.get(_k + 60, BASE16[_k])
# Backgrounds reuse the same rungs: 40-47 mirror 30-37, 100-107 mirror 90-97.
BASE16_BG = {}
for _k in range(30, 38):
    BASE16_BG[_k + 10] = BASE16[_k]
    BASE16_BG[_k + 70] = BASE16[_k + 60]


def xterm256(n):
    if n < 16:
        std = [
            (0, 0, 0), (205, 0, 0), (0, 205, 0), (205, 205, 0),
            (0, 0, 238), (205, 0, 205), (0, 205, 205), (229, 229, 229),
            (127, 127, 127), (255, 0, 0), (0, 255, 0), (255, 255, 0),
            (92, 92, 255), (255, 0, 255), (0, 255, 255), (255, 255, 255),
        ]
        return std[n]
    if n < 232:
        n -= 16
        r, g, b = (n // 36), ((n % 36) // 6), (n % 6)
        return tuple(0 if c == 0 else 55 + 40 * c for c in (r, g, b))
    g = 8 + 10 * (n - 232)
    return (g, g, g)


def parse_ansi(text):
    """Yield (row, col, char, fg, bg, bold) cells for a terminal frame.

    fg/bg are None where the frame carries no code (SGR reset or bare text):
    there the terminal's own default shows, which render_png resolves to the
    frame canvas.
    """
    rows = []
    cur = []
    fg, bg, bold = None, None, False

    def new_row():
        return []

    lines = text.split("\n")
    for r, line in enumerate(lines):
        cur = []
        pos = 0
        for m in ANSI_RE.finditer(line):
            chunk = line[pos:m.start()]
            for ch in chunk:
                cur.append((ch, fg, bg, bold))
            pos = m.end()
            params, final = m.group(1) or "", m.group(2)
            if final != "m":
                continue  # e.g. ?25l cursor-hide: drop
            if params == "":
                fg, bg, bold = None, None, False
                continue
            nums = [int(x) if x.isdigit() else 0 for x in params.split(";")]
            i = 0
            while i < len(nums):
                n = nums[i]
                if n == 0:
                    fg, bg, bold = None, None, False
                elif n == 1:
                    bold = True
                elif n == 22:
                    bold = False
                elif n == 39:
                    fg = None
                elif n == 49:
                    bg = None
                elif n in BASE16:
                    fg = BASE16[n]
                elif n in BASE16_BG:
                    bg = BASE16_BG[n]
                elif n in (38, 48):
                    target = "fg" if n == 38 else "bg"
                    if i + 1 < len(nums) and nums[i + 1] == 2 and i + 4 < len(nums):
                        c = tuple(nums[i + 2:i + 5])
                        if target == "fg":
                            fg = c
                        else:
                            bg = c
                        i += 4
                    elif i + 1 < len(nums) and nums[i + 1] == 5 and i + 2 < len(nums):
                        c = xterm256(nums[i + 2])
                        if target == "fg":
                            fg = c
                        else:
                            bg = c
                        i += 2
                i += 1
        for ch in line[pos:]:
            cur.append((ch, fg, bg, bold))
        rows.append(cur)
    return rows


def load_fonts(size):
    from PIL import ImageFont

    def pick(*names):
        for n in names:
            try:
                return ImageFont.truetype(n, size)
            except OSError:
                continue
        return ImageFont.load_default()

    # Meslo Nerd Font (ships with Windows Terminal) covers every headend
    # glyph, including ★ U+2605 which most mono fonts lack.
    reg = pick("MesloLGS NF Regular.ttf", "FiraCodeNerdFontMono-Regular.ttf", "CascadiaMono.ttf")
    bld = pick("MesloLGS NF Bold.ttf", "FiraCodeNerdFontMono-Bold.ttf", "consolab.ttf")
    return reg, bld


def build_cmaps():
    """Best-cmap for the two fonts, or Nones when fontTools is unavailable."""
    try:
        from fontTools.ttLib import TTFont

        def cmap_of(name):
            try:
                return TTFont(f"C:\\Windows\\Fonts\\{name}").getBestCmap() or {}
            except Exception:
                return {}

        return (
            cmap_of("MesloLGS NF Regular.ttf"),
            cmap_of("MesloLGS NF Bold.ttf"),
        )
    except ImportError:
        return None, None


_REG_CMAP, _BLD_CMAP = None, None


def pick_font(ch, bold, reg, bld):
    # Meslo Bold lacks the rule chars, so bold cells fall back to regular
    # for glyphs the bold face does not carry.
    global _REG_CMAP, _BLD_CMAP
    if _REG_CMAP is None and _BLD_CMAP is None:
        _REG_CMAP, _BLD_CMAP = build_cmaps()
    if bold and _BLD_CMAP is not None:
        if ord(ch) in _BLD_CMAP:
            return bld
        return reg
    return bld if bold else reg


def render_png(rows, path, font_size=22, pad=28):
    from collections import Counter
    from PIL import Image, ImageDraw

    reg, bld = load_fonts(font_size)
    # Cell metrics from a full block + font ascent/descent.
    asc, desc = reg.getmetrics()
    cell_w = reg.getbbox("M")[2]
    cell_h = asc + desc + 4

    ncols = max(len(r) for r in rows)
    nrows = len(rows)
    # The app paints its ground on every themed cell, so the frame's most
    # common explicit background IS the theme ground and the most common
    # explicit foreground its ink; codeless cells resolve to those.
    bgs = [cell[2] for row in rows for cell in row if cell[2] is not None]
    fgs = [cell[1] for row in rows for cell in row if cell[1] is not None]
    canvas = Counter(bgs).most_common(1)[0][0] if bgs else GROUND
    ink = Counter(fgs).most_common(1)[0][0] if fgs else INK
    img = Image.new("RGB", (ncols * cell_w + pad * 2, nrows * cell_h + pad * 2), canvas)
    d = ImageDraw.Draw(img)

    for r, row in enumerate(rows):
        y = pad + r * cell_h
        for c in range(ncols):
            x = pad + c * cell_w
            if c < len(row):
                ch, fg, bg, bold = row[c]
                if fg is None:
                    fg = ink
                if bg is None:
                    bg = canvas
            else:
                ch, fg, bg, bold = " ", ink, canvas, False
            if bg != canvas:
                d.rectangle([x, y, x + cell_w, y + cell_h], fill=bg)
            if ch != " ":
                font = pick_font(ch, bold, reg, bld)
                d.text((x, y), ch, font=font, fill=fg)
    img.save(path)
    return img


def main():
    frames = ["01-live", "02-movies", "03-epg", "04-series", "05-help", "06-mpv", "07-light",
              "theme-gruvbox-dark-hard", "theme-dracula", "theme-nord", "theme-tokyo-night",
              "theme-gruvbox-light-hard", "theme-solarized-light", "theme-one-light",
              "theme-catppuccin-latte"]
    pngs = []
    for name in frames:
        src = os.path.join(HERE, name + ".ansi")
        if not os.path.exists(src):
            print(f"missing {src} -- run IPTV_TUI_GEN_SHOTS=1 go test first")
            sys.exit(1)
        with open(src, encoding="utf-8") as f:
            rows = parse_ansi(f.read())
        out = os.path.join(HERE, name + ".png")
        img = render_png(rows, out)
        pngs.append(out)
        print(f"wrote {out} {img.size[0]}x{img.size[1]}")

    # Demo GIF: tour the views, downscaled for size.
    from PIL import Image

    gif_frames = []
    for p in [os.path.join(HERE, n + ".png") for n in ["01-live", "02-movies", "03-epg", "04-series", "06-mpv", "05-help"]]:
        im = Image.open(p)
        w = 1100
        h = int(im.size[1] * w / im.size[0])
        im = im.resize((w, h))
        im = im.quantize(colors=256, method=2)
        gif_frames.append(im)
    gif_path = os.path.join(HERE, "demo.gif")
    gif_frames[0].save(
        gif_path, save_all=True, append_images=gif_frames[1:],
        duration=1500, loop=0, optimize=True,
    )
    print(f"wrote {gif_path} {os.path.getsize(gif_path) // 1024} KiB")


if __name__ == "__main__":
    main()
