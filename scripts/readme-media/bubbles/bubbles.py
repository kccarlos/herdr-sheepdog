"""The README's hero GIF: the real iPhone app on the left (the hero phone
take, scripts/readme-media/hero/phone.sh) and the conversation as chat
bubbles on the right, 12 s, looping. Every frame is drawn with Pillow at
1600x900 from the take and lines.tsv, so the same inputs give the same
GIF.

    uv run --with pillow python scripts/readme-media/bubbles/bubbles.py WORK LOGO_PNG OUT_DIR

WORK holds phone/ from phone.sh (lock0.png, lock1.png, cfr2.mp4, rec2.t0,
marks.txt). Writes OUT_DIR/hero.gif (800x450, 15 fps) and
OUT_DIR/hero-loop.mp4 (1600x900, 30 fps), and WORK/bubbles/timing.txt.
"""

import math
import os
import shutil
import subprocess
import sys

from PIL import Image, ImageChops, ImageDraw, ImageFilter, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
WORK = os.path.abspath(sys.argv[1])
LOGO = os.path.abspath(sys.argv[2])
OUT = os.path.abspath(sys.argv[3])
TMP = os.path.join(WORK, "bubbles")
PHONE = os.path.join(WORK, "phone")

S = 2  # drawn at 2x: 1600x900; the GIF is scaled down to 800x450
W, H = 800 * S, 450 * S
FPS = 30
LENGTH = 12.0
END_AT = 11.0  # the end tag: logo + "a herdr plugin"

BG = (247, 243, 236)
INK = (24, 30, 41)
MUTED = (104, 112, 125)
DOG_BUBBLE = (255, 255, 255)
YOU_BUBBLE = (33, 46, 68)
YOU_INK = (255, 255, 255)
GOLD = (205, 146, 42)
SHADOW = (0, 0, 0, 30)

SF = "/System/Library/Fonts/SFNS.ttf"


def font(px, weight=b"Medium"):
    f = ImageFont.truetype(SF, px)
    f.set_variation_by_name(weight)
    return f


TEXT = font(45)  # 22.5 px in the 800 px GIF
END_TITLE = font(76, b"Bold")
END_SUB = font(46)


def run(*args, **kw):
    return subprocess.run(args, check=True, **kw)


def ease(x):
    x = min(1.0, max(0.0, x))
    return 1 - (1 - x) ** 3


# --- inputs -----------------------------------------------------------------

bubbles = []
for row in open(os.path.join(HERE, "lines.tsv")):
    row = row.rstrip("\n")
    if not row or row.startswith("#"):
        continue
    at, who, text = row.split("\t", 2)
    bubbles.append({"at": float(at), "who": who, "text": text})

t0 = float(open(os.path.join(PHONE, "rec2.t0")).read())
marks = {}
for row in open(os.path.join(PHONE, "marks.txt")):
    step, at = row.split()
    marks[step] = float(at) - t0  # seconds into cfr2.mp4


def scenes():
    out = subprocess.run(["ffmpeg", "-hide_banner", "-i", os.path.join(PHONE, "cfr2.mp4"), "-vf",
                          "select='gt(scene,0.004)',showinfo", "-f", "null", "-"], capture_output=True, text=True)
    return [float(x.split(":")[1]) for x in out.stderr.split() if x.startswith("pts_time:")]


SCENES = scenes()


def change_after(t, within=4.0):
    """The first picture change after t in the phone video: the UI test
    marks a step before it taps, and the tap lands a moment later."""
    for s in SCENES:
        if t <= s <= t + within:
            return s
    return t + 1.0


# --- the phone --------------------------------------------------------------

SCREEN_H = 404 * S
src = Image.open(os.path.join(PHONE, "lock0.png"))
SCREEN_W = round(src.width * SCREEN_H / src.height)
PAD = 7 * S
PHONE_X, PHONE_Y = 34 * S, (H - SCREEN_H) // 2
RADIUS = int(SCREEN_W * 0.15)


