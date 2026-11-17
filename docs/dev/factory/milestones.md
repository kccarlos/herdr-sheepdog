# Milestones and the plan file

Design section 4 splits a project into milestones, tasks and dependencies. SDF-31 builds the first half of that: milestones, a plan file the coordinator writes, `plan apply` to check and add it, and `plan amend` to change it. The critical path, merge order and `owner graph` come in SDF-32. Code: `factory/mods/tasks/bin/herdr_task/plan.py`. Tests: `factory/test/test_plan.py`.

## Commands

| Command | What it does |
|---|---|
| `herdr-task plan apply <file>` | Checks the whole plan and adds its milestones and tasks, plus one acceptance task per milestone. Refused when the folder already has a plan. |
| `herdr-task plan amend <file> --reason "…"` | Changes the plan. The reason is kept in the plan's log, and each amend counts as one replan. |
| `herdr-task plan show` | Lists the milestones, their states and tasks, the replan count and the log. |

`--check` on `apply` and `amend` runs every check and changes nothing. A refused plan lists every problem at once, and the store stays as it was.

## The plan file

The plan file is JSON: `{"milestones": [...]}`. Unknown fields are refused, so a typo can't pass silently, and a plan can't set the state itself.

| Milestone field | Meaning |
|---|---|
| `id` | `M1`, `M2`, … New milestones go after the existing ones, in the order the file lists them. |
| `title`, `goal`, `exit_test` | What the milestone is for, and what proves it done. |
| `gate` | `auto`, `fable` or `owner`. With `owner`, the acceptance task gets `merge="ask me first"`, also when an amend changes the gate before that task starts. Gate asks of their own come with SDF-21. |
| `mandate` | The owner's words as said. An amend can't change it: wider scope is a new milestone. |
| `ui` | `true` when the milestone changes what people see (field note 17). |
| `release` | A tag like `v1.2.0` when the milestone ships one (field note 31). |
| `tasks` | Its tasks, below. |

| Task field | Meaning |
|---|---|
| `key` | A short lowercase name, unique in the plan and kept on the task (`plan_key`). An amend finds tasks by it. |
| `title`, `prompt` | As in `herdr-task add`. The brief is the milestone's line, then the prompt. |
| `lines` | The size estimate, required, at most 500 (`PLAN_MAX_LINES`, field notes 27 and 42). It becomes `estimate.lines`, which `ready`'s size check reads. |
| `after` | Plan keys, or refs of tasks in this folder (`SDF-4`). |
| `kind` | `design` or `doc` (`review.kind`). |
| `set` | Any `--set` key, such as `{"agent": "claude", "merge": "ask me first"}`, except `estimate.lines` and `review.kind`: those go in `lines` and `kind`. |
| `split_of` | In an amend only: the key or ref of the task this one replaces. |

## Checks

- **Shape.** Milestone ids must be unique and look like `M1`, and the required fields must be there. `gate` and `release` must be valid values, task keys unique, and `set` keys the ones `--set` takes.
- **Graph.** Every `after` must name a plan key or a task in this folder. There must be no loop, counting this folder's existing tasks and the edges the engine adds below. A task can't wait for a task of a later milestone.
- **Size.** Every task needs `lines`, at most 500.
- **Design before UI.** A `ui` milestone has a `kind: design` task, and every other task in it comes after one, directly or through other tasks.
- **Release.** A plan can't place a release anywhere but its milestone's acceptance task. That task's brief tells it to ask (`herdr-task ask --kind release`) before it says ready. An ask made after the merge is lost when the task closes (field note 31).

## What the engine adds

- **One acceptance task per milestone**, titled `M1 acceptance: …`. A new agent that wrote none of the milestone's code runs it. It comes after every other task in the milestone and after the previous milestone's acceptance task, so an empty milestone keeps its place. Its `after` is recomputed on every apply and amend while it hasn't started. Plan files can't list it.
- **Milestone order.** A milestone's first tasks (those that wait for nothing inside it) come after the previous milestone's acceptance task. An owner gate on M1 then holds M2.
- **States, derived when read, never stored.** `done` means the acceptance task is done. `validating` means everything its acceptance task waits for is done. `active` means it is the first milestone not done, or one of its tasks has started. Otherwise it is `planned`. An archived done task still counts as done.

## Amend

The file holds the milestones that change, each with all its open tasks, plus new milestones. Milestones the file leaves out stay as they are.

- **A task still to do** can change its title, prompt, lines, kind, set and after.
- **A started task** is listed by its `key` alone. Changing its brief or what it waits for is refused (for the brief, use `herdr-task edit --amend`). It can't move to another milestone.
- **A task that is still open** in a listed milestone and missing from the file is refused. Remove it with `herdr-task rm` first, or split it.
- **`herdr-task edit --after` or `--prompt`** on a plan task still to do is overwritten by the next amend: change plan tasks through the plan.
- **A milestone whose acceptance task has started** takes no new tasks (put them in a new milestone), and its exit test, gate and release stay as they are.

## A split keeps the parent's place

The parts carry `"split_of": "<parent key or ref>"` and are listed where the parent was, in the same milestone (another milestone is refused). Each part waits for what the parent waited for, plus its own `after`. Every task that waited for the parent, the acceptance task included, now waits for all the parts. Then the parent is removed.

A split also changes the waits of a task that has already started, though an amend can't change a started task's waits otherwise. The two rules don't clash. The amend rule stops new, unmet waits being put on running work. A split only swaps a removed task for its parts. A running task's waits no longer gate it, and keeping the removed id would leave it pointing at nothing. The loop check makes the same swap first, so a split can't make two tasks wait on each other.

The parent may be to do, queued or scheduled. It may also be running when its agent is idle, as after a recap decided `split`: the split ends that agent. A parent whose agent is mid-turn is refused. `herdr-task rm`, by contrast, drops the removed task from others' `after`, so a dependent could start too early. Splitting is therefore done through `plan amend`, not `rm`.
