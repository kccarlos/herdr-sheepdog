# Launch media

`assets/readme/launch.mp4` (1920x1080, H.264 + AAC), `launch-vertical.mp4`
(1080x1920), `launch-poster.png` (the title lockup with a play button) and
`assets/readme/install.gif` (800 px, about 18 s). The storyboard is
[docs/dev/launch-video.md](../../../docs/dev/launch-video.md); this cut is its
synthetic version, made from the parts the offline demo can show honestly.

## Video

```sh
SIM=<udid of an existing iPhone simulator> op run --env-file=.env -- scripts/readme-media/launch/make.sh
STEPS=compose WORK=<dir from the last run> scripts/readme-media/launch/make.sh   # re-cut only
```

Needs Xcode, xcodegen, Go, ffmpeg and uv. `phone.sh` never creates a
simulator: pass an existing one that is shut down or free.

What is real:

- **Phone**: the real app in the iOS Simulator, offline demo, dark mode,
  driven by `LaunchRecordingUITests` and filmed with `simctl io recordVideo`.
  Two launches: the demo paused (`-sheepdogDemoPaused`, so all four agents
  stay at work) for the Agents page, claude-auth's window and its Changes
  and diff; then the demo running for codex-payments' question, Approve,
  "What's going on?" and the "Your agents" card. `marks.txt` holds the
  test's steps; `compose.py` cuts the scenes by them.
- **Voices**: Gemini TTS on Vertex AI (`lines.tsv`, the demo world's exact
  lines as the take shows them; Puck is you, Kore is sheepdog) through
  `hero/tts.go`, with `VERTEX_API_KEY` and `VERTEX_PROJECT` from the
  environment. A rendered line is cached and never requested again; with
  `-strict` a failed line stops the run instead of falling back to `say`.
  The only other sound is a two-tone chime made with ffmpeg's sine source.
- **Everything else** is drawn by `compose.py` in the brand
  ([docs/dev/brand.md](../../../docs/dev/brand.md)): the title, captions, speaker
  labels, the setup steps and the end card, in Outfit and Inter from
  `web/static/fonts`. There is no waveform: the voices are TTS, so nothing
  imitates a live session, and a footnote says so.

Not shown, because the demo cannot do them honestly: the lock-screen
notification and the Live Activity (a real device), sending a prompt or a
photo, starting an agent and the shell (a real daemon; the demo answers
"This is the demo"). The storyboard lists the demo changes that would make
them filmable.

Simulator note (iOS 27): after a lock and unlock the display stays frozen
for the recorder, so the take never locks the phone.

## install.gif

```sh
scripts/readme-media/launch/install-gif.sh   # needs $GEMINI_API_KEY, vhs, Brave or Chrome
```

Terminal half (vhs): `herdr plugin install kccarlos/herdr-sheepdog` is a
shell function that runs the plugin's real build step
(`scripts/plugin-fetch.sh`) against a local release folder, because there is
no GitHub release yet; then the real `sheepdog setup`. Browser half
(`../cdprec.go`, headless): the real `/setup` page of the daemon it started,
on port 7790 in a throwaway HOME. `setup-page.js` types a placeholder key
while the daemon validates the real key from `$GEMINI_API_KEY` (never on
screen or in a file that outlives the run), turns every address into
192.168.1.20 and swaps the pairing QR for a dummy one. The login-service box
is never ticked.
