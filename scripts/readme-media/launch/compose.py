"""The launch video (docs/dev/launch-video.md), cut from the iOS demo take.

usage: compose.py WORK OUT_DIR

WORK holds what make.sh made:
  tts/<id>.wav    the voice lines (lines.tsv, Gemini TTS)
  phone/cfr.mp4   the take (phone.sh), 30 fps
  phone/marks.txt <step> <unix s> lines from LaunchRecordingUITests
  phone/rec.t0    the unix time of the take's first frame
Writes OUT_DIR/launch.mp4 (1920x1080), launch-vertical.mp4 (1080x1920)
and launch-poster.png.

The brand (docs/dev/brand.md): ink background, cream type, slate for
secondary lines, Outfit for titles and Inter for captions (both from
web/static/fonts), the colour logo, and orange only for the one command on
the end card. The voices are TTS, so nothing here looks like a live
session: a speaker label and a footnote that says so. The one thing that
moves with the sound is the waveform: when someone speaks, bars scroll out
from the middle with the loudness of that line's own WAV (RMS per frame), so
the picture is driven by the audio, not by an animation.

The devices scene (terminal, diff, Agents by computer) uses a phone shot from
the take for the terminal and the diff, and a screenshot of the Agents page
grouped by computer (agents-by-computer-dark.png beside this file).
"""
import array, json, math, os, subprocess, sys, wave

from PIL import Image, ImageChops, ImageDraw, ImageFilter, ImageFont

WORK = os.path.abspath(sys.argv[1])
OUT = os.path.abspath(sys.argv[2])
HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "../../.."))
PHONE = os.path.join(WORK, "phone")
TTS = os.path.join(WORK, "tts")
CACHE = os.path.join(WORK, "compose")
os.makedirs(CACHE, exist_ok=True)
FPS = 30

# --- brand -----------------------------------------------------------------
BG = (0x16, 0x18, 0x1C)
CARD = (0x1F, 0x21, 0x26)
CREAM = (0xF5, 0xF2, 0xEA)
SLATE = (0x8E, 0x92, 0x9A)
ORANGE = (0xF3, 0x9C, 0x1F)
EDGE = (0x2C, 0x2F, 0x35)
MONO = "/System/Library/Fonts/SFNSMono.ttf"
if not os.path.exists(MONO):
    MONO = "/System/Library/Fonts/Menlo.ttc"


def ttf(name):
    """web/static/fonts/<name>-latin-wght-normal.woff2 as a TTF in CACHE."""
    out = os.path.join(CACHE, name + ".ttf")
    if not os.path.exists(out):
        from fontTools.ttLib import TTFont
        f = TTFont(os.path.join(REPO, "web/static/fonts", name + "-latin-wght-normal.woff2"))
        f.flavor = None
        f.save(out)
    return out


OUTFIT, INTER = ttf("outfit"), ttf("inter")


def font(path, px, weight):
    f = ImageFont.truetype(path, px)
    try:
        f.set_variation_by_axes([weight])
    except Exception:
        pass
    return f


def mono(px):
    f = ImageFont.truetype(MONO, px)
    try:
        f.set_variation_by_name(b"Medium")
    except Exception:
        pass
    return f


def clamp(x, lo=0.0, hi=1.0):
    return max(lo, min(hi, x))


def ease(x):
    # cubic-bezier(.2,.8,.2,1) is close to an ease-out; this is smooth both ways.
    x = clamp(x)
    return 1 - (1 - x) ** 3


def fade(t, t0, t1, d=0.2):
    """1 inside [t0, t1], easing in and out over d seconds."""
    return ease((t - t0) / d) * (1 - ease((t - (t1 - d)) / d))


def run(*a):
    subprocess.run(a, check=True)


# --- the take --------------------------------------------------------------
T0 = float(open(os.path.join(PHONE, "rec.t0")).read())
MARKS = {}
for line in open(os.path.join(PHONE, "marks.txt")):
    k, v = line.split()
    MARKS[k] = float(v) - T0


def m(name, off=0.0):
    return MARKS[name] + off


