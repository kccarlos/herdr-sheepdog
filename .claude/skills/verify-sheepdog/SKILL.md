---
name: verify-sheepdog
description: "Drive sheepdog like a user and capture proof across a private Go daemon (mock, hc, real worker), the Herdr panel and Tart VM, Linux via ssh oci, /voice in headless Chromium, real Gemini Live voice within daily budget, and iPhone 13 or iOS simulators. Use whenever verifying daemon, web, panel, voice, or iOS changes end to end."
---

# Verify Sheepdog

Reuses `scripts/e2e/*`, `ios/scripts/fixture-daemons.sh`, and `scripts/readme-media/cdprec.go` (no new helper scripts; panel checklist at [/docs/dev/e2e/checklists/panel.md](/docs/dev/e2e/checklists/panel.md), architecture at [/docs/dev/e2e.md](/docs/dev/e2e.md)). Use glossary terms only: `user`, `voice`, `orchestrator`, `agents`/`workers`.

## Hard Safety Rules (Non-Negotiable)

- **Never touch live user state:** `:7777`, `~/.config/sheepdog`, `~/.local/share/sheepdog`, the herdr TUI server, `~/.config/herdr`, `~/.config/herdr-app`, or `Sheepdog.app`.
- **Own `HOME` from first launch:** every daemon, `hc`, and `Herdr.app` copy must start with private `HOME`, `XDG_{CONFIG,DATA,STATE,CACHE}_HOME`, `HERDR_APP_HOME`, `HC_SESSION`, `HC_SOCKET`, and `--data-dir` (`scripts/e2e/up.sh` sets all of these under `/tmp/sde-xxxx`).
- **Never `pkill` or `killall` by pattern:** kill only recorded PIDs (`daemon.pid`, `wrap.pid`, `hc.pid`, `app.pid`, `relay.pid`, `ws.pid`, `tart.pid`, `main.pid`, `pair.pid`).
- **Time-limit every wait and command:** use `e2e_wait <sec>`, `curl --max-time`, `perl -e 'alarm <sec>; exec @ARGV'`, or `timeout --kill-after=30 <sec>`.
- **Disk floor:** stop immediately if free space on `/tmp` is below 8 GB: `[ "$(df -k /tmp | awk 'NR==2{print $4}')" -gt 8388608 ]`.
- **Physical iPhone gate:** confirm the active computer is `"QA scratch"` before ANY voice or approval tap; stop at once if unsure. Never pass `-sheepdogResetState` on a phone (wipes real pairings), and unpair `"QA scratch"` before tearing down its daemon.
- **Simulators:** use ONLY `E0CEECF1-202B-497F-AD1D-B0B90193D1C0` (iPhone) and `1B6602A4-C05F-4933-92F2-9D300FA6148E` (iPad); shut both down afterwards.
- **Zero secrets or private identifiers:** never print API keys, tokens, `.env` values, or `/api/pair` / `sheepdog://pair#...` URLs; never put real names, hostnames, or LAN IPs in docs or fixtures.

## Evidence & Doctor

`scripts/e2e/down.sh "$DIR"` deletes `$DIR`. Always store proof outside `$DIR` so evidence survives cleanup:
```sh
[ "$(df -k /tmp | awk 'NR==2{print $4}')" -gt 8388608 ] || { echo "below 8 GB free" >&2; exit 1; }
EVID="/tmp/sd-verify-$(date +%Y%m%d-%H%M%S)" && mkdir -p "$EVID"
```
**Doctor** (run right after `up.sh` and whenever behavior looks off):
```sh
. "$DIR/env.sh" && [ "$E2E_PORT" != 7777 ] && kill -0 "$(cat "$DIR/daemon.pid")"
[ "$(lsof -tiTCP:"$E2E_PORT" -sTCP:LISTEN | head -1)" = "$(cat "$DIR/daemon.pid")" ]
curl -fsS --max-time 3 "$E2E_URL/api/hello" | jq -e '.name == "sheepdog"'
curl -fsS --max-time 3 "$E2E_URL/api/state" | jq '{port:env.E2E_PORT,voice:.voice,agents:(.agents|length),questions:(.questions|length)}'
```
**Proof standards:** drive user-facing surfaces (`/voice`, `/api/utterance`, `/api/pending/answer`, `herdr-app`, XCUITest) rather than internal state setters; use `--dev` `/api/inject/*` only through `scenario.sh` to stage worker terminal fixtures at the herdr boundary; capture both UI artifacts (frames/screenshots) and daemon side effects (`/api/state`, `/api/conversation`, `data/events/*.jsonl`) into `$EVID` before teardown.

