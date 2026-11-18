# What a re-plan resets, keeps, and archives

A **re-plan** changes a started task's approach without replacing the task itself:

- `herdr-task recap <id> --decide new --why "…"` on a task with an open recap; or
- `herdr-task plan amend <file> --reason "…"` targeting an open task (explicit `"retro": true` on the task entry, or a `--reason` containing `retro`).

One table in [`factory/mods/tasks/bin/herdr_task/replan.py`](../../../factory/mods/tasks/bin/herdr_task/replan.py) (`REPLAN_TABLE`) drives every re-plan. Each row marks one piece of task state `reset`, `kept`, or `archived`, with its reason, and [`factory/test/test_replan.py`](../../../factory/test/test_replan.py) checks 1-to-1 parity with this page and runs one test per row.

## State table

| Row | Piece of task state | Disposition | Why |
|---|---|---|---|
| `live_reviewers` | Live reviewers | `reset` | Live gate reviewers (`open_runs`) and worker-started reviewer subagents (`role=reviewer`) judge the pre-replan commit; they move into `retiring_reviewers`, receive `end_reviewer`, and hold replacement gate rounds (`retiring_live`, failing closed on transient `hc` errors) until exited. |
| `helper_terminals` | Helper terminals and worker | `kept` | The task's worker (`agents`), non-reviewer helper terminals (`role=helper`), and terminal launch records (`reviewers`) stay so the worker continues in the same worktree and `lease` / `revive` keep terminal lineage. |
| `review_rounds` | Review rounds and their records | `archived` | Pre-replan entries in `reviews`, `review_runs`, and `review_failures` stay in history marked `before_replan: true` (`review_reset_at` records when, shown by `herdr-task show`), while active round counters and transient round waits (`review`, `review_out`, `review_wait`, `review_quota_wait`, `merge_only_runs`, reviewer-failed `attention`) clear so the new approach starts at round 1. |
| `fable_runs` | Fable runs | `archived` | Pre-replan Fable entries in `review_runs` and `reviews` stay in history marked `before_replan: true`; `fable_runs` and `fable_said` ignore archived entries so Fable's `FABLE_CHANGES` cap, `gate_need`, and `accept_bar` start fresh. |
| `review_more` | The review-more grant | `reset` | Clears `review_more_granted`, `review_rounds_added`, and `review_auto_grants` so the fresh round budget starts at its base limit with `review-more` and `AUTO_GRANTS` available again. |
| `consecutive_majors` | The consecutive-majors count | `reset` | `review_nonconverging` counts only non-Fable rounds of the current approach after the re-plan (`before_replan` and rounds at or before any `"new"` recap excluded), whereas `recap --decide continue` keeps the streak intact. |
| `open_recap` | Open recap | `reset` | Clears `recap` and `"Recap required…"` `attention` so `reviewed` and `ready` are no longer blocked. |
| `recaps` | Decided recaps log | `kept` | Past recap decisions stay in `recaps`, and a new `{"decision": "new", "at": …}` entry becomes `recaps[-1]`, advancing the CI, time, and token baselines and superseding any prior `continue` or `continue --small` bound. |
| `verdict_told` | `verdict_told` | `reset` | `verdict_told` stores delivered round numbers (`1, 2, …`); clearing it ensures the new round 1 verdict is sent to the worker. |
| `merge_queued` | `merge_queued` and a PR queued on GitHub | `reset` | When the task has an open GitHub PR in the merge queue (`isInMergeQueue`) or with auto-merge enabled (`autoMergeRequest`), the engine dequeues it (`dequeuePullRequest`) and disables auto-merge (`gh pr merge --disable-auto`) before clearing `merge_queued` and `requeued`; if the PR already merged or GitHub cannot dequeue it, the re-plan is refused before mutating task state. |
| `back_auto` | `back.auto` and `settle_back` | `reset` | Calls `back_to_work` with `auto=False` and clears `settled_at`, preventing `tick.settle_back()` from returning a re-planned task to `Ready`. |
| `ready_and_auto_accept` | Ready and auto-accept state and passing rounds | `reset` | Moves `ready` back to `running`, clears `ready_at`, `ready_head`, `ready_turns`, `stray_since`, `auto_merge`, `merge_requested`, `accepted_by`, `accepted_at`, `auto_accepted`, `accept_wait`, `owner_approved`, `nothing_to_merge`, `head_moved_at`, `push_held`, and transient PR Ready waits (`pr.wait`, `pr.no_ci`, `pr.drift_hold`); archived pre-replan passes no longer count in `gate_passes`, `gate_passes_for`, `reviewed_for_head`, `accept_bar`, or `drift`. |
| `held_news` | Held news | `reset` | Clears `review_followup` and `pr.news` on the task, drops `.held.json` items for retiring reviewers, drops stale pre-replan `.held.json` messages for the task's workers (keeping only `ask` `event="answer"` and `edit --amend` brief updates), and drops pre-replan task status lines in `.held.json` and `.news.json`. |
| `task_context` | Task brief, worktree, PR identity, asks, and CI log | `kept` | Keeps `title`, `prompt`, `amendments`, `worktree`, `branch`, `base`, `pr` (`number`, `url`, `repo`), open `asks`, `simulator`, `device_wait`, `tokens`, and `ci_runs` (`ci_since` counts past `recaps[-1]["at"]`). |

