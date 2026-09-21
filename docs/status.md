# Status — 2026-09-18 (morning after the overnight run)

Living handoff note. Update it whenever a work package lands or a live check
is done; it is what a fresh session reads first.

## Where main is

`main` = `3ccb9a4`. Everything in `sheepdog-v1-plan.md` (WP0–WP7) and in
`docs/roles.md` Delivery (1a, 1b, 1c, 2, 3) is merged. Twenty-five PRs so
far, every one squash-merged on green CI after independent verification.

| Area | State |
|---|---|
| herdr client, state table, event log | done (WP1, WP5) |
| Gemini Live session, `/voice` page, silence gate, stall recycle | done (WP2, WP6, #16) |
| Supervisor tools, pending action, chattiness levels | done (WP3, #17) |
| CLI query tool (`codex exec` / `claude -p`, read-only) | done (WP4) |
| Packaging: config init, changelog | done (WP7a); the plugin shim and launchd template were deleted in PR 2 — the socket subscription is the only event source and the daemon is run by hand |
| Live e2e against real panes | done (WP7b): scenarios 1–9 pass, 10 partial |
| `/debug`: Swimlane, Conversation, Supervisor, Events, State tabs | done (#16, #19, #22) |
| Brain (stateful SVP), prompt workspace + overrides, evals | done (#20, #21) |
| Enrichment + promises; push judgment under the chattiness ceiling | done (#23, #24) |
| Logo (README picture, both pages, favicons) | done (#18) |
| Brain model switch at runtime (`/debug` picker, `GET /api/brain/models`, `PUT /api/brain/model`, `session.brain_model`) | done (#25); verified with `--brain=fake` and one live model-list call |
| DeepSeek as a second brain provider (`[deepseek]`, `brain.provider`, `OpenAICompatModel`, provider select on `/debug`, `session.brain_provider`) | in review; verified live: 11 DeepSeek calls (model list, tool-using asks at `low` and twice at `high`, a `prompt`+`promise` propose, a seeded second turn without earlier reasoning) |

## Verified live vs. verified with the fake model only

Live (real Gemini) so far: WP0–WP7b, #16, #17, brain 1a (4 turns, 2.75
model calls/turn avg), 1b assistant eval (23/4/20 skipped when the daily
quota ran out).

**Fake model only** (Gemini free tier hit its ~20 req/day/model/key cap on
2026-09-18): 1c Supervisor tab, phase 2 (enrichment, promises), phase 3
(push judgment, yield, coalescing). The logic is unit- and daemon-tested
with `--brain=fake`; the real model's behaviour on it is not yet observed.

Live on DeepSeek (`deepseek-flash`, 2026-09-18, `brain-test`): an ask
that reads a screen (2 calls, 2.8–2.9 s at `low` and `high`), a propose
that sends `prompt` and `promise` in one step (3.3 s; the
`reasoning_content` replay was accepted on the second call), and a
second turn seeded with a transcript whose reasoning had been stripped
(accepted: the replay rule holds within a turn's tool loop only). The
provider switch itself (`/debug` select, `PUT`, restart) was verified
with `--brain=fake` and its two fake providers.

## Live checks to run when quota allows (text first, audio last)

0. Key check (free): `set -a; source .env; set +a; curl -s -H "x-goog-api-key: $GEMINI_API_KEY" https://generativelanguage.googleapis.com/v1beta/models | head -c 200`
1. Brain re-check (2 turns): `bin/sheepdog brain-test --env-file .env --question "what is auth doing?" --agent auth --state working --fixture codex-working.screen.txt` and the propose variant with `--kind propose --question "run the tests and let me know when it's done" --say "..." --alias auth --state idle` (expect `prompt` then `promise`).
2. Phase 2/3 sequence, real brain, no voice (~10 calls):
   `SHEEPDOG_DATA_DIR=/tmp/sheepdog-live bin/sheepdog serve --env-file .env --brain=on --herdr=mock --voice=off --dev --port 7799`, then on `/debug`: inject Codex approval preset → utterance "yes" → "tell auth to run the tests and let me know when it's done" → state working → inject done (after-approval screen). Expect: question pushed before the first `brain.step`; one `voice.push` per finish carrying the promise line; `brain.push_decision {via: brain}`; repeat at `quiet` (only the promise line); a second untouched pane at `normal` → `urgency_applied: skip, reason: ceiling`, no turn.
3. `make eval-live` — a full day's budget on one key; run deliberately, once.
4. Model switch, real brain (~3 calls): switch to another Gemini model
   on `/debug` after a turn that used a tool, then ask something that
   reads a screen; expect the new model's `brain.step`s to succeed (the
   earlier tool call is replayed without its thought signature). The
   provider half — switch to deepseek, one tool-using question — is
   done live (DeepSeek PR); what is left is a Gemini→DeepSeek→Gemini
   sequence in one daemon run, which needs Gemini quota.
5. One voice session (ask the user; audio is the quota-hungry path): Codex in `--sandbox read-only -a on-request`; say "why did that one fail?" after a finish, "let me know when it's done", "stop" mid-echo.

## Next: the 2026-09-18 redesign (PR 1 of 7 in review)

`docs/issues-2026-09-18.md` records what the headset sessions on
2026-09-18 showed (the assistant answering approval prompts by itself,
`blocked → blocked` invisible, pushes cutting speech) and the decisions
agreed with the CEO: one decision-maker (the assistant keeps only
`to_supervisor(text)` and `stop()`; the SVP owns intent behind the
rail), stop as a per-phase code procedure, nothing interrupting the
assistant mid-sentence, `blocked → blocked` detection. Work order is at
the end of that file. PR 1 (D1-revised) is in review: the voice's two
tools, `brain.KindUtterance` with the roster sentence in the request,
the `note` tool on the brain, the code fast paths, the stop procedure
(`supervisor.cancel_window_s`), `voice.name` and the `/debug` labels.
PR 2 (cleanup) removed the template LLM: approval prompts are parsed
in code (`internal/herdr/prompt.go`), a finish is phrased by the brain
or by the code sentence "<alias> finished.", every event gets exactly
one push, and `[llm]`, `llm-test`/`ask-test` and the plugin shim are
gone. PR 3 (investigations) added the brain's `investigate` tool and the
background job behind it: the read-only CLI sub-agent runs in the
worker's repository with no time limit beyond `brain.investigate_max_s`,
the brain acknowledges at once with `pending: true`, the answer comes
back as a `KindInvestigation` event turn pushed `idle` and tagged
`answers_job`, the ledger lives in `session.investigations` (so `recall`
and follow-ups survive a restart), a follow-up resumes the same CLI
session, and stop cancels a running look. Verified with a stub CLI
backend on the fake daemon and four real CLI runs for the session/resume
mechanics; `source: worker` is deliberately still open. Still open from
that list: `blocked → blocked` (4) and no mid-sentence interruption (5). Names from
here on: shepherd (operator), the dog (voice + brain), the flock
(workers); `docs/roles.md` is the contract and maps the old CEO / SVP /
assistant terms still present in code.

## Known gaps and follow-ups

- Found in the 18:44Z session on 2026-09-18: `working → idle` (a finish the
  user was looking at) was never announced, only `working → done` was —
  fixed in `transitions.go`/`onDone` (see PR for the promise-matching fix).
- herdr 0.9.0 does not detect Antigravity CLI prompts as `blocked` (needs a local `~/.config/herdr/agent-detection/agy.toml` override or an upstream manifest fix); user chose to skip agy for now.
- Eval: "Okay, let's use that then." still routes to `confirm` (left rather than over-fit).
- Scenario 10 (GoAway/compression) never observed live; `voice.turn`/`voice.stalled`/`voice.recycle` are the signals.
- Consider a billed Gemini project for the brain (2–3 calls per CEO turn will exhaust the free daily cap quickly); DeepSeek is now the paid alternative for the brain only (`brain.provider = "deepseek"` or the `/debug` picker).
- `SHEEPDOG_DATA_DIR` does not move `state.path` (it is resolved against the default data dir at `Default()` time): a second daemon started with only `SHEEPDOG_DATA_DIR` set shares the user's `state.json`. Always set `SHEEPDOG_STATE_PATH` too for a scratch daemon.
- `v0.1.0` not tagged yet (release workflow is ready; user wanted a satisfying voice session first).

## How work is done here (see also the memory notes)

Orchestrator + one coding sub-agent at a time in an isolated worktree under
`.claude/worktrees/<name>` (gitignored); Conventional Commits; PR per work
package; orchestrator re-runs `go build/vet`, `go test -race ./...`,
`golangci-lint run`, `node --test` (after `source ~/.nvm/nvm.sh`) and a
secret grep before opening the PR; CI watched with
`GH_TOKEN="$GITHUB_PAT" gh run watch`; squash-merge on green; worktree
removed and branch deleted after merge. Sub-agents never touch port 7777
(the user's daemon) or herdr's `kvoice` workspace; they use port 7799 and
`--herdr=mock`.