## 1. Private Daemon (`mock`, `hc`, Real Worker)

- **Mock herdr + fake orchestrator:**
  ```sh
  OUT=$(scripts/e2e/up.sh --no-herdr-app) && DIR=$(printf '%s\n' "$OUT" | sed -n 's/^environment: //p') && . "$DIR/env.sh"
  scripts/e2e/scenario.sh "$DIR" approval          # also: agent-blocked|agent-working|agent-done|agent-unread|agent-seen|agent-pin|files
  curl -fsS --max-time 10 -H 'Content-Type: application/json' -d '{"text":"what needs me?"}' "$E2E_URL/api/utterance" >"$EVID/utterance.json"
  QID=$(curl -fsS --max-time 3 "$E2E_URL/api/state" | jq -r '.questions[0].id')
  curl -fsS --max-time 10 -H 'Content-Type: application/json' -H "Idempotency-Key: verify-$E2E_ID" \
    -d "{\"id\":\"$QID\",\"option\":1}" "$E2E_URL/api/pending/answer" >"$EVID/answer.json"
  curl -fsS --max-time 3 "$E2E_URL/api/state" | tee "$EVID/state.json" | jq -e '(.questions | length) == 0'
  cp "$DIR/daemon.log" "$EVID/daemon.log" && cat "$DIR"/data/events/*.jsonl >"$EVID/events.jsonl"
  ```
  One-shot full kit self-check: `perl -e 'alarm 180; exec @ARGV' scripts/e2e/selftest.sh --no-herdr-app`.
- **Private `hc` core + local push relay stub:**
  ```sh
  OUT=$(scripts/e2e/up.sh --no-herdr-app --herdr hc --push-stub) && DIR=$(printf '%s\n' "$OUT" | sed -n 's/^environment: //p')
  scripts/e2e/scenario.sh "$DIR" space-mute && scripts/e2e/scenario.sh "$DIR" files
  cp "$DIR/relay.jsonl" "$EVID/relay.jsonl"
  ```
  `scripts/e2e/scenario.sh "$DIR" label-sync` tests `herdr.pinned`/`herdr.seen` across daemon restart on a real idle `claude` terminal (spends 1 worker task from `budget.sh`).
- **Real worker (`claude` on Haiku or `codex`):** runs on private `hc` in `/tmp`, triggers a real permission prompt, answers via `/api/pending/answer`, and verifies `nightly-ok.txt` appears (exits `3` if CLI/auth/budget unavailable):
  ```sh
  scripts/e2e/budget.sh show
  perl -e 'alarm 330; exec @ARGV' scripts/e2e/real-agent.sh claude --timeout 300 | tee "$EVID/real-agent.log"   # or: codex
  ```

## 2. Herdr Panel & Tart VM

- **Private local Herdr window** (copies `$HOME/Applications/Herdr.app` into `$DIR/Herdr.app` with `DISABLE_AUTOUPDATER=1` and `sheepdog.url` in `$DIR/app-home/shared.json`):
  ```sh
  OUT=$(scripts/e2e/up.sh) && DIR=$(printf '%s\n' "$OUT" | sed -n 's/^environment: //p')
  scripts/e2e/scenario.sh "$DIR" approval && scripts/e2e/shot.sh "$DIR" panel-approval | tee "$EVID/panel-status.json"
  jq -e '.layout == "sheepdog" and (.errors | length == 0)' "$EVID/panel-status.json"
  cp "$DIR/shots/panel-approval.png" "$EVID/"
  ```
