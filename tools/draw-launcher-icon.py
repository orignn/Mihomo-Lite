"""Render the Mihomo Lite Windows and tray icons (requires Pillow)."""

import io
import struct
from pathlib import Path

from PIL import Image, ImageDraw, ImageOps

ROOT = Path(__file__).resolve().parents[1]
ASSETS = ROOT / "assets"
SIZES = (16, 20, 24, 32, 40, 48, 64, 128, 256)


def blend(first, second, amount):
    return tuple(round(a + (b - a) * amount) for a, b in zip(first, second))


def render(size):
    scale = 4
    pixels = size * scale
    unit = pixels / 256
    small = size <= 24
    canvas = Image.new("RGBA", (pixels, pixels))

    def box(*coords):
        return tuple(round(coord * unit) for coord in coords)

    def rounded_fill(bounds, radius, top, bottom):
        mask = Image.new("L", canvas.size)
        ImageDraw.Draw(mask).rounded_rectangle(box(*bounds), radius=round(radius * unit), fill=255)
        fill = Image.new("RGBA", canvas.size)
        draw = ImageDraw.Draw(fill)
        for y in range(pixels):
            draw.line((0, y, pixels, y), fill=blend(top, bottom, y / (pixels - 1)))
        canvas.paste(fill, mask=mask)

    rounded_fill((8, 9, 248, 249), 56, (59, 107, 129), (9, 25, 42))
    rounded_fill((12, 13, 244, 244), 52, (23, 55, 76), (10, 23, 43))

    def disk(x, y, radius, color):
        draw.ellipse(box(x-radius, y-radius, x+radius, y+radius), fill=color)

    def trace(points, width, color):
        draw.line([box(*point) for point in points], fill=color, width=round(width * unit))
        for x, y in points:
            disk(x, y, width / 2, color)

    draw = ImageDraw.Draw(canvas)
    if not small:
        # Subtle corner traces leave the letter as the only focal point.
        trace([(31, 66), (31, 53), (47, 38), (67, 38)], 2, (41, 84, 105))
        trace([(190, 218), (208, 218), (224, 202), (224, 190)], 2, (36, 67, 93))

    left = [(57, 180), (57, 75), (124, 149)]
    right = [(199, 180), (199, 75), (132, 149)]
    width = 27 if small else 22
    for points in (left, right):
        trace([(x, y + 4) for x, y in points], width + 4, (7, 25, 41))

    trace(left, width, (101, 242, 198))
    trace(right, width, (89, 184, 255))
    disk(128, 151, 13 if small else 14, (197, 250, 245))
    if not small:
        for x, y in (left[0], right[0]):
            disk(x, y, 17, (11, 32, 52))
        disk(*left[0], 12, (101, 242, 198))
        disk(*right[0], 12, (89, 184, 255))
        disk(*left[0], 5.5, (14, 42, 57))
        disk(*right[0], 5.5, (14, 42, 57))
        disk(128, 151, 5.5, (32, 119, 140))

    return canvas.resize((size, size), Image.Resampling.LANCZOS)


def write_ico(path, icons):
    # Preserve the artwork tailored to each size instead of resizing one PNG.
    frames = []
    for icon in icons:
        buf = io.BytesIO()
        icon.save(buf, format="PNG")
        frames.append((icon.width, buf.getvalue()))
    offset = 6 + 16 * len(frames)
    with path.open("wb") as file:
        file.write(struct.pack("<HHH", 0, 1, len(frames)))
        for size, frame in frames:
            file.write(struct.pack("<BBBBHHII", size % 256, size % 256, 0, 0, 1, 32, len(frame), offset))
            offset += len(frame)
        for _, frame in frames:
            file.write(frame)


if __name__ == "__main__":
    ASSETS.mkdir(exist_ok=True)
    icons = [render(size) for size in SIZES]
    icons[-1].save(ASSETS / "mihomolite-icon.png")
    write_ico(ASSETS / "mihomolite.ico", icons)

    tray_dir = ROOT / "cmd" / "mihomolite" / "assets"
    tray_icons = [icon for icon in icons if icon.width <= 64]
    write_ico(tray_dir / "tray-green.ico", tray_icons)
    gray_icons = []
    for icon in tray_icons:
        gray = ImageOps.grayscale(icon)
        gray_icons.append(Image.merge("RGBA", (gray, gray, gray, icon.getchannel("A"))))
    write_ico(tray_dir / "tray-gray.ico", gray_icons)