# Scenes: (kind, length, data). Phone scenes play the take at 1x from
# `from` and hold its frame at `to`. Lines: (id, who, text, at), `at` in
# seconds into the scene.
SCENES = [
    ("title", 4.2, {}),
    ("phone", 5.0, {"from": m("agents", -0.2), "to": m("tap_window", -0.2),
                    "caption": "Four agents at work.\nYou go for a walk."}),
    ("phone", 7.0, {"from": m("ask", -1.2), "to": m("tap_approve", -0.8),
                    "caption": "An agent is blocked.\nYou hear it first.", "chime": 0.3,
                    "lines": [("s_ask", "sheepdog", "codex-payments wants to run npm install p-retry. Approve?", 1.0)]}),
    # the clips skip the take's tap and the card's slide, so only the settled
    # result shows; `voice` rewrites its "by tap" to "by voice" (retouch_by_voice)
    ("phone", 5.4, {"clips": [(m("tap_approve", -0.8), m("tap_approve", 0.6)), (m("approved", -0.4), m("approved", 2.8))],
                    "voice": m("approved", -0.4),
                    "caption": "Approve with a tap,\nor just say it.",
                    "lines": [("u_yes", "you", "Yes, go ahead.", 0.2),
                              ("s_ok", "sheepdog", "Approved. Installing it now.", 2.1)]}),
    ("phone", 8.2, {"from": m("tap_status", -0.6), "to": m("status", 5.0),
                    "caption": "Ask what's going on.",
                    "lines": [("u_status", "you", "What's going on?", 0.2),
                              ("s_status", "sheepdog", "claude-auth is done, claude-migration failed, codex-docs is still writing.",
                               m("status") - m("tap_status", -0.6) + 0.1)]}),
    ("phone", 9.4, {"from": m("tap_card", -0.8), "to": m("end"),
                    "caption": "Ask to see it,\nand it's on your screen.",
                    "lines": [("u_card", "you", "Show me the agents.", 0.0),
                              ("s_card", "sheepdog", "It's on your screen: 3 done, 1 failed.",
                               m("card") - m("tap_card", -0.8) + 0.3)]}),
    ("devices", 6.4, {"caption": "Everything, on your phone."}),
    ("end", 6.5, {}),
]
for k, s in enumerate(SCENES):
    if s[0] == "phone" and "clips" not in s[2]:
        s[2]["clips"] = [(s[2]["from"], s[2]["to"])]
    if s[1] is None:
        SCENES[k] = (s[0], round(sum(b - a for a, b in s[2]["clips"]), 2), s[2])
STARTS, acc = [], 0.0
for s in SCENES:
    STARTS.append(acc)
    acc += s[1]
LENGTH = round(acc, 2)
XF = 0.2  # the cross-fade between scenes


def wav_len(path):
    with wave.open(path) as w:
        return w.getnframes() / w.getframerate()


class Env:
    """A line's loudness per video frame, from its own WAV: RMS of each
    1/FPS slice, scaled so the loudest slice is 1, and where speech starts
    and ends (the slices above a small floor)."""

    def __init__(self, path):
        with wave.open(path) as w:
            rate, n = w.getframerate(), w.getnframes()
            a = array.array("h")
            a.frombytes(w.readframes(n))
        hop = rate // FPS
        env = [math.sqrt(sum(x * x for x in a[i:i + hop]) / hop) / 32768 for i in range(0, len(a) - hop + 1, hop)]
        peak = max(env) or 1
        self.env = [e / peak for e in env]
        loud = [i for i, e in enumerate(self.env) if e > 0.06]
        self.s0, self.s1 = loud[0] / FPS, (loud[-1] + 1) / FPS

    def level(self, t):
        """Loudness 0..1 at t seconds into the line, interpolated."""
        x = t * FPS
        i = int(math.floor(x))
        if i < 0 or i >= len(self.env) - 1:
            return 0.0
        f = x - i
        return self.env[i] * (1 - f) + self.env[i + 1] * f


ENVS = {}


def env(id):
    if id not in ENVS:
        ENVS[id] = Env(os.path.join(TTS, id + ".wav"))
    return ENVS[id]


LINES = []  # (id, who, text, start in the video, length)
for (kind, length, d), st in zip(SCENES, STARTS):
    for id, who, text, at in d.get("lines", []):
        n = wav_len(os.path.join(TTS, id + ".wav"))
        LINES.append((id, who, text, st + at, n))
        print(f"{id}: {st + at:.1f}-{st + at + n:.1f} s (scene ends {st + length:.1f})", file=sys.stderr)
        assert st + at + n <= st + length + 0.6, f"{id} runs past its scene"