def screen_mask():
    m = Image.new("L", (SCREEN_W, SCREEN_H), 0)
    ImageDraw.Draw(m).rounded_rectangle((0, 0, SCREEN_W - 1, SCREEN_H - 1), radius=RADIUS, fill=255)
    return m


MASK = screen_mask()

# The phone's timeline: (time in the GIF, source). A source is "lock0",
# "lock1" or a time in cfr2.mp4. Between two video keys the video plays at
# the speed they imply (equal sources hold a frame); a key at the same GIF
# time as the last one is a cut, softened by a short crossfade.
tap_approve = change_after(marks["tap_approve"])
tap_status = change_after(marks["tap_status"])
status_shown = change_after(tap_status + 0.3)
tap_failed = change_after(marks["tap_failed"])
answer_shown = change_after(tap_failed + 0.3)


def at_bubble(i):
    return bubbles[i]["at"]


# Each tap lands 0.25 s after its spoken line's bubble; each answer shows
# on the phone with its bubble.
PHONE_KEYS = [
    (0.0, "lock0"),
    (0.45, "lock1"),
    (1.5, tap_approve - (at_bubble(1) + 0.25 - 1.5)),
    (4.0, tap_approve + (4.0 - at_bubble(1) - 0.25)),
    (4.0, tap_status - (at_bubble(2) + 0.25 - 4.0)),
    (at_bubble(2) + 0.25, tap_status),
    (at_bubble(3), status_shown),
    (8.0, status_shown + (8.0 - at_bubble(3))),
    (8.0, tap_failed - (at_bubble(4) + 0.25 - 8.0)),
    (at_bubble(4) + 0.25, tap_failed),
    (at_bubble(5), answer_shown),
    (END_AT + 0.3, answer_shown + (END_AT + 0.3 - at_bubble(5))),
]
XFADE = 0.18


def key_value(i, t):
    """Key i's source at t: a still, or the video interpolated towards the
    next key (held when there is none)."""
    gt, s = PHONE_KEYS[i]
    if isinstance(s, str) or i + 1 == len(PHONE_KEYS):
        return s
    nt, ns = PHONE_KEYS[i + 1]
    if isinstance(ns, str) or nt <= gt:
        return s
    return s + (ns - s) * (t - gt) / (nt - gt)


def phone_source(t):
    """(source, source before a cut or None, blend 0..1) at GIF time t."""
    i = max(j for j, (gt, _) in enumerate(PHONE_KEYS) if gt <= t)
    gt, s = PHONE_KEYS[i]
    cur = key_value(i, t)
    if i > 0 and t - gt < XFADE:
        pt, ps = PHONE_KEYS[i - 1]
        if pt == gt or isinstance(s, str) or isinstance(ps, str):
            prev = ps if pt == gt else key_value(i - 1, gt)
            return cur, prev, (t - gt) / XFADE
    return cur, None, 1.0


def extract_video():
    d = os.path.join(TMP, "video")
    if os.path.isdir(d):
        shutil.rmtree(d)
    os.makedirs(d)
    run("ffmpeg", "-loglevel", "error", "-i", os.path.join(PHONE, "cfr2.mp4"), "-vf",
        f"fps={FPS},scale={SCREEN_W}:{SCREEN_H}:flags=lanczos", os.path.join(d, "f%05d.png"))
    return sorted(os.listdir(d)), d


FRAMES, FRAME_DIR = extract_video()
_cache = {}


def phone_image(source):
    if isinstance(source, str):
        key = source
    else:
        key = min(len(FRAMES) - 1, max(0, round(source * FPS)))
    if key not in _cache:
        if isinstance(key, str):
            img = Image.open(os.path.join(PHONE, key + ".png")).convert("RGB").resize((SCREEN_W, SCREEN_H), Image.LANCZOS)
        else:
            img = Image.open(os.path.join(FRAME_DIR, FRAMES[key])).convert("RGB")
        _cache[key] = img
    return _cache[key]