## Who may trigger a re-plan

Only the **coordinator** and the **owner** may trigger a re-plan (`recap --decide` and `plan amend`). A task's own worker, a reviewer, or a helper terminal may never decide a recap or amend the plan (`classify_replan_caller` in `replan.py`):

1. **Worker environment markers (`SHEEPDOG_TASK`, `HERDR_BROWSER_TASK`).** `launch()` sets both on every task terminal. If either is set in `os.environ`, the caller is refused even if `HC_TERMINAL_ID` was unset in a subshell.
2. **Terminal lookup (`HC_TERMINAL_ID`).** When `HC_TERMINAL_ID` is set, the engine reads `hc get $HC_TERMINAL_ID --json` and **fails closed** if `hc` errors, times out, or returns invalid JSON, so a transient `hc` failure never degrades to an unlabelled caller.
3. **Labels and task lineage.** Any terminal carrying `sheepdog.task` / `herdr.task`, carrying `sheepdog.role` / `herdr.role` in `("worker", "reviewer", "helper", "checks", "ask")`, listed in any task's `agents` or `reviewers`, or parented to one of those terminals is refused.
4. **Coordinator vs owner.** A terminal claiming coordinator role (`role=coordinator` or an assistant template) is verified against the project's `coordinator` lease in `s.data["coordinator"]` ([`coordinator.md`](coordinator.md)) whenever a lease exists (`address`, plus `created_ms` when present), refusing spoofed role labels. A caller outside `hc` (no `HC_TERMINAL_ID` and no task env vars) or in an unlabelled owner shell tab inside `hc` is accepted as the **owner**.

## Target selection in `plan amend`

`plan amend` re-plans only the targeted open task(s), never unrelated tasks (`select_retro_plan_targets`):

1. If any task entry in the plan file sets `"retro": true` (valid only on an existing non-`done` task), those tasks are the targets.
2. Otherwise, when `--reason` matches `\bretro`, open plan tasks named by ref (`SDF-12`) or `plan_key` (`api`, matched with `(?<![\w-])…(?![\w-])` so `api-v2` never matches `api`) are selected.
3. If `--reason` names a non-open task ref or a `done` / removed `plan_key`, or if the amend itself splits or adds tasks without naming an open target, no fallback runs.
4. Otherwise (plain prose such as `"retro: use copy-on-write"` with no structural task additions or splits), the single open plan task with `recap` or `review_out` is selected; if multiple open tasks qualify, `plan amend` refuses until one is named or marked `"retro": true`.

## What `plan amend --check` may and may not touch

`herdr-task plan amend <file> --check --reason "…"` validates everything and changes nothing:

- **May read:** the plan file, the task store opened read-only (`write=False`, shared lock), caller identity (`HC_TERMINAL_ID` / env vars), and live agent status on split parents (`busy`). It runs `Plan.write` and `loop_in` against an in-memory `deepcopy` of `s.data`, then restores `s.data` with `s.dirty = False`.
- **May never touch:** the task store on disk (nor `.sheepdog/project_id`), git (`set_base`, remotes, branches, worktrees), GitHub (`dequeuePullRequest`, `gh pr merge --disable-auto`), `hc` terminals or simulators (`end_agent`, `end_reviewer`, `release_task` in `Plan.finish()`), or `.held.json` and `.news.json`.