print(f"length {LENGTH:.1f} s", file=sys.stderr)


# --- phone frames ----------------------------------------------------------
class Phone:
    """The take scaled for one layout: frames on disk, a bezel, a mask."""

    def __init__(self, sh):
        src = json.loads(subprocess.run(["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",
                                         "stream=width,height", "-of", "json", os.path.join(PHONE, "cfr.mp4")],
                                        capture_output=True, check=True).stdout)["streams"][0]
        self.sh = sh
        self.sw = int(round(sh * src["width"] / src["height"] / 2)) * 2
        self.dir = os.path.join(CACHE, f"pf{sh}")
        if not os.path.exists(os.path.join(self.dir, "done")):
            os.makedirs(self.dir, exist_ok=True)
            run("ffmpeg", "-loglevel", "error", "-y", "-i", os.path.join(PHONE, "cfr.mp4"),
                "-vf", f"fps={FPS},scale={self.sw}:{self.sh}:flags=lanczos", "-q:v", "2", os.path.join(self.dir, "%05d.jpg"))
            open(os.path.join(self.dir, "done"), "w").close()
        self.count = len([f for f in os.listdir(self.dir) if f.endswith(".jpg")])
        self.chrome()

    def chrome(self):
        """The bezel, its shadow and the screen mask for self.sw x self.sh."""
        sh = self.sh
        r = int(self.sw * 0.13)
        self.mask = rounded_mask(self.sw, self.sh, r)
        self.bz = max(8, int(sh * 0.012))
        b = self.bz
        self.bezel = Image.new("RGBA", (self.sw + 2 * b, self.sh + 2 * b), (0, 0, 0, 0))
        ImageDraw.Draw(self.bezel).rounded_rectangle((0, 0, self.sw + 2 * b - 1, self.sh + 2 * b - 1), r + b,
                                                     fill=(0x0E, 0x0F, 0x12, 255), outline=EDGE + (255,), width=2)
        sh_ = Image.new("RGBA", (self.sw + 2 * b + 120, self.sh + 2 * b + 120), (0, 0, 0, 0))
        ImageDraw.Draw(sh_).rounded_rectangle((60, 76, 60 + self.sw + 2 * b, 76 + self.sh + 2 * b), r + b, fill=(0, 0, 0, 130))
        self.shadow = sh_.filter(ImageFilter.GaussianBlur(26))
        self._cache = {}

    def frame(self, v):
        i = clamp(int(round(v * FPS)) + 1, 1, self.count)
        if i not in self._cache:
            if len(self._cache) > 40:
                self._cache.clear()
            im = Image.open(os.path.join(self.dir, f"{int(i):05d}.jpg")).convert("RGBA")
            im.putalpha(self.mask)
            self._cache[i] = im
        return self._cache[i]

    retouch = False

    def retouch_by_voice(self, im):
        """The settled card says "You approved ... by tap". When the user said
        it, the daemon's log says by voice, so repaint that last word: same
        row colour, same green, same type, in the take's own coordinates
        (1320 px wide; x 745..862, baseline row 2228..2268)."""
        sx = self.sw / 1320
        im = im.copy()
        px = im.getpixel((int(900 * sx), int(2250 * sx)))
        green = max((im.getpixel((int(x * sx), int(2248 * sx))) for x in range(100, 130)), key=lambda c: c[1] - c[0])
        d = ImageDraw.Draw(im)
        d.rectangle((int(738 * sx), int(2226 * sx), int(905 * sx), int(2272 * sx)), fill=px)
        f = font(INTER, int(round(40 * sx * 1.0)), 600)
        d.text((int(745 * sx), int(2248 * sx)), "by voice", font=f, fill=green, anchor="lm")
        return im

    def draw(self, im, xy, v, alpha=1.0):
        x, y = xy
        layer = Image.new("RGBA", im.size, (0, 0, 0, 0))
        layer.alpha_composite(self.shadow, (x - 60, y - 60))
        layer.alpha_composite(self.bezel, (x, y))
        if v is not None:
            fr = self.frame(v)
            if self.retouch:
                fr = self.retouch_by_voice(fr)
            layer.alpha_composite(fr, (x + self.bz, y + self.bz))
        blend(im, layer, alpha)


