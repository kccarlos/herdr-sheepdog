# Status — 2026-09-29

Living handoff note: what is on `main`, what is verified how, and what is
open. Update it whenever a PR lands or a live check is done; it is what a
fresh session reads first. The contract is [`roles.md`](roles.md), the
code map is [`architecture.md`](architecture.md), every option is in
[`../CONFIG.md`](../CONFIG.md). How the 2026-09-18 redesign landed, PR by
PR, is in [`history/roles-status.md`](history/roles-status.md); the
original plan and older run reports are in [`history/`](history/).

## Where main is

`main` = `69b61ab` (#75), 75 PRs, every one squash-merged on green CI.
The v1 plan (WP0–WP7) and the whole 2026-09-18 redesign
([`history/issues-2026-09-18.md`](history/issues-2026-09-18.md), PRs 1–6
of its work order) are merged; so is everything since, including all of
iteration 2's phase-1 work packages (WP1–WP5, #54/#55/#57/#58/#59, plus
the follow-on caps in #60), WP8 (dive deep, #68/#69) and WP9 (timelines,
#71/#72).

**GitHub Actions is blocked by billing on this account** (since
2026-09-27: "recent account payments have failed or your spending limit
needs to be increased"). The release workflow
(`.github/workflows/release.yml`) still runs — it is ubuntu-only,
cross-compiling both binaries with `CGO_ENABLED=0` — so releases are
unaffected; the darwin binary is then signed and notarized locally with
`make release-sign` (#75, [`signing.md`](signing.md)). Both this repo and
the iOS companion repo are private, and private macOS runners bill at ten
times the Linux rate on top of being unavailable right now, so neither
repo has macOS CI; the iOS repo uses Xcode Cloud instead (its own compute,
unaffected by this account's GitHub Actions billing).

| Area | State |
|---|---|
| herdr client, debounced watcher, state table, event log (stable ids, day files, `Last-Event-ID` replay) | done |
| Voice: Gemini Live and GPT-Live providers, runtime switch (`PUT /api/voice/provider`), two tools (`to_supervisor`, `stop`), no mid-sentence interruption, silence gate, stall recycle, adaptive playback buffer | done (#30, #34, #42, #43, #45, #46) |
| Brain: stateful agent loop over a transcript window, tools, provenance, promises, push judgment under the chattiness ceiling, Gemini and DeepSeek providers, runtime model switch | done (#20–#26) |
| Brain-side intent: one `KindUtterance` turn per shepherd input with the roster, the pending prompt's options and the open promises in the request; code fast paths (bare yes/no, bare stop word); stop procedure | done (#30, #41) |
| The one rail: `send_keys` only on the shepherd's real input (voice transcription, typed text or a tap), within `supervisor.authorise_window_s` | done (#39) |
| Approval prompts parsed by code; blocked → blocked (post-answer poll + reconciler); revived prompts | done (#31, #33, #37, #48) |
| Investigations: background CLI sub-agent jobs with session resume and a ledger | done (#32) |
| Deliver-later: a `to_supervisor` call waits `brain.reply_wait_s`, then the reply is pushed when it lands | done (#47) |
| Finish delivery (iteration 2 WP2): a finish waits for the brain's line, only a block speaks at `brain.announce_wait_s`, a late `needs_user` line is always spoken, "started on" for the brain's own prompt is silent | done (#54) |
| The shepherd's words (iteration 2 WP3): every utterance runs on the recorded input, not the voice's argument (the echo guard is gone); GPT-Live relays a paused sentence whole; the daemon relays an utterance the voice left unanswered for 4 s; back-to-back utterances queued behind a running turn coalesce | done (#58) |
| "New conversation" (brain transcript window + voice memory) | done (#44, #48) |
| Phone/LAN access: `web.bind` / `web.token`, cookie login, `GET /api/hello` | done (#36) |
| `/debug`: Conversation, Swimlane, Supervisor, Context, Events, State tabs | done (#16, #19, #22, #49) |
| Evals (`sheepdog eval --set svp\|assistant`, offline by default) | done (#21) |
| Brain assembly keeps a step's tool calls adjacent to their results in every request, even one heard mid-step; heals transcripts already recorded out of order | done (#62) |
| Honest fallback when the brain fails (no more false "no change"/"brain is off"); a narrow echo guard drops the voice's own words heard back through the mic and retracts them from the brain's window | done (#63, #65) |
| Spoken pushes (and a late reply landing with no one attached) are held while no `/ws` client is listening and released on reconnect, dropped after a 5 min TTL; playback stats say how old the sample is | done (#64, #65) |
| `read_screen(source: session)` reads the worker's own transcript instead of only its pane; a reply may run to 4 sentences / 60 words when the shepherd asked for detail (`docs/dive-deep.md` WP8a/b) | done (#68) |
| `investigate(source: worker)` forks the worker's own CLI session (Claude `--fork-session`, Codex `exec fork`) read-only and asks there, instead of a fresh sub-agent with no memory of what the worker tried (`docs/dive-deep.md` WP8c) | done (#69) |
| Client playback reports (`started`/`drained`/`flushed`, `docs/ws-contract.md`); `Speaking()` follows real client playback, not generation; a barge-in cuts the brain's "Assistant said" line to what actually played; a client speech detector holds pushes while the shepherd is talking | done (#70) |
| Timeline gaps (`docs/timelines.md` WP9): Gemini reconnect after a 1011 re-seeds a recap of what was said instead of only the silent briefing; the push queue lets an owed line overtake idle news instead of stalling behind it; provider-switch and failed-send drops are logged (`voice.push_carried`/`push_dropped`), never silent; the echo window anchors to the client's playback end, not `turnComplete` | done (#72) |
| GPT-Live input is paced at real wall-clock time while the client is silent (measured `realtime_ratio` ≈1.0 on a live 42 s run, was 0.74–0.87×); playback start buffer 1200 ms → 400 ms; the browser warns when echo cancellation is off | done (#73) |
| Gemini Live on Vertex AI: `gemini.backend = "vertex"` with `[gemini.vertex]` (`VERTEX_API_KEY`, `VERTEX_PROJECT`, location `us-central1`); API key in `x-goog-api-key`, same `gemini-3.8-live`, one key backs off on 429; live-checked with `voice-test` and a scratch daemon ([`research/vertex-live-spike-2026-09-29.md`](research/vertex-live-spike-2026-09-29.md)) | done (#77) |
| iOS companion app (separate repo, `herdr-sheepdog-ios`, the `/ws` voice contract): playback reports and the client speech detector (#14); Flock pane with per-agent state and answerable prompts, brain-reply cards, held/released/dropped push cards, typed input (#17); Health pane (daemon/voice/brain/pushes/audio/recent-problems), echo-cancellation status, brain and voice model pickers, seven `SessionCore` glitch fixes (#18); Xcode Cloud CI (PR + TestFlight workflows), TestFlight build 0.1.0 (2) available in the internal **Me** group (2026-09-28) | started |

The ear contract (iteration 2, WP1): the brain's reply is a headline
(`spoken`, capped in code at two sentences and thirty words with no
dashes, PR numbers or hashes), one `ask` heard last, and a plain
`detail` the voice explains from; the voice adds no question of its
own, and "never mind" is no longer a stop word (voice prompts, the
Gemini `stop` tool description, `supervisor.BareStopWords`).

The pre-redesign surface is gone: the supervisor's old tool methods
(`Briefing`, `Ask`, `Propose`, `Confirm`, `Cancel`, `Note`) and the
brain's `ask` / `propose` request kinds. Production runs on the voice's
two tools and one `utterance` turn per shepherd input; `brain.propose_timeout_s`
and `brain.enrich_timeout_s` are now `brain.utterance_timeout_s` and
`brain.announce_turn_timeout_s` (old names still accepted, CONFIG.md
"Deprecated keys").

## Verified live vs. with the fake model only

Live: the v1 scenarios (WP7b, 1–9 pass, 10 partial,
[`history/e2e-results.md`](history/e2e-results.md)), the brain on Gemini
and on DeepSeek (tool-using turns, the model switch), investigations with
real Codex/Claude CLI runs, headset sessions on Gemini Live and GPT-Live,
the audio e2e harness ([`audio-e2e-results.md`](audio-e2e-results.md)).
The one #73 GPT-Live pacing run (42 s, scratch daemon, fake brain, mock
herdr, one billed session) is live-verified: `realtime_ratio` 0.94–1.06
against 0.74–0.87 before, `max_gap_ms` 88–313 against 626–1464.

Everything else that landed 2026-09-24/25 (#62–#72, and the rest of
#73) is unit- and harness-tested only — golden requests, replayed
fixtures, the offline `sheepdog eval`, `web/voice/*.test.mjs` — and has
not been run against a real phone or a live headset session yet. That
includes the honest fallback and echo guard (#63/#65), held pushes
(#64/#65), `read_screen`/`investigate` reading a worker's own session
(#68/#69), playback reports and the client speech detector (#70), and
the timeline-gap fixes (#72).

Fake model / mock herdr only (`--brain=fake --herdr=mock`): most of the
supervisor's edge cases (reprompt races, deliver-later staleness, reset
leftovers, the relay gate, the daemon's relay of an unanswered
utterance, coalescing), which are unit- and daemon-tested. Gemini's free
tier allows about 20 requests per model per key per day; run live checks
sparingly and `make eval-live` deliberately.

## Open

- **agy blocks**: herdr 0.9.0 does not detect Antigravity CLI prompts as
  `blocked` (needs a local `~/.config/herdr/agent-detection/agy.toml`
  override or an upstream fix).
- **GoAway / context compression** (v1 scenario 10) never observed live;
  `voice.turn`, `voice.stalled` and `voice.recycle` are the signals.
- **Quota**: 2–3 brain calls per shepherd turn exhaust the Gemini free
  tier quickly; DeepSeek (`brain.provider = "deepseek"`) is the paid
  alternative for the brain.
- **Scratch daemons**: `SHEEPDOG_DATA_DIR` does not move `state.path`;
  set `SHEEPDOG_STATE_PATH` too, or a second daemon shares the user's
  `state.json`.
- **Cancelling a promise**: nothing the shepherd can say cancels an open
  promise (the old cancel tool did; it was not reachable from the voice
  since the redesign). It expires after `supervisor.promise_ttl_s`.
- **`v0.1.0`** is not tagged yet (the release workflow is ready and builds on Ubuntu only; the darwin binary is signed and notarized afterwards on the Mac with `make release-sign`, see [`signing.md`](signing.md)).
- **Device verification of the iOS work (#14/#17/#18)**: playback
  reports, the client speech detector, the Flock and Health panes and
  the echo-cancellation rebuild-after-route-change fix have each only
  run in the simulator against fixture daemons; none has been checked
  on a real phone yet (each PR's own "To check on the phone"/"on the
  device" section is the checklist).
- **The relayed-answer path of GPT-Live pacing was not re-measured**:
  #73's one live run measured the model replying to its own filler
  ("count slowly from one to twenty"); a reply relayed from the brain
  (`to_supervisor` → spoken push) through the paced input timeline has
  not been measured live.
- **The live model's use of `source` arguments** (`read_screen`'s
  `source: session`, `investigate`'s `source: worker`) is verified only
  offline against the fake model and the eval harness; a live run
  against DeepSeek — which the orchestrator is expected to schedule —
  has not happened yet.
- **iOS `PlaybackProfile` should follow the 400 ms GPT-Live start
  buffer** (#73 lowered it from 1200 ms on the daemon/web side; not
  done here). Checked 2026-09-25: `herdr-sheepdog-ios`'s
  `PlaybackRatePolicy.swift` still has `PlaybackProfile.openAI` at
  1,200 ms — still open.

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
