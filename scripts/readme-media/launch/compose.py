"""The launch video: voice first, the phone as the proof.

usage: compose.py WORK OUT_DIR

WORK holds what make.sh made: tts/*.wav (the voice lines), phone/ (the
iOS demo take from phone.sh: lock1.png, cfr.mp4) and
logo.png. Writes OUT_DIR/launch.mp4 (1920x1080), launch-vertical.mp4
(1080x1920) and launch-poster.png.

Everything on screen is drawn here, frame by frame, from the audio:
  - the waveform is the voice clip's own loudness, scrolling outward from
    the middle;
  - the words are lit in order across the part of the clip that is
    loud (karaoke): the timing is spread over the letters, not read from a
    speech recogniser;
  - captions are burned in, so it works muted.
No chat bubbles, no typing dots.
"""
import array, json, math, os, subprocess, sys, wave

from PIL import Image, ImageDraw, ImageFilter, ImageFont, ImageChops

WORK = os.path.abspath(sys.argv[1])
OUT = os.path.abspath(sys.argv[2])
PHONE = os.path.join(WORK, "phone")
TTS = os.path.join(WORK, "tts")
CACHE = os.path.join(WORK, "launch")
os.makedirs(CACHE, exist_ok=True)
FPS = 30

# --- the cut ---------------------------------------------------------------
# (id, who, shown text, start in the video in s). The audio files are
# tts/<id>.wav; the shown text is what is spoken, as written.
LINES = [
    ("s_ask", "sheepdog", "codex-payments wants to run npm install p-retry. Approve?", 3.3),
    ("u_approve", "you", "Approve.", 8.7),
    ("u_status", "you", "What's going on?", 11.6),
    ("s_status", "sheepdog", "claude-auth is done, claude-migration failed, codex-docs is still writing.", 13.5),
    ("u_failed", "you", "Why did the migration fail?", 19.9),
    ("s_failed", "sheepdog", "The column already existed. It rolled back, nothing changed.", 22.5),
]
HOOK_END = 3.0      # the hook headline, then the voices
APP_AT = 5.2        # the phone leaves the lock screen
AGENTS_AT = 27.2    # the app's agent list beat
CAPTION_AT = 28.0   # "the app shows the same agents"
END_AT = 30.4       # the end card
LENGTH = 36.0
# The phone video's moments, in seconds into cfr2.mp4: (out time, video time).
# Filled from the take's scene changes by phone_map() below.

# --- look ------------------------------------------------------------------
BG_TOP, BG_BOT = (20, 28, 44), (9, 13, 22)
INK = (244, 246, 250)
DIM = (96, 108, 130)
YOU = (255, 196, 84)      # warm: your voice
DOG = (92, 214, 196)      # cool: sheepdog
SF = "/System/Library/Fonts/SFNS.ttf"
MONO = "/System/Library/Fonts/Menlo.ttc"
EMOJI = "/System/Library/Fonts/Apple Color Emoji.ttc"


def font(px, weight=b"Semibold"):
    f = ImageFont.truetype(SF, px)
    try:
        f.set_variation_by_name(weight)
    except Exception:
        pass
    return f


def run(*a, **k):
    return subprocess.run(a, check=True, **k)


def lerp(a, b, x):
    return a + (b - a) * x


def clamp(x, lo=0.0, hi=1.0):
    return max(lo, min(hi, x))


def ease(x):
    x = clamp(x)
    return x * x * (3 - 2 * x)


def mix(c1, c2, x):
    return tuple(int(lerp(a, b, clamp(x))) for a, b in zip(c1, c2))


# --- audio analysis --------------------------------------------------------
def read_wav(path):
    with wave.open(path) as w:
        n, rate = w.getnframes(), w.getframerate()
        a = array.array("h")
        a.frombytes(w.readframes(n))
    return a, rate


