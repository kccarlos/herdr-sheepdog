# Glossary

One set of words for living docs, code comments and anything a person
reads. The product and binary stay **sheepdog**. The contract behind the
words is [`roles.md`](roles.md); the models read the same words in
`prompts/`.

| Term | Meaning |
|---|---|
| **User** | The person, talking by voice. Owns intent and decisions. Never "CEO", "shepherd" or "boss". |
| **Orchestration agent** | The one agent the user talks to, operating their herdr. It has two parts that are one character to the user: the voice and the orchestrator. |
| **Voice** | The part that talks with the user and owns the conversation (Gemini Live or GPT-Live; two tools, `to_supervisor` and `stop`). Formerly the fast brain, the dog, the Gemini Live layer, "assistant". |
| **Orchestrator** | The part that reads screens, investigates, decides, remembers and acts behind the rails: one stateful agent with tools (`internal/brain`). Formerly the slow brain, the brain, the supervisor brain, SVP. |
| **Agents / workers** | The coding agents (Claude Code, Codex, agy) running in herdr panes. Say "agents" or "workers", never "flock". |
| **herdr** | The family of programs that host agent panes and report their state: herdr-tui, herdr-core, and herdr-app. Use it when a statement is true of all three. |
| **herdr-tui** | The original terminal herdr. Select it with `herdr.backend = "herdr"` or `SHEEPDOG_HERDR_BACKEND=herdr`. |
| **herdr-core** (`hc`) | The engine behind herdr-app. Select it with `herdr.backend = "hc"` or `SHEEPDOG_HERDR_BACKEND=hc`. |
| **herdr-app** | The desktop app that runs on herdr-core. It has no `herdr.backend` value of its own. |
| **Briefing** | A labelled message the orchestrator or the daemon hands to the voice (a function response, a silent context line) so it can tell the user in its own words. |
| **Rail** | A check in code that no model can talk its way past, for example "an approval prompt is answered only on the user's own fresh words". Rails make the orchestrator safe to give tools. |
| **Chattiness** | The user's setting (`quiet`, `normal`, `chatty`) that caps how much the voice volunteers unasked. |

## Factory (engine v2)

Terms used by `internal/factory` and [`docs/dev/engine-v2/design.md`](engine-v2/design.md).

| Term | Meaning |
|---|---|
| **Owner** | **The user**, in factory docs: sets goals, answers gated asks, and approves overrides or standing rules. |
| **Factory engine** | The single-writer Go loop inside the daemon (`internal/factory`) that drives started projects from plan to merge over `herdr-core` (`hc`), git worktrees, and GitHub (`gh`). |
| **Coordinator** | The single coordinator agent run per factory project in `hc` (Claude `opus` by default; configurable via `models.coordinator` with Claude, Codex, or `agy`). Plans tasks (`plan apply` / `plan amend`), answers `decision` asks, decides recaps, and escalates gated asks to the owner. |
| **Worker / reviewer** | Task runs started by the engine in `hc`: workers implement tasks in worktrees and run `sheepdog factory report ready`; reviewers judge diffs in fresh sessions and run `sheepdog factory report review` (or emit a `<report>` tag on Codex). |
| **Run** | One agent process in one `hc` terminal, authenticated to the daemon with `SHEEPDOG_RUN_TOKEN` in its environment (never on its command line). |
| **Record / Fact / Rule** | A **record** is a stored commit-tied decision or report in `state.json`; a **fact** is a live world reading from observers (PR checks, worktree tip, run state, quota pools); a **rule** is one row of the ordered first-match table (`internal/factory/flow`). |
| **Head (`H`) / Covers** | `H` is an attempt's current commit. An earlier record (review round, gate answer, or override) **covers** `H` when `H` is the same commit or ahead of it only by clean base merges (`merge_only`). |
| **Ask / Gated ask / Answer proof** | A structured question (`flow.Ask`). `decision` asks are answered by the coordinator; gated kinds (`gate`, `release`, `override`, `paid-key`, `delete`, `install`, `reset`, `rule`) go to the owner and require an in-process `AnswerProof` (`proofs.json`) from a paired-device tap or live voice answer. |
| **Quiet rule (factory)** | `herdr.IsFactoryQuiet` silences non-coordinator factory terminals completely and silences coordinator transitions except `blocked` (`StateBlocked`), so only owner asks, override results, and coordinator dialog blocks reach voice and APNs pushes on `main` (factory alerts are persisted in `state.json` and `log.jsonl` and surfaced in `sheepdog factory status` and `log`). |
| **Stopping alert** | A persisted alert episode (`no_pool`, `disk`, `consent_prompt`) that holds new factory starts until its clear condition is observed; non-stopping alerts (`main_red`, `ci_stalled`, `agents_down`) surface conditions without halting unrelated starts. |

## Former names

Old word on the left, current word on the right.

| Former | Now |
|---|---|
| shepherd, CEO, boss | user |
| flock | agents, workers |
| dog, fast brain, Gemini Live layer | voice |
| slow brain, brain, supervisor brain, SVP | orchestrator |
| chief of staff | orchestration agent |

## What keeps the old words

The rename covers prose: docs, comments, log lines and UI text meant for
people. These keep their names, because changing them would break
stored data, clients or history:

- Go identifiers and file names (`internal/brain`, `Brain`, `braintest`),
  tests and metric ids.
- Event types and payload fields (`brain.turn`, `ceo_say`, `trigger: ceo`),
  JSON and wire fields, state-file fields, config keys and env vars
  (`[brain]`, `SHEEPDOG_BRAIN_*`).
- History: `docs/dev/history/`, dated reports under `docs/dev/eval/`,
  `docs/dev/research/` and `docs/dev/bench/results/`, `docs/handover-*.md`, the
  changelog and the history sections of design docs. They say what was
  true when written.
