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
  stay at work) for the Agents page, claude-auth's live terminal and its
  diff; then the demo running for codex-payments' question, Approve,
  "What's going on?" and the "Your agents" card. `marks.txt` holds the
  test's steps; `compose.py` cuts the scenes by them.
- **Voices**: Gemini TTS on Vertex AI (`lines.tsv`, the demo world's exact
  lines as the take shows them; Puck is you, Kore is sheepdog) through
  `hero/tts.go`, with `VERTEX_API_KEY` and `VERTEX_PROJECT` from the
  environment. A rendered line is cached and never requested again; with
  `-strict` a failed line stops the run instead of falling back to `say`.
  Two lines are not in the demo: the spoken approval, "Yes, go ahead." and
  "Approved. Installing it now." (`u_yes`, `s_ok`). The take shows the
  approval as a tap, so `compose.py` skips the tap and repaints the settled
  card's "by tap" as "by voice", which is what the app logs for a spoken
  approval. The only other sound is a two-tone chime made with ffmpeg's sine source.
- **Devices scene**: three phones side by side, the live terminal (the take,
  playing), the diff (a frame of the take) and the Agents page grouped by
  computer. The last is `agents-by-computer-dark.png`, a simulator
  screenshot of the app with two computers paired and one offline (660 px
  wide), with the take's 9:41 status bar pasted over its clock. Replace it
  with a fresh screenshot of the same page when the page changes.
- **Waveform**: when someone speaks, a bar waveform fills the speaker's
  pill, orange for you (the product listening) and cream for the voice. It
  is drawn from the audio: `compose.py` reads each line's WAV, takes the RMS
  of every 1/30 s slice, scales the loudest to 1, and draws 51 or so bars
  from the middle outward, each bar showing the loudness a few frames
  earlier the farther it is from the centre (so it scrolls outward). Between
  turns the bars rest as dots. Nothing else is animated to the sound.
- **Everything else** is drawn by `compose.py` in the brand
  ([docs/dev/brand.md](../../../docs/dev/brand.md)): the title, captions,
  speaker labels, the devices scene and the end card (one orange command,
  `herdr plugin install kccarlos/herdr-sheepdog`), in Outfit and Inter from
  `web/static/fonts`. A footnote says the voices are text-to-speech.

Scenes (52 s): title 4.2, Agents 5.0, blocked 7.0, approve by voice 5.4,
"What's going on?" 8.2, "Show me the agents" 9.4, devices 6.4, end card 6.5.

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
