"""Joins install.gif's two halves into one 800x560 MP4.

usage: install-compose.py terminal.mp4 frames-dir out.mp4

The terminal take (vhs) is kept as recorded, minus its idle tail. The
browser take is cdprec's screencast: a frame is only written when the page
changes, so a wait is one long frame. Waits are cut to at most HOLD seconds
(the key validating, the daemon restarting), and the last frame is held a
little. Frames are the page itself: no speed-up of anything that moves.
"""
import os, re, subprocess, sys

term, frames, out = sys.argv[1:4]
HOLD, TERM_MAX, W, H = 0.9, 6.0, 800, 560
work = os.path.dirname(out)

lines = [l.strip() for l in open(os.path.join(frames, "frames.txt")) if l.strip()]
items, i = [], 0
while i + 1 < len(lines):
    if lines[i].startswith("file") and lines[i + 1].startswith("duration"):
        name = re.findall(r"'(.*)'", lines[i])[0]
        items.append((name, float(lines[i + 1].split()[1])))
        i += 2
    else:
        i += 1
with open(os.path.join(work, "browser.txt"), "w") as f:
    for k, (name, d) in enumerate(items):
        d = min(d, HOLD + (1.2 if k == len(items) - 1 else 0))
        f.write(f"file '{os.path.join(frames, name)}'\nduration {d:.3f}\n")
    f.write(f"file '{os.path.join(frames, items[-1][0])}'\n")
vf = f"fps=30,scale={W}:{H}:flags=lanczos,format=yuv420p"
run = lambda *a: subprocess.run(a, check=True)
run("ffmpeg", "-loglevel", "error", "-y", "-f", "concat", "-safe", "0", "-i", os.path.join(work, "browser.txt"),
    "-vf", vf, "-c:v", "libx264", "-crf", "16", os.path.join(work, "b.mp4"))
run("ffmpeg", "-loglevel", "error", "-y", "-i", term, "-t", str(TERM_MAX), "-vf", vf, "-c:v", "libx264", "-crf", "16",
    os.path.join(work, "t.mp4"))
with open(os.path.join(work, "both.txt"), "w") as f:
    f.write(f"file '{os.path.join(work, 't.mp4')}'\nfile '{os.path.join(work, 'b.mp4')}'\n")
run("ffmpeg", "-loglevel", "error", "-y", "-f", "concat", "-safe", "0", "-i", os.path.join(work, "both.txt"), "-vf", "fps=30", "-c:v", "libx264", "-crf", "16", "-pix_fmt", "yuv420p", out)
