# Status — 2026-10-03

Living handoff note: what is on `main`, what is verified how, and what is
open. Update it whenever a PR lands or a live check is done; it is what a
fresh session reads first. The contract is [`roles.md`](roles.md), the
code map is [`architecture.md`](architecture.md), every option is in
[`../CONFIG.md`](../CONFIG.md), and the PR-by-PR history is
`CHANGELOG.md`.

## Where main is

`main` = `e068ee7` (#85). The v1 plan, the 2026-09-18 redesign and
iteration 2's phase-1 work packages (WP1–WP5, WP8, WP9;
[`iteration-2.md`](iteration-2.md)) are merged.

**CI and releases.** Both repos are private. CI (`.github/workflows/ci.yml`:
vet, race tests, lint, web tests, cross builds) runs on Ubuntu; GitHub
Actions was blocked by billing from 2026-09-27 and has run green again
since #79. The release workflow is Ubuntu-only; the darwin binary is
signed and notarized locally with `make release-sign`
([`signing.md`](signing.md)). Nothing runs on macOS runners. The iOS
companion app (`herdr-sheepdog-ios`) is built and shipped by Xcode Cloud,
not GitHub Actions; TestFlight build 0.1.0 (10) is in the internal
**Me** group.

What is in:

- **herdr side:** socket client, debounced watcher, persisted state
  table, event log with stable ids and `Last-Event-ID` replay;
  approval prompts (including Claude Code's AskUserQuestion widget)
  parsed in code; blocked → blocked via the post-answer poll and the
  reconciler.
- **Voice:** Gemini Live on AI Studio or Vertex AI (`gemini.backend`)
  and GPT-Live, switchable at runtime; two tools (`to_supervisor`,
  `stop`); no mid-sentence interruption, based on the client's playback
  reports; held pushes while no client listens; re-seed after a Gemini
  reconnect; GPT-Live input paced at real time.
- **Brain:** stateful agent over a transcript window with providers
  `gemini`, `vertex` and `deepseek` and a runtime model switch; the ear
  contract (`spoken` / `ask` / `detail`); push judgment under the
  chattiness ceiling; promises; background investigations, including
  forking the worker's own session; reading the worker's own transcript
  and a work journal per agent; an honest code fallback when it fails.
- **Supervisor:** every utterance runs on the shepherd's recorded words;
  the one rail in code (`supervisor.authorise_window_s`); the stop
  procedure, acknowledged by a code sentence with no brain turn;
  deliver-later replies (`brain.reply_wait_s`); "New conversation".
- **Surfaces:** `/voice`, `/debug` (Swimlane, Conversation, Supervisor,
  Context, Events, State), phone access with `web.bind` / `web.token`,
  APNs notifications for the iOS app ([`push.md`](push.md)).
- **Measurement:** `sheepdog eval` (offline L0 corpus), `sheepdog
  scorecard`, `sheepdog audio-e2e`.

## Verified live

- **The v1 success test on the phone, 2026-10-02** (iPhone, TestFlight,
  Gemini Live on Vertex, brain on Vertex `gemini-3.8-flash`): status,
  proactive block with voice approval, deep ask (codex investigation),
  instruction with read-back, and stop all pass (approval and stop after
  #81). The brain has run on DeepSeek since Vertex `gemini-3.8-flash`
  degraded with 504s the same night.
- **Earlier live runs:** the v1 scenarios against real agy, Codex and
  Claude Code panes ([`history/e2e-results.md`](history/e2e-results.md));
  the brain on Gemini and DeepSeek; investigations with real Codex and
  Claude CLIs; headset sessions on both voice providers; the audio e2e
  harness and the GPT-Live pacing run
  ([`audio-e2e-results.md`](audio-e2e-results.md)); Vertex Live and the
  Vertex brain with `voice-test`, `brain-test` and a scratch daemon; the
  APNs key against both APNs hosts.
- **Fake model / mock herdr only:** most supervisor edge cases
  (reprompt races, deliver-later staleness, reset leftovers, coalescing,
  the daemon's relay of an unanswered utterance) are unit- and
  daemon-tested. Gemini's free tier allows about 20 requests per model
  per key per day: run live checks and `make eval-live` sparingly.

## Open

- **Bench fixes (decided 2026-10-03):** the open issues and the agreed
  fix for each (voice-session refresh, dropped CEO input, unheard pushes,
  CJK-aware runaway cap, AskUserQuestion meta options, asks/repeats,
  approval lifetime, agent spoken names, provenance hardening) are in
  [`bench-2026-10-03.md`](bench-2026-10-03.md), the single backlog for
  that work.
- **Pending decision (CEO testing first): should every pending approval
  notify the phone?** Today the APNs notification follows the brain's
  announce decision, so a block the brain skips sends no notification.
  The alternative notifies for every pending answer-prompt action
  regardless of the brain; it is a one-line change in
  `internal/supervisor/notify.go`.
- **Later options:** a brain-written compaction as the reseed source
  instead of the verbatim recap (needs a good prompt); a "current focus"
  silent context from brain to voice after each brain turn (agent in
  focus, open question, key points) once the bench fixes have landed.
- **A stop does not drop a queued instruction:** a single Ctrl-C to a
  busy Claude Code pane interrupts the running turn but the queued prompt
  still runs (acceptance run, 2026-10-02 00:34). An idle-then-working
  agent stops cleanly.
- **`v0.1.0` of the daemon is not tagged** (the release workflow builds
  on Ubuntu; the darwin binary is then signed and notarized on the Mac
  with `make release-sign`, see [`signing.md`](signing.md)).
- **Not yet checked on a device:** AirPods and route changes, and a
  multi-question (tabbed) AskUserQuestion widget.
- **agy blocks:** herdr 0.9.0 does not detect Antigravity CLI prompts as
  `blocked` (needs a local agent-detection override or an upstream fix).
- **Scratch daemons:** `SHEEPDOG_DATA_DIR` does not move `state.path`;
  set `SHEEPDOG_STATE_PATH` too, or a second daemon shares the user's
  `state.json`.

## How work is done here

An orchestrating session delegates work packages to at most two coding
sub-agents at a time, each in its own worktree under
`.claude/worktrees/<name>` (gitignored). Conventional Commits; one PR per
package; before opening it: `go build ./... && go vet ./...`,
`go test -race ./...` with every API key empty, `golangci-lint run`,
`node --test 'web/voice/*.test.mjs' 'web/debug/*.test.mjs'` and the
offline evals. CI is watched to green; PRs are squash-merged with the
conventional title as the subject. Sub-agents never touch port 7777 (the
user's daemon), the herdr socket or the default data dir: a scratch
daemon gets its own port, `SHEEPDOG_DATA_DIR`, `SHEEPDOG_STATE_PATH`, an
empty `--config` and `--brain=fake --herdr=mock --voice=off --dev`.
Documents are written in English.
