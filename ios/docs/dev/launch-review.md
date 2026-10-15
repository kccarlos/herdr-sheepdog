# Launch review: the first five minutes

A review of the iPhone app before public TestFlight testing, from the point
of view of a stranger who installed the daemon from the herdr plugin
marketplace and opens the app for the first time, and of an App Store
reviewer who has no daemon at all. Done on 2026-10-15 against `main` at
0.1.7, on an iOS 27 simulator (iPhone 17 Pro, and iPhone SE 3rd generation
for the small screen).

**Priorities.** P0 would fail review or stop a new user. P1 is painful.
P2 is polish. Every P0 and P1 below is fixed in the same pull request as
this file; each fix has a test.

**Evidence.** The screenshots come from
`Tests/SheepdogUITests/LaunchReviewScreenshotsUITests.swift`, which needs no
daemon and runs only when asked:

```sh
TEST_RUNNER_SHEEPDOG_LAUNCH_SHOTS=/tmp/shots TEST_RUNNER_SHEEPDOG_LAUNCH_PREFIX=light \
  xcodebuild -scheme Sheepdog -destination 'platform=iOS Simulator,name=iPhone 17 Pro' \
  test -only-testing:SheepdogUITests/LaunchReviewScreenshotsUITests
```

Set the simulator's appearance and text size first (`xcrun simctl ui <udid>
appearance dark`, `xcrun simctl ui <udid> content_size
accessibility-extra-extra-extra-large`). `before-*` files are `main` before
the fixes; `light-*`, `dark-*`, `ax5-*` (the largest accessibility text
size) and `se-*` are after. All are in
[`screenshots/launch/`](../screenshots/launch/).

Error states without a daemon: "unreachable" is a closed loopback port, and
"wrong token" is `StubDaemon`, a small HTTP server inside the UI test runner
that answers `/api/hello` like sheepdog and everything else with 401.

## P0

### 1. The first screen is a keyboard over stale instructions

[`before-01-first-run.png`](../screenshots/launch/before-01-first-run.png):
the app opened on Settings ▸ Daemon with the keyboard already up, covering
half the screen. The headline sat under the navigation bar. The text told
the user to run `sheepdog serve` with `web.bind` and `web.token` set and to
read a `lan url` line, which is the manual setup from before
`sheepdog setup` existed; the scanner's footer pointed at a developer page
(`/debug ▸ State ▸ Pair a phone`). Nothing said what the app is for or what
it needs. "Try the demo" was a plain row among the others.

**Fix.** The first-run screen is titled **Welcome** and starts with
"Connect to your computer": *Sheepdog needs the sheepdog daemon running on
your computer. Install it from the herdr plugin marketplace and run
`sheepdog setup`: its last step shows a pairing code to scan.* A link,
**How to set up sheepdog**, opens the daemon's `docs/iphone-app.md`.
**Try the demo** is a full-width prominent button right under it. No
keyboard on the first run. The scanner footer names `sheepdog setup` (step
4) and `sheepdog pair`. The token placeholder says "From the pairing code"
instead of `web.token`.
[`light-01-first-run.png`](../screenshots/launch/light-01-first-run.png),
[`dark-01-first-run.png`](../screenshots/launch/dark-01-first-run.png),
[`ax5-01-first-run.png`](../screenshots/launch/ax5-01-first-run.png),
[`se-01-first-run.png`](../screenshots/launch/se-01-first-run.png).
Test: `FirstRunUITests.testFirstRunSaysWhatTheAppNeedsAndOffersTheDemo`
(the intro, the guide link, the demo button hittable without scrolling, no
keyboard).

### 2. A pairing link that fails says nothing

[`before-10-pair-unreachable.png`](../screenshots/launch/before-10-pair-unreachable.png):
opening a `sheepdog://pair` link whose addresses do not answer (the phone
on another network) showed its error in a banner on the main screen, then
reopened the first-run Settings sheet on top of it. The user saw the same
setup screen as before, with no sign that anything happened.

**Fix.** The failure is shown on the Daemon screen, where the fix is: "None
of the code's addresses answered.", the addresses tried, the plain fix and
a **How to fix this** link.
[`light-10-pair-unreachable.png`](../screenshots/launch/light-10-pair-unreachable.png).
Test: `PairingLinkUITests.testPairReportsAnUnreachableCode`.

