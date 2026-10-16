# Status — 2026-10-07

Living handoff note: what is on `main`, what is verified how, and what is
open. Update it whenever a PR lands or a live check is done; it is what a
fresh session reads first. The contract is [`roles.md`](roles.md), the
code map is [`architecture.md`](architecture.md), every option is in
[`../CONFIG.md`](../../CONFIG.md), and the PR-by-PR history is
`CHANGELOG.md`.

For agents picking up the work: the working handover, with the user's
principles, the latest test results, the decisions waiting and the next
work queue, is [`handover-2026-10-04.md`](handover-2026-10-04.md). Read it
after this page.

## Where main is

`main` = `5506d97` (#186; the dual-brain voice layer landed in #122 and is the adopted design since the user's phone test of 2026-10-04; unified roles, roles only, in #185). The v1 plan, the 2026-09-18 redesign and
iteration 2's phase-1 work packages (WP1–WP5, WP8, WP9;
[`iteration-2.md`](iteration-2.md)) are merged.

**CI and releases.** Both repos are private. CI (`.github/workflows/ci.yml`:
vet, race tests, lint, web tests, eval, cross builds) runs on Linux. GitHub-hosted
minutes ran out again on 2026-10-05, so since #162 every job runs on
self-hosted ephemeral runners in a Cloud Run worker pool (GCP project
`herdr-sheepdog-ci`, scaled 0–3 by a webhook, about $0.07 per CI run on
trial credit; [`infra/ci-runners`](../../infra/ci-runners/README.md)); the repo
variable `CI_RUNNER=ubuntu-latest` switches back. The release workflow is Ubuntu-only; the darwin binary is
signed and notarized locally with `make release-sign`
([`signing.md`](signing.md)). Nothing runs on macOS runners. The iOS
companion app ([`ios/`](../../ios/README.md), until 2026-10-15 the separate
`herdr-sheepdog-ios` repository) is built and shipped by Xcode Cloud,
not GitHub Actions; TestFlight build 0.1.3 (35) (Settings ▸ Languages)
is in the internal **Me** group (iOS status: [`ios/docs/dev/status.md`](../../ios/docs/dev/status.md)).

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
  the orchestrator, #101).
- **Dual-brain voice layer** (#122, [`design/dual-brain.md`](design/dual-brain.md),
  [`eval/dual-brain-2026-10-04.md`](eval/dual-brain-2026-10-04.md)): Gemini
  Live is the voice and owns the conversation; the orchestrator owns
  actions behind the rails. Everything the daemon tells it (answers, worker
  news, focus and names lines, cut-off facts) arrives as a **briefing**, a
  function response, never a user turn; `to_supervisor` answers use the
  same shape and wait up to `brain.reply_wait_s` (default 30, above the
  25 s utterance budget). The Gemini heard watch and re-queue, the
  self-relay and rescue guards and the echo text guard were removed: the
  phone's playback report becomes a cut-off fact for the voice, words
  that began over the voice's own line a timing fact for the orchestrator.
- **Fast orchestrator toward facts** (#141, prompt v5.1,
  [`eval/fast-brain-facts-2026-10-04.md`](eval/fast-brain-facts-2026-10-04.md)):
  grounding first; the voice answers only its own last line, word
  meanings and small talk, and hands off anything new, comparisons,
  "why", history and doubt; asks when the agent is unclear. Every
  briefing carries its age at delivery and the agent's spoken name and
  state; orchestrator tool timeouts reach it as `not_verified`; a spoken
  briefing that drew no speech is reported to the voice and the orchestrator
  (`voice.briefing_unvoiced`); the roster says "finished work N min
  ago" instead of splicing a report; a spoken name the user heard or said
  is pinned (`supervisor.name_pinned`); a new agent herdr does not see
  after 20 s is reported to the user with its pane's last lines, and new
  panes get `DISABLE_AUTO_UPDATE=true`. Tier-2 A/B (prompt only, same
  binary): phone-test cases 9/18 → 11/18, hand-off 75 % → 90 %, rewind
  prompts on an ambiguous "it" 4 → 0, existing suite 21/36 → 21/36;
  self-answer ≈ 1.2 s vs hand-off ≈ 5.5 s.
