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
make test-web      # web/voice, web/debug and web/static *.test.mjs (skipped without node)
make vet lint
make eval          # offline eval sets (no network)
make eval-live     # the small eval sets against the real model (burns quota)
make cross         # dist/sheepdog-darwin-arm64, dist/sheepdog-linux-amd64
make install       # copy bin/sheepdog to ~/.local/bin/
make changelog     # regenerate CHANGELOG.md
make release-sign TAG=v0.1.0   # local fallback: sign and notarize a release's darwin binary (signing.md)
```

### CI/CD

GitHub Actions runs Linux jobs, on self-hosted runners in Cloud Run
worker pools ([infra/ci-runners](../../infra/ci-runners/README.md); repo variable
`CI_RUNNER=ubuntu-latest` switches back to GitHub-hosted): vet, test, lint, web
tests, the offline eval, the policy guard and the cross-compile on every PR, and the
Ubuntu release on `v*` tags. The one exception is `ios.yml`, which runs the iPhone app's whole test
suite on a GitHub-hosted Mac (free for a public repository; the `xcode-27`
image, Xcode 27.1 picked with `xcode-select`) for PRs and pushes to `main`
that touch `ios/`, `cmd/`, `internal/web/` or `go.mod`. It builds the
daemon with Go in the same job and starts two fixture daemons on free ports
(`ios/scripts/fixture-daemons.sh`, `--brain=fake --herdr=mock --voice=off`,
temp data dirs), then runs the unit tests (`SheepdogKitTests`) and the UI
tests (`SheepdogUITests`) against them; `HistoryApprovalUITests` runs after
a fresh daemon restart, and `ReconnectUITests` with
`ios/scripts/fixture-daemon-restarter.sh` running. Only the opt-in
recording and screenshot tests skip. It builds for the simulator only and
uses no secrets; it is not a required check (see below). Archives, signing
and TestFlight stay on Xcode Cloud, which runs no tests, and no signing
certificate or App Store Connect key for the app goes into GitHub. The other exception
is `release.yml`'s `sign-macos` job, which signs and notarizes the daemon's
darwin binary on a GitHub-hosted Mac in the `release` environment
(maintainer approval, `v*` tags only), followed by the `homebrew` job that
pushes the formula to the tap ([signing.md](signing.md)). The `policy` CI
job (`scripts/workflow-policy.sh`, tested by
`scripts/workflow-policy_test.sh`) fails if a macOS runner or `xcodebuild`
appears anywhere else, including a `release.yml` job without
`environment: release`, if `release.yml` reads a secret other than
`GITHUB_TOKEN` outside such a job, or if `ios.yml` reads any secret.

`ios.yml` is not a required check because of its `paths:` filter: on a PR
that touches none of those paths it never starts, and a required check that
never starts blocks the merge. Making it required means giving it the same
`changes` gate as `ci.yml` first (the job always starts and skips its steps
when nothing relevant changed), then adding the `tests` check of the `iOS`
workflow to the branch protection of `main`.

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
