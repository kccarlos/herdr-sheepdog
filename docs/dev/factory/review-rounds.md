# Review rounds

Each review round records its verdict, major and blocker count, findings, commit, reviewer CLI and model. A worker records its own review with `herdr-task reviewed --verdict pass|changes --majors N --report "findings"`; the engine records rounds it starts itself. Older free-text rounds remain in the history but cannot establish a major count.

Once the normal rounds are used, the coordinator can grant `review-more` once per task. From round three, two consecutive rounds that request changes and report majors stop the task for a coordinator recap; `review-more` and recap `--decide continue` are refused. The coordinator can choose a split, a new approach or dropping the task. Merge-only reviews do not count toward this rule. Size, time, token and CI triggers still ask the worker for a recap.

The count starts again only after a recap decision that answered a review stop. A `continue` on a size, time or CI recap does not reset it, and folder tasks (no worktree) stop the same way.

## The retro round

When review does not converge, the engine starts one retro round per task (`herdr_task/retro.py`). This is a fresh Fable session with the reviewer's read-only allow list, prompted from [`factory/templates/retro.md`](../../../factory/templates/retro.md), with every slot filled from the store. Its last message ends in a `<retro>{…}</retro>` JSON tail, which the engine reads from the session's terminal and stores as `task.retro` (one record, never edited). A missing tail, a tail that isn't JSON, or a cause outside the set is a failed round: it does not use up the task's retro, and after three failed rounds the coordinator is told instead.

| Verdict | What the engine does |
|---|---|
| `SHIP` | Records it as the passing round on that head and closes the recap as a continue. |
| `FIX` | Sends the majors and the worker's one change to the worker and closes the recap as a bounded continue (one round, 60 more lines). A Fable round alone checks the fix as a delta. A second FIX verdict (that Fable round asking for changes) is the cap. |
| `REDESIGN`, or the FIX cap | Leaves the recap open with `continue` refused. The coordinator chooses split, new or drop and records it with `plan amend --reason retro:<task>`. |

`confidence: low` is stored and not acted on: the recap stays open for the coordinator, with `continue` still refused. Every outcome wakes the coordinator with `event:retro <task>`. A split of a task that had a retro carries the worker's one change into each child's brief. If such a child draws a retro too, the engine asks the owner once (kind `decision`) instead of starting a second retro.

The eval evidence is in [template-replay.md](template-replay.md#the-retro-template-sdf-49).
