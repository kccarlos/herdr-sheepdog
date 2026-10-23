# Status (2026-10-05)

## Agent window over /ws (in 0.1.5 (57))

The agent window streams its screen over the existing `/ws` connection
when the daemon lists the `screen` feature (daemon #151): one
subscription, a frame only when the window changed, unsubscribe on close,
resubscribe after a reconnect. It polls `GET /api/agents/{pane}/screen`
every 1.5 s as before with an older daemon or while the socket is down.
Kit tests: `AgentScreenStreamTests`; `AgentScreenUITests.testTheWindowStreamsOverTheSocket`
connects with the orb, opens a window, sees new output arrive and checks the
daemon logged the subscribe and the unsubscribe (passed on the iPhone 17
simulator against a `--herdr=mock` scratch daemon with daemon #151). In
TestFlight 0.1.5 (57).

## Polish batch B (PR #36, in 0.1.4 (48))

In TestFlight 0.1.4 (48): connection chip words
(Connecting on the first connect, Reconnecting after a drop, Offline, a
refused-pairing state, spinner while in flight; a quiet `/events` stream counts as open once
the daemon answers 200), one agent window for the strip and the Flock cards,
Health behind the chip and under Settings ▸ Diagnostics, empty and error
states as `ContentUnavailableView`, Reduce Motion on the orb, and a shallow
Settings menu with no Save or Cancel. Simulator only so far. Screenshots:
`docs/screenshots/polish-{light,dark,xxxl}-*.png` (the older `review-*` ones
are the "before" for chip, Flock, Health and Settings).

## Distribution

TestFlight build **0.1.5 (57)**, internal group **Me** (VALID, IN_BETA_TESTING),
from tag `ios-v0.1.5` (`b414dda`, PR #41): the agent window streams over /ws (#40).
Before it, **0.1.4 (48)**: Xcode Cloud built it from tag `ios-v0.1.4` (`b90c38d`, PR #38) with the **TestFlight**
workflow, the first signed archive with the widget extension; it exported with
no signing change. It adds the UI polish (#36) and the Live Activity widget,
App Intents and earbud remote (#37) to 0.1.3 (35) (Settings ▸ Languages, #33).
Xcode Cloud only archives TestFlight builds on `ios-v*` tags ([`xcode-cloud.md`](xcode-cloud.md));
the tests run on GitHub Actions (`.github/workflows/ios.yml`, below).
The App Store Connect record is **"Sheepdog for herdr"**. For setup,
workflows and troubleshooting, see [`xcode-cloud.md`](xcode-cloud.md).

## Verified on a real iPhone

The CEO checked these on 2026-10-01 and 2026-10-02 with TestFlight build
0.1.0 against a live daemon (Gemini Live, with the brain on Vertex AI):

- Voice interaction: talking to the dog and hearing it answer.
- Answering approvals by voice.
- A push notification on the lock screen, answered with **Approve**.

On 2026-10-22, with TestFlight 0.1.16: pairing on a real iPhone over
Tailscale, scanning the code with the in-app camera, then enrolling and
signing in over pinned TLS. The same path (code, pinned TLS, enroll,
ticket, `/ws`, revoke) runs in the simulator against the pair fixture
daemon (`PairingE2ETests`, `PairingLinkUITests`).

## Not verified on a device yet

- **The offline demo:** simulator only (`DemoUITests`, `DemoWorldTests`,
  `DemoDaemonTests`). Spoken questions (on-device speech recognition) are
  untested: the simulator has no usable input, so the demo there runs on
  the suggested questions and typing. To check on a phone: the voice reads
  the announcements, a spoken "what failed?" is answered, mute stops
  listening, and the voice does not hear itself.

- **Everything new in 0.1.1 (20)** has only run in the simulator and the
  unit and fixture-daemon tests: the `client-ready` / `bot-ready`
  handshake and the Protocol row against the user's own daemon (the
  handshake itself is checked by `LiveHandshakeTests` against a fixture
  daemon), the Models screen, the running-only flock and Markdown.
- **The agent window (#31, since 0.1.2 (29)):** a live terminal and key bar per agent from its Flock card, over the daemon's `/api/agents/{pane}/screen` and `.../input`.
- **Settings ▸ Languages (#33):** pick the languages you speak, most
  preferred first, from the daemon's catalog (`GET /api/settings`
  `language_options`; 简体中文 and 繁體中文 are separate); each change is
  `PUT /api/settings {"languages": [...]}` and both brains are told
  (daemon #142). In 0.1.3 (35); unit tests and a simulator build only so far, not tried on the phone.
- **Away from the screen (#37, not in TestFlight yet):** the voice
  session's Live Activity on the lock screen and in the Dynamic Island
  (listening / speaking / muted / thinking / paused, agents waiting for
  an answer, Mute and End buttons), the earbud remote (play/pause toggles
  mute, next track is Stop, Now Playing shows the session), "Talk to
  Sheepdog" (Siri, Shortcuts, the Action Button, an iOS 18 Control Center
  control), `sheepdog://talk` and `sheepdog://agent/<pane id>`, and runs
  of daemon pushes and notices folded into one "N updates" row. Unit
  tests and simulator builds only. A headless simulator neither locks nor
  draws the Dynamic Island, and `simctl openurl` stops at the system's
  "Open in Sheepdog?" prompt, so the activity's views were checked in the
  in-app gallery (below), not on a real lock screen. Whether AirPods send
  play/pause to the app during a `.voiceChat` session is the main thing
  to try on the phone.
- **AirPods and audio route changes.** This includes AirPods going in or
  out mid-session and whether the needs-you chime reaches them.
- **AskUserQuestion with several questions** in one prompt.
- **The daemon's 2026-10-03 fixes** (voice session refresh, unheard
  pushes, spoken agent names) on the phone: see the daemon's
  `docs/dev/status.md`.

## Known rough edges

- The blocked state is **amber** here (asset catalog `Blocked`, plus the
  darker `BlockedText` for words). The daemon's `/debug` page uses red. On a
  phone a red dot reads as "broken", while a blocked agent is only waiting
  for you. This difference is deliberate.
- `SheepdogUITests` sometimes fails with "Timed out while evaluating UI
  query" while the app is reconnecting. A re-run passes.

## Running the tests

```sh
xcodegen generate
xcodebuild -scheme Sheepdog -destination 'platform=iOS Simulator,name=iPhone 17' -skipPackagePluginValidation build test
```

The unit suite (`SheepdogKitTests`) has about 650 tests. `LiveDaemonTests` and
the UI tests that need a daemon skip themselves when none answers on `127.0.0.1:7799`.

**In CI** (`.github/workflows/ios.yml`, GitHub-hosted Macs) one build job
builds the app and the daemon, and a smoke job and three shards split the
suite; which changes run which jobs is in
[development.md](../../../docs/dev/development.md#cicd). Each test job
starts both fixture daemons on free ports with
`scripts/fixture-daemons.sh start` (it also seeds one exchange and restarts
the main daemon, so the history has a restart divider), and passes the
ports as `TEST_RUNNER_SHEEPDOG_FIXTURE_PORT` and
`TEST_RUNNER_SHEEPDOG_FIXTURE_PAIR_PORT`. Smoke runs
`HistoryApprovalUITests` after `scripts/fixture-daemons.sh restart` and
`ReconnectUITests` with `scripts/fixture-daemon-restarter.sh` running. Only
the opt-in recording and screenshot tests skip there.

**A new UI test class** goes into [`scripts/ci-shards.txt`](../../scripts/ci-shards.txt),
in the shortest shard (the plan job fails until it is listed); run
`bash scripts/ci-plan_test.sh` to check.

To run the UI tests for real on your Mac, start the two fixture daemons
from the daemon repository root (`..` from `ios/`). Neither needs a model
key: `--brain=fake` is a scripted local model, and `--voice=off` means there
is no live session. The script does it (`SHEEPDOG_FIXTURE_PORT`,
`SHEEPDOG_FIXTURE_PAIR_PORT` and `SHEEPDOG_FIXTURE_DATA` move it off 7799,
7798 and `/tmp/sd-bugs`; `stop` stops them):

```sh
make build && SHEEPDOG_DAEMON_REPO=$PWD ios/scripts/fixture-daemons.sh start
```

or by hand:

```sh
cd ..   # the repository root; build bin/sheepdog first with make build
SHEEPDOG_DATA_DIR=/tmp/sheepdog-ios SHEEPDOG_STATE_PATH=/tmp/sheepdog-ios/state.json \
  bin/sheepdog serve --brain=fake --herdr=mock --voice=off --dev --port 7799 --env-file /dev/null &
SHEEPDOG_DATA_DIR=/tmp/sheepdog-ios-pair SHEEPDOG_STATE_PATH=/tmp/sheepdog-ios-pair/state.json \
  SHEEPDOG_WEB_BIND=0.0.0.0 SHEEPDOG_WEB_ADVERTISE=127.0.0.1:7798 \
  bin/sheepdog serve --brain=fake --herdr=mock --voice=off --dev --port 7798 --env-file /dev/null &

cd ios
xcrun simctl privacy <udid> grant microphone com.github.kccarlos.herdr-sheepdog-ios
xcodebuild -scheme SheepdogUITests -destination 'platform=iOS Simulator,name=iPhone 17' -skipPackagePluginValidation test
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

These come from the simulator (iPhone 17, iOS 27) against a fixture
daemon. The launch flags `-sheepdogFakeVoice`, `-sheepdogFakeProviders` and
the DEBUG-only `-sheepdogDemoMarkdown` stand in for things a fixture daemon
cannot produce.

| Light | Dark | What |
|---|---|---|
| ![](../screenshots/review-light-first-run.png) | ![](../screenshots/review-dark-first-run.png) | First run. Test connection sits right under the fields. |
| ![](../screenshots/review-light-voicebar-disconnected.png) | ![](../screenshots/review-dark-voicebar-disconnected.png) | Not connected: "Tap the orb to connect". |
| ![](../screenshots/review-light-voicebar-live.png) | ![](../screenshots/review-dark-voicebar-live.png) | The voice bar: mic, orb, status line, Type, End. |
| ![](../screenshots/review-light-voicebar-muted.png) | ![](../screenshots/review-dark-voicebar-muted.png) | Muted. The first time, the status line explains what Mute does. |
| ![](../screenshots/review-light-conversation-approval.png) | ![](../screenshots/review-dark-conversation-approval.png) | A long approval with nothing truncated. The question's other copies fold to a pointer. |
| ![](../screenshots/review-light-flock.png) | ![](../screenshots/review-dark-flock.png) | Flock: blocked agents first, with the amber badge. |
| ![](../screenshots/review-light-flock-markdown.png) | ![](../screenshots/review-dark-flock-markdown.png) | Markdown in a task and in a worker's message. |
| ![](../screenshots/review-light-flock-not-running.png) | ![](../screenshots/review-dark-flock-not-running.png) | "Not running (1)", opened. |
| ![](../screenshots/review-light-health.png) | ![](../screenshots/review-dark-health.png) | Health: voice and brain shown read-only, with "Change in Settings ›". |
| ![](../screenshots/review-light-models.png) | ![](../screenshots/review-dark-models.png) | Settings ▸ Models: voice and brain. |
| ![](../screenshots/review-light-models-notifications.png) | ![](../screenshots/review-dark-models-notifications.png) | Notifications: this phone, and the daemon's `GET /api/push/status`. |
| ![](../screenshots/review-light-agent-screen.png) | ![](../screenshots/review-dark-agent-screen.png) | Agent window: an agent's live terminal from its Flock card, wide lines scrolling sideways, with the key bar and prompt field. |
| ![](../screenshots/review-light-agent-screen-confirm.png) | ![](../screenshots/review-dark-agent-screen-confirm.png) | Ctrl-C asks before it is sent (Esc no longer does, so Esc Esc works). |
| ![](../screenshots/review-light-agent-screen-sent.png) | ![](../screenshots/review-dark-agent-screen-sent.png) | A prompt sent from the window, echoed on the (mock) screen. |

The Live Activity's lock-screen banner, drawn in the DEBUG gallery
(`-sheepdogLiveActivityGallery one`) on a stand-in lock screen: listening,
two agents waiting with their questions, the counts of agents working and
done, Mute and End. `-sheepdogDemoLiveActivity listening|speaking|muted`
starts a real activity with no session.

| Light | Dark |
|---|---|
| ![](../screenshots/live-activity-light.png) | ![](../screenshots/live-activity-dark.png) |

Batch B (`polish-*`, light and dark; `polish-xxxl-*` at the largest size):

| Light | Dark | What |
|---|---|---|
| ![](../screenshots/polish-light-conversation-strip.png) | ![](../screenshots/polish-dark-conversation-strip.png) | Chip, two panes, strip: tap a chip for the agent window. |
| ![](../screenshots/polish-light-agent-window.png) | ![](../screenshots/polish-dark-agent-window.png) | Agent window: state, kind, workspace and the open question in its header. |
| ![](../screenshots/polish-light-chip-popover.png) | ![](../screenshots/polish-dark-chip-popover.png) | The chip's popover, with Health. |
| ![](../screenshots/polish-light-health.png) | ![](../screenshots/polish-dark-health.png) | Health, pushed from the popover. |
| ![](../screenshots/polish-light-settings-menu.png) | ![](../screenshots/polish-dark-settings-menu.png) | Settings as a shallow menu. |
| ![](../screenshots/polish-light-flock-empty.png) | ![](../screenshots/polish-dark-flock-empty.png) | No agents: one action. |
| ![](../screenshots/polish-light-flock-unreachable.png) | ![](../screenshots/polish-dark-flock-unreachable.png) | Daemon unreachable: Offline chip, one action. |

At the largest accessibility text size (AX5):

| | | | |
|---|---|---|---|
| ![](../screenshots/review-xxxl-voicebar-live.png) | ![](../screenshots/review-xxxl-conversation-approval.png) | ![](../screenshots/review-xxxl-flock.png) | ![](../screenshots/review-xxxl-flock-markdown.png) |