def bezel():
    """The phone's body under the screen: a dark frame with a soft shadow."""
    layer = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    shadow = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    box = (PHONE_X - PAD, PHONE_Y - PAD, PHONE_X + SCREEN_W + PAD, PHONE_Y + SCREEN_H + PAD)
    ImageDraw.Draw(shadow).rounded_rectangle((box[0], box[1] + 8 * S, box[2], box[3] + 8 * S), radius=RADIUS + PAD, fill=(0, 0, 0, 60))
    layer = Image.alpha_composite(layer, shadow.filter(ImageFilter.GaussianBlur(10 * S)))
    d = ImageDraw.Draw(layer)
    d.rounded_rectangle(box, radius=RADIUS + PAD, fill=(20, 22, 27, 255))
    d.rounded_rectangle((box[0] + 2, box[1] + 2, box[2] - 2, box[3] - 2), radius=RADIUS + PAD - 2, outline=(70, 74, 82, 255), width=2)
    return layer


# --- the chat ---------------------------------------------------------------

CHAT_X0 = PHONE_X + SCREEN_W + PAD + 30 * S
CHAT_X1 = W - 22 * S
AVATAR = 38 * S
GAP = 9 * S
BUBBLE_PAD_X, BUBBLE_PAD_Y = 15 * S, 10 * S
LINE_H = 29 * S
BUBBLE_MAX = CHAT_X1 - CHAT_X0 - AVATAR - GAP
CHAT_TOP, CHAT_BOTTOM = 26 * S, H - 26 * S
SPACING = 14 * S


def wrap(text):
    d = ImageDraw.Draw(Image.new("RGB", (1, 1)))
    words, out, line = text.split(), [], ""
    for w in words:
        trial = (line + " " + w).strip()
        if d.textlength(trial, font=TEXT) <= BUBBLE_MAX - 2 * BUBBLE_PAD_X:
            line = trial
        else:
            out.append(line)
            line = w
    out.append(line)
    if len(out) > 2:
        sys.exit(f"bubble over two lines: {text!r}")
    return out