### 3. A wrong token opens Settings without saying why

[`before-14-token-refused.png`](../screenshots/launch/before-14-token-refused.png):
when the daemon turned the token down, the app opened the Settings menu.
The reason was in a banner under the sheet.

**Fix.** A refused token opens Settings ▸ Daemon with the reason and the
fix: "The token changed or was mistyped. Run `sheepdog pair` on your
computer and scan the code again.", plus **How to fix this**. The scanner
is the first row on that screen. The Agents pane's "Token refused" state
says the same and its button goes to the same screen.
[`light-14-token-refused.png`](../screenshots/launch/light-14-token-refused.png).
Test: `FirstRunUITests.testWrongTokenOpensTheDaemonScreenWithTheFix`.

### 4. Notifications promised what this build cannot deliver

[`before-16-notifications.png`](../screenshots/launch/before-16-notifications.png):
Settings ▸ Notifications said "the daemon sends a notification even with
the app closed", and the app asked for notification permission after the
first connection to any daemon. Apple delivers a push only when it is
signed with an APNs key from the Apple team that signed the app. A
stranger's self-hosted daemon does not have that key, so for every
TestFlight user except the owner the permission prompt and the copy
promise something that can never happen (Guidelines 2.1 and 2.3, and a
broken promise to the user).

**Fix.** The app asks for notification permission only when the connected
daemon reports push as enabled (`GET /api/push/status`). Without it, "This
phone" reads "Not needed yet", and the footer says what works (approval
cards and the voice while the app is open and connected; the Live Activity
on the Lock Screen and in the Dynamic Island during a voice session) and
what needs a self-built app with your own push key.
[`light-16-notifications.png`](../screenshots/launch/light-16-notifications.png).
Tests: `SetupHelpTests.testNotificationsAreAskedForOnlyWhenTheDaemonCanSend`,
`SetupHelpTests.testNotificationCopyIsHonestWithoutPush`.

**Decision for the CEO.** No push relay was built. The options are: (a)
leave it as above, so public TestFlight users get no closed-app
notifications and are told so; (b) run a small relay that holds the
project's APNs key and forwards pushes from users' daemons, which means a
server, an account or device-token scheme, abuse limits and a privacy
policy change ("no sheepdog server" would no longer be true); or (c) document
self-building as the way to get them (already in the README). The app
currently does (a) and (c).

## P1

### 5. "Can't reach the daemon" did not say what to check

[`before-11-unreachable-conversation.png`](../screenshots/launch/before-11-unreachable-conversation.png),
[`before-12-unreachable-agents.png`](../screenshots/launch/before-12-unreachable-agents.png),
[`before-13-unreachable-test-connection.png`](../screenshots/launch/before-13-unreachable-test-connection.png):
the banner's hint was "Nothing is listening on that port." and Test
connection said the same. The empty-state hint without a detail told the
user to read the `lan url` line off `sheepdog serve`'s banner.

**Fix.** Every unreachable error ends with *Check that sheepdog is running
on your computer (`sheepdog status`) and that this phone is on the same
Wi-Fi, or on Tailscale with the same account.* The banner, the Agents
pane and Test connection link to the daemon's `docs/remote-access.md`
("How to fix this"). Something that is not sheepdog on the port says to
scan the code instead of typing.
[`light-11-unreachable-conversation.png`](../screenshots/launch/light-11-unreachable-conversation.png),
[`light-12-unreachable-agents.png`](../screenshots/launch/light-12-unreachable-agents.png),
[`light-13-unreachable-test-connection.png`](../screenshots/launch/light-13-unreachable-test-connection.png).
Tests: `FirstRunUITests.testUnreachableDaemonSaysSameWiFiOrTailscale`,
`FirstRunUITests.testNotSheepdogSaysCheckTheAddress`, `SetupHelpTests`.

### 6. Purpose strings were not accurate (5.1.1)

The microphone string said audio goes "only to the sheepdog daemon", but
the demo uses the microphone on the phone. The camera string named only
`sheepdog pair`. Three strings said "Mac"; the daemon also runs on Linux.

**Fix.** Microphone: "…Your voice goes only to the sheepdog daemon on your
own computer; in the demo it stays on this iPhone." Camera: "…the pairing
code that "sheepdog setup" or "sheepdog pair" shows on your computer.
Nothing is recorded." Local network: "…the sheepdog daemon on your own
computer, over your Wi-Fi or Tailscale." Test: `PurposeStringsTests`
reads the built Info.plist.

