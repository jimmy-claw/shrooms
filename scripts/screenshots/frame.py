#!/usr/bin/env python3
"""Puts demo renders of the Shrooms view inside a real Basecamp window.

assets/basecamp-frame.png is a Basecamp 0.3.1 window photographed on the Duet
tablet, with its content panel blanked: the sidebar and the tab bar are real,
and nothing of anybody's mesh is left in it. The view is rendered at the
panel's size (minus an inset the same colour as both) and pasted in.

    frame.py RENDER_DIR OUT_DIR
"""
import glob, os, sys
from PIL import Image

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
AT = (115, 48)  # the panel's inner corner in the frame

src, out = sys.argv[1], sys.argv[2]
frame = Image.open(os.path.join(ROOT, "assets", "basecamp-frame.png")).convert("RGB")
for f in sorted(glob.glob(os.path.join(src, "framed-*.png"))):
    im = frame.copy()
    im.paste(Image.open(f).convert("RGB"), AT)
    name = "window-" + os.path.basename(f)[len("framed-"):]
    im.save(os.path.join(out, name), optimize=True)
    print("framed", name)