class Clip:
    """A voice clip: its loudness at FPS, and where speech starts and ends."""

    def __init__(self, id, who, text, at):
        self.id, self.who, self.text, self.at = id, who, text, at
        a, rate = read_wav(os.path.join(TTS, id + ".wav"))
        self.dur = len(a) / rate
        hop = rate // FPS
        env = []
        for i in range(0, len(a) - hop + 1, hop):
            seg = a[i:i + hop]
            env.append(math.sqrt(sum(x * x for x in seg) / len(seg)) / 32768)
        peak = max(env) or 1
        self.env = [min(1.0, e / peak) for e in env]
        loud = [i for i, e in enumerate(self.env) if e > 0.06]
        self.s0 = loud[0] / FPS
        self.s1 = (loud[-1] + 1) / FPS
        self.words = self.timings()

    def timings(self):
        words = self.text.split()
        w = [len(x.strip(".,?!")) + (4 if x[-1] in ",.?" else 0) for x in words]
        tot = sum(w)
        out, acc = [], 0
        for word, wt in zip(words, w):
            out.append((word, self.s0 + (self.s1 - self.s0) * acc / tot))
            acc += wt
        return out

    def level(self, t):
        """Loudness 0..1 at t seconds into the clip."""
        x = t * FPS
        i = int(math.floor(x))
        if i < 0 or i >= len(self.env) - 1:
            return 0.0
        f = x - i
        return lerp(self.env[i], self.env[i + 1], f)


clips = [Clip(*l) for l in LINES]
for c in clips:
    print(f"{c.id}: {c.at:.1f} s, speech {c.at + c.s0:.1f}-{c.at + c.s1:.1f}", file=sys.stderr)
    assert c.at + c.s1 < END_AT


# --- the phone -------------------------------------------------------------
SW, SH = 460, 1000     # screen
V0 = 48.0              # the phone take is cut from this second of cfr.mp4
BEZEL = 12


def prep_phone():
    d = os.path.join(CACHE, "pf")
    marker = os.path.join(d, "done")
    if not os.path.exists(marker):
        os.makedirs(d, exist_ok=True)
        run("ffmpeg", "-loglevel", "error", "-y", "-ss", str(V0), "-i", os.path.join(PHONE, "cfr.mp4"), "-t", "28",
            "-vf", f"fps={FPS},scale={SW}:{SH}:flags=lanczos", "-q:v", "3", os.path.join(d, "%04d.jpg"))
        for n in ("lock1",):
            Image.open(os.path.join(PHONE, n + ".png")).convert("RGB").resize((SW, SH), Image.LANCZOS).save(os.path.join(d, n + ".png"))
        open(marker, "w").close()
    return d


PF = prep_phone()
_cache = {}


def phone_frame(v):
    """Video frame at video time v (s)."""
    i = max(1, int(round((v - V0) * FPS)) + 1)
    p = os.path.join(PF, f"{i:04d}.jpg")
    while not os.path.exists(p) and i > 1:
        i -= 1
        p = os.path.join(PF, f"{i:04d}.jpg")
    if p not in _cache:
        if len(_cache) > 60:
            _cache.clear()
        _cache[p] = Image.open(p).convert("RGB")
    return _cache[p]


LOCK1 = Image.open(os.path.join(PF, "lock1.png")).convert("RGB")


def load_map():
    """out time -> video time anchors (from scenes.json, made by phone_map)."""
    return json.load(open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "phone-map.json")))


MAP = load_map()  # list of [out_t, video_t]; between anchors the video plays at 1x or holds


def video_time(t):
    a = MAP
    if t <= a[0][0]:
        return a[0][1]
    for (t0, v0), (t1, v1) in zip(a, a[1:]):
        if t0 <= t < t1:
            return lerp(v0, v1, (t - t0) / (t1 - t0))
    return a[-1][1]


def phone_image(t):
    if t < APP_AT - 0.35:
        return LOCK1
    v = phone_frame(video_time(t))
    if t < APP_AT:
        return Image.blend(LOCK1, v, ease((t - (APP_AT - 0.35)) / 0.35))
    return v


# --- drawing ---------------------------------------------------------------
def gradient(w, h):
    g = Image.new("RGB", (1, h))
    for y in range(h):
        g.putpixel((0, y), mix(BG_TOP, BG_BOT, y / h))
    return g.resize((w, h))


