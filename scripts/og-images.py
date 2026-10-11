#!/usr/bin/env python3
"""The link-preview images for the site: site/og.png and site/og-agents.png.

What a chat app or a social site shows when somebody pastes a link — the first
impression most people will ever have of the project, so it is generated from
the same marks as everything else (assets/logo.png, assets/agents.png) rather
than drawn by hand and left to drift.

    python3 scripts/og-images.py      # needs Pillow and DejaVu Sans Mono
"""
import os
from PIL import Image, ImageDraw, ImageFilter, ImageFont

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
W, H = 1200, 630
VOID = (7, 9, 11)
BONE = (214, 221, 227)
ASH = (107, 118, 128)
PHOSPHOR = (53, 240, 160)
VIOLET = (154, 123, 255)


def font(bold, size):
    for d in ("/usr/share/fonts/truetype/dejavu", "/usr/share/fonts/dejavu"):
        p = os.path.join(d, "DejaVuSansMono-Bold.ttf" if bold else "DejaVuSansMono.ttf")
        if os.path.exists(p):
            return ImageFont.truetype(p, size)
    raise SystemExit("DejaVu Sans Mono not found")


def glow(img, xy, r, colour, alpha):
    layer = Image.new("RGBA", img.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    x, y = xy
    d.ellipse((x - r, y - r, x + r, y + r), fill=colour + (alpha,))
    img.alpha_composite(layer.filter(ImageFilter.GaussianBlur(r * 0.6)))


def card(mark, title, lines, url, out):
    img = Image.new("RGBA", (W, H), VOID + (255,))
    glow(img, (220, 160), 260, PHOSPHOR, 70)
    glow(img, (1040, 520), 300, VIOLET, 70)
    logo = Image.open(os.path.join(ROOT, "assets", mark)).convert("RGBA").resize((300, 300), Image.LANCZOS)
    img.alpha_composite(logo, (90, (H - 300) // 2))
    d = ImageDraw.Draw(img)
    x = 450
    d.text((x, 150), title, font=font(True, 64), fill=BONE)
    y = 260
    for line in lines:
        d.text((x, y), line, font=font(False, 34), fill=BONE)
        y += 50
    d.text((x, H - 110), url, font=font(False, 26), fill=PHOSPHOR)
    img.convert("RGB").save(os.path.join(ROOT, "site", out), optimize=True)
    print("wrote site/" + out)


card("logo.png", "shrooms",
     ["Your devices, on one", "network that nobody runs.", "No server. No account."],
     "shrooms.vpavlin.xyz", "og.png")
card("agents.png", "Shrooms Agents",
     ["Your coding agents,", "on your own machines,", "from your phone."],
     "shrooms.vpavlin.xyz/agents.html", "og-agents.png")