- **Tart VM (`sd-qa` cloned from `sd-qa-base`):** `scripts/e2e/panel-auto.sh` sends real `System Events` keystrokes to the frontmost app—**never run it on the host desktop while someone may type**. Run inside the VM (`HEAD` if pushed, else `origin/main`):
  ```sh
  perl -e 'alarm 1800; exec @ARGV' scripts/e2e/nightly.sh --only vm --no-fixer
  ```
  Reads screenshots and `report.md` from `$HOME/Library/Logs/sheepdog-nightly/<YYYY-MM-DD>/` and stops `sd-qa` on exit.

## 3. Linux (`ssh oci`)

- **Nightly Linux suite** (pushed `HEAD` else `origin/main`; runs `go test ./...`, `selftest.sh --no-herdr-app`, and `real-agent.sh`):
  `perl -e 'alarm 1800; exec @ARGV' scripts/e2e/nightly.sh --only linux --no-fixer`
- **Targeted check on `ssh oci`:**
  ```sh
  ssh -o BatchMode=yes -o ConnectTimeout=10 oci \
    'cd "$HOME/herdr-sheepdog" && export PATH="$HOME/sdk/go1.27.3/bin:$HOME/.local/bin:/usr/local/bin:$PATH" && timeout --kill-after=30 600 scripts/e2e/selftest.sh --no-herdr-app'
  ```
  For unpushed worktree edits, `rsync -az --delete --exclude=.git ./ oci:~/herdr-sheepdog-verify/` first, run under `timeout`, then `rm -rf ~/herdr-sheepdog-verify`.

## 4. `/voice` in a Headless Browser

`cdprec.go` launches headless Chromium (`--headless=new`) with a throwaway `--user-data-dir`, fake mic/media UI, and muted audio (pick Brave or Chrome, and a free DevTools `-port`):
```sh
BROWSER="/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"
[ -x "$BROWSER" ] || BROWSER="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
scripts/e2e/scenario.sh "$DIR" voice-on && scripts/e2e/scenario.sh "$DIR" approval
printf '3.0\t(()=>{const i=document.querySelector("#text-input");i.value="status check";i.dispatchEvent(new Event("input",{bubbles:true}));document.querySelector("#btn-text").click();})()\n5.0\tdocument.querySelector("#approvals .approval-opt[data-option-index=\\"1\\"]").click()\n' >"$EVID/steps.tsv"
perl -e 'alarm 45; exec @ARGV' go run scripts/readme-media/cdprec.go \
  -browser "$BROWSER" -port 19333 -url "$E2E_URL/voice" -click '#btn-connect' \
  -steps "$EVID/steps.tsv" -done-when 'document.querySelectorAll("#transcript .line.out").length>=1 && document.querySelectorAll(".approval-outcome").length>=1' \
  -seconds 15 -out "$EVID/voice-frames" | tee "$EVID/cdprec.log"
curl -fsS --max-time 3 "$E2E_URL/api/state" | jq -e '(.questions | length) == 0' && ls "$EVID"/voice-frames/frame-*.jpg >/dev/null
```
Real `/voice` DOM handles (`web/voice/index.html`, `web/voice/approvals.js`): `#btn-connect`, `#btn-mute`, `#btn-stop`, `#ws-text` (`ws: open`), `#voice-text` (`voice: connected`), `#text-input`, `#btn-text`, `#transcript .line.out`, `#approvals .approval-card.primary`, `#approvals .approval-opt[data-option-index="1"]`, `.approval-outcome`, `#source` (`file`), `#file-input`, `#file-bundled`, `#file-play`.

## 5. Real Voice & Daily Budget

