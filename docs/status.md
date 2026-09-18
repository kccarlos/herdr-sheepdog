# Status — 2026-09-18 (morning after the overnight run)

Living handoff note. Update it whenever a work package lands or a live check
is done; it is what a fresh session reads first.

## Where main is

`main` = `c34cc90`. Everything in `sheepdog-v1-plan.md` (WP0–WP7) and in
`docs/roles.md` Delivery (1a, 1b, 1c, 2, 3) is merged. Twenty-four PRs so
far, every one squash-merged on green CI after independent verification.
No open PRs, no worktrees, `bin/sheepdog` built from main.

| Area | State |
|---|---|
| herdr client, state table, event log | done (WP1, WP5) |
| Gemini Live session, `/voice` page, silence gate, stall recycle | done (WP2, WP6, #16) |
| Supervisor tools, pending action, chattiness levels | done (WP3, #17) |
| CLI query tool (`codex exec` / `claude -p`, read-only) | done (WP4) |
| Packaging: plugin shim, launchd template, config init, changelog | done (WP7a); launchd never installed (user runs the daemon by hand) |
| Live e2e against real panes | done (WP7b): scenarios 1–9 pass, 10 partial |
| `/debug`: Swimlane, Conversation, Supervisor, Events, State tabs | done (#16, #19, #22) |
| Brain (stateful SVP), prompt workspace + overrides, evals | done (#20, #21) |
| Enrichment + promises; push judgment under the chattiness ceiling | done (#23, #24) |
| Logo (README picture, both pages, favicons) | done (#18) |

Brain model switch at runtime (`/debug` picker over the discovered
Gemini model list, `GET /api/brain/models`, `PUT /api/brain/model`,
persisted in `session.brain_model`): in review; verified with
`--brain=fake` and one live model-list call (free endpoint).

## Verified live vs. verified with the fake model only

Live (real Gemini) so far: WP0–WP7b, #16, #17, brain 1a (4 turns, 2.75
model calls/turn avg), 1b assistant eval (23/4/20 skipped when the daily
quota ran out).

**Fake model only** (Gemini free tier hit its ~20 req/day/model/key cap on
2026-09-18): 1c Supervisor tab, phase 2 (enrichment, promises), phase 3
(push judgment, yield, coalescing). The logic is unit- and daemon-tested
with `--brain=fake`; the real model's behaviour on it is not yet observed.

## Live checks to run when quota allows (text first, audio last)

0. Key check (free): `set -a; source .env; set +a; curl -s -H "x-goog-api-key: $GEMINI_API_KEY" https://generativelanguage.googleapis.com/v1beta/models | head -c 200`
1. Brain re-check (2 turns): `bin/sheepdog brain-test --env-file .env --question "what is auth doing?" --agent auth --state working --fixture codex-working.screen.txt` and the propose variant with `--kind propose --question "run the tests and let me know when it's done" --say "..." --alias auth --state idle` (expect `prompt` then `promise`).
2. Phase 2/3 sequence, real brain, no voice (~10 calls):
   `SHEEPDOG_DATA_DIR=/tmp/sheepdog-live bin/sheepdog serve --env-file .env --brain=on --herdr=mock --voice=off --dev --port 7799`, then on `/debug`: inject Codex approval preset → utterance "yes" → "tell auth to run the tests and let me know when it's done" → state working → inject done (after-approval screen). Expect: question pushed before the first `brain.step`; one `voice.push` per finish carrying the promise line; `brain.push_decision {via: brain}`; repeat at `quiet` (only the promise line); a second untouched pane at `normal` → `urgency_applied: skip, reason: ceiling`, no turn.
3. `make eval-live` — a full day's budget on one key; run deliberately, once.
4. Model switch, real brain (~3 calls): switch to another model on
   `/debug` after a turn that used a tool, then ask something that
   reads a screen; expect the new model's `brain.step`s to succeed (the
   earlier tool call is replayed without its thought signature).
5. One voice session (ask the user; audio is the quota-hungry path): Codex in `--sandbox read-only -a on-request`; say "why did that one fail?" after a finish, "let me know when it's done", "stop" mid-echo.

## Known gaps and follow-ups

- herdr 0.9.0 does not detect Antigravity CLI prompts as `blocked` (needs a local `~/.config/herdr/agent-detection/agy.toml` override or an upstream manifest fix); user chose to skip agy for now.
- Eval: "Okay, let's use that then." still routes to `confirm` (left rather than over-fit).
- Ollama `gemma4` is too slow for the default 15 s summary timeout on cold prompts (`supervisor.summary_timeout_s`, `llm.timeout_s` are configurable) and drifts off the JSON contract for the brain — not a brain backend.
- Scenario 10 (GoAway/compression) never observed live; `voice.turn`/`voice.stalled`/`voice.recycle` are the signals.
- The 10-minute silent briefing re-push re-bills the context each time; make it conditional on state change.
- Consider a billed Gemini project for the brain (2–3 calls per CEO turn will exhaust the free daily cap quickly).
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
