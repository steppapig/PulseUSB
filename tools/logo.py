#!/usr/bin/env python3
"""Draws the PulseUSB logo (a pulse line running into a USB plug) and writes
the icon files the app embeds. Needs Pillow. Run from the repository root,
then rebuild the Windows icon resource:

    python3 tools/logo.py
    cd cmd/pulseusb-gui
    llvm-rc /FO app.res app.rc
    llvm-cvtres /MACHINE:X64 /OUT:rsrc_windows_amd64.syso app.res && rm app.res
"""
import shutil

from PIL import Image, ImageDraw

S = 1024
BLUE = (22, 78, 200, 255)
WHITE = (255, 255, 255, 255)

im = Image.new("RGBA", (S, S), (0, 0, 0, 0))
d = ImageDraw.Draw(im)
d.rounded_rectangle((40, 40, S - 40, S - 40), radius=220, fill=BLUE)
pts = [(150, 560), (300, 560), (370, 400), (470, 740), (560, 300), (640, 560), (700, 560)]
d.line(pts, fill=WHITE, width=64, joint="curve")
for x, y in pts:
    d.ellipse((x - 32, y - 32, x + 32, y + 32), fill=WHITE)
d.rounded_rectangle((690, 470, 820, 650), radius=26, fill=WHITE)  # plug body
d.rectangle((820, 500, 900, 620), fill=WHITE)                     # plug tip
d.rectangle((845, 528, 880, 552), fill=BLUE)
d.rectangle((845, 568, 880, 592), fill=BLUE)

im.resize((256, 256), Image.LANCZOS).save(
    "cmd/pulseusb-gui/icon.ico", format="ICO",
    sizes=[(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (256, 256)], bitmap_format="bmp")
shutil.copyfile("cmd/pulseusb-gui/icon.ico", "internal/gui/icon.ico")
im.resize((128, 128), Image.LANCZOS).save("internal/gui/icon.png")
