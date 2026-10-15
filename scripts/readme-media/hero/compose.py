"""Cuts the hero video from the phone take, the desk takes and the voice
clips: caption cards and overlays drawn with Pillow (Homebrew's ffmpeg has
no drawtext), each shot rendered as its own clip with its audio, then
joined and encoded.

    uv run --with pillow python scripts/readme-media/hero/compose.py WORK OUT_DIR

WORK holds phone/ (phone.sh), desk/ (desk.sh) and voice/ (tts.go).
Writes OUT_DIR/hero.mp4 (1920x1080) and hero-vertical.mp4 (1080x1920), and
WORK/compose/hero-cut.gif (the notification → approve moment) and
WORK/compose/timeline.txt.
The spoken lines come from lines.tsv; every caption is the same text.
"""

import os
import subprocess
import sys
import wave

from PIL import Image, ImageDraw, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
WORK = os.path.abspath(sys.argv[1])
OUT = os.path.abspath(sys.argv[2])
TMP = os.path.join(WORK, "compose")
os.makedirs(TMP, exist_ok=True)

FPS = 30
CREAM = (243, 236, 223)
NAVY = (31, 42, 60)
GOLD = (200, 144, 46)
MUTED = (110, 118, 132)
AVENIR = "/System/Library/Fonts/Avenir Next.ttc"
MONO = "/System/Library/Fonts/Menlo.ttc"


def font(size, weight="demi"):
    index = {"bold": 0, "demi": 2, "medium": 5, "regular": 7, "heavy": 8}[weight]
    return ImageFont.truetype(AVENIR, size, index=index)


def mono(size):
    return ImageFont.truetype(MONO, size)


def run(*args):
    subprocess.run(args, check=True)


def ffmpeg(*args):
    run("ffmpeg", "-loglevel", "error", "-y", *args)


# --- inputs -----------------------------------------------------------------

lines = {}
for row in open(os.path.join(HERE, "lines.tsv")):
    row = row.rstrip("\n")
    if not row or row.startswith("#"):
        continue
    ident, role, text = row.split("\t", 2)
    lines[ident] = (role, text)


def duration(ident):
    with wave.open(os.path.join(WORK, "voice", ident + ".wav")) as w:
        return w.getnframes() / w.getframerate()


phone_dir = os.path.join(WORK, "phone")
# The phone: two lock-screen screenshots (before and after the
# notification) and the video after the unlock (cfr2, its clock in
# rec2.t0: unix seconds at the video's 0).
t0 = {2: float(open(os.path.join(phone_dir, "rec2.t0")).read())}
marks = {}
for row in open(os.path.join(phone_dir, "marks.txt")):
    step, at = row.split()
    marks[step] = float(at)


def m(step, video=2):
    """Seconds into the phone video at a step of the UI test."""
    return marks[step] - t0[video]


def scenes(video):
    """Times where the phone video's picture changes."""
    out = subprocess.run(["ffmpeg", "-hide_banner", "-i", os.path.join(phone_dir, f"cfr{video}.mp4"), "-vf",
                          "select='gt(scene,0.01)',showinfo", "-f", "null", "-"], capture_output=True, text=True)
    return [float(x.split(":")[1]) for x in out.stderr.split() if x.startswith("pts_time:")]


SCENES = {2: scenes(2)}


def tap(step, video=2, before=0.1):
    """When a step marked in a phone video shows: the test marks a step
    just before it taps, and the tap itself can take a second or more (the
    query), so the first picture change after the mark is the tap."""
    at = m(step, video)
    for t in SCENES[video]:
        if at - before <= t <= at + 3:
            return t
    return at + 1.2


# --- drawing ----------------------------------------------------------------


def wrap(draw, text, fnt, width):
    words, out, line = text.split(), [], ""
    for w in words:
        trial = (line + " " + w).strip()
        if draw.textlength(trial, font=fnt) <= width:
            line = trial
        else:
            out.append(line)
            line = w
    if line:
        out.append(line)
    return out