def rounded_mask(w, h, r):
    m = Image.new("L", (w * 2, h * 2), 0)
    ImageDraw.Draw(m).rounded_rectangle((0, 0, w * 2 - 1, h * 2 - 1), r * 2, fill=255)
    return m.resize((w, h), Image.LANCZOS)


SCREEN_MASK = rounded_mask(SW, SH, 56)
BEZEL_IMG = Image.new("RGBA", (SW + 2 * BEZEL, SH + 2 * BEZEL), (0, 0, 0, 0))
ImageDraw.Draw(BEZEL_IMG).rounded_rectangle((0, 0, SW + 2 * BEZEL - 1, SH + 2 * BEZEL - 1), 68, fill=(34, 38, 48, 255), outline=(70, 76, 92, 255), width=2)


def emoji(ch, px):
    f = ImageFont.truetype(EMOJI, 160)
    im = Image.new("RGBA", (200, 200), (0, 0, 0, 0))
    ImageDraw.Draw(im).text((0, 0), ch, font=f, embedded_color=True)
    im = im.crop(im.getbbox())
    h = px
    return im.resize((int(im.width * h / im.height), h), Image.LANCZOS)


EMOJI_MIC, EMOJI_SPK = {}, {}


def logo(px):
    return Image.open(os.path.join(WORK, "logo.png")).convert("RGBA").resize((px, px), Image.LANCZOS)