def logo_avatar(size):
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    d.ellipse((0, 0, size - 1, size - 1), fill=(255, 255, 255, 255), outline=(222, 214, 200, 255), width=S)
    logo = Image.open(LOGO).convert("RGBA")
    inner = int(size * 0.84)
    logo = logo.resize((inner, inner), Image.LANCZOS)
    img.alpha_composite(logo, ((size - inner) // 2, (size - inner) // 2))
    return img


AVATAR_IMG = logo_avatar(AVATAR)


def mic_icon(size, t):
    """A gold disc with a voice wave: the line was spoken. The bars move
    for the first 0.8 s after the bubble shows."""
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    d.ellipse((0, 0, size - 1, size - 1), fill=GOLD + (255,))
    base = [0.35, 0.65, 1.0, 0.65, 0.35]
    bw = size * 0.085
    gap = size * 0.06
    total = len(base) * bw + (len(base) - 1) * gap
    x = (size - total) / 2
    for i, b in enumerate(base):
        if t < 0.8:
            b = b * (0.55 + 0.45 * abs(math.sin(t * 11 + i * 1.3)))
        h = size * 0.5 * b
        d.rounded_rectangle((x, size / 2 - h / 2, x + bw, size / 2 + h / 2), radius=bw / 2, fill=(255, 255, 255, 255))
        x += bw + gap
    return img


for b in bubbles:
    b["lines"] = wrap(b["text"])
    d = ImageDraw.Draw(Image.new("RGB", (1, 1)))
    b["w"] = int(max(d.textlength(l, font=TEXT) for l in b["lines"]) + 2 * BUBBLE_PAD_X)
    b["h"] = len(b["lines"]) * LINE_H + 2 * BUBBLE_PAD_Y

# Where each bubble sits in the conversation, top to bottom.
y = 0
for b in bubbles:
    b["y"] = y
    y += b["h"] + SPACING


def scroll_at(t):
    """How far the conversation has scrolled up at t: the newest bubble's
    bottom stays inside the column, eased over 0.35 s after it appears."""
    offset = 0.0
    for b in bubbles:
        need = max(0, b["y"] + b["h"] - (CHAT_BOTTOM - CHAT_TOP))
        if need > offset:
            offset = offset + (need - offset) * ease((t - b["at"]) / 0.35) if t >= b["at"] else offset
    return offset


def draw_bubble(layer, b, x, y, alpha):
    d = ImageDraw.Draw(layer)
    fill = DOG_BUBBLE if b["who"] == "sheepdog" else YOU_BUBBLE
    ink = INK if b["who"] == "sheepdog" else YOU_INK
    a = int(255 * alpha)
    d.rounded_rectangle((x, y + 2 * S, x + b["w"], y + b["h"] + 2 * S), radius=18 * S, fill=(0, 0, 0, int(22 * alpha)))
    d.rounded_rectangle((x, y, x + b["w"], y + b["h"]), radius=18 * S, fill=fill + (a,))
    for i, line in enumerate(b["lines"]):
        d.text((x + BUBBLE_PAD_X, y + BUBBLE_PAD_Y + i * LINE_H + LINE_H / 2), line, font=TEXT, fill=ink + (a,), anchor="lm")


def typing(layer, x, y, t):
    d = ImageDraw.Draw(layer)
    w, h = 74 * S, 2 * BUBBLE_PAD_Y + LINE_H
    d.rounded_rectangle((x, y, x + w, y + h), radius=18 * S, fill=DOG_BUBBLE + (255,))
    for i in range(3):
        k = 0.45 + 0.55 * max(0, math.sin(t * 9 - i * 0.9))
        r = 4.5 * S
        cx, cy = x + 20 * S + i * 17 * S, y + h / 2
        d.ellipse((cx - r, cy - r, cx + r, cy + r), fill=tuple(int(c + (200 - c) * (1 - k)) for c in MUTED) + (255,))


def chat_layer(t):
    layer = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    off = scroll_at(t)
    for b in bubbles:
        if t < b["at"]:
            continue
        k = ease((t - b["at"]) / 0.28)
        y = CHAT_TOP + b["y"] - off + (1 - k) * 14 * S
        if b["who"] == "sheepdog":
            x = CHAT_X0 + AVATAR + GAP
            layer.alpha_composite(AVATAR_IMG, (CHAT_X0, int(y + b["h"] - AVATAR)))
        else:
            x = CHAT_X1 - AVATAR - GAP - b["w"]
            layer.alpha_composite(mic_icon(AVATAR, t - b["at"]), (CHAT_X1 - AVATAR, int(y + b["h"] - AVATAR)))
        draw_bubble(layer, b, x, y, k)
    # Sheepdog is "typing" just before its first line.
    first = bubbles[0]
    if first["who"] == "sheepdog" and first["at"] - 0.6 <= t < first["at"]:
        y = CHAT_TOP
        layer.alpha_composite(AVATAR_IMG, (CHAT_X0, int(y + 2 * BUBBLE_PAD_Y + LINE_H - AVATAR)))
        typing(layer, CHAT_X0 + AVATAR + GAP, y, t)
    # Bubbles that scroll off fade out at the top of the column.
    r, g, b_, a = layer.split()
    layer.putalpha(ImageChops.multiply(a, TOP_FADE))
    return layer


def top_fade():
    m = Image.new("L", (W, H), 255)
    d = ImageDraw.Draw(m)
    for y in range(CHAT_TOP + 24 * S):
        d.line((0, y, W, y), fill=int(255 * ease(y / (CHAT_TOP + 24 * S))))
    return m


TOP_FADE = top_fade()


# --- the end tag ------------------------------------------------------------


def end_card():
    img = Image.new("RGBA", (W, H), BG + (255,))
    # The logo as an app-icon tile (its PNG has a white square).
    size = 190 * S
    logo = Image.open(LOGO).convert("RGBA").resize((size, size), Image.LANCZOS)
    mask = Image.new("L", (size, size), 0)
    ImageDraw.Draw(mask).rounded_rectangle((0, 0, size - 1, size - 1), radius=int(size * 0.22), fill=255)
    shadow = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    x, y = (W - size) // 2, 70 * S
    ImageDraw.Draw(shadow).rounded_rectangle((x, y + 6 * S, x + size, y + size + 6 * S), radius=int(size * 0.22), fill=(0, 0, 0, 45))
    img.alpha_composite(shadow.filter(ImageFilter.GaussianBlur(9 * S)))
    tile = Image.new("RGBA", (size, size), (255, 255, 255, 255))
    tile.alpha_composite(logo)
    img.paste(tile, (x, y), mask)
    d = ImageDraw.Draw(img)
    d.text((W / 2, 70 * S + size + 34 * S), "sheepdog", font=END_TITLE, fill=INK + (255,), anchor="mm")
    d.text((W / 2, 70 * S + size + 82 * S), "a herdr plugin", font=END_SUB, fill=MUTED + (255,), anchor="mm")
    return img


# --- frames -----------------------------------------------------------------

BEZEL = bezel()
END = end_card()


def scene(t):
    img = Image.new("RGBA", (W, H), BG + (255,))
    img.alpha_composite(BEZEL)
    cur, prev, k = phone_source(t)
    screen = phone_image(cur)
    if prev is not None and k < 1:
        screen = Image.blend(phone_image(prev), screen, k)
    img.paste(screen, (PHONE_X, PHONE_Y), MASK)
    img.alpha_composite(chat_layer(t))
    return img


def frame(t):
    if t < END_AT - 0.2:
        return scene(t)
    if t < END_AT + 0.15:
        return Image.blend(scene(t), END, ease((t - END_AT + 0.2) / 0.35))
    if t < LENGTH - 0.25:
        return END
    # The loop: the end tag fades into the first frame.
    return Image.blend(END, scene(0.0), ease((t - LENGTH + 0.25) / 0.25))


def main():
    fdir = os.path.join(TMP, "frames")
    if os.path.isdir(fdir):
        shutil.rmtree(fdir)
    os.makedirs(fdir)
    n = int(LENGTH * FPS)
    for i in range(n):
        frame(i / FPS).convert("RGB").save(os.path.join(fdir, f"f{i:04d}.png"))
    with open(os.path.join(TMP, "timing.txt"), "w") as f:
        for b in bubbles:
            f.write(f"{b['at']:5.2f}  {b['who']:8s}  {b['text']}\n")
        f.write(f"{END_AT:5.2f}  end tag   sheepdog / a herdr plugin\n")
        f.write("phone keys (gif s -> source):\n")
        for gt, s in PHONE_KEYS:
            f.write(f"  {gt:5.2f} -> {s if isinstance(s, str) else f'video {s:6.2f}'}\n")
    os.makedirs(OUT, exist_ok=True)
    mp4 = os.path.join(OUT, "hero-loop.mp4")
    run("ffmpeg", "-loglevel", "error", "-y", "-framerate", str(FPS), "-i", os.path.join(fdir, "f%04d.png"),
        "-c:v", "libx264", "-crf", "20", "-preset", "slow", "-pix_fmt", "yuv420p", "-movflags", "+faststart", mp4)
    gif = os.path.join(OUT, "hero.gif")
    run("ffmpeg", "-loglevel", "error", "-y", "-framerate", str(FPS), "-i", os.path.join(fdir, "f%04d.png"), "-vf",
        "fps=15,scale=800:450:flags=lanczos,split[a][b];"
        "[a]palettegen=stats_mode=diff:max_colors=128[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle",
        "-loop", "0", gif)
    if shutil.which("gifsicle"):
        run("gifsicle", "-O3", "--batch", gif)
    for p in (gif, mp4):
        print(f"{p}  {os.path.getsize(p) / 1e6:.2f} MB")


main()
