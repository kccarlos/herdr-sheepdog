# Development guide

For people working on sheepdog itself. Users: [../README.md](../README.md).
The design is in [`design-overview.md`](design-overview.md), the code map in
[`architecture.md`](architecture.md), every config key in
[`CONFIG.md`](../../CONFIG.md), the commands users run in
[`commands.md`](../commands.md).

## Developer commands and daemon flags

The developer commands are under `sheepdog dev` (`sheepdog dev --help`);
each also still runs under its older top-level name (`sheepdog eval` =
`sheepdog dev eval`):

| Command | What it does |
|---|---|
| `sheepdog herdr watch` | Print debounced agent state transitions as JSONL; `--once` prints the persisted state table. |
| `sheepdog voice-test` | Stream a WAV (or `--text`) through a Gemini Live session with echo tools; no daemon needed. |
| `sheepdog brain-test` | Run one orchestrator turn against a mock herdr and a screen fixture (`--question`, `--say`, `--provider`; `--fake` needs no network). |
| `sheepdog prompts export` \| `show` | Seed `prompts.dir` from the embedded prompts \| print one file's effective text and source. |
| `sheepdog eval` | The conversation eval sets (`--set assistant\|svp\|regressions\|extracted\|expanded\|all`); offline by default, `--live` calls the model. `eval extract` / `eval expand` build cases from the event log. `eval sim` runs simulated-user conversations graded by an LLM judge (`--live`, `--repeat`, `--fallback-provider`; [`eval/eval-campaign-2026-10-03.md`](eval/eval-campaign-2026-10-03.md)). |
| `sheepdog scorecard` | Ear and latency metrics over the event log ([`iteration-2.md`](iteration-2.md) "Scorecard metrics"). |
| `sheepdog feedback` | The user's feedback marks from the app ("that paused", `POST /api/feedback`), each diagnosed from its 30 s snapshot: model gap, delivery gap, client playback, reply wait (`--last N`, `--format json`; [`ws-contract.md`](ws-contract.md) "Feedback and drains"). |
| `sheepdog bench` \| `bench validate` | The tier-2 voice benchmark: synthetic speech through a phone emulator into scratch daemons ([`bench/README.md`](bench/README.md), results in [`bench/tier2-2026-10-03.md`](bench/tier2-2026-10-03.md)). |
| `sheepdog audio-e2e` | Speak scripted scenarios (macOS `say` + noise) into a running daemon's `/ws` ([`history/audio-e2e-results.md`](history/audio-e2e-results.md)). |
| `sheepdog version` \| `help` | Version, commit and build date \| the command list. |

Useful `serve` flags:

```sh
sheepdog serve --voice=off --herdr=mock --dev              # no voice, no herdr: drive it from /debug
sheepdog serve --brain=fake --voice=off --herdr=mock --dev # the orchestrator loop over a scripted model, no key
sheepdog serve --voice=openai                              # start on GPT-Live (on | gemini | openai | off)
sheepdog serve --brain=off                                 # no orchestrator: code sentences only, nothing sent to a worker
sheepdog serve --cli=off                                   # no read-only CLI lookups
sheepdog serve --verbose --port 8080                       # echo every event to stderr
```

`--config`, `--env-file` (default `./.env`) and `--data-dir` point it at
other files.

