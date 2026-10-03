# Status (2026-10-02)

## Distribution

TestFlight build **0.1.0 (10)**, internal group **Me**. Xcode Cloud built it
from tag `v0.1.0` (`2355322`, PR #24) with the **TestFlight** workflow.
The App Store Connect record is **"Sheepdog for herdr"**. For setup,
workflows and troubleshooting, see [`xcode-cloud.md`](xcode-cloud.md).

## Verified on a real iPhone

The CEO checked these on 2026-10-01 and 2026-10-02 with the TestFlight
build against a live daemon (Gemini Live, with the brain on Vertex AI):

- Voice interaction: talking to the dog and hearing it answer.
- Answering approvals by voice.
- A push notification on the lock screen, answered with **Approve**.

## Not verified on a device yet

- **AirPods and audio route changes.** This includes AirPods going in or
  out mid-session and whether the needs-you chime reaches them.
- **AskUserQuestion with several questions** in one prompt.

## Known rough edges

- iOS offers to save the token to Passwords when Settings closes.
  `.textContentType(.oneTimeCode)` does not suppress the offer. It is a
  system dialog, and "Not Now" is the right answer.
- The blocked state is **amber** here (asset catalog `Blocked`, plus the
  darker `BlockedText` for words). The daemon's `/debug` page uses red. On a
  phone a red dot reads as "broken", while a blocked agent is only waiting
  for you. This difference is deliberate.
- `SheepdogUITests` sometimes fails with "Timed out while evaluating UI
  query" while the app is reconnecting. A re-run passes.

## Running the tests

```sh
xcodegen generate
xcodebuild -scheme Sheepdog -destination 'platform=iOS Simulator,name=iPhone 17 Pro' build test
```

The unit suite (`SheepdogKitTests`) has 400 tests. `LiveDaemonTests` and
every UI test skip themselves when no daemon answers on `127.0.0.1:7799`,
so Xcode Cloud stays green without a daemon.

To run the UI tests for real, start two fixture daemons from the daemon
repository. Neither needs a model key: `--brain=fake` is a scripted local
model, and `--voice=off` means there is no live session.

```sh
cd ../herdr-sheepdog
SHEEPDOG_DATA_DIR=/tmp/sheepdog-ios SHEEPDOG_STATE_PATH=/tmp/sheepdog-ios/state.json \
  bin/sheepdog serve --brain=fake --herdr=mock --voice=off --dev --port 7799 --env-file /dev/null &
SHEEPDOG_DATA_DIR=/tmp/sheepdog-ios-token SHEEPDOG_STATE_PATH=/tmp/sheepdog-ios-token/state.json \
  SHEEPDOG_WEB_BIND=0.0.0.0 SHEEPDOG_WEB_TOKEN=devtoken \
  bin/sheepdog serve --brain=fake --herdr=mock --voice=off --dev --port 7798 --env-file /dev/null &

cd ../herdr-sheepdog-ios
xcrun simctl privacy <udid> grant microphone com.github.kccarlos.herdr-sheepdog-ios
xcodebuild -scheme SheepdogUITests -destination 'platform=iOS Simulator,name=iPhone 17 Pro' test
```

- `HistoryApprovalUITests` expects a restart divider, so restart the 7799
  daemon shortly before it runs.
- `ReconnectUITests` skips unless `scripts/fixture-daemon-restarter.sh` is
  running.
- `ReviewScreenshotsUITests` writes the screenshots below. Run it with
  `TEST_RUNNER_SHEEPDOG_SHOTS=light|dark|xxxl` against a fixture daemon on
  7797.
- `AgentScreenUITests` opens an agent window, presses Enter, cancels a
  Ctrl-C and sends a prompt, and writes the agent-window screenshots. Same
  flags as the review screenshots; it needs a daemon with the
  `/api/agents/{pane_id}/screen` and `/input` routes and skips otherwise.

## Screenshots

These come from the simulator (iPhone 17 Pro, iOS 27) against a fixture
daemon. The launch flags `-sheepdogFakeVoice`, `-sheepdogFakeProviders` and
the DEBUG-only `-sheepdogDemoMarkdown` stand in for things a fixture daemon
cannot produce.

| Light | Dark | What |
|---|---|---|
| ![](screenshots/review-light-first-run.png) | ![](screenshots/review-dark-first-run.png) | First run. Test connection sits right under the fields. |
| ![](screenshots/review-light-voicebar-disconnected.png) | ![](screenshots/review-dark-voicebar-disconnected.png) | Not connected: "Tap the orb to connect". |
| ![](screenshots/review-light-voicebar-live.png) | ![](screenshots/review-dark-voicebar-live.png) | The voice bar: mic, orb, status line, Type, End. |
| ![](screenshots/review-light-voicebar-muted.png) | ![](screenshots/review-dark-voicebar-muted.png) | Muted. The first time, the status line explains what Mute does. |
| ![](screenshots/review-light-conversation-approval.png) | ![](screenshots/review-dark-conversation-approval.png) | A long approval with nothing truncated. The question's other copies fold to a pointer. |
| ![](screenshots/review-light-flock.png) | ![](screenshots/review-dark-flock.png) | Flock: blocked agents first, with the amber badge. |
| ![](screenshots/review-light-flock-markdown.png) | ![](screenshots/review-dark-flock-markdown.png) | Markdown in a task and in a worker's message. |
| ![](screenshots/review-light-flock-not-running.png) | ![](screenshots/review-dark-flock-not-running.png) | "Not running (1)", opened. |
| ![](screenshots/review-light-health.png) | ![](screenshots/review-dark-health.png) | Health: voice and brain shown read-only, with "Change in Settings ›". |
| ![](screenshots/review-light-models.png) | ![](screenshots/review-dark-models.png) | Settings ▸ Models: voice and brain. |
| ![](screenshots/review-light-models-notifications.png) | ![](screenshots/review-dark-models-notifications.png) | Notifications: this phone, and the daemon's `GET /api/push/status`. |
| ![](screenshots/review-light-agent-screen.png) | ![](screenshots/review-dark-agent-screen.png) | Agent window: an agent's live terminal from its Flock card, wide lines scrolling sideways, with the key bar and prompt field. |
| ![](screenshots/review-light-agent-screen-confirm.png) | ![](screenshots/review-dark-agent-screen-confirm.png) | Ctrl-C (and Esc) ask before they are sent. |
| ![](screenshots/review-light-agent-screen-sent.png) | ![](screenshots/review-dark-agent-screen-sent.png) | A prompt sent from the window, echoed on the (mock) screen. |

At the largest accessibility text size (AX5):

| | | | |
|---|---|---|---|
| ![](screenshots/review-xxxl-voicebar-live.png) | ![](screenshots/review-xxxl-conversation-approval.png) | ![](screenshots/review-xxxl-flock.png) | ![](screenshots/review-xxxl-flock-markdown.png) |