Daily paid-check caps in `$HOME/Library/Logs/sheepdog-nightly/budget.json`: `voice` = 20 turns/day, `agent` = 30 tasks/day (`scripts/e2e/budget.sh show`). `real-voice.sh` injects `GEMINI_API_KEY` into the daemon process only via `op run` (`~/.config/op/cloud-agents.token` + `SHEEPDOG_NIGHTLY_GEMINI_REF`) and drives `/ws` with `sheepdog audio-e2e` (`say` + `afconvert`; exits `3` on missing `op`/token/budget):
```sh
scripts/e2e/budget.sh show
perl -e 'alarm 240; exec @ARGV' scripts/e2e/real-voice.sh --only status-question,room-noise-only --out "$EVID"
```

## 6. Simulators & iPhone 13 (`ios/`)

- **Simulator builds & tests** (ONLY `IPHONE=E0CEECF1-202B-497F-AD1D-B0B90193D1C0` and `IPAD=1B6602A4-C05F-4933-92F2-9D300FA6148E`; launch offline demo with `-sheepdogDemoSilent` and UI tap tests with `-sheepdogNoAnimations`):
  ```sh
  IPHONE=E0CEECF1-202B-497F-AD1D-B0B90193D1C0; IPAD=1B6602A4-C05F-4933-92F2-9D300FA6148E
  make build && (cd ios && xcodegen generate)
  SHEEPDOG_DAEMON_REPO=$PWD ios/scripts/fixture-daemons.sh start   # 7799 main, 7798 pair, /tmp/sd-bugs
  (cd ios && TEST_RUNNER_SHEEPDOG_FIXTURE_PORT=7799 TEST_RUNNER_SHEEPDOG_FIXTURE_PAIR_PORT=7798 \
    xcodebuild -scheme Sheepdog -destination "platform=iOS Simulator,id=$IPHONE" \
    -skipPackagePluginValidation -resultBundlePath "$EVID/sim.xcresult" test)
  xcrun xcresulttool export attachments --path "$EVID/sim.xcresult" --output-path "$EVID/sim-shots"
  SHEEPDOG_DAEMON_REPO=$PWD ios/scripts/fixture-daemons.sh stop && xcrun simctl shutdown "$IPHONE" "$IPAD" 2>/dev/null || true
  ```