- **Language preference** (#142, [`design/language-preference.md`](design/language-preference.md)):
  the user's languages, most preferred first, are a runtime setting
  (`PUT /api/settings {"languages": [...]}`, the iOS app's Settings ▸
  Languages; state file `session.languages`). The voice and the orchestrator are told it as
  information: a `## Language` section in the orchestrator's state block,
  the last line of the voice's system instruction (Gemini Live and
  GPT-Live), and a silent line to a running voice session on change.
  The catalog is Gemini Live's official language table intersected
  with OpenAI's published speech-language list (GPT-Live publishes no
  list of its own); 简体中文 and 繁體中文 are separate entries.
- **Baseline information fixes** (#146, items from
  [`eval/baseline-2026-10-05.md`](eval/baseline-2026-10-05.md); no new
  judgment in code, no new action rails): a decision-open fact on every
  announcement for the voice and the orchestrator (`decision_open` in the briefing,
  "Decisions open right now" in the announce turn); task title,
  instruction and last activity in the briefing's `agent_state`; the
  worker's newest report in full in the orchestrator's agent record;
  prompt v5.2 and RULES.md (technical terms stay English in Chinese, "not
  yet / 先不要 / 等一下" is a deferral, a question is never an
  instruction); tolerant `options` decode and one same-provider retry on
  a 499 (`brain.retry`); and the #139 gap closed: a reconnect from any
  connection opened by a resume presents no handle. Interleaved tier 2
  ×2 on 14 affected scenarios: pass 14/28 → 13/28 (flat, judge noise),
  announcements ending in a question with nothing open 2/15 → 0/15,
  `esc` on a deferral 1 → 0, "race detector" kept in English in Chinese
  answers; results in
  [`bench/results/2026-10-05-info5/`](bench/results/2026-10-05-info5/).
- **Orchestrator:** stateful agent over a transcript window with providers
  `gemini`, `vertex` and `deepseek` and a runtime model switch; the ear
  contract (`spoken` / `ask` / `detail`); push judgment under the
  chattiness ceiling; promises; background investigations, including
  forking the worker's own session; reading the worker's own transcript
  and a work journal per agent; an honest code fallback when it fails;
  compaction off the turn lock, per-tool timeouts, a 25 s utterance
  budget, and one-step provider failover on 429 / 5xx / timeout with a
  spoken notice after three in a row (fake-model tests only; `eval sim
  --fallback-provider` can now exercise it live, not yet run).
- **Supervisor:** every utterance runs on the user's recorded words;
  the one rail in code (`supervisor.authorise_window_s`); the stop
  procedure, acknowledged by a code sentence with no orchestrator turn;
  replies handed back as the call's own result within `brain.reply_wait_s` (default 30 s), later as a briefing; "New conversation".