class DevicePhone(Phone):
    """A phone showing given PIL images: a still, or a short list of frames
    played from their start (the last one holds)."""

    def __init__(self, sh, frames):
        self.sh = sh
        self.sw = int(round(sh * frames[0].width / frames[0].height / 2)) * 2
        self.frames = [f.convert("RGB").resize((self.sw, sh), Image.LANCZOS).convert("RGBA") for f in frames]
        for f in self.frames:
            f.putalpha(rounded_mask(self.sw, sh, int(self.sw * 0.13)))
        self.chrome()

    def frame(self, v):
        return self.frames[int(clamp(round(v * FPS), 0, len(self.frames) - 1))]


def rounded_mask(w, h, r):
    mk = Image.new("L", (w * 2, h * 2), 0)
    ImageDraw.Draw(mk).rounded_rectangle((0, 0, w * 2 - 1, h * 2 - 1), r * 2, fill=255)
    return mk.resize((w, h), Image.LANCZOS)


def blend(im, layer, a):
    if a <= 0.004:
        return
    if a < 0.996:
        layer.putalpha(ImageChops.multiply(layer.getchannel("A"), Image.new("L", im.size, int(255 * a))))
    im.alpha_composite(layer)


def take_frames(t0, t1):
    """The take's frames from t0 to t1 s (1/FPS apart) as PIL images."""
    d = os.path.join(CACHE, f"take-{t0:.2f}-{t1:.2f}")
    if not os.path.exists(os.path.join(d, "done")):
        os.makedirs(d, exist_ok=True)
        run("ffmpeg", "-loglevel", "error", "-y", "-ss", f"{t0:.3f}", "-t", f"{t1 - t0:.3f}", "-i", os.path.join(PHONE, "cfr.mp4"),
            "-vf", f"fps={FPS},scale=660:-1", "-q:v", "2", os.path.join(d, "%04d.jpg"))
        open(os.path.join(d, "done"), "w").close()
    return [Image.open(os.path.join(d, f)).convert("RGB") for f in sorted(os.listdir(d)) if f.endswith(".jpg")]


def agents_shot():
    """agents-by-computer-dark.png: the app's Agents page grouped by computer
    (a simulator screenshot with two computers, one offline), 660 px wide,
    with the take's 9:41 status bar pasted over its clock (README.md)."""
    return Image.open(os.path.join(HERE, "agents-by-computer-dark.png")).convert("RGB")


LOGO_SRC = Image.open(os.path.join(REPO, "assets/brand/logo/sheepdog-sticker-transparent-1024.png")).convert("RGBA")


def logo(px):
    return LOGO_SRC.resize((px, int(px * LOGO_SRC.height / LOGO_SRC.width)), Image.LANCZOS)


