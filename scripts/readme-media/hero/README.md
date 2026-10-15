# Hero video

`assets/readme/hero.mp4` (1920x1080, about 59 s), `hero-vertical.mp4`
(1080x1920) and `hero.gif` (the notification → approve moment, 800 px wide).

```sh
IOS_REPO=/path/to/herdr-sheepdog-ios scripts/readme-media/hero/make.sh
STEPS=compose WORK=<dir from the last run> scripts/readme-media/hero/make.sh   # re-cut only
```

`IOS_REPO` must be a herdr-sheepdog-ios checkout that has
`HeroRecordingUITests` (kccarlos/herdr-sheepdog-ios#52). Needs Xcode and xcodegen, Go, herdr, vhs, ffmpeg,
gifsicle and uv (for Pillow; Homebrew's ffmpeg has no drawtext, so every
caption is a PNG).

## What is real

- **The phone** is the real Sheepdog app in the iOS Simulator, in its offline
  demo mode (four scripted agents on the phone: auth, billing, migrate,
  docs). `phone.sh` runs the iOS repo's `HeroRecordingUITests`, which locks
  the simulator, opens the app from the lock screen, taps "Yes, proceed"
  and the "What's going on?" and "What failed?" suggestions.
- **The notification** is sent with `xcrun simctl push`; its payload is the
  daemon's own (`payload.go` builds it with `internal/push`). The demo mode
  itself sends no notifications. The lock screen is two simulator
  screenshots (before and after it arrives), because the simulator's video
  recording freezes across a lock or an unlock; the app is filmed after the
  unlock. `phone.sh` fails when that recording froze anyway: run it again.
- **The desk** is real herdr (0.9) in vhs, with a throwaway HOME and config
  (never the real herdr, its sessions or plugins). The four panes run
  `demo-agent.sh`, which prints the same demo world's story and reports
  its state to herdr; they are labelled "scripted demo agent" and the shots
  carry a "demo agents" badge.
- **The voices** are text-to-speech of the demo world's exact lines
  (`lines.tsv`, from the iOS repo's `DemoWorld.swift`): Gemini TTS on
  Vertex AI (voice Kore for Sheepdog, Puck for you) with the
  `[gemini.vertex]` key from the sheepdog config, or macOS `say`
  (`TTS_SAY=1`, or when there is no key). The demo mode on a phone reads
  the same lines with the on-device voice. The questions are asked by
  tapping the suggestion buttons; the voice clips stand in for saying them
  (the demo also takes spoken questions). The "What failed?" answer is
  cut after its second sentence. Every caption is the spoken line.
- No music.

## Files

| file | does |
|---|---|
| `make.sh` | runs the steps below into `$WORK` and `assets/readme/` |
| `phone.sh` | the phone take: lock screenshots, `cfr2.mp4`, `marks.txt` |
| `payload.go` | the APNs payload for billing's approval |
| `desk.sh`, `desk.tape`, `desk-setup.sh`, `demo-agent.sh` | the two herdr shots |
| `tts.go`, `lines.tsv` | the voice clips |
| `compose.py` | cards, captions, timing and the encodes |

The cut follows the test's step marks; a tap shows up to a second or two
after its mark (the UI query), so `compose.py` moves each tap to the first
picture change after it. Re-cut with `STEPS=compose` after a change to the
captions or the timing.