def logo(size):
    img = Image.open(os.path.join(WORK, "logo.png")).convert("RGBA")
    px = img.load()
    for y in range(img.height):
        for x in range(img.width):
            r, g, b, a = px[x, y]
            if r > 245 and g > 245 and b > 245:
                px[x, y] = (r, g, b, 0)
    return img.resize((size, size), Image.LANCZOS)


def card(path, W, H, draw_fn):
    img = Image.new("RGBA", (W, H), CREAM + (255,))
    draw_fn(img, ImageDraw.Draw(img), W, H)
    img.save(path)
    return path


def centered(draw, W, y, text, fnt, fill):
    w = draw.textlength(text, font=fnt)
    draw.text(((W - w) / 2, y), text, font=fnt, fill=fill)


def title_card(img, d, W, H):
    s = min(W, H)
    lg = logo(int(s * 0.30))
    y = int(H * 0.5 - s * 0.36)
    img.alpha_composite(lg, ((W - lg.width) // 2, y))
    y += lg.height + int(s * 0.03)
    centered(d, W, y, "sheepdog", font(int(s * 0.11), "bold"), NAVY)
    y += int(s * 0.15)
    f = font(int(s * 0.042), "medium")
    for ln in wrap(d, "Walk away from your desk. Your coding agents keep working; sheepdog keeps you in the loop, in your earbuds.", f, W * 0.8):
        centered(d, W, y, ln, f, NAVY)
        y += int(s * 0.058)


def big_card(text, sub):
    def fn(img, d, W, H):
        s = min(W, H)
        f = font(int(s * 0.10), "bold")
        centered(d, W, H * 0.40, text, f, NAVY)
        centered(d, W, H * 0.40 + s * 0.15, sub, font(int(s * 0.045), "medium"), MUTED)
    return fn


def end_card(img, d, W, H):
    s = min(W, H)
    lg = logo(int(s * 0.20))
    y = int(H * 0.5 - s * 0.40)
    img.alpha_composite(lg, ((W - lg.width) // 2, y))
    y += lg.height + int(s * 0.04)
    centered(d, W, y, "Install it in herdr", font(int(s * 0.05), "demi"), MUTED)
    y += int(s * 0.09)
    cmd = "herdr plugin install kccarlos/herdr-sheepdog"
    size = int(s * 0.05)
    while d.textlength(cmd, font=mono(size)) > W * 0.86:
        size -= 2
    fm = mono(size)
    w = d.textlength(cmd, font=fm)
    pad = int(s * 0.03)
    box = ((W - w) / 2 - pad, y - pad * 0.6, (W + w) / 2 + pad, y + size * 1.3 + pad * 0.6)
    d.rounded_rectangle(box, radius=int(s * 0.02), fill=NAVY)
    d.text(((W - w) / 2, y), cmd, font=fm, fill=CREAM)
    y = box[3] + int(s * 0.06)
    url = "github.com/kccarlos/herdr-sheepdog"
    size = int(s * 0.055)
    while d.textlength(url, font=font(size, "bold")) > W * 0.86:
        size -= 2
    centered(d, W, y, url, font(size, "bold"), NAVY)
    y += int(s * 0.09)
    centered(d, W, y, "iPhone app: Sheepdog for herdr", font(int(s * 0.04), "medium"), MUTED)
    y += int(s * 0.12)
    centered(d, W, y, "Shown: the real app and herdr; the agents are scripted demo agents.", font(int(s * 0.026), "regular"), MUTED)


def speech_block(d, x, y, width, ident, size):
    role, text = lines[ident]
    who = "Sheepdog" if role == "sheepdog" else "You"
    d.text((x, y), who, font=font(int(size * 0.75), "bold"), fill=GOLD)
    y += int(size * 1.1)
    f = font(size, "medium")
    quoted = "“" + text + "”" if role == "you" else text
    for ln in wrap(d, quoted, f, width):
        d.text((x, y), ln, font=f, fill=NAVY)
        y += int(size * 1.32)
    return y


# --- layouts ----------------------------------------------------------------
# A layout knows where the phone goes and draws the beat title and the
# speech captions as overlays.


class Landscape:
    W, H = 1920, 1080
    name = "landscape"
    phone_h = 1000

    def phone_box(self, pw, ph):
        h = self.phone_h
        w = round(pw * h / ph / 2) * 2
        return (250, (self.H - h) // 2, w, h)

    def text_area(self):
        return 820, 1820  # x0, x1

    def phone_overlay(self, path, beat, ident, note):
        img = Image.new("RGBA", (self.W, self.H), (0, 0, 0, 0))
        d = ImageDraw.Draw(img)
        x0, x1 = self.text_area()
        y = 150
        fb = font(72, "bold")
        for ln in wrap(d, beat, fb, x1 - x0):
            d.text((x0, y), ln, font=fb, fill=NAVY)
            y += 90
        y += 50
        if ident:
            speech_block(d, x0, y, x1 - x0, ident, 50)
        if note:
            d.text((x0, self.H - 70), note, font=font(26, "regular"), fill=MUTED)
        img.save(path)

    def desk_overlay(self, path, beat, note):
        img = Image.new("RGBA", (self.W, self.H), (0, 0, 0, 0))
        d = ImageDraw.Draw(img)
        fb = font(64, "bold")
        band_h = 150
        d.rectangle((0, self.H - band_h, self.W, self.H), fill=NAVY + (238,))
        w = d.textlength(beat, font=fb)
        d.text(((self.W - w) / 2, self.H - band_h + 30), beat, font=fb, fill=CREAM)
        if note:
            fn = font(30, "demi")
            w = d.textlength(note, font=fn)
            d.rounded_rectangle((self.W - w - 70, 70, self.W - 30, 124), radius=27, fill=GOLD + (255,))
            d.text((self.W - w - 50, 76), note, font=fn, fill=(255, 255, 255))
        img.save(path)

    def desk_filter(self):
        return "scale=1920:1080"


class Portrait(Landscape):
    W, H = 1080, 1920
    name = "portrait"
    phone_h = 1180

    def phone_box(self, pw, ph):
        h = self.phone_h
        w = round(pw * h / ph / 2) * 2
        return ((self.W - w) // 2, 60, w, h)

    def phone_overlay(self, path, beat, ident, note):
        img = Image.new("RGBA", (self.W, self.H), (0, 0, 0, 0))
        d = ImageDraw.Draw(img)
        x0, x1 = 70, self.W - 70
        y = 60 + self.phone_h + 40
        fb = font(58, "bold")
        for ln in wrap(d, beat, fb, x1 - x0):
            d.text((x0, y), ln, font=fb, fill=NAVY)
            y += 72
        y += 20
        if ident:
            role, text = lines[ident]
            size = 40 if len(text) < 140 else 34
            speech_block(d, x0, y, x1 - x0, ident, size)
        if note:
            d.text((x0, self.H - 50), note, font=font(22, "regular"), fill=MUTED)
        img.save(path)

    def desk_overlay(self, path, beat, note):
        img = Image.new("RGBA", (self.W, self.H), (0, 0, 0, 0))
        d = ImageDraw.Draw(img)
        fb = font(64, "bold")
        y = 1380
        for ln in wrap(d, beat, fb, self.W - 140):
            w = d.textlength(ln, font=fb)
            d.text(((self.W - w) / 2, y), ln, font=fb, fill=NAVY)
            y += 80
        if note:
            fn = font(30, "demi")
            w = d.textlength(note, font=fn)
            d.rounded_rectangle(((self.W - w) / 2 - 20, 1250, (self.W + w) / 2 + 20, 1304), radius=27, fill=GOLD + (255,))
            d.text(((self.W - w) / 2, 1256), note, font=fn, fill=(255, 255, 255))
        img.save(path)

    def desk_filter(self):
        # The terminal's middle, as large as fits the width.
        return "crop=1540:1080:372:0,scale=1080:-2,pad=1080:1920:0:(1920-ih)/2-120:color=0xf3ecdf"


# --- the shots ----------------------------------------------------------------
# Each shot: a source (card / desk take / phone range), a length, and
# voice cues at offsets. A phone range shorter than its cues holds its last
# frame.

NOTE = "The real Sheepdog app (iOS Simulator), demo mode: scripted agents on the phone."


def shots():
    p = []
    p.append(dict(kind="card", draw="title", length=2.8))
    p.append(dict(kind="desk", take="working", start=0.6, length=5.0,
                  beat="Four coding agents at work in herdr.", note="demo agents"))
    p.append(dict(kind="card", draw="walk", length=2.2))
    # Lock screen: the notification arrives, the voice says it.
    lock = lock_video()
    p.append(dict(kind="phone", video="lock", start=0, end=LOCK_BEFORE + 0.3, beat="An agent needs you. You hear it first.",
                  cues=[("s_ask", LOCK_BEFORE + 0.3)]))
    # Unlocked: billing's card; approve.
    t = tap("tap_approve")
    a = max(0.0, m("approval", 2) - 0.3, t - 1.8)
    p.append(dict(kind="phone", video=2, start=a, end=t + 1.2, beat="Approve with one tap.",
                  cues=[("s_approved", t - a + 0.3)]))
    # "What's going on?": said just before the question shows (the tap on
    # the suggestion stands in for the words; the demo also takes them
    # spoken), the first frame held for it. The answer shows at the tap.
    t = tap("tap_status")
    a = t - 0.6
    q = duration("u_status")
    lead = max(0.0, q + 0.3 - (t - a))
    p.append(dict(kind="phone", video=2, start=a, end=tap("tap_failed") - 0.3, lead=lead, beat="Ask what\u2019s going on.",
                  cues=[("u_status", lead + t - a - q - 0.1), ("s_status", lead + t - a + 0.4)]))
    # "What failed?"
    t = tap("tap_failed")
    a = t - 0.6
    q = duration("u_failed")
    lead = max(0.0, q + 0.3 - (t - a))
    p.append(dict(kind="phone", video=2, start=a, end=m("agents_end", 2) - 0.2, lead=lead, beat="Ask what failed.",
                  cues=[("u_failed", lead + t - a - q - 0.1), ("s_failed", lead + t - a + 0.4)]))
    p.append(dict(kind="desk", take="done", start=0.3, length=4.6,
                  beat="Back at the desk, nothing is a surprise.", note="demo agents"))
    p.append(dict(kind="card", draw="end", length=4.6))
    return p


LOCK_BEFORE = 0.9  # seconds of the bare lock screen before the notification


def lock_video():
    """The lock-screen shot: the bare lock screen, then the notification."""
    out = os.path.join(phone_dir, "cfrlock.mp4")
    ffmpeg("-loop", "1", "-framerate", str(FPS), "-t", str(LOCK_BEFORE), "-i", os.path.join(phone_dir, "lock0.png"),
           "-loop", "1", "-framerate", str(FPS), "-t", "10", "-i", os.path.join(phone_dir, "lock1.png"),
           "-filter_complex", "[0:v][1:v]concat=n=2:v=1[v]", "-map", "[v]", "-c:v", "libx264", "-crf", "14",
           "-pix_fmt", "yuv420p", out)
    return out


def phone_size():
    out = subprocess.run(["ffprobe", "-v", "error", "-select_streams", "v", "-show_entries", "stream=width,height",
                          "-of", "csv=p=0", os.path.join(phone_dir, "cfr2.mp4")], capture_output=True, text=True, check=True)
    w, h = out.stdout.strip().split(",")
    return int(w), int(h)


def phone_mask(layout, w, h):
    path = os.path.join(TMP, f"mask-{layout.name}.png")
    img = Image.new("L", (w, h), 0)
    ImageDraw.Draw(img).rounded_rectangle((0, 0, w - 1, h - 1), radius=int(w * 0.13), fill=255)
    img.save(path)
    return path


def phone_bezel(layout, box):
    x, y, w, h = box
    path = os.path.join(TMP, f"bg-{layout.name}.png")
    img = Image.new("RGBA", (layout.W, layout.H), CREAM + (255,))
    d = ImageDraw.Draw(img)
    pad = max(10, w // 40)
    d.rounded_rectangle((x - pad, y - pad, x + w + pad, y + h + pad), radius=int(w * 0.13) + pad, fill=(22, 24, 28))
    img.save(path)
    return path


def render(layout):
    pw, ph = phone_size()
    box = phone_box = layout.phone_box(pw, ph)
    mask = phone_mask(layout, box[2], box[3])
    bezel = phone_bezel(layout, box)
    clips, timeline, spans, t = [], [], [], 0.0
    for i, s in enumerate(shots()):
        clip = os.path.join(TMP, f"{layout.name}-{i:02d}.mp4")
        cues = s.get("cues", [])
        length = s.get("length") or (s["end"] - s["start"])
        for ident, at in cues:
            length = max(length, at + duration(ident) + 0.5)
        audio_in, mix = [], []
        for k, (ident, at) in enumerate(cues):
            audio_in += ["-i", os.path.join(WORK, "voice", ident + ".wav")]
            mix.append((k, at))
        if s["kind"] == "card":
            png = os.path.join(TMP, f"card-{layout.name}-{s['draw']}.png")
            fn = {"title": title_card, "walk": big_card("You walk away.", "Earbuds in, phone in your pocket."), "end": end_card}[s["draw"]]
            card(png, layout.W, layout.H, fn)
            video_in = ["-loop", "1", "-framerate", str(FPS), "-t", f"{length:.3f}", "-i", png]
            vf = "[0:v]format=yuv420p[v]"
            n_v = 1
        elif s["kind"] == "desk":
            ov = os.path.join(TMP, f"desk-{layout.name}-{i}.png")
            layout.desk_overlay(ov, s["beat"], s.get("note"))
            video_in = ["-ss", f"{s['start']:.3f}", "-t", f"{length:.3f}", "-i", os.path.join(WORK, "desk", s["take"] + ".mp4"),
                        "-loop", "1", "-framerate", str(FPS), "-t", f"{length:.3f}", "-i", ov]
            vf = f"[0:v]fps={FPS},{layout.desk_filter()},format=rgba[d];[d][1:v]overlay=0:0,format=yuv420p[v]"
            n_v = 2
        else:
            # The voice line's caption shows while (and after) it is said;
            # before the first cue the beat stands alone.
            ovs = []
            first = cues[0][1] if cues else length
            o0 = os.path.join(TMP, f"phone-{layout.name}-{i}-0.png")
            layout.phone_overlay(o0, s["beat"], None, NOTE)
            ovs.append((o0, 0, first))
            for k, (ident, at) in enumerate(cues):
                end = cues[k + 1][1] if k + 1 < len(cues) else length
                o = os.path.join(TMP, f"phone-{layout.name}-{i}-{k + 1}.png")
                layout.phone_overlay(o, s["beat"], ident, NOTE)
                ovs.append((o, at, end))
            span = s["end"] - s["start"]
            lead = s.get("lead", 0.0)
            length = max(length, span + lead)
            hold = max(0.0, length - span - lead)
            video_in = ["-ss", f"{max(0, s['start']):.3f}", "-t", f"{span:.3f}", "-i", os.path.join(phone_dir, f"cfr{s['video']}.mp4"),
                        "-loop", "1", "-framerate", str(FPS), "-t", f"{length:.3f}", "-i", bezel,
                        "-loop", "1", "-framerate", str(FPS), "-t", f"{length:.3f}", "-i", mask]
            for o, _, _ in ovs:
                video_in += ["-loop", "1", "-framerate", str(FPS), "-t", f"{length:.3f}", "-i", o]
            x, y, w, h = box
            vf = (f"[0:v]fps={FPS},setpts=PTS-STARTPTS,tpad=start_mode=clone:start_duration={lead:.3f}:stop_mode=clone:stop_duration={hold + 0.1:.3f},"
                  f"scale={w}:{h}:flags=lanczos,format=rgba[ph];[2:v]format=gray[mk];[ph][mk]alphamerge[phm];"
                  f"[1:v][phm]overlay={x}:{y}:shortest=1[b0]")
            last = "b0"
            for k, (o, a_, b_) in enumerate(ovs):
                vf += f";[{last}][{3 + k}:v]overlay=0:0:enable='between(t,{a_:.3f},{b_:.3f})'[b{k + 1}]"
                last = f"b{k + 1}"
            vf += f";[{last}]format=yuv420p[v]"
            n_v = 3 + len(ovs)
        # Audio: the cues at their offsets over silence.
        af = f"anullsrc=r=48000:cl=stereo,atrim=0:{length:.3f}[sil]"
        labels = ["[sil]"]
        for k, at in mix:
            af += f";[{n_v + k}:a]aresample=48000,pan=stereo|c0=c0|c1=c0,adelay={int(at * 1000)}|{int(at * 1000)}[a{k}]"
            labels.append(f"[a{k}]")
        af += f";{''.join(labels)}amix=inputs={len(labels)}:normalize=0:duration=first,volume=1.0[a]"
        all_shots = shots()
        fades = []
        if i == 0 or all_shots[i - 1]["kind"] != s["kind"] or s["kind"] == "card":
            fades.append("fade=t=in:st=0:d=0.25")
        if i == len(all_shots) - 1 or all_shots[i + 1]["kind"] != s["kind"] or s["kind"] == "card":
            fades.append(f"fade=t=out:st={length - 0.25:.3f}:d=0.25")
        fade = f";[v]{','.join(fades) or 'null'}[vf]"
        ffmpeg(*video_in, *audio_in, "-filter_complex", vf + fade + ";" + af, "-map", "[vf]", "-map", "[a]",
               "-t", f"{length:.3f}", "-r", str(FPS), "-c:v", "libx264", "-crf", "16", "-preset", "veryfast",
               "-c:a", "pcm_s16le", clip)
        clips.append(clip)
        desc = s.get("beat") or s.get("draw")
        timeline.append(f"{t:6.2f}-{t + length:6.2f}  {s['kind']:5s}  {desc}" +
                        "".join(f"\n{'':16s}{t + at:6.2f}  {lines[ident][0]}: {lines[ident][1]}" for ident, at in cues))
        spans.append((t, t + length))
        t += length
    lst = os.path.join(TMP, f"{layout.name}.txt")
    with open(lst, "w") as f:
        for c in clips:
            f.write(f"file '{c}'\n")
    return lst, timeline, spans, t


def encode(lst, out, crf):
    ffmpeg("-f", "concat", "-safe", "0", "-i", lst, "-c:v", "libx264", "-preset", "slow", "-crf", str(crf),
           "-pix_fmt", "yuv420p", "-profile:v", "high", "-c:a", "aac", "-b:a", "128k", "-ar", "48000",
           "-movflags", "+faststart", out)


def main():
    os.makedirs(OUT, exist_ok=True)
    land = Landscape()
    lst, timeline, spans, total = render(land)
    encode(lst, os.path.join(OUT, "hero.mp4"), int(os.environ.get("CRF", "24")))
    with open(os.path.join(TMP, "timeline.txt"), "w") as f:
        f.write("\n".join(timeline) + f"\ntotal {total:.2f} s\n")
    print("\n".join(timeline), f"\ntotal {total:.2f} s")
    # A short GIF: the notification lands on the lock screen, then the card
    # is approved (shots 3 and 4, cut short). It stays in WORK: the
    # README's hero.gif comes from scripts/readme-media/bubbles/.
    lock, appr = spans[3], spans[4]
    parts = [(lock[0] + 0.4, lock[0] + 4.0), (appr[0], min(appr[1], appr[0] + 4.4))]
    gif = os.path.join(TMP, "hero-cut.gif")
    trims = "".join(f"[0:v]trim={a:.2f}:{b:.2f},setpts=PTS-STARTPTS[g{k}];" for k, (a, b) in enumerate(parts))
    ffmpeg("-i", os.path.join(OUT, "hero.mp4"), "-filter_complex",
           f"{trims}[g0][g1]concat=n=2:v=1,fps=10,scale=800:-1:flags=lanczos,split[a][b];"
           "[a]palettegen=stats_mode=diff:max_colors=96[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle", gif)
    subprocess.run(["gifsicle", "-O3", "--batch", gif])
    if not os.environ.get("NO_VERTICAL"):
        lst, _, _, _ = render(Portrait())
        encode(lst, os.path.join(OUT, "hero-vertical.mp4"), int(os.environ.get("CRF", "24")))


main()