### 7. The demo's Settings listed empty daemon pages (2.1)

In the demo, Settings listed Models ("—"), Languages ("Not set") and
Notifications, all of which belong to a daemon the demo does not have. A
reviewer who taps them finds placeholder screens.

**Fix.** The demo's Settings lists Leave the demo, Voice, Diagnostics and
About only.
[`light-08-demo-settings.png`](../screenshots/launch/light-08-demo-settings.png).
Test: `FirstRunUITests.testDemoSettingsHaveNoEmptyDaemonPages`.

### 8. "Mac" in user-facing text

The pairing confirmation ("Only pair with a Mac you trust"), the Agents
pane, Languages, agent windows and the "another client took the voice"
banner said "Mac". They now say "computer".

### 9. DemoUITests failed on every local run

`testTryTheDemoFromTheFirstRun` waited for the approval card's "answered"
line, which the demo replaces within a moment as its agent moves on. The
test now accepts either the answered line or the question closing. On
Xcode Cloud the demo also aborted in `AURemoteIO::Initialize` (the
simulator's audio server hung), so the demo UI tests launch with
`-sheepdogDemoSilent` (debug builds only): no speech synthesis or
recognition and no permission prompts.

### 10. The largest text size buried the demo and pushed the screen up

At the largest accessibility size the first-run introduction filled two
screens before **Try the demo**, and the button's label was clipped ("Try
th demo"). In the demo, the pinned approval card (up to half the screen)
plus the header, agent strip and controls overflowed, so the demo header
slid under the navigation bar.

**Fix.** At the accessibility sizes the demo button comes first, its label
wraps, and the pinned approval card scrolls inside a third of the screen
instead of half.
[`ax5-01-first-run.png`](../screenshots/launch/ax5-01-first-run.png),
[`ax5-05-demo-approval.png`](../screenshots/launch/ax5-05-demo-approval.png),
[`ax5-03-demo-conversation.png`](../screenshots/launch/ax5-03-demo-conversation.png).

## Checked and fine

- **The offline demo shows real value (4.2).** Four scripted agents,
  an approval to answer, agent windows, suggested questions with spoken
  answers, all on the device:
  [`light-03-demo-conversation.png`](../screenshots/launch/light-03-demo-conversation.png),
  [`light-04-demo-agents.png`](../screenshots/launch/light-04-demo-agents.png),
  [`light-05-demo-approval.png`](../screenshots/launch/light-05-demo-approval.png),
  [`light-06-demo-agent-window.png`](../screenshots/launch/light-06-demo-agent-window.png),
  [`light-07-demo-answer.png`](../screenshots/launch/light-07-demo-answer.png).
- **The pairing link asks first** ("Pair with 127.0.0.1?", Cancel returns
  to setup):
  [`light-09-pair-confirm.png`](../screenshots/launch/light-09-pair-confirm.png).
  Tested by `PairingLinkUITests`.
- **Camera denied** shows "Camera access is off" with the way to type the
  address instead (`PairingScanner`). **Microphone denied** shows a banner
  that says to turn it on in Settings; the demo still works by tap and
  typing.
- **Dark mode.** Every status colour has a dark variant in the asset
  catalogue; the `dark-*` screenshots show no unreadable text.
- **Live Activity.** Unchanged and local (no push needed);
  [`screenshots/live-activity-light.png`](../screenshots/live-activity-light.png),
  [`screenshots/live-activity-dark.png`](../screenshots/live-activity-dark.png).

## P2 (not fixed)

- Entering the demo asks for speech recognition and the microphone at
  once, before the user has done anything that needs them. Asking on the
  first tap of the microphone would follow the HIG better and remove the
  alerts the demo UI tests have to dismiss.
- The first-run Daemon screen has a back button to the Settings menu, and
  Settings cannot be closed until an address is in (the demo is the way
  out). Acceptable, but a stand-alone welcome screen would be cleaner.
- "turned the token down. Check the token in Settings." is the headline
  even when the user is already in Settings.
- An agent window's terminal lines run past the right edge (they scroll
  sideways).
- The Notifications footer is long.
- Two deprecation warnings (`requestRecordPermission`, `allowBluetooth`).
