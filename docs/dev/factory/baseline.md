# Phase 0 Baseline: Owner Touches and Outside Interventions

This document records the phase 0 baseline for `sheepdog-factory` before phase 1 extraction: how often **the owner** (the [user](../glossary.md)) touches the factory per day, and how often an outside "babysitter" agent session intervenes on factory tasks or workers. Phase 5's self-hosting exit test ("no babysitter intervention; owner touches are only the five product touchpoints") is measured against this baseline using [`scripts/factory/baseline.py`](../../../scripts/factory/baseline.py).

> **Terminology note:** Following [`docs/dev/glossary.md`](../glossary.md), *agents* / *workers* run in `herdr` panes and *orchestrator* in `sheepdog` daemon prose means `internal/brain` ([`docs/dev/architecture.md`](../architecture.md)). In factory measurement below, *project Assistant* refers to each project's `herdr` coordinator session, and *outside orchestrator* ("babysitter") refers to an interactive Claude Code or Codex session outside the factory that acted on factory tasks by hand.

## Definitions

### Owner touch

One intentional action by the owner on a factory project or its tasks:

| Channel | Source | Deduplication & filtering rules |
|---|---|---|
| `assistant_chat` | Project Assistant JSONL logs (`~/.claude/projects/*/*.jsonl`, `~/.codex/sessions/`) | Counts genuine human turns only. Excludes `tool_result` entries, compaction summaries (`isCompactSummary`), scheduled `/loop` turns (`turnOrigin: "scheduled"`), CLI-injected tags, `<herdr-autopilot>`, `<herdr-news>`, `<herdr-github>`, `<herdr-setting>`, `<herdr-assistant>`, whole-message `<hc>…</hc>` inter-terminal sends (unless `from="artifact"`), `herdr-task` / watchdog system nudges, Assistant template/handoff openings without a trailing owner request, and messages arriving within an outside session's `hc send` or `herdr-app do chat.send` delivery window. |
| `worker_chat` | Worker JSONL logs | Any genuine human turn after turn 1 (the task brief) that survives the same non-human filters and was not sent by a project Assistant or outside session via `hc send`. |
| `outside_orchestrator` | Outside babysitter JSONL logs | Genuine human turns in an outside session that performs mutating actions on factory targets (a session that only runs read-only `herdr-task list` is ignored), **restricted to turns whose message or tool calls resolve to at least one factory project** (via task ref `PREFIX-n`, known factory terminal or space ID, store slug, `--folder`, or project name). Excludes scheduled `/loop` wakes (`turnOrigin: "scheduled"`), `<task-notification>` wakes, and `<hc from="…">` agent replies. Turns referencing two or more factory projects at once appear under `cross-project`. |
| `ui_accept` | Task stores (`~/.local/share/shepherdr/tasks/*.json`) and `usage.jsonl` (`kind: "act"`, `type: "accept"`) | Tasks with `accepted_at` where `accepted_by` is neither `"auto"`, `"engine"`, nor `"assistant"`, `auto_accepted` is false, and no outside or Assistant CLI `herdr-task accept` (or wrapper script) ran for that task around `accepted_at`. Deduplicated within 30 s against `usage.jsonl`. |
| `ask_card` | `~/.local/share/shepherdr/tasks/asks/*.json` and `usage.jsonl` (`kind: "ask"`) | Answered ask records (`answered_at`) and closed ask cards not answered by an agent's `hc keys` into the ask terminal. |
| `settings` | `usage.jsonl` (`kind: "act"` with `key`) and Assistant `<herdr-setting name="…">` notices (`name != "all"`) | Settings changes in the UI, supplemented by historical `<herdr-setting>` change notices when `usage.jsonl` was cleared (deduplicated within 60 s across spaces for `everywhere` changes). |

Each owner touch is classified in memory (without storing or quoting words) into one of the five product touchpoints or `other`:

1. **`goal`** — Starting a project or directing a feature, milestone, roadmap, or task plan (opening message to an Assistant, turns that add tasks or run `project start`, or goal-setting requests).
2. **`rule`** — Changing a standing setting or policy (`settings` channel, turns running `herdr-task config` or `autopilot`, edits to `AGENTS.md` / `CLAUDE.md` / settings / heartbeats, or standing model/quota/delegation rules).
3. **`ask_answer`** — Answering an `ask_card` (non-release) or replying concisely to a preceding question (`?` or `AskUserQuestion`) from an Assistant or worker.
4. **`release_approval`** — Answering a `release` ask, accepting a release task, or approving/triggering a version tag, release workflow, or TestFlight upload.
5. **`digest`** — Read-only status, progress, or blocker inquiries where the responding turn runs only read-only checks.
6. **`other`** — Everything Phase 5 aims to eliminate: manual UI task accepts on routine code tasks, hands-on troubleshooting, nudging stuck workers or Assistants, and babysitter chatter.

