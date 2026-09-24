# Status — 2026-09-23

Living handoff note: what is on `main`, what is verified how, and what is
open. Update it whenever a PR lands or a live check is done; it is what a
fresh session reads first. The contract is [`roles.md`](roles.md), the
code map is [`architecture.md`](architecture.md), every option is in
[`../CONFIG.md`](../CONFIG.md). How the 2026-09-18 redesign landed, PR by
PR, is in [`history/roles-status.md`](history/roles-status.md); the
original plan and older run reports are in [`history/`](history/).

## Where main is

`main` = `cecab0a` (#49), 49 PRs, every one squash-merged on green CI.
The v1 plan (WP0–WP7) and the whole 2026-09-18 redesign
([`history/issues-2026-09-18.md`](history/issues-2026-09-18.md), PRs 1–6
of its work order) are merged; so is everything since.

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
| Finish delivery (iteration 2 WP2): a finish waits for the brain's line, only a block speaks at `brain.announce_wait_s`, a late `needs_user` line is always spoken, "started on" for the brain's own prompt is silent | in review |
| "New conversation" (brain transcript window + voice memory) | done (#44, #48) |
| Phone/LAN access: `web.bind` / `web.token`, cookie login, `GET /api/hello` | done (#36) |
| `/debug`: Conversation, Swimlane, Supervisor, Context, Events, State tabs | done (#16, #19, #22, #49) |
| Evals (`sheepdog eval --set svp\|assistant`, offline by default) | done (#21) |
| iOS companion app (separate repo, `herdr-sheepdog-ios`, the `/ws` voice contract) | started |

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

Fake model / mock herdr only (`--brain=fake --herdr=mock`): most of the
supervisor's edge cases (reprompt races, deliver-later staleness, reset
leftovers, echo guard), which are unit- and daemon-tested. Gemini's free
tier allows about 20 requests per model per key per day; run live checks
sparingly and `make eval-live` deliberately.

## Open

- **`source: worker`** for investigations (asking the idle worker in its
  own pane): its finish would produce both the announce line and the
  delivery line for one event, which the one-push-per-event rule
  forbids. The ledger already carries the field.
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
- **`v0.1.0`** is not tagged yet (the release workflow is ready).

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