# --- layouts ---------------------------------------------------------------
class Layout:
    def __init__(self, W, H):
        self.W, self.H = W, H
        self.portrait = H > W
        if self.portrait:
            self.phone = Phone(880)
            self.phone_xy = ((W - self.phone.sw) // 2 - self.phone.bz, 140)
            self.tx, self.tw = 90, W - 180
            self.cap_y = 140 + 880 + 2 * self.phone.bz + 56
            self.f_cap = font(OUTFIT, 62, 600)
            self.f_line = font(INTER, 40, 500)
            self.f_label = font(INTER, 26, 600)
            self.f_foot = font(INTER, 24, 400)
            self.f_mark = font(OUTFIT, 40, 600)
            self.mark_xy = (90, 52)
            self.logo_small = logo(64)
        else:
            self.phone = Phone(930)
            self.phone_xy = (1250, (H - 930) // 2 - self.phone.bz - 6)
            self.tx, self.tw = 150, 880
            self.cap_y = 330
            self.f_cap = font(OUTFIT, 72, 600)
            self.f_line = font(INTER, 42, 500)
            self.f_label = font(INTER, 26, 600)
            self.f_foot = font(INTER, 24, 400)
            self.f_mark = font(OUTFIT, 40, 600)
            self.mark_xy = (150, 84)
            self.logo_small = logo(64)
        s = 1.0 if not self.portrait else 0.95
        self.f_title = font(OUTFIT, int(132 * s), 700)
        self.f_tag = font(INTER, int(46 * s), 500)
        self.f_head = font(OUTFIT, int(64 * s), 600)
        self.f_step = font(OUTFIT, int(34 * s), 600)
        self.f_cmd = mono(int(40 * s) if not self.portrait else 34)
        self.f_end_cmd = mono(54 if not self.portrait else 44)
        self.f_small = font(INTER, 32 if not self.portrait else 36, 500)
        self.logo_big = logo(300 if not self.portrait else 360)
        self.bg = Image.new("RGBA", (W, H), BG + (255,))


def wrap(text, fnt, maxw, d):
    """Words into lines no wider than maxw; a newline in text always breaks."""
    if "\n" in text:
        return [l for part in text.split("\n") for l in wrap(part, fnt, maxw, d)]
    lines, cur = [], ""
    for w in text.split():
        t = (cur + " " + w).strip()
        if cur and d.textlength(t, font=fnt) > maxw:
            lines.append(cur)
            cur = w
        else:
            cur = t
    if cur:
        lines.append(cur)
    return lines


def centered(d, text, fnt, cx, y, fill):
    d.text((cx - d.textlength(text, font=fnt) / 2, y), text, font=fnt, fill=fill)


# --- scenes ----------------------------------------------------------------
def scene_at(t):
    for i in range(len(SCENES) - 1, -1, -1):
        if t >= STARTS[i]:
            return i
    return 0


def draw_brandmark(im, L, a):
    layer = Image.new("RGBA", im.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    x, y = L.mark_xy
    layer.alpha_composite(L.logo_small, (x, y))
    d.text((x + L.logo_small.width + 16, y + L.logo_small.height / 2), "sheepdog", font=L.f_mark, fill=CREAM + (255,), anchor="lm")
    blend(im, layer, a)


def draw_caption(im, L, text, a):
    layer = Image.new("RGBA", im.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    y = L.cap_y
    for line in wrap(text, L.f_cap, L.tw, d):
        if L.portrait:
            centered(d, line, L.f_cap, L.W / 2, y, CREAM + (255,))
        else:
            d.text((L.tx, y), line, font=L.f_cap, fill=CREAM + (255,))
        y += int(L.f_cap.size * 1.18)
    blend(im, layer, a)
    return y


def draw_wave(d, x, y, w, h, who, e, tc, a):
    """Bars scroll outward from the middle, each showing the line's loudness
    a few frames earlier the farther it is from the centre. While nobody is
    speaking the bars rest as dots. The speaker in orange is you (the
    product listening); the voice is cream."""
    col = ORANGE if who == "you" else CREAM
    active = e.s0 - 0.05 <= tc <= e.s1 + 0.1
    bw, gap = 8, 7
    n = int((w + gap) // (bw + gap)) | 1  # odd, so one bar is the centre
    x0 = x + (w - (n * bw + (n - 1) * gap)) / 2
    for k in range(n):
        side = abs(k - (n - 1) / 2)
        lv = e.level(tc - side * 0.03) if active else 0.0
        bh = bw + (h - bw) * (lv ** 0.7) * (1 - 0.3 * side / (n / 2))
        bx = x0 + k * (bw + gap)
        al = int(255 * a * (1.0 if active else 0.35))
        d.rounded_rectangle((bx, y + h / 2 - bh / 2, bx + bw, y + h / 2 + bh / 2), bw / 2, fill=col + (al,))


def draw_line(im, L, y, who, text, a, e, tc):
    """A speaker label, its waveform and the spoken words in an ink pill."""
    layer = Image.new("RGBA", im.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    label = "YOU" if who == "you" else "VOICE"
    pad = 28
    lines = wrap(text, L.f_line, L.tw - 2 * pad, d)
    lh = int(L.f_line.size * 1.32)
    w = L.tw if not L.portrait else L.tw
    wave_h = 84 if not L.portrait else 72
    h = len(lines) * lh + 2 * pad - (lh - L.f_line.size) + 46 + wave_h + 22
    x = L.tx
    d.rounded_rectangle((x, y, x + w, y + h), 14, fill=CARD + (235,))
    d.text((x + pad, y + pad - 4), label, font=L.f_label, fill=SLATE + (255,))
    draw_wave(d, x + pad, y + pad + 40, w - 2 * pad, wave_h, who, e, tc, 1.0)
    ty = y + pad + 42 + wave_h + 22
    for l in lines:
        d.text((x + pad, ty), l, font=L.f_line, fill=CREAM + (255,))
        ty += lh
    blend(im, layer, a)


def draw_footnote(im, L, a):
    layer = Image.new("RGBA", im.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    text = "The iPhone app's offline demo, in the iOS Simulator. Voices are text-to-speech."
    if L.portrait:
        for i, l in enumerate(wrap(text, L.f_foot, L.W - 200, d)):
            centered(d, l, L.f_foot, L.W / 2, L.H - 110 + i * 34, SLATE + (255,))
    else:
        d.text((L.tx, L.H - 92), text, font=L.f_foot, fill=SLATE + (255,))
    blend(im, layer, a)


def draw_phone_scene(im, L, i, t, a, pa):
    kind, length, d = SCENES[i]
    tl = t - STARTS[i]
    # the clips back to back with hard cuts; the last one holds its frame
    v = None
    for a0, a1 in d["clips"]:
        if tl <= a1 - a0:
            v = a0 + tl
            break
        tl -= a1 - a0
    if v is None:
        v = d["clips"][-1][1]
    L.phone.retouch = d.get("voice") is not None and v >= d["voice"]
    L.phone.draw(im, L.phone_xy, v, pa)
    y = draw_caption(im, L, d["caption"], a)
    for id, who, text, at in d.get("lines", []):
        n = wav_len(os.path.join(TTS, id + ".wav"))
        st = STARTS[i] + at
        nxt = [x for x in d["lines"] if x[3] > at]
        end = STARTS[i] + (nxt[0][3] - 0.1 if nxt else length)
        la = fade(t, st - 0.15, end) * a
        if la > 0:
            draw_line(im, L, y + 40, who, text, la, env(id), t - st)


def draw_devices(im, L, t, a):
    """Three phones side by side: the live terminal (the take, playing), the
    diff and the Agents page grouped by computer."""
    tl = t - STARTS[scene_at(t)]
    if not hasattr(L, "dev"):
        sh = 660 if L.portrait else 700
        term = take_frames(m("terminal", 0.2), m("tap_menu", -0.1))
        diff = take_frames(m("diff", 1.0), m("diff", 1.1))
        L.dev = [DevicePhone(sh, term), DevicePhone(sh, diff), DevicePhone(sh, [agents_shot()])]
    names = ["Live terminal", "The diff", "Every computer"]
    gap = 44 if not L.portrait else 20
    pw = L.dev[0].sw + 2 * L.dev[0].bz
    total = 3 * pw + 2 * gap
    x0 = (L.W - total) / 2
    layer = Image.new("RGBA", im.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    capy = 96 if not L.portrait else (L.H - 900) // 2
    for line in wrap(SCENES[scene_at(t)][2]["caption"], L.f_cap, L.W - 160, d):
        centered(d, line, L.f_cap, L.W / 2, capy, CREAM + (255,))
        capy += int(L.f_cap.size * 1.18)
    blend(im, layer, a)
    top = capy + (90 if not L.portrait else 130)
    for k, ph in enumerate(L.dev):
        st = 0.3 + 0.25 * k
        pa = ease((tl - st) / 0.35) * a
        x = int(x0 + k * (pw + gap))
        rise = int(24 * (1 - ease((tl - st) / 0.5)))
        lay = Image.new("RGBA", im.size, (0, 0, 0, 0))
        ld = ImageDraw.Draw(lay)
        centered(ld, names[k], L.f_label, x + pw / 2, top - 52 + rise, SLATE + (255,))
        blend(im, lay, pa)
        ph.draw(im, (x, top + rise), max(0.0, tl - st), pa)


def draw_title(im, L, t, a):
    layer = Image.new("RGBA", im.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    lg = L.logo_big
    total = lg.height + 40 + L.f_title.size + 40 + L.f_tag.size
    y = (L.H - total) / 2 - 20
    layer.alpha_composite(lg, (int((L.W - lg.width) / 2), int(y)))
    y += lg.height + 40
    centered(d, "sheepdog", L.f_title, L.W / 2, y - 18, CREAM + (255,))
    y += L.f_title.size + 40
    tag = "Manage your Herdr agents by voice."
    if L.portrait:
        for l in wrap(tag, L.f_tag, L.W - 160, d):
            centered(d, l, L.f_tag, L.W / 2, y, CREAM + (255,))
            y += int(L.f_tag.size * 1.3)
    else:
        centered(d, tag, L.f_tag, L.W / 2, y, CREAM + (255,))
    blend(im, layer, a)


def cmd_pill(d, L, x, y, text, color, w=None, fnt=None):
    fnt = fnt or L.f_cmd
    pad_x, pad_y = 30, 20
    tw = d.textlength(text, font=fnt)
    w = w or tw + 2 * pad_x
    h = fnt.size + 2 * pad_y
    d.rounded_rectangle((x, y, x + w, y + h), 14, fill=CARD + (255,), outline=EDGE + (255,), width=2)
    d.text((x + pad_x, y + h / 2), text, font=fnt, fill=color + (255,), anchor="lm")
    return h


def draw_end(im, L, t, a):
    layer = Image.new("RGBA", im.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    lg = logo(200 if not L.portrait else 280)
    y = 110 if not L.portrait else 330
    layer.alpha_composite(lg, (int((L.W - lg.width) / 2), y))
    y += lg.height + 28
    centered(d, "sheepdog", L.f_title, L.W / 2, y - 18, CREAM + (255,))
    y += L.f_title.size + 24
    tag = "Manage your Herdr agents by voice."
    for l in (wrap(tag, L.f_tag, L.W - 160, d) if L.portrait else [tag]):
        centered(d, l, L.f_tag, L.W / 2, y, CREAM + (255,))
        y += int(L.f_tag.size * 1.3)
    y += 44
    cmd = "herdr plugin install kccarlos/herdr-sheepdog"  # the README's command
    fnt = L.f_end_cmd
    while d.textlength(cmd, font=fnt) + 60 > L.W - 100:  # as wide as the frame allows
        fnt = mono(fnt.size - 2)
    w = d.textlength(cmd, font=fnt) + 60
    h = cmd_pill(d, L, (L.W - w) / 2, y, cmd, ORANGE, fnt=fnt)
    y += h + 26
    y += int(L.f_small.size * 1.2)
    centered(d, "iPhone app: testflight.apple.com/join/bjCaFYgK", L.f_small, L.W / 2, y, CREAM + (255,))
    y += int(L.f_small.size * 1.5)
    centered(d, "github.com/kccarlos/herdr-sheepdog", L.f_small, L.W / 2, y, CREAM + (255,))
    blend(im, layer, a)


def render(L, t):
    """One scene at a time: what changes fades out, then the next fades in
    (XF each), so two captions never overlap. The phone, the brand mark and
    the footnote stay put across the phone scenes."""
    im = L.bg.copy()
    i = scene_at(t)
    kind, length, d = SCENES[i]
    st, en = STARTS[i], STARTS[i] + length
    a = fade(t, st, en, XF)
    if i == 0:
        a = ease(t / 0.5) * (1 - ease((t - (en - XF)) / XF))
    if i == len(SCENES) - 1:
        a = ease((t - st) / XF)
    ph = [k for k, s in enumerate(SCENES) if s[0] == "phone"]
    p0, p1 = STARTS[ph[0]], STARTS[ph[-1]] + SCENES[ph[-1]][1]
    pa = fade(t, p0, p1, 2 * XF)
    if kind == "phone":
        draw_phone_scene(im, L, i, t, a, pa)
        draw_brandmark(im, L, pa)
        draw_footnote(im, L, pa)
    elif kind == "title":
        draw_title(im, L, t, a)
    elif kind == "devices":
        draw_devices(im, L, t, a)
    elif kind == "end":
        draw_end(im, L, t, a)
    return im.convert("RGB")


# --- audio -----------------------------------------------------------------
def make_audio(path):
    ins, fl = [], []
    for k, (id, who, text, st, n) in enumerate(LINES):
        ins += ["-i", os.path.join(TTS, id + ".wav")]
        ms = int(st * 1000)
        fl.append(f"[{k}:a]aresample=48000,volume={1.0 if who == 'sheepdog' else 0.95},adelay={ms}|{ms}[a{k}]")
    # the "needs you" chime: two generated tones, no music
    chime = [STARTS[i] + s[2]["chime"] for i, s in enumerate(SCENES) if s[2].get("chime") is not None][0]
    ins += ["-f", "lavfi", "-t", "1.2", "-i", "sine=frequency=1318:sample_rate=48000",
            "-f", "lavfi", "-t", "1.2", "-i", "sine=frequency=1760:sample_rate=48000"]
    k = len(LINES)
    c1, c2 = int(chime * 1000), int((chime + 0.26) * 1000)
    fl.append(f"[{k}:a]afade=t=out:st=0.0:d=0.5,volume=0.25,adelay={c1}|{c1}[c1]")
    fl.append(f"[{k + 1}:a]afade=t=out:st=0.0:d=0.7,volume=0.2,adelay={c2}|{c2}[c2]")
    names = "".join(f"[a{i}]" for i in range(len(LINES))) + "[c1][c2]"
    fl.append(f"{names}amix=inputs={len(LINES) + 2}:normalize=0,loudnorm=I=-16:TP=-1.5:LRA=9,"
              f"apad=whole_dur={LENGTH},atrim=0:{LENGTH}[out]")
    run("ffmpeg", "-loglevel", "error", "-y", *ins, "-filter_complex", ";".join(fl), "-map", "[out]",
        "-ar", "48000", "-c:a", "pcm_s16le", path)


def encode(L, name, audio, crf):
    out = os.path.join(OUT, name)
    p = subprocess.Popen(["ffmpeg", "-loglevel", "error", "-y", "-f", "rawvideo", "-pix_fmt", "rgb24", "-s", f"{L.W}x{L.H}",
                          "-r", str(FPS), "-i", "-", "-i", audio, "-c:v", "libx264", "-preset", "slow", "-crf", str(crf),
                          "-tune", "stillimage", "-pix_fmt", "yuv420p", "-profile:v", "high", "-c:a", "aac", "-b:a", "96k",
                          "-movflags", "+faststart", "-t", str(LENGTH), out], stdin=subprocess.PIPE)
    for f in range(int(LENGTH * FPS)):
        p.stdin.write(render(L, f / FPS).tobytes())
    p.stdin.close()
    p.wait()
    assert p.returncode == 0
    print(out, os.path.getsize(out) // 1024, "KB", file=sys.stderr)


def poster(L, t):
    """The title lockup with a play button."""
    im = render(L, t).convert("RGBA")
    d = ImageDraw.Draw(im, "RGBA")
    cx, cy, r = L.W - 170, L.H - 150, 62
    d.ellipse((cx - r, cy - r, cx + r, cy + r), fill=CREAM + (240,))
    d.polygon([(cx - 18, cy - 30), (cx - 18, cy + 30), (cx + 34, cy)], fill=BG + (255,))
    f = font(INTER, 30, 500)
    d.text((cx - r - 24, cy), "Play with sound", font=f, fill=SLATE + (255,), anchor="rm")
    im.convert("RGB").resize((1600, 900), Image.LANCZOS).save(os.path.join(OUT, "launch-poster.png"), optimize=True)


if __name__ == "__main__":
    os.makedirs(OUT, exist_ok=True)
    land, port = Layout(1920, 1080), Layout(1080, 1920)
    if os.environ.get("FRAMES"):
        # FRAMES="1.0,9.5": stills for checking, no video
        for t in os.environ["FRAMES"].split(","):
            for L, tag in ((land, "h"), (port, "v")):
                render(L, float(t)).save(os.path.join(OUT, f"frame-{tag}-{float(t):05.1f}.png"))
        sys.exit(0)
    audio = os.path.join(CACHE, "audio.wav")
    make_audio(audio)
    poster(land, 2.6)
    if os.environ.get("ONLY") == "poster":
        sys.exit(0)
    encode(land, "launch.mp4", audio, int(os.environ.get("CRF", "24")))
    if os.environ.get("ONLY") != "landscape":
        encode(port, "launch-vertical.mp4", audio, int(os.environ.get("CRF", "24")))