`/debug` has six tabs: **Swimlane** (every hop between user, voice,
orchestrator and workers with its latency), **Conversation** (the voice session
as a chat), **Supervisor** (the orchestrator's transcript and prompt budget),
**Context** (each model's exact or reconstructed context, step by step;
[`architecture.md`](architecture.md) "Context capture"),
**Events** (the live event log) and **State** (the state table, the
pending action, fake-event injection). Its header switches the orchestrator
provider and model, the voice provider and the chattiness level at
runtime. **New conversation** (on `/debug` and `/voice`, or saying
"start over" and confirming) resets the orchestrator's transcript and the voice
model's memory but keeps the roster, promises and open prompts.

## How it behaves

The voice relays what you say to the orchestrator; the orchestrator answers, acts on
the workers (`prompt`, `send_keys`, `interrupt`), starts background
read-only **investigations** for questions a screen cannot answer, and
decides what is worth telling you when a worker blocks or finishes. The
contract is [`roles.md`](roles.md).

- **Promises:** "tell me when auth is done" is recorded and spoken at the
  matching event at every chattiness level; it survives a restart and
  expires after `supervisor.promise_ttl_s`.
- **Stop:** a bare "stop" (or "wait", "cancel", …) is acknowledged at
  once by the daemon and interrupts what is in flight; "never mind" is
  not a stop.
- **The one rail:** a worker's approval prompt is answered only on your
  own recorded words (or a tap) in that turn, with a key from the
  prompt's options. The code enforces it, not a prompt.
- **Prompts:** `sheepdog prompts export` seeds
  `~/.config/sheepdog/prompts/`; edits are picked up on `SIGHUP` or
  `POST /api/prompts/reload` ([`roles.md`](roles.md) "Prompt
  files").

## Development

Requires Go (`go.mod`), [golangci-lint](https://golangci-lint.run/) v2,
and Node for the browser tests.

```sh
make build         # bin/sheepdog
make run-dev       # serve --voice=off --herdr=mock --dev, for poking at /debug
make test          # go test -race ./...
make test-web      # web/voice, web/debug and web/static *.test.mjs, and the panel's (skipped without node)
make test-panel    # only the herdr-app panel's tests, herdr-app/mods/sheepdog (`herdr-app check herdr-app/mods/sheepdog` is its load check, run by hand)
make vet lint
make eval          # offline eval sets (no network)
make eval-live     # the small eval sets against the real model (burns quota)
make cross         # dist/sheepdog-darwin-arm64, dist/sheepdog-linux-amd64
make install       # copy bin/sheepdog to ~/.local/bin/
make changelog     # regenerate CHANGELOG.md
make release-sign TAG=v0.1.0   # local fallback: sign and notarize a release's darwin binary (signing.md)
```

**Protocol goldens.** `testdata/protocol/` holds what the daemon puts on
the wire: every `/ws` and `/term` message, `/api/hello` and `/api/state`.
`internal/web/protocol_golden_test.go` writes the files and fails when
they are stale. The app's `ProtocolGoldenTests` decode the same files, so
a wire change the app cannot read fails the iOS job. After a deliberate
wire change, run `PROTOCOL_GOLDEN_UPDATE=1 go test ./internal/web -run
TestProtocolGoldens` and commit the files with it.

### CI/CD

GitHub Actions runs Linux jobs, on self-hosted runners in Cloud Run
worker pools ([infra/ci-runners](../../infra/ci-runners/README.md); repo variable
`CI_RUNNER=ubuntu-latest` switches back to GitHub-hosted): vet, test, lint, web
tests, the offline eval, the policy guard and the cross-compile on every PR, and the
Ubuntu release on `v*` tags. The one exception is `ios.yml`, which runs the iPhone app's tests
on GitHub-hosted Macs (free for a public repository; the `xcode-27`
image, Xcode 27.1 picked with `xcode-select`). A Linux `plan` job reads the
paths a PR or push changes (`ios/scripts/ci-plan.sh tier`) and picks a tier:

| Change | Mac jobs |
|---|---|
| A PR that changes `ios/` (Markdown and `ios/docs/` aside) or `ios.yml`; a manual run | smoke and four shards, the full suite |
| A PR that changes only the daemon's API: `cmd/`, `internal/web/`, `internal/voice/wsproto/`, `internal/termproto/`, `testdata/protocol/`, `go.mod`, `go.sum` | smoke |
| A push to `main` that changes any of the above | smoke |
| Anything else (docs, other Go code, the web UI) | none |

Smoke is the unit tests (`SheepdogKitTests`, which include the protocol
goldens) and the fixture-daemon UI tests closest to the API (pairing,
history, settings, reconnect, questions), about ten minutes of tests; the
shards split the other UI test classes, balanced by their CI durations:
one runs `IPadUITests` on an iPad, three split the rest on an iPhone.
`ios/scripts/ci-shards.txt` lists which job runs which tests, and the
`plan` job fails if a UI test class is in no job or a job mixes iPad and
iPhone tests (`ci-plan.sh check`, tested by `ci-plan_test.sh`), so a new
class cannot be skipped silently.

Every Mac job is a test job (one Mac, one simulator), at most five: GitHub
runs five macOS jobs of an account at once, and a separate build job
beside five test jobs could wait for a slot while they wait for it. One
of them, the first to get a Mac (each job that finds no build yet
uploads a tiny claim artifact, and the oldest claim wins; a job named in
advance could get its Mac last while the others wait), builds the app
and its test bundles once
(`build-for-testing` for any simulator, arm64 only, no index store, about
three minutes; twelve with a simulator booting beside it, so it boots its
own only after the upload) and the daemon, and hands Build/Products,
`bin/sheepdog` and the scripts and test sources the tests read to the
others as one zstd tar (an artifact of about 50 MB, kept for a day). The
others boot their simulator meanwhile (two to eight minutes, the biggest
fixed cost of a test job), wait for the boot to finish, then wait for the
artifact (polled through the job's Actions runtime token, as
`actions/download-artifact` itself does; the building job uploads a
marker when its build fails, so they stop at once), download it and run
`xcodebuild test-without-building -xctestrun` for their tests; they need
no checkout, Go or XcodeGen. A booting simulator slows everything else
on the Mac badly (a download or the daemon's first launch takes minutes
beside it), so nothing but the build runs beside it. A test job less saves
Mac minutes and a test job more saves wall clock: five is where the
slowest job, smoke or a shard, takes about as long as the others. Smoke
and the shards run side by side and the first that fails cancels the
rest (`fail-fast`); the shards do not wait for smoke, because that would
add smoke's whole time to every run. Each test job prints its
timings (build, boot, download, first test) in the job summary and every test
case with its time in the log. Each iPhone job starts two fixture daemons
on free ports
(`ios/scripts/fixture-daemons.sh`, `--brain=fake --herdr=mock --voice=off`,
with data, config and `HOME` in a temp dir, so they never read the Mac's
`~/.config/sheepdog` or its herdr socket) for its tests; `HistoryApprovalUITests` runs after
a fresh daemon restart, and `ReconnectUITests` with
`ios/scripts/fixture-daemon-restarter.sh` running. `IPadUITests` runs on an
iPad simulator, the rest on the newest iPhone that is not a foldable. Only the opt-in
recording and screenshot tests skip. The tap-flow UI tests launch the app with
`-sheepdogNoAnimations` (debug builds only), which turns UIKit and
SwiftUI animations off, so a tap never lands on a sheet or menu that is
still moving; they tap through helpers that wait for an element to be on
screen and check that the tap landed (`ios/Tests/SheepdogUITests/PaneSwitch.swift`).

Beside the test jobs, a `macos` job builds `SheepdogKitMac` (the Kit for
the Mac app, [desktop-client.md](design/desktop-client.md)) and runs
its unit tests (`SheepdogKitMacTests`, the platform-neutral share of
`SheepdogKitTests`) on its own Mac with `xcodebuild -scheme SheepdogKitMac
-destination 'platform=macOS' test`: no simulator, fixture daemon or shared
build, and nothing waits on it. It runs in the same tiers as the test jobs
(not on `none`), and `ios-summary` needs it.
Each test step runs `ios/scripts/ci-xctest.sh`: a failing test is run once
more (`-retry-tests-on-failure -test-iterations 2`) and the job fails only
if it fails twice, and a test runner that never started (once seen on a
simulator that had just booted) gets one more start; a test
that passed only on the second run is a flake, and
`ios/scripts/ci-test-summary.sh` lists it in the job summary and as a
warning on the run. It builds for the simulator only and
uses no secrets. Archives, signing
and TestFlight stay on Xcode Cloud, which runs no tests, and no signing
certificate or App Store Connect key for the app goes into GitHub. The other exception
is `release.yml`'s `sign-macos` job, which signs and notarizes the daemon's
darwin binary and builds, signs, notarizes and staples Sheepdog.app on a
GitHub-hosted Mac in the `release` environment
(maintainer approval, `v*` tags only), followed by the `homebrew` job that
pushes the formula and the cask to the tap ([signing.md](signing.md)). The `policy` CI
job (`scripts/workflow-policy.sh`, tested by
`scripts/workflow-policy_test.sh`) fails if a macOS runner or `xcodebuild`
appears anywhere else, including a `release.yml` job without
`environment: release`, if `release.yml` reads a secret other than
`GITHUB_TOKEN` outside such a job, or if `ios.yml` reads any secret.

`ios.yml` starts on every PR (no `paths:` filter), so its `ios-summary`
job always reports: it passes when the tier's Mac jobs passed, or when the
tier is none and none ran, and fails otherwise. It is the one iOS check to
require in the branch protection of `main`; the per-job `tests (…)` checks
come and go with the tier and must not be required.

The iPhone app lives in [`ios/`](../../ios/README.md); Xcode Cloud only
archives its TestFlight builds ([xcode-cloud-monorepo.md](xcode-cloud-monorepo.md)). A `changes`
job in `ci.yml` (dorny/paths-filter) tells the other jobs whether anything
outside `ios/` changed. On an ios-only PR every required check still starts
and passes, but skips its heavy steps; only the policy guard and the docs
check (`TestMarkdownLinks`, `TestDocCommandsExist`) run, so the app's
Markdown links are still checked. Never move this to workflow-level
`paths-ignore`: a required check that never starts stays pending and blocks
the merge. The relay workflow runs only for `relay/**`. The app's tags are
`ios-v*` (they start the TestFlight build); the daemon's releases are `v*`.

The original v1 plan and older run reports are in
[`history/`](history/).

## Known limits

- **agy blocks:** herdr 0.9.0 does not detect Antigravity CLI prompts as
  `blocked` (needs a local agent-detection override or an upstream fix), so
  sheepdog cannot tell you an agy agent is waiting.
- **Push needs an APNs key** from the team that owns the app
  ([push.md](push.md)); without it nothing is pushed while the app is closed.
- **Not yet checked on a device:** AirPods and route changes
  ([status.md](status.md)).