- **iPhone 13 + private LAN daemon (preventing device-walk failures):**
  1. **Distinct name & fresh TLS pairing:** export `SHEEPDOG_COMPUTER_NAME="QA scratch" SHEEPDOG_WEB_BIND=0.0.0.0 SHEEPDOG_WEB_ADVERTISE="<lan-ip>:18080"` before `scripts/e2e/up.sh --no-herdr-app --port 18080` (without `SHEEPDOG_COMPUTER_NAME`, private runs reuse the Mac's hostname and stack duplicate entries). Every new daemon has a new ID and TLS certificate (stale phone entries fail with TLS EOFs and `"Your computer is taking longer than usual to answer"`): read `GET /api/pair?new=1` on loopback into a shell variable, rewrite `#<fragment>` onto `sheepdog://pair#<fragment>` **without printing it**, open on phone, tap SpringBoard `"Open"`, `pairing.confirm` -> `pairing.primary` -> wait `pairing.paired` -> `pairing.primary`, and verify `web.device.enroll` + `web.device.session` in `/api/events`.
  2. **Confirm `"QA scratch"` before any orb or approval tap:** **never read `voice.capsule.label`**—`voice.capsule` (`VoiceBar.swift`) is a `.contain` `ScrollView` whose `.label` is always `""`! Read `app.buttons["voice.menu"].label`: with >1 computer paired it is `"Voice: <ComputerName>"` and MUST equal `"Voice: QA scratch"`, and on `main.pane.flock` `computer.voiceMarker.<daemonID>.label` MUST equal `"Voice is on QA scratch"`. With 1 computer (`computerLabel.*` absent), `voice.menu.label` is `"Voice: <voiceName>"`; verify `toolbar.more` -> `toolbar.settings` -> `settings.daemon` -> `settings.address` matches `<lan-ip>:<port>`. Switch via tapping `computer.voiceMarker.<daemonID>`, `voice.menu` -> button `label BEGINSWITH "Talk to QA scratch"` (SwiftUI `Menu` strips `voice.talkTo.<id>` IDs), or `toolbar.more` -> `toolbar.settings` -> `computerLabel.settings.<id>` -> `computer.talkTo`. **Stop if unsure.**
  3. **Real `ios/Sources` IDs & `PaneSwitch.swift` flows** (`tapWhenTappable`, `tapMenuItem(until:)`, `typeWhenFocused`, `openAgentWindow`, `revealAboveCapsule`):
     - **Dismiss consent** (`ConsentUITests.swift`): when `consent.screen` appears on first connect, `tapWhenTappable(app.buttons["consent.agree"])` (`consent.notNow` cancels; later in `toolbar.more` -> `toolbar.settings` -> `settings.privacy` -> `privacy.aiSwitch`).
     - **Start voice** (`VoiceRobustnessUITests.swift`): run `scenario.sh "$DIR" voice-on`, pass step 2's computer check, tap `controls.orb` (or `voice.menu` -> `voice.start`; becomes `controls.end`). Handle in order: `consent.agree` (`consent.screen`), `primer.allow` (`primer.microphone`), poll `XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.buttons["Allow"].firstMatch` inside the wait loop (interruption monitors do not fire during waits), `controls.coachMark.dismiss` if tappable; assert `controls.statusLine` (`"Listening — just talk"`) and `/api/state` `.voice.client_connected == true`.
     - **Open Files** (`FilesDemoUITests.swift`, `FlockView.swift`): non-selected computers (`otherSection`) set `showsWindowButton: false`, hiding BOTH `flock.agent.<alias>.window` and `flock.agent.<alias>.files`—`"QA scratch"` MUST be selected! Waiting agents (`question != nil`) ignore `.toggle`, and `.unknown`/`.exited` fold into `flock.notRunning`. Reach `files.list` via: (a) `main.pane.flock` -> `openAgentWindow(app, alias: "filer")` (`flock.agent.filer.window` -> `agentScreen.terminal`) -> `tapMenuItem(app, menu: "agentScreen.menu", item: "agentScreen.menu.files", until: any(app, "files.list"))`; (b) `revealAboveCapsule` + `flock.agent.filer.toggle` -> `flock.agent.filer.files`; (c) `toolbar.more` -> `toolbar.spaces` -> `spaces.row.<name>` -> `space.files`. Inside: `files.row.<path>`, `files.search`, `files.crumb.root`/`.current`, `viewer.markdown`, `viewer.mode.raw`, `viewer.code`, `viewer.image.zoom` (`viewer.image.zoomChoice.actual`/`.fit`), `browser.state.outside`/`too_large`/`viewer.binary` -> `browser.state.back`, `browser.tab.changes`, `browser.done`.
     - **Answer approval** (`QuestionsUITests.swift`, `ApprovalCard.swift`): verify no `computerLabel.question.<otherID>`. On `main.pane.conversation`, tap 1-based `approval.option.1` on `approval.card` (`question.<alias>.option.1` for extra open questions), or tap `questions.answer` -> `questions.sheet` (`questions.sheet.close`) -> `approval.option.1` (also inside `flock.agent.<alias>` on `main.pane.flock`). Assert `approval.answered` and `/api/state` `.questions | length == 0`.
  4. **Unpair `"QA scratch"` before teardown:** `computerLabel.settings.<id>` -> `computer.remove` -> `computer.remove.confirm` (single computer: `settings.daemon` -> `settings.remove` -> `settings.remove.confirm`).

## Cleanup

Copy logs/events/shots into `$EVID` first, then tear down only what you started and confirm `$EVID` survived:
```sh
scripts/e2e/down.sh "$DIR"
SHEEPDOG_DAEMON_REPO=$PWD ios/scripts/fixture-daemons.sh stop 2>/dev/null || true
xcrun simctl shutdown E0CEECF1-202B-497F-AD1D-B0B90193D1C0 1B6602A4-C05F-4933-92F2-9D300FA6148E 2>/dev/null || true
ls -la "$EVID"
```