- **Current focus:** after an orchestrator reply the voice gets one silent line
  (agent in focus, open question, key points) so it resolves "it" and
  answers follow-ups ([`timelines.md`](timelines.md); #101).
- **Names:** every agent has a pane id, an alias and a spoken name (from
  its project, kind and session title; a user-given name wins), which the
  voice also gets in its vocabulary; "call that one docs" renames by voice
  ([`roles.md`](roles.md) "Agent names").
- **Surfaces:** `/voice`, `/debug` (Swimlane, Conversation, Supervisor,
  Context, Events, State), phone access with `web.bind` / `web.token`,
  the agent window API (`GET /api/agents/{pane}/screen`, `POST
  .../input`; [`ws-contract.md`](ws-contract.md); #103) and the same
  window streamed over `/ws` (feature `screen`: one subscription per
  client, a frame only when the window changed, read every 1 s or sooner
  on a transition, reads ≥ 250 ms apart; "Agent window over /ws"), the `/ws` protocol version and `client-ready` / `bot-ready` handshake
  ([`ws-contract.md`](ws-contract.md)), APNs notifications for the iOS
  app ([`push.md`](push.md)).
- **Unified roles, v6-r** (#185, [`design/dual-brain.md`](design/dual-brain.md),
  [`eval/roles-arms-2026-10-07.md`](eval/roles-arms-2026-10-07.md)): every
  model reads one framing, one orchestration agent operating the user's
  herdr with a voice and an orchestrator; "the user" in every prompt and
  model-facing string; prompt length rules unchanged (short by default).
  Default chattiness is `chatty` (#178).
- **Evals:** every experiment so far, with its decision, is indexed in
  [`eval/README.md`](eval/README.md); read it before proposing an eval or
  a prompt change. Current tier-1 workday baseline at `chatty`
  (Sonnet segment judge): segment Understood 92 % on the pre-#185 main,
  89 % for the roles-only prompts (difference not significant).
- **Measurement:** `sheepdog eval` (offline L0 corpus), `sheepdog eval
  sim` (a simulated CEO plus an LLM judge; `--live` for the real orchestrator),
  `sheepdog scorecard`, `sheepdog audio-e2e`, and `sheepdog bench` (tier-2
  voice benchmark, [`bench/README.md`](bench/README.md)).
- **Observability:** `turn_id` / `utterance_id` on events, `turn.latency` per user utterance (scorecard "Turn latency"), the observe-only daemon watchdog (`daemon.heartbeat_missed` / `_ok`); [`architecture.md`](architecture.md) "Observers".

## Verified live

- **Dual-brain phone test, 2026-10-04** (iPhone speakerphone, TestFlight
  0.1.2 (29), Gemini Live on Vertex, brain on Vertex `gemini-3.8-flash`
  then `gemini-3.5-flash`): the CEO judged it smooth and adopted it. Of 73
  turns the fast brain answered 33 itself (about 24 right, 3 stale, 4
  wrong); no unauthorised action; 3 news briefings never voiced; replies in
  Traditional instead of Simplified Chinese; occasional echo on
  speakerphone. Details and the ranked fixes are in the handover.
- **Tier-3 bench (real herdr, Claude Haiku workers) and campaign E,
  2026-10-03/04:** tier 1 88 %, tier 2 67 %, tier 3 text 7/12 and voice
  5/12 ([`eval/campaign-e-2026-10-03.md`](eval/campaign-e-2026-10-03.md));
  dual-brain vs main on tier 2: 61/90 vs 46/90
  ([`eval/dual-brain-2026-10-04.md`](eval/dual-brain-2026-10-04.md)).
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

- **Next work (agreed, not started; see the handover's queue):** voice
  prompt toward facts (hand off anything new, comparative or historical;
  ask when unsure), briefings carrying their age and a finished flag, tool
  timeouts reported as not verified, unvoiced briefings reported to the
  voice, the roster formatter, stable agent names, reliable spawn
  launch; then a **language preference in Settings** fed into the voice and the orchestrator'
  prompts. Prompt and information changes only, no judgment in code.
- **Orchestrator failover is broken in practice:** DeepSeek returns 402
  Insufficient Balance (since 2026-10-03), so a Vertex timeout ends in the
  fallback line. The user is to top it up or pick another fallback. The live
  orchestrator is Vertex `gemini-3.5-flash`; `gemini-3.8-flash` on Vertex has
  bursts of 20-50 s steps and 504s.
- **Gemini Live loses context on the second and later resumes** (a handle from a resumed connection keeps only that connection's content; first resume lossless; [`research/live-resume-context-2026-10-04.md`](research/live-resume-context-2026-10-04.md)). Worked around (user option 1, #139): the daemon never resumes a resumed session; the reconnect after one opens a fresh session re-seeded from the recap and the recent briefings (`voice.handle_dropped {reason: resumed_twice}`). Bench `resume-recall-twice` (two recycles, a turn between): both facts recalled 5/5 with the rail, 2/3 without. `voice.resume_context` and the scorecard's resume-retention line (now with a "fresh instead of a 2nd resume" count) track it; `cmd/livecanary -arm chain` is the platform canary (still 0/8: the platform is unchanged).
- **Gemini Live mid-answer pauses** (~7 s, Vertex `gemini-3.8-live` only,
  [`research/live-pauses-2026-10-04.md`](research/live-pauses-2026-10-04.md)):
  trial `gemini.backend=aistudio` pending.
- **Pending decision (user testing first): should every pending approval
  notify the phone?** Today the APNs notification follows the orchestrator's
  announce decision, so a block the orchestrator skips sends no notification.
  The alternative notifies for every pending answer-prompt action
  regardless of the orchestrator; it is a one-line change in
  `internal/supervisor/notify.go`.
- **Later options:** a orchestrator-written compaction as the reseed source
  instead of the verbatim recap (needs a good prompt); a "current focus"
  silent context from orchestrator to voice after each orchestrator turn (agent in
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
