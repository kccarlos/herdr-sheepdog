# Launch media

`assets/readme/launch.mp4` (1920x1080, 36 s, H.264 + AAC), `launch-vertical.mp4`
(1080x1920), `launch-poster.png` (the README's poster, a play icon over the
first strong frame) and `assets/readme/install.gif` (800 px, about 18 s).

## Video

```sh
scripts/readme-media/launch/make.sh
STEPS=compose WORK=<dir from the last run> scripts/readme-media/launch/make.sh   # re-cut only
```

`IOS_REPO` (default: `ios/` of this repository) needs `HeroRecordingUITests` and `LaunchRecordingUITests`. Needs Xcode, xcodegen, Go, ffmpeg and uv.

What is real:

- **Voices**: Gemini TTS on Vertex AI (`lines.tsv`, the demo world's exact
  lines; Puck is you, Kore is sheepdog) through `hero/tts.go`. No Gemini Live
  session, no music. The only other sound is a two-tone chime made with
  ffmpeg's sine source.
- **Captions and waveform**: drawn by `compose.py` from the clips. The bars
  are the clip's own loudness; the words are lit across the loud part of the
  clip in proportion to their letters (not recognised from the audio).
- **Phone**: the real app in the iOS Simulator, offline demo ("demo agents").
  The lock screen is two simulator screenshots with the daemon's own push
  payload (`hero/payload.go`); the app is `LaunchRecordingUITests` filmed with
  `simctl io recordVideo`. Cuts and holds are in `phone-map.json` (output
  second to video second). The notification reads "codex-payments wants to run
  npm install p-retry. Approve?" because that is what the daemon sends.
- **Live Activity**: not shown. A headless simulator does not draw it on the
  lock screen (`-sheepdogDemoLiveActivity` only feeds the app's own state).

Simulator notes (iOS 27): after a lock and unlock the display stays frozen
for the recorder, so the app is filmed in a separate, never-locked pass
(`phone.sh`). A new simulator never asks for notification permission with the
fixture daemon, so use one cloned from a simulator that has it, and empty its
delivered notifications (`Library/UserNotificationsUI/NotificationListPersistentState.json`
sections and the folders in `Library/UserNotifications/`, with the simulator
shut down) for a lock screen with no stacked old notifications.

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