class Layout:
    def __init__(self, W, H, portrait):
        self.W, self.H, self.portrait = W, H, portrait
        if portrait:
            self.phone_xy = ((W - SW - 2 * BEZEL) // 2, 56)
            self.px0, self.pw = 80, W - 160
            self.hook_y, self.label_y, self.wave_cy, self.text_y = 1210, 1160, 1330, 1450
            self.f_label, self.f_text, self.f_head, self.f_small = font(58), font(64, b"Medium"), font(88, b"Bold"), font(32, b"Medium")
            self.wave_bars, self.wave_h = 40, 150
            self.cmd = font(34, b"Medium")
        else:
            self.phone_xy = (150, (H - SH - 2 * BEZEL) // 2)
            self.px0, self.pw = 800, 1000
            self.hook_y, self.label_y, self.wave_cy, self.text_y = 380, 300, 470, 610
            self.f_label, self.f_text, self.f_head, self.f_small = font(64), font(72, b"Medium"), font(104, b"Bold"), font(32, b"Medium")
            self.wave_bars, self.wave_h = 52, 170
            self.cmd = font(44, b"Medium")
        self.bg = gradient(W, H)
        self.logo = logo(84 if not portrait else 70)


def wrap(text_words, fnt, maxw, draw):
    lines, cur = [], []
    for w in text_words:
        t = " ".join(cur + [w])
        if cur and draw.textlength(t, font=fnt) > maxw:
            lines.append(cur)
            cur = [w]
        else:
            cur.append(w)
    if cur:
        lines.append(cur)
    return lines


def draw_waveform(im, L, who, clip, tc, active, fade):
    """Bars scroll outward from the middle with the voice's loudness."""
    d = ImageDraw.Draw(im, "RGBA")
    col = YOU if who == "you" else DOG
    n, hmax = L.wave_bars, L.wave_h
    gap = 6
    bw = (L.pw - gap * (n - 1)) / n
    cy = L.wave_cy
    for k in range(n):
        side = abs(k - (n - 1) / 2)
        lv = clip.level(tc - side * 0.035) if active else 0.0
        # a little shape so quiet parts still breathe, and the centre is tallest
        h = 8 + hmax * (lv ** 0.8) * (1 - 0.35 * side / (n / 2))
        x = L.px0 + k * (bw + gap)
        a = int(255 * fade * (1.0 if active else 0.25))
        d.rounded_rectangle((x, cy - h / 2, x + bw, cy + h / 2), bw / 2, fill=col + (a,))


def draw_voice_ui(im, L, t):
    d = ImageDraw.Draw(im, "RGBA")
    # the active clip (or the last one, dimmed, between turns)
    cur = None
    for c in clips:
        if t >= c.at - 0.15:
            cur = c
    if cur is None:
        return
    tc = t - cur.at
    active = cur.s0 - 0.05 <= tc <= cur.s1 + 0.12
    # fade the whole block in at the start of a clip and out when the next begins
    fin = ease((t - (cur.at - 0.15)) / 0.25)
    nxt = [c for c in clips if c.at > cur.at]
    fout = 1.0
    if nxt:
        fout = 1 - ease((t - (nxt[0].at - 0.45)) / 0.3)
    # a long silence after the clip: let the text rest, then clear
    hold_end = cur.at + cur.s1 + (0.6 if cur is clips[-1] else 1.6)
    if not nxt or nxt[0].at > hold_end + 0.5:
        fout = min(fout, 1 - ease((t - hold_end) / 0.5))
    a = fin * fout
    if a <= 0.01:
        return
    col = YOU if cur.who == "you" else DOG
    # speaker label: emoji + name
    key = (cur.who, L.W)
    em = emoji("\U0001F399" if cur.who == "you" else "\U0001F50A", 62 if not L.portrait else 52)
    name = "You" if cur.who == "you" else "sheepdog"
    layer = Image.new("RGBA", im.size, (0, 0, 0, 0))
    ld = ImageDraw.Draw(layer)
    layer.paste(em, (L.px0, L.label_y), em)
    ld.text((L.px0 + em.width + 22, L.label_y - 4), name, font=L.f_label, fill=col + (255,))
    # the waveform and the words
    draw_waveform(layer, L, cur.who, cur, tc, active, 1.0)
    words = [w for w, _ in cur.words]
    lines = wrap(words, L.f_text, L.pw, ld)
    lh = int(L.f_text.size * 1.28)
    i = 0
    for r, line in enumerate(lines):
        x, y = L.px0, L.text_y + r * lh
        for w in line:
            start = cur.words[i][1]
            lit = ease((tc - start) / 0.12)
            c = mix(DIM, INK, lit)
            ld.text((x, y), w, font=L.f_text, fill=c + (255,))
            x += ld.textlength(w + " ", font=L.f_text)
            i += 1
    if a < 1:
        layer.putalpha(ImageChops.multiply(layer.getchannel("A"), Image.new("L", im.size, int(255 * a))))
    im.alpha_composite(layer)


def draw_phone(im, L, t):
    x, y = L.phone_xy
    shadow = Image.new("RGBA", im.size, (0, 0, 0, 0))
    ImageDraw.Draw(shadow).rounded_rectangle((x - 4, y + 14, x + SW + 2 * BEZEL + 4, y + SH + 2 * BEZEL + 22), 76, fill=(0, 0, 0, 120))
    im.alpha_composite(shadow.filter(ImageFilter.GaussianBlur(18)))
    im.alpha_composite(BEZEL_IMG, (x, y))
    screen = phone_image(t).convert("RGBA")
    screen.putalpha(SCREEN_MASK)
    im.alpha_composite(screen, (x + BEZEL, y + BEZEL))


def centered(d, text, fnt, cx, y, fill):
    w = d.textlength(text, font=fnt)
    d.text((cx - w / 2, y), text, font=fnt, fill=fill)


def draw_hook(im, L, t):
    if t > HOOK_END + 0.4:
        return
    a = ease(t / 0.5) * (1 - ease((t - HOOK_END) / 0.4))
    layer = Image.new("RGBA", im.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    words = "Manage your Herdr agents by voice.".split()
    lines = wrap(words, L.f_head, L.pw, d)
    y = L.hook_y
    for line in lines:
        d.text((L.px0, y), " ".join(line), font=L.f_head, fill=INK + (255,))
        y += int(L.f_head.size * 1.14)
    d.text((L.px0, y + 24), "Hands-free, from your phone.", font=L.f_label, fill=DOG + (255,))
    layer.putalpha(ImageChops.multiply(layer.getchannel("A"), Image.new("L", im.size, int(255 * a))))
    im.alpha_composite(layer)


def draw_chrome(im, L, t):
    """Logo + name, and the 'demo agents' note while the demo world shows."""
    d = ImageDraw.Draw(im, "RGBA")
    x = L.px0
    y = 70 if not L.portrait else 1030
    if not L.portrait:
        im.alpha_composite(L.logo, (x, y))
        d.text((x + L.logo.width + 18, y + 18), "sheepdog", font=L.f_label, fill=INK + (230,))
    if APP_AT - 1 < t < END_AT:
        a = int(255 * ease((t - APP_AT + 1) / 0.5) * (1 - ease((t - END_AT + 0.4) / 0.4)))
        txt = "demo agents: scripted, on this phone"
        fy = L.H - 90 if not L.portrait else L.H - 120
        d.text((L.px0, fy), txt, font=L.f_small, fill=DIM + (a,))


def draw_agents_caption(im, L, t):
    if not (CAPTION_AT - 0.2 < t < END_AT):
        return
    a = ease((t - CAPTION_AT) / 0.4) * (1 - ease((t - END_AT + 0.5) / 0.4))
    layer = Image.new("RGBA", im.size, (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    d.text((L.px0, L.text_y - 60 if not L.portrait else L.text_y - 30), "The app shows the same agents.", font=L.f_text, fill=INK + (255,))
    d.text((L.px0, (L.text_y - 60 if not L.portrait else L.text_y - 30) + int(L.f_text.size * 1.4)), "Your hands never have to.", font=L.f_text, fill=DOG + (255,))
    layer.putalpha(ImageChops.multiply(layer.getchannel("A"), Image.new("L", im.size, int(255 * a))))
    im.alpha_composite(layer)


def draw_end(im, L, t):
    if t < END_AT - 0.4:
        return
    a = ease((t - END_AT + 0.4) / 0.6)
    layer = Image.new("RGBA", im.size, BG_BOT + (255,))
    layer.paste(L.bg, (0, 0))
    d = ImageDraw.Draw(layer, "RGBA")
    cx = L.W // 2
    big = logo(200 if not L.portrait else 190)
    y = 150 if not L.portrait else 330
    layer.alpha_composite(big, (cx - big.width // 2, y))
    y += big.height + 40
    head = L.f_head
    words = "Manage your Herdr agents by voice.".split()
    for line in wrap(words, head, L.W - 200 if not L.portrait else L.W - 140, d):
        centered(d, " ".join(line), head, cx, y, INK + (255,))
        y += int(head.size * 1.16)
    y += 50

    def cmdbox(text, y, label=None):
        w = d.textlength(text, font=L.cmd)
        pad = 34
        bx0, bx1 = cx - w / 2 - pad, cx + w / 2 + pad
        d.rounded_rectangle((bx0, y, bx1, y + L.cmd.size + 2 * 22), 18, fill=(255, 255, 255, 22), outline=(255, 255, 255, 60), width=2)
        centered(d, text, L.cmd, cx, y + 20, DOG + (255,))
        return y + L.cmd.size + 2 * 22 + 26

    y = cmdbox("herdr plugin install kccarlos/herdr-sheepdog", y)
    centered(d, "then", L.f_small, cx, y - 8, DIM + (255,))
    y += 44
    y = cmdbox("sheepdog setup", y)
    centered(d, "github.com/kccarlos/herdr-sheepdog", L.f_label if L.portrait else L.f_small, cx, y + 20, INK + (200,))
    centered(d, "demo agents in this video are scripted", L.f_small, cx, L.H - 90, DIM + (255,))
    layer.putalpha(Image.new("L", im.size, int(255 * a)))
    im.alpha_composite(layer)


def render(L, t):
    im = L.bg.convert("RGBA")
    draw_phone(im, L, t)
    draw_chrome(im, L, t)
    draw_hook(im, L, t)
    draw_voice_ui(im, L, t)
    draw_agents_caption(im, L, t)
    draw_end(im, L, t)
    return im.convert("RGB")


# --- audio -----------------------------------------------------------------
def make_audio(path):
    ins, fl = [], []
    for k, c in enumerate(clips):
        ins += ["-i", os.path.join(TTS, c.id + ".wav")]
        ms = int(c.at * 1000)
        gain = 1.0 if c.who == "sheepdog" else 0.95
        fl.append(f"[{k}:a]aresample=48000,volume={gain},adelay={ms}|{ms}[a{k}]")
    # a notification chime at the first frame (generated tones, no music)
    ins += ["-f", "lavfi", "-t", "1.2", "-i", "sine=frequency=1318:sample_rate=48000",
            "-f", "lavfi", "-t", "1.2", "-i", "sine=frequency=1760:sample_rate=48000"]
    k = len(clips)
    fl.append(f"[{k}:a]afade=t=out:st=0.0:d=0.5,volume=0.25,adelay=300|300[c1]")
    fl.append(f"[{k+1}:a]afade=t=out:st=0.0:d=0.7,volume=0.2,adelay=560|560[c2]")
    names = "".join(f"[a{i}]" for i in range(len(clips))) + "[c1][c2]"
    fl.append(f"{names}amix=inputs={len(clips)+2}:normalize=0,loudnorm=I=-16:TP=-1.5:LRA=9,"
              f"apad=whole_dur={LENGTH},atrim=0:{LENGTH},afade=t=out:st={LENGTH-1.2}:d=1.2[out]")
    run("ffmpeg", "-loglevel", "error", "-y", *ins, "-filter_complex", ";".join(fl), "-map", "[out]",
        "-c:a", "pcm_s16le", path)


def encode(L, name, audio):
    out = os.path.join(OUT, name)
    p = subprocess.Popen(["ffmpeg", "-loglevel", "error", "-y", "-f", "rawvideo", "-pix_fmt", "rgb24", "-s", f"{L.W}x{L.H}",
                          "-r", str(FPS), "-i", "-", "-i", audio, "-c:v", "libx264", "-preset", "slow", "-crf", "21",
                          "-pix_fmt", "yuv420p", "-profile:v", "high", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart",
                          "-t", str(LENGTH), out], stdin=subprocess.PIPE)
    for f in range(int(LENGTH * FPS)):
        p.stdin.write(render(L, f / FPS).tobytes())
    p.stdin.close()
    p.wait()
    assert p.returncode == 0
    print(out, os.path.getsize(out) // 1024, "KB", file=sys.stderr)


def poster(L, t):
    """The first strong frame (the headline over the lock screen) with a play icon."""
    im = render(L, t).convert("RGBA")
    d = ImageDraw.Draw(im, "RGBA")
    cx, cy, r = L.px0 + 90, 845, 78
    layer = Image.new("RGBA", im.size, (0, 0, 0, 0))
    ld = ImageDraw.Draw(layer)
    ld.ellipse((cx - r, cy - r, cx + r, cy + r), fill=(255, 255, 255, 235))
    ld.polygon([(cx - 22, cy - 38), (cx - 22, cy + 38), (cx + 42, cy)], fill=(20, 28, 44, 255))
    im.alpha_composite(layer.filter(ImageFilter.GaussianBlur(0.6)))
    ld2 = ImageDraw.Draw(im, "RGBA")
    ld2.text((cx + r + 30, cy - 22), "Play with sound", font=L.f_label, fill=INK + (240,))
    im.convert("RGB").resize((1600, 900), Image.LANCZOS).save(os.path.join(OUT, "launch-poster.png"), optimize=True)


if __name__ == "__main__":
    os.makedirs(OUT, exist_ok=True)
    audio = os.path.join(CACHE, "audio.wav")
    make_audio(audio)
    land, port = Layout(1920, 1080, False), Layout(1080, 1920, True)
    poster_t = float(os.environ.get("POSTER_T", "3.6"))
    if os.environ.get("ONLY") == "poster":
        poster(land, poster_t)
        sys.exit(0)
    encode(land, "launch.mp4", audio)
    encode(port, "launch-vertical.mp4", audio)
    poster(land, poster_t)
