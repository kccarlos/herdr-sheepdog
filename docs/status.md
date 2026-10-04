# Status — 2026-10-04

Living handoff note: what is on `main`, what is verified how, and what is
open. Update it whenever a PR lands or a live check is done; it is what a
fresh session reads first. The contract is [`roles.md`](roles.md), the
code map is [`architecture.md`](architecture.md), every option is in
[`../CONFIG.md`](../CONFIG.md), and the PR-by-PR history is
`CHANGELOG.md`.

## Where main is

`main` = `6a45016` (#129; the dual-brain voice layer landed in #122). The v1 plan, the 2026-09-18 redesign and
iteration 2's phase-1 work packages (WP1–WP5, WP8, WP9;
[`iteration-2.md`](iteration-2.md)) are merged.

**CI and releases.** Both repos are private. CI (`.github/workflows/ci.yml`:
vet, race tests, lint, web tests, cross builds) runs on Ubuntu; GitHub
Actions was blocked by billing from 2026-09-27 and has run green again
since #79. The release workflow is Ubuntu-only; the darwin binary is
signed and notarized locally with `make release-sign`
([`signing.md`](signing.md)). Nothing runs on macOS runners. The iOS
companion app (`herdr-sheepdog-ios`) is built and shipped by Xcode Cloud,
not GitHub Actions; TestFlight build 0.1.1 (20) is in the internal
**Me** group (iOS status: `herdr-sheepdog-ios` `docs/status.md`).

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
  reconnect; a fresh Gemini session after 30 min away or 2 h of age;
  the recap follows a provider switch; GPT-Live input paced at real time;
  GPT-Live keeps the heard watch (unheard pushes re-queued or reported to
  the brain, #101).
- **Dual-brain voice layer** (#122, [`design/dual-brain.md`](design/dual-brain.md),
  [`eval/dual-brain-2026-10-04.md`](eval/dual-brain-2026-10-04.md)): Gemini
  Live is the fast brain and owns the conversation; the slow brain owns
  actions behind the rails. Everything the daemon tells it (answers, worker
  news, focus and names lines, cut-off facts) arrives as a **briefing**, a
  function response, never a user turn; `to_supervisor` answers use the
  same shape and wait up to `brain.reply_wait_s` (default 30, above the
  25 s utterance budget). The Gemini heard watch and re-queue, the
  self-relay and rescue guards and the echo text guard were removed: the
  phone's playback report becomes a cut-off fact for the fast brain, words
  that began over the voice's own line a timing fact for the slow brain.
- **Brain:** stateful agent over a transcript window with providers
  `gemini`, `vertex` and `deepseek` and a runtime model switch; the ear
  contract (`spoken` / `ask` / `detail`); push judgment under the
  chattiness ceiling; promises; background investigations, including
  forking the worker's own session; reading the worker's own transcript
  and a work journal per agent; an honest code fallback when it fails;
  compaction off the turn lock, per-tool timeouts, a 25 s utterance
  budget, and one-step provider failover on 429 / 5xx / timeout with a
  spoken notice after three in a row (fake-model tests only; `eval sim
  --fallback-provider` can now exercise it live, not yet run).
- **Supervisor:** every utterance runs on the shepherd's recorded words;
  the one rail in code (`supervisor.authorise_window_s`); the stop
  procedure, acknowledged by a code sentence with no brain turn;
  replies handed back as the call's own result within `brain.reply_wait_s` (default 30 s), later as a briefing; "New conversation".
- **Current focus:** after a brain reply the voice gets one silent line
  (agent in focus, open question, key points) so it resolves "it" and
  answers follow-ups ([`timelines.md`](timelines.md); #101).
- **Names:** every agent has a pane id, an alias and a spoken name (from
  its project, kind and session title; a CEO-given name wins), which the
  voice also gets in its vocabulary; "call that one docs" renames by voice
  ([`roles.md`](roles.md) "Agent names").
- **Surfaces:** `/voice`, `/debug` (Swimlane, Conversation, Supervisor,
  Context, Events, State), phone access with `web.bind` / `web.token`,
  the agent window API (`GET /api/agents/{pane}/screen`, `POST
  .../input`; [`ws-contract.md`](ws-contract.md); #103), the `/ws` protocol version and `client-ready` / `bot-ready` handshake
  ([`ws-contract.md`](ws-contract.md)), APNs notifications for the iOS
  app ([`push.md`](push.md)).
- **Measurement:** `sheepdog eval` (offline L0 corpus), `sheepdog eval
  sim` (a simulated CEO plus an LLM judge; `--live` for the real brain),
  `sheepdog scorecard`, `sheepdog audio-e2e`, and `sheepdog bench` (tier-2
  voice benchmark, [`bench/README.md`](bench/README.md)).
- **Observability:** `turn_id` / `utterance_id` on events, `turn.latency` per CEO utterance (scorecard "Turn latency"), the observe-only daemon watchdog (`daemon.heartbeat_missed` / `_ok`); [`architecture.md`](architecture.md) "Observers".

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
  ([`audio-e2e-results.md`](history/audio-e2e-results.md)); Vertex Live and the
  Vertex brain with `voice-test`, `brain-test` and a scratch daemon; the
  APNs key against both APNs hosts.
- **Live eval campaign, 2026-10-03** (simulated CEO in text, real
  brain, mock herdr; [`eval-campaign-2026-10-03.md`](eval/eval-campaign-2026-10-03.md)):
  26 scenarios x 5 per brain. **Run C**, after the H1-H7 fixes: DeepSeek
  `deepseek-flash` **87 %** pass, Vertex `gemini-3.8-flash` **73 %**
  (Vertex provider latency, 504s, costs most of its failures). It
  measured #88-#95; #98 and #99 landed after it.
- **Tier-2 voice bench, 2026-10-03** (synthetic speech through a phone
  emulator into scratch daemons with the real voice model and brain,
  mock herdr; [`bench/tier2-2026-10-03.md`](bench/tier2-2026-10-03.md)):
  34 scenarios x 3, **58 % pass over 102 runs**, against 89 % for the same
  worlds in tier 1: the voice layer is the biggest remaining loss. Tier 3
  (real herdr workers, #105) is pending.
- **Unit / eval only, not re-run on the phone:** every bench fix
  ([`bench-2026-10-03.md`](history/bench-2026-10-03.md)), the voice session
  refresh and unheard-push handling, current-focus sync, the agent window API, the `/ws` handshake against the
  iOS 0.1.1 client, spoken agent names, brain failover.
- **Fake model / mock herdr only:** most supervisor edge cases
  (reprompt races, deliver-later staleness, reset leftovers, coalescing,
  the daemon's relay of an unanswered utterance) are unit- and
  daemon-tested. Gemini's free tier allows about 20 requests per model
  per key per day: run live checks and `make eval-live` sparingly.

- **Phone-session fixes, 2026-10-03 (this PR, unit tests + isolated
  herdr check):** voice spawns split the named workspace's pane
  (`target_pane_id`); renames send herdr's spelling and report refusals;
  a new agent not ready in 60 s is waited for (≤ 10 min) and the brain
  told; agents can be closed by voice (`interrupt` with `close`, words
  rail, protected own-directory agent); Gemini handle updates logged and
  post-handle silent context re-sent on resume; the push-queue watchdog
  counts from the listener's attach.

## Open

- **Bench backlog:** every item of
  [`bench-2026-10-03.md`](history/bench-2026-10-03.md) is fixed; none has been
  re-checked live with a real worker and the phone yet. That re-run is the
  next acceptance step.
- **Tier-2 bench findings:** the failures named in
  [`bench/tier2-2026-10-03.md`](bench/tier2-2026-10-03.md) (the voice
  answering for the brain, deferrals read as declines, echo of the bot's
  own voice, cut and resumed pushes, markers read aloud) are the next fix
  list; tier 3 is pending.
- **Eval campaign leftovers** (run C, [`eval-campaign-2026-10-03.md`](eval/eval-campaign-2026-10-03.md)):
  the run-C fixes (#99: H4 language, the duplicate block line, H7 fact
  source) are not re-measured; Vertex step latency (p90 8.4 s, up to 25 s)
  still causes fallbacks; failover has not been measured live (run
  `eval sim --live --fallback-provider deepseek`); should failover also
  trigger on step latency, not only errors?
- **Pending decision (CEO testing first): should every pending approval
  notify the phone?** Today the APNs notification follows the brain's
  announce decision, so a block the brain skips sends no notification.
  The alternative notifies for every pending answer-prompt action
  regardless of the brain; it is a one-line change in
  `internal/supervisor/notify.go`.
- **Later options:** a brain-written compaction as the reseed source
  instead of the verbatim recap (needs a good prompt); a "current focus"
  silent context from brain to voice after each brain turn (agent in
  focus, open question, key points), now that the bench fixes have landed.
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