### Outside intervention

One mutating action **that resolves to at least one factory project** (via a known factory terminal in `term_map`, `--folder`, task ref `PREFIX-n`, factory space ID, store slug, or project name; actions on an outside session's own non-factory helper panes are excluded), executed by a session that is neither a project's Assistant nor a task's own worker or gate reviewer:

| Type | What it counts |
|---|---|
| `hc_send` | `hc send` into a known factory worker or project Assistant terminal from outside. |
| `hc_keys` | `hc keys` into a known factory worker, Assistant, or ask terminal (answering permission prompts or dialogs from outside). |
| `hc_kill` | `hc kill` or `hc rm` ending a known factory terminal from outside. |
| `herdr_task_accept` | `herdr-task accept` (or a wrapper script invoking `accept` on a task ref) run from outside. Note: `herdr-task accept` records `accepted_by: "person"` whenever the caller is not a coordinator, so cross-checking outside CLI calls is required to avoid miscounting babysitter accepts as owner UI accepts. |
| `herdr_task_edit` | `herdr-task edit` retargeting models, reviewers, or settings per task from outside. |
| `herdr_task_lifecycle` | Mutating task lifecycle subcommands from outside (`merge`, `done`, `resume`, `switch`, `review-more`, `reopen`, `fix`, `start`, `add`, `rm`, `recap`, `ready`, `reviewed`, `tell-assistant`). Read-only queries (`list`, `show`, `where`, `assistants`, `machines`, `home`, `status`, `asks`) and a gate reviewer's own `herdr-task review --pass|--changes` verdict are excluded. |
| `herdr_task_admin` | Engine/space administration from outside (`config`, `autopilot`, `scheduler`, `tick`, `project`) resolved to a factory project. |
| `herdr_app_do` | `herdr-app do …` UI automation commands (`chat.send`, `tab.new`, `space.new`, `space.select`, `layout.switch`) targeting a factory project or space from an outside orchestrator session. |
| `kill_process` | Direct `kill` / `pkill` / `killall` targeting agent processes on a factory project from an outside orchestrator session. |
| `store_edit` | Direct file edits or shell/Python writes to a factory project's task store under `~/.local/share/shepherdr/tasks/` (or `~/.herdr/tasks/`). |

Commands that simultaneously reference two or more factory projects appear under `cross-project`; commands that resolve to no factory project are dropped. The script reports both **total intervention actions** and **intervention turns** (distinct turns in the outside session that performed at least one factory intervention). As a sanity check against night one of `wukongmind` (2026-11-11 23:30 to 2026-11-12 08:15, where field notes estimated roughly 40 overnight step-ins), the script measures **36 intervention turns** (70 individual commands) during that overnight window.

## How to run

Run the measurement script (read-only, Python 3 standard library only):

```sh
uv run --python 3 scripts/factory/baseline.py
uv run --python 3 scripts/factory/baseline.py --since 2026-11-08 --until 2026-11-16
uv run --python 3 scripts/factory/baseline.py --json
```

If `uv` is not installed, run with plain `python3`:

```sh
python3 scripts/factory/baseline.py --since 2026-11-08 --until 2026-11-16
```

Run the synthetic-fixture unit tests (no live data or home-directory files read):

```sh
uv run --python 3 -m unittest scripts/factory/test_baseline.py
# or without uv:
python3 -m unittest scripts/factory/test_baseline.py
```

## Limits (what the script cannot see)

1. **`agy` worker transcripts.** `agy` stores conversation state in SQLite databases rather than Claude/Codex JSONL files. Worker briefs and task store records for `agy` tasks are captured, and all Assistants and outside sessions ran in Claude Code, but a direct message typed by the owner into an `agy` worker's own terminal while `usage.jsonl` was off or cleared is invisible.
2. **Cleared or rotated `usage.jsonl`.** Shepherdr's usage log (`~/.local/state/shepherdr/service/usage.jsonl` and `usage.1.jsonl`, symlinked from `~/.herdr/shepherdr/`) caps at 4 MB and is wiped when Delete is pressed (`usage.deleted`). Earlier client surface labels (`web`, `phone`, `window`, `voice`, `terminal`) and ephemeral permission cards not persisted under `asks/*.json` cannot be recovered before a delete, though Assistant logs and `<herdr-setting>` notices preserve chat and setting counts across the full date range.
3. **At-the-Mac OS and browser actions.** Clicking macOS TCC / removable-volume consent prompts, completing Apple two-factor sign-in or App Store Connect agreements in a browser, running interactive CLI `/login` flows, and pressing provider quota-reset buttons leave no trace in Shepherdr or agent JSONL logs.
4. **Plain shell commands outside agent sessions.** If the owner runs `hc` or `herdr-task` directly in a raw shell tab rather than inside an agent session, only resulting task-store timestamps (`accepted_at`, `answered_at`) are observable.
5. **Single-label heuristic on free-text turns.** Structured records (`settings`, `ask_card`, `ui_accept`) map deterministically, whereas free-text chat turns are classified into one primary touchpoint from turn structure (preceding question, resulting tool calls) and keyword patterns without storing text. Mixed turns (for example, a status check that also nudges a stuck task) land in `other`.

## Measured baseline (2026-11-08 to 2026-11-16)

Measured at `2026-11-16 16:41 PST` with `uv run --python 3 scripts/factory/baseline.py --since 2026-11-08 --until 2026-11-16` across the three factory projects on this machine (`factory-run` / `FAC`, `wukongmind` / `WUKO`, and `herdr-sheepdog` / `SDF`). Note that `2026-11-16` is an active partial day, so later runs on or after `2026-11-16` may reflect newer `2026-11-16` activity while `--since 2026-11-08 --until 2026-11-15` reproduces the completed 8-day prefix (`2026-11-09` and `2026-11-10` had no factory runs):

### Per day

| Date | Owner touches | `goal` | `rule` | `ask_answer` | `release_approval` | `digest` | `other` | Outside interventions | Intervention turns | Autopilot wakes |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 2026-11-08 | 53 | 2 | 7 | 9 | 0 | 0 | 35 | 127 | 76 | 0 |
| 2026-11-09 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 2026-11-10 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 2026-11-11 | 5 | 0 | 2 | 0 | 0 | 0 | 3 | 5 | 2 | 0 |
| 2026-11-12 | 47 | 1 | 6 | 1 | 1 | 0 | 38 | 93 | 52 | 11 |
| 2026-11-13 | 42 | 0 | 7 | 1 | 0 | 0 | 34 | 74 | 42 | 48 |
| 2026-11-14 | 41 | 2 | 1 | 2 | 2 | 1 | 33 | 22 | 20 | 48 |
| 2026-11-15 | 19 | 1 | 3 | 0 | 2 | 0 | 13 | 11 | 8 | 48 |
| 2026-11-16 | 51 | 5 | 9 | 1 | 2 | 1 | 33 | 18 | 9 | 31 |
| **Total** | **258** | **11** | **35** | **14** | **7** | **2** | **189** | **350** | **209** | **186** |
| **Median / calendar day (9 d)** | **41** | | | | | | | **18** | **9** | |
| **Median / active day (7 d)** | **42** | | | | | | | **22** | **20** | |

### Per project (2026-11-08 to 2026-11-16)

| Project | Prefix | Tasks | Owner touches | Outside interventions |
|---|---|---:|---:|---:|
| `factory-run` | `FAC` | 103 | 67 | 117 |
| `herdr-sheepdog` | `SDF` | 37 | 5 | 10 |
| `wukongmind` | `WUKO` | 131 | 159 | 176 |
| `cross-project` | — | 0 | 27 | 47 |
| **Total** | | **271** | **258** | **350** |

### Breakdown by channel and intervention type

| Owner touch channel | Count | Outside intervention type | Count |
|---|---:|---|---:|
| `assistant_chat` | 119 | `hc_send` | 196 |
| `outside_orchestrator` | 111 | `herdr_task_accept` | 56 |
| `settings` | 10 | `herdr_app_do` | 26 |
| `worker_chat` | 9 | `hc_keys` | 20 |
| `ask_card` | 7 | `herdr_task_lifecycle` | 18 |
| `ui_accept` | 2 | `hc_kill` | 13 |
| | | `herdr_task_admin` | 13 |
| | | `herdr_task_edit` | 6 |
| | | `kill_process` | 1 |
| | | `store_edit` | 1 |
| **Total** | **258** | **Total** | **350** |
