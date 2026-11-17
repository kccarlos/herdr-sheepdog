# The coordinator lease

Each project (a repo folder) has exactly one coordinator: the Opus agent that plans, writes briefs and answers workers. This page says how the engine knows which terminal that is, and how it keeps one running with nobody at the Mac. It implements the "Watchdog, recovery, identity" rules of the sheepdog-factory design, section 5, and field notes 53 to 55. The code is `factory/mods/tasks/bin/herdr_task/lease.py`; the tests are `factory/test/test_lease.py`.

## Identity: the lease

The project's task list (`<data_dir>/tasks/<slug>.json`) holds a `coordinator` record: the terminal's address and `created_ms`, when it took the lease, the template hash, and its last session id and log. The lease holder is the coordinator. `launch.coordinator_in()` returns it, and news, Autopilot wakes, GitHub news, asks and workers' parents all go through that function. Three other places read the lease directly. Revive brings back only lease holders, checked by `created_ms` too (and task agents). Settings notices go only to lease holders. `herdr-task assistants` lists the lease holders, with the agent's own cwd when it differs from the project folder.

- Only the engine writes the lease, when it starts a coordinator: `herdr-task project start`, the reconcile pass and the watchdog's restart.
- A second claimant is refused while the holder lives. A restart hands the lease on explicitly.
- A terminal at the same address with another `created_ms` is not the holder.
- A terminal that only looks like a coordinator is never used, ended or relabelled. That covers a tab named "Assistant", a hand-started `claude` and a terminal someone gave the role labels. The tick reports each one under `not_coordinator`, with its own cwd.

When hc fails to answer (an error other than "no such terminal"), nothing is decided that tick: no holder is replaced and no handoff record is dropped.

There is no adoption by labels. A coordinator started before the lease existed is not adopted: the engine starts its own.

## Starting one

`lease.start()` starts a coordinator with `hc run --keep --no-parent --cwd <repo>`. The cwd always comes from the project record, never from the old terminal or the focused pane. It uses the `assistant` template's command (no auto-update, a clean environment) and its words, then the project's settings block, then a handoff when there is one.

The handoff names the previous coordinator's session log and its saved screen (`<data_dir>/tasks/handoff/`). It lists the workers still running and points to `herdr-task list`.

## Every tick: reconcile

`reconcile_pass` runs right after `revive_pass` and before the news and Autopilot passes. It looks at every repo project with an open task (running, ready, queued, to do or scheduled):

1. **The holder's terminal is live:** nothing to do. A kept terminal that hc gave a shell counts too while revive types its agent back, but only for 15 minutes. A holder that is still a bare shell after that (its resume failed, or its agent exited again) is replaced like a gone one, and the shell is ended. The holder's session and log are copied onto the lease for a later handoff.
2. **No live holder:** a new coordinator starts with a handoff and takes the lease. No message from the owner is needed. First a timed probe of the repo's disk runs. If the disk is stalled, nothing starts (note 54 began with a disk stall).
3. **Re-parenting:** workers whose parent was the old coordinator, or is gone, get the new one as their parent in their task's agent record. hc cannot change a terminal's parent. Messages held for the old coordinator move to the new one. Taking the lease from an earlier holder adds a handoff record to the lease's `handoffs` list: that terminal, its `created_ms`, when the record was written, and its agent's pid (kept only while the lease still named that terminal). Records not finished yet are kept, oldest first. `finish_handoff` carries out each record, then deletes it. Workers and held news move to the new holder. If the earlier terminal itself still runs, it ends. Its process group ends whenever its pid is known, also when a tick stopped right after the kill. That happens only if `ps` shows the process started before the record was written, so a recycled pid is never signalled. Every step can run twice. A later terminal at its address is left alone, news and all. A restart, a reconcile start and a tick cut short in between all finish through this one function.
4. **Limits:** after a start, no new start for 10 minutes. A start blocks the tick until the new coordinator takes its first message (hc's own wait, up to 60 s). After 3 starts in an hour, the tick stops starting and reports `gave-up`.

The home folder and other plain folders are not projects, so no coordinator starts for them.

## The watchdog

`watchdog_pass` looks after lease holders only. Besides the earlier rules (stale Needs you, API errors, an old template, a stalled disk), it treats a coordinator as stuck in two cases:

| Condition | Action |
|---|---|
| "Working" with no child process and no output for 20 minutes (note 53) | `hc interrupt` once. Still working 90 s later with nothing new in its log (a repainted screen does not count): restart. Stuck again within 2 hours: alert |
| "Working" with an unchanged screen for 30 minutes | Restart |

These terms decide the first row:

- **Output** means the session log's modification time when there is a log, else the screen. A screen whose only change is the working line's spinner, timer or token count counts as unchanged.
- **A child** is a helper terminal at work, a command in its terminal, or a process under the agent that was not there while it was idle. An MCP server that always runs is no child. Without an idle baseline, any process under it counts as a child, which errs on the safe side.

A restart (`lease.restart`) is engine code that replaced `mods/coordinator/restart.sh`, so it works headless. It starts the new coordinator in the repo folder with a handoff, hands on the lease, re-parents the workers, then ends the old one and its process group.
