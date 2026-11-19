# Engine v2: build plan and cutover

2026-11-18. How to build the engine in [design.md](design.md) and [prompts.md](prompts.md), and how to replace the old one. This version is an agile split, asked for by the owner: the earlier plan (kept as [notes/plan-before-agile.md](notes/plan-before-agile.md)) built layer by layer, started a real agent at PR 13 and merged a real PR at PR 22. Sizes are lines of Go without tests and are estimates. Rule ids (N.., L.., J.., R.., A..) are design.md's. `KB-…` ids are rows of [knowledge-base.md](knowledge-base.md).

## 1. How this plan works

| Rule | Why |
|---|---|
| **Slice vertically.** Every slice ends in something that runs end to end on a scratch repo, which the owner can watch. Each later slice adds one capability a person can see | A plan built by layer shows nothing real for 22 PRs, and every size estimate in it was wrong four times |
| **Slice 1 is a walking skeleton:** one task from plan to a merged PR, with only the rules, records and adapter code that path needs. Everything else is hard-coded, stubbed or done by hand, and listed | The earliest possible contact with real agents, real git and real GitHub |
| **A review point after every slice.** Compare real lines and review rounds with the estimate; write what was learned; re-plan the slices that follow | "Reading does not converge; running does" (design.md 17.5) |
| **Only the next slice is planned in PRs.** Later slices are outlines: goal, demo, what they bring in, done-when, a size guess | Detail planned before the skeleton runs is detail planned on wrong numbers |
| **The design is the target, not the schedule.** design.md and prompts.md say what the engine is when finished. A slice names the rule rows and table parts it brings in. A row the design has and no slice has reached yet does not exist in code | The tables stay one source of truth; the skeleton does not fork them |
| Every PR is at most 300 lines of code. A PR that passes 450 stops and asks | The engine's own size rule |
| Tables before code; a history before its rows. Each slice writes its histories (pass-by-pass walks, as in [notes/table-audit.md](notes/table-audit.md)) as failing tests first | The tables were read three times and each reading found new faults |
| Every behaviour test names a `KB-…` id | The knowledge base replaces the old tests |
| A spike runs only when the next slice needs its answer | Section 5 |
| No flag that chooses between two engines. The new engine runs in the daemon from slice 1, for projects started with its own command on scratch repos; the old glue stays untouched and serves nothing new. From slice 6 the supervisor lists asks from both; slice 8 deletes the old one | Migrate callers, then delete; the overlap is two slices long and is named |
| A gap found in review is sorted first (this slice, a later slice, not carried), then built or not | Fixes for review findings are where designs grow most |

**Who builds it.** A hand-run orchestrator with Codex or Carbon workers and an outside review per PR. The new engine does not build its own code until it has run a week on other work with no intervention.

## 2. The slices at a glance

| # | Slice | What the owner sees work at the end | Size guess | Spikes first |
|---|---|---|---|---|
| 1 | Walking skeleton | A goal typed once; a coordinator plans one task; a worker builds it; a reviewer passes it; the PR merges on GitHub; nobody types in a terminal | 2,300 in 8 PRs | S0.3, S0.6 |
| 2 | The review loop | A task that fails review is fixed and reviewed again; two passing rounds are needed; a worker can reject a finding; a task that will not converge, or grows past its size, stops, the coordinator is woken and decides | 1,200 | — |
| 3 | Quality up front | A plan with no scope fields or no proof for an outcome is refused; a worker's "ready" runs its proofs and is refused when one fails; the real templates of prompts.md | 700 | — |
| 4 | Several tasks, real GitHub | Three tasks with a dependency run with a slot limit, and `sheepdog factory status` shows what each waits on; a PR behind its base is updated; a flaky check is re-run once; a red base holds merges; a PR merged or closed by hand is handled | 1,400 | S0.5 |
| 5a | Leave it alone: agents | Kill a worker, the reviewer, the coordinator or the daemon: the factory carries on. An agent stuck at a screen or a dialog is restarted | 1,300 | S0.2, S0.8 |
| 5b | Leave it alone: the machine | The daemon runs as a service; an empty quota pool stops the factory and the reset starts it; a second CLI reviews; stray terminals and leftovers are reported | 1,000 | S0.4 |
| 6 | The owner's asks | A worker's question goes to the coordinator; a decision only the owner can make appears as a card on the phone and its answer unblocks the task; stop and start by the owner | 800 | S0.10 |
| 7 | The override | "Merge these now": one card listing two open PRs; a yes merges them without waiting for CI or review | 300 | — |
| 8 | Doctor and the switch | `sheepdog factory start` checks the computer and the repo and refuses what would dead-end; the old Python glue is gone | 500 | — |
| | **Version 1 complete** | The exit test of slice 8 | **8,000 to 10,000** | |
| 9 | The factory page on the web | One page beside setup, voice and debug: tasks by what they wait on, the cards with their answers, the log | 650 | S0.9 |
| 10 | Evidence by the engine | The engine runs each task's proofs again in a clean checkout; a failed proof costs no review round | 400 | S0.7 |
| 11 | Plan review | A plan with an "optional" task comes back with "delete" before any code is written | 250 | — |
| 12 | The retro round | A task with majors in rounds 2 and 3 gets one retro, then a bounded fix or a recap | 300 | — |
| 13 | Rotation and the journal | A fresh coordinator every few hours continues from a written entry | 250 | — |
| 14 | Settings by voice and card | "Review rounds 1 for this project": read back, confirmed, in force at the next pass | 350 | — |
| later | Alerts to push and voice and the digest; milestones and the verify task; agy; the design loop; cheap sync; secrets; the phone and iPad boards | | | S0.1 for agy |

Every size here is a guess. The checker of this plan redid slice 1 on design.md section 13's bases and found it a third larger than first written; the later guesses were raised by about that much and are not better founded. Review point 1 replaces them with a measured cost per rule row.

The order after slice 1 is by value and risk: the review loop is the engine's reason to exist; quality up front shapes every later task's brief and is cheap; real GitHub and recovery are where the old engine's field failures came from; the owner's asks and the override are what make it hands-off. The order is re-decided at each review point.

## 3. Slice 1: the walking skeleton

**Goal.** One task goes from a goal to a merged PR on a scratch GitHub repo with CI, with real agents, and nobody types in a terminal after the start command.

**Demo.** The owner runs `sheepdog factory start <folder> --goal "<one sentence>"` in a terminal and watches, in herdr and on GitHub: a coordinator appears and applies a one-task plan; a worker appears in its own worktree, commits and reports ready; a PR opens; a reviewer appears and gives its verdict; CI runs; the PR merges; `sheepdog factory log` shows each step with the rule behind it.

**What it brings in from the design.**

| Part | In the skeleton | Left for |
|---|---|---|
| Records (section 3) | Task; attempt with its base commit; `ready`; `round`; `accept`; `acts[]` (the ledger that makes "once" and "already asked" work); the run's terminal identity; a settings record holding the defaults of the numbers these rows use; `state.json` and `log.jsonl` | every other record |
| Task rules | N1, N2, N11, N22, N23, N26, N29, N36, N39, N41, N42, N43 and the first-match loop | all other rows |
| A stand-in row | "The run this row needs has ended": stop the task and say so in the log. It takes the place of section 4.8 and of N5 until slice 5a | 4.8, N5 |
| Starting a run | Only the first two lines of 4.8: a start is pending (wait), and none yet (start fresh). A reviewer is stopped when its verdict is in | the rest of 4.8 |
| Ladder | L7 (no round: review), L9 (a pass: passed), L13 (changes: fix), with `review_rounds` set to 1 | L8 and two passing rounds: slice 2 |
| Acceptance table (7.2) | `plan apply` (shape only), `ready` (clean tree, a commit ahead of `H`), `verdict` (the run's one-time code, round, commit); `start` and `stop` from a terminal with no run's token | everything else |
| Runs | Start, send, stop over herdr-core; a run is idle, working or gone, read from herdr-core's status each pass. No run table yet | the run table: slice 5a |
| Adapter | Claude Code only, for all three roles, with a different model for the reviewer. Model, effort and permission mode stated on every start; its auto-updater switched off (KB-LCH-11) | Codex as reviewer, `Resolve`, candidates: slice 5b |
| git and gh | A worktree from the remote base; push; open a PR; a snapshot with state, head, mergeable, behind, "GitHub answered", and a simple checks rollup (red, pending, green, none); squash merge pinned to the head; delete the branch after the PR reads merged | the rest of sections 7.3 and 9.4: slice 4 |
| The loop | One goroutine in the daemon; every 15 s it reads herdr-core, the worktree tip and the PR, finds each task's row and calls the executors. One input is one commit of the state file | keys, back-off, giving up, events: slice 5a |
| Reaching the daemon | A local POST route for reports and a small client for `sheepdog factory …` (today the CLI has only a GET helper) | — |
| Test hygiene (section 16) | Every path comes from one function that tests override; one guard test fails if a test touches the real home or a running daemon (KB-TST-1, KB-TST-3) | — |
| Prompts | Three short stand-in templates: coordinator (plan one task, then wait), worker (build, commit, report ready), reviewer (review, end with the tag) | prompts.md's templates: slice 3 |

**Hard-coded, stubbed or by hand in slice 1** (each is a row of a later slice, not a decision):

- The daemon is run in the foreground from a terminal, so git and gh are called directly. Going through herdr-core for a daemon under launchd is slice 5b.
- The scratch repo's folder is trusted for Claude Code by hand once, and `gh` is signed in by hand. No doctor.
- One task at a time. No dependencies, slots, asks, alerts, size cap or proofs.
- A run that dies is not restarted. The stand-in row stops the task and the log says which run ended. The owner starts again.
- The coordinator is started once by the start command and is not woken afterwards. Waking it is slice 2.
- One passing review round. The owner's rule is two; slice 2 sets it.

**PRs.** Eight, each at most 300 lines. A real agent runs at PR 1.3; the real merge is at PR 1.8.

| # | PR | Lines | Done when (and what can be watched) |
|---|---|---|---|
| 1.1 | `flow`: the first-match loop; the skeleton's record types, `acts[]` and the settings record; the store; the path function and the guard test | 300 | State survives a save and a load; the guard test fails when a test is pointed at the real home |
| 1.2 | `flow`: rows N1, N2, N11, N22, N23, N26, N29, N36, N39, N41, N42, N43, the stand-in row, the two start lines, and ladder rows L7, L9, L13. **The happy-path history is written first, as a failing test** | 300 | The history passes: the expected row at every pass, nothing asked twice, and "no rule matched" is never reached |
| 1.3 | The live loop in the daemon; start, send and stop over the existing `internal/hc` client; the Claude Code argv for the three roles; `sheepdog factory start` and `stop` | 300 | **A real coordinator appears in herdr with its model and mode named,** takes its first prompt, and is stopped by `sheepdog factory stop` leaving no process (KB-LCH-2, KB-LCH-4, KB-LCH-11) |
| 1.4 | The local POST route, the CLI's client and run tokens; `plan apply` (shape only) | 300 | **The coordinator applies a one-task plan** and the log shows the task; a report with another run's token is refused (KB-REV-5) |
| 1.5 | The worktree from the remote base; the worker's start; `report ready` | 250 | **A worker starts in its own worktree, commits and reports ready**; a ready with uncommitted changes is refused (design.md 9.4; KB-MRG-78 for the worktree rules) |
| 1.6 | `world`: push; open the PR; the snapshot with its simple checks rollup | 300 | **A PR opens for the task's branch** with a Conventional Commit title; the log shows the task waiting for a review; with no checks at all nothing would merge (KB-MRG-15) |
| 1.7 | The reviewer's start and stop; the report tag reader with its one-time code; `verdict`; N22 tells the worker the findings | 300 | **A real reviewer on another model reviews the pushed commit and its tag is read from the terminal;** "changes" sends the worker back and a new commit is reviewed again as the next round; text without the code is not a verdict (KB-REV-53, KB-REV-5) |
| 1.8 | Merge, pinned to the head (N39, N41, N42); done on a snapshot that says merged; branch and worktree cleanup; `sheepdog factory log` | 250 | **The PR is merged by the engine and the task is done.** The merge call's own result is not trusted; a merge is asked once, not on every pass; a dirty worktree is not removed (KB-MRG-9, KB-MRG-10, KB-MRG-78) |

**Size.** About 2,300 lines in 8 PRs. The first draft of this slice said 1,700 in 6; the checker redid it on design.md section 13's bases (13 rows at about 20 lines, the types, the store, the loop, the route) and got 2,200 to 2,600. It is the first estimate in this project that will be tested within days.

**Tests slice 1 adds.** The happy-path history; one case per row brought in; the checks-rollup cases for red, pending, green and none; golden argv for the three roles; one test on a temporary repo for the worktree and push; a fake `gh` for the PR path; the guard test. And the demo itself, once, by hand, on the scratch repo.

**Review point 1** (before slice 2 is planned in PRs). Write into section 7:

| Question | Feeds |
|---|---|
| Real lines per PR against the estimate; how many lines one rule row cost | Every size guess in section 2 |
| Review rounds each PR took, and what the reviewers found | Whether PRs should be smaller |
| What the demo showed that the tables did not expect (a screen, a timing, a GitHub answer) | New rows for slices 4, 5a and 5b; fixes to design.md |
| Did reading the reviewer's tag from the terminal work every time? | Slice 5b's second adapter; the fallback of design.md 9.3 |
| How long one task took end to end, and what it cost | Whether evidence (slice 10) is affordable |
| Which stub hurt most | The order of slices 2 to 5 |

**Every later review point** asks the first three questions again, plus: did this slice's demo run without a hand on a terminal; which of its "leaves out" items turned out to be needed now; and one question of its own, named in its outline.

## 4. The later slices (outlines)

Each is planned in PRs only at the review point before it. "Done when" lines are the behaviours the slice must show; each becomes a test that names its row.

### Slice 2: the review loop

- **Goal.** Review works as the owner decided: two passing rounds, real fix rounds, and a stop when it will not converge, with the coordinator woken to decide.
- **Demo.** A task whose first review asks for changes is fixed and passes twice. A worker rejects a finding and the next reviewer is given the rejection. A task forced to fail three rounds stops; the coordinator is woken with the allowed choices and splits it. A task that grows past twice its estimate stops.
- **Brings in.** L8 and L10 to L13 without the retro row; `ready --reject` and the `reject` record; N12 (size cap) with `estimate_lines`; N25, N6, N8 and the coordinator's commands of 4.9 (`recap decide`, `plan amend`, `send-back`, `close`, `park`, `unpark`, `drop`) with `recap-written` and `ready --nothing-to-change`; waking the coordinator (J12) with the facts block of section 10; `covers` and merge-only deltas (N20); the review diff of 9.4; the settings these rows use. A split's children pass the same shape-only plan check as slice 1; the full plan checks arrive in slice 3.
- **Done when.** A pass needs `review_rounds` passing rounds on the final head (design D12). From round 3, two rounds in a row with majors stop the task for a recap (KB-CAP-1, KB-CAP-9). The size check cannot be turned off by a missing estimate (KB-CAP-18). The review diff is the task's own changes from the merge base computed now (KB-REV-13, KB-REV-12). A rejected finding comes back only with a run that proves it (KB-REV-62). A task sent back by the coordinator stays with the worker until it says ready again (KB-TSK-3, KB-RUN-6). The histories "review never converges" and "size cap" of the audit pass.
- **Its review question.** Did the coordinator, woken with facts and allowed choices, decide well without being told how?
- **Size guess.** 1,200. **Leaves out.** The retro (slice 12); the owner's ask for a split child (N24: slice 6); the CI and time limits N13 and N14 (slices 4 and 5a).

### Slice 3: quality up front

- **Goal.** What the owner used to prompt for by hand is required by the engine: scope, proofs, self-review.
- **Demo.** A plan missing "for whom" or "not doing", or an outcome with no proof, is refused with its reason. A worker's `ready` runs its proof commands and is refused when one fails. The coordinator, worker and reviewer run on the templates of prompts.md section 5.
- **Brings in.** The plan checks of 11.7 (scope fields, outcomes with `proved_by`, the estimate and its limit, the title); `ready` with proofs run by the report client, `touched`, `skipped`; the verdict's `self_check_misses`, `class`, `deletable`, `not_run`; the three templates with replay evidence (prompts.md section 7); the first-look pass rate in the log.
- **Done when.** A plan with any problem is refused whole (KB-PLN-4). Every start states model and effort and never falls back to a CLI default (KB-LCH-4). A reviewer's finding needs a run behind it and the reviewer is read-only (KB-REV-63). Each template is 40 lines or fewer and every command it names exists.
- **Its review question.** What share of tasks passed review on the first look, and what did the reviewers count as self-check misses?
- **Size guess.** 900. **Leaves out.** The plan reviewer (slice 11); the engine's own run of the proofs (slice 10).

### Slice 4: several tasks, real GitHub

- **Goal.** A real plan of several tasks lands on a real repo that other things also touch, and the owner can see where each task stands.
- **Demo.** Three tasks with one dependency run with `max_running` 2; `sheepdog factory status` lists each with what it waits on and why. A PR that falls behind is brought up to date with a merge commit and merges when its new checks pass. A check that fails once is re-run and passes. With the base red, merges wait. A PR merged by hand ends its task; a PR closed by hand wakes the coordinator.
- **Brings in.** N9, N10 and scheduling (4.10); the landing rows N32 to N38 and N40; N13 (too many CI failures); the full checks rollup, PR adoption and `merge_only` tables; N4, N7; the executor rules of 9.4; the base observer; the stage and "why" view of 4.11 as a terminal command.
- **Done when.** No queued task starts at the running cap (KB-TSK-16). A PR behind its base is updated once and merged only when the new head's checks pass, with no review round spent (KB-MRG-43). A red base holds updates and merges (KB-MRG-47). A network failure is never remembered as "not a GitHub repo" (KB-GIT-20). A wake with no new commit does not undo a pass (KB-MRG-6). The same failing check on the same commit is reported once (KB-GIT-14). The engine never adopts a fork's PR or one at another head (KB-GIT-2).
- **Its review question.** Which GitHub answers did the real repo give that the rollup and the landing rows did not expect?
- **Size guess.** 1,400. **Spike first.** S0.5 (the repo's branch protection and merge queue). **Leaves out.** Merge queue handling unless S0.5 finds one; cheap sync; the owner's gates (N30, N31: slice 6).

### Slice 5a: leave it alone, agents

- **Goal.** An agent that dies, hangs or gets stuck does not stop the factory.
- **Demo.** With a plan running, the owner kills a worker, then the reviewer, then the coordinator, then the daemon: each comes back and the work finishes. An agent left at a first-run screen or a dialog is restarted.
- **Brings in.** The run table (section 5) with its matrix, replacing slice 1's status reading; "making sure a run is alive" (4.8), replacing the stand-in row; keys, back-off and N5 with `retry` (section 8); N3; N14 (the time limit); the project rules for the coordinator (J1 to J10, J12 to J14; rotation is slice 13); the invariants I1 to I5; `say`; the clock rule.
- **Done when.** A project has exactly one coordinator, and a replacement starts in the repo folder (KB-COO-1, KB-COO-4). A restart ends the replaced agent's whole process group (KB-COO-12). A worker that died mid-turn is seen as gone (KB-RUN-8). A start stuck at a trust or sign-in screen is told apart from a blank one (KB-RUN-28, KB-RUN-43). A restart of the daemon after every step of every history gives the same end. A failing action backs off and ends at N5, never in a loop.
- **Its review question.** How many restarts did a night's run need, and which of them were the engine's own doing?
- **Size guess.** 1,300. **Spikes first.** S0.2 (session ids and resume), S0.8 (what herdr-core can report).

### Slice 5b: leave it alone, the machine

- **Goal.** The factory runs as a service and copes with quota, a second CLI and its own leftovers.
- **Demo.** The daemon runs under launchd and still reads the project folder. A pool at its floor stops new starts and the reset starts them again. The reviewer runs on Codex. A stray terminal and a leftover worktree are reported.
- **Brings in.** git and gh through herdr-core (design 2.4); pools and the sign-in check; the alerts table (raise and clear); `Resolve` and candidate lists; the Codex adapter; the sweep of 11.3.
- **Done when.** git runs in the user's session and every call has a time limit (KB-ENV-7, KB-GIT-22). A task never starts on an empty pool and waits for the reset (KB-QTA-2, KB-QTA-3). A stopping alert clears by itself when its cause is gone. A done task's leftover worktree is listed, not silently kept (KB-WKT-5).
- **Its review question.** Did anything on the machine (a consent prompt, an update, a full disk) stop the run in a way no alert named?
- **Size guess.** 1,000. **Spike first.** S0.4 (the daemon under launchd).

### Slice 6: the owner's asks

- **Goal.** The owner hears only what needs him, and his answer counts only when it is his.
- **Demo.** A worker's question is answered by the coordinator. A release question appears as a card on the phone; the tap unblocks the task. A task with an owner gate waits for his yes. The owner says "stop the factory" and later starts it; the plan is kept.
- **Brings in.** The ask table (7.1) without delegation (A1 is slice 14) and its acceptance lines; the `Engine` interface; `AnswerProof` moved out of `contract.go`; the supervisor listing the new engine's asks beside the old client's (the old client is removed in slice 8); the gates N24, N30, N31; owner stop and start; the quiet rule's labels.
- **Done when.** An owner's answer counts only from a paired device or live voice; a local answer with no session ticket to a gated ask is refused (KB-ASK-19; design 7.1). An open question outlives the worker that asked it (KB-ASK-10). A gate ask whose commit is gone is cancelled.
- **Its review question.** How many cards did the owner get in a day, and which could a rule have answered?
- **Size guess.** 900. **Spike first.** S0.10 (what the supervisor's existing card can show). **Leaves out.** Delegation rules and rule read-back (slice 14); push and voice for alerts; `touch`.

### Slice 7: the override

- **Goal.** The owner's "merge these now".
- **Demo.** Two open PRs, one with CI still running. The coordinator raises the card; the owner taps yes; both merge. Then: a card made at one commit does not merge a newer commit; checks worse than the card showed are not merged and the owner is told.
- **Brings in.** N15 to N19b, L1, the `override` record, the `override` ask kind, `sheepdog factory ask --kind override` and `sheepdog factory override`.
- **Done when.** The histories of review-2 section 2 and of notes/verify-review2.md M1 pass. A delegation rule cannot answer an override. A worker's terminal cannot raise one.
- **Its review question.** How often was the override used in a week? Often means the queue itself needs work (a merge queue, cheap sync).
- **Size guess.** 400.

### Slice 8: doctor and the switch

- **Goal.** One command starts a factory safely, and the old engine's glue leaves the daemon.
- **Demo.** `sheepdog factory start` on a repo with no PR workflow is refused with its fix line. On a good repo it runs the whole exit test.
- **Brings in.** The doctor rework (start checks: a signed-in CLI, gh signed in, a PR workflow, no required approving review, branch protection on the base); the deletion of `client.go`, `engine.go`, `python.go`, `tick.go`, `factory_embed.go` and the rest of section 6's C3.
- **Done when (the exit test of version 1).** On a scratch GitHub repo with CI, with real CLIs: one goal, three tasks with a dependency. During the run: one forced review change; one reviewer that exits with no report; one owner ask answered from the phone; one override of two independent open PRs; the base turned red once; one worker ended by a usage limit; one daemon restart mid-task; one worker killed by hand; one PR merged by hand. All tasks end done with nobody typing in a terminal, and the log names the rule behind every step.
- **Size guess.** 600, net negative in the daemon.

**What "version 1" covers.** knowledge-base.md marks 426 of its 599 rows as wanted in the first version. That marking was made before the owner moved two things later: the retro round (its 13 rows are met at slice 12) and the engine's own run of proofs (4 rows, slice 10). About 20 rows about what the phone shows are met in part by `sheepdog factory status` in slice 4 and in full by the web page in slice 9. Everything else among the 426 is named by a slice from 1 to 8 above. The checker of this plan matched rows to slices by script and by hand for the misses, not by reading all 426.

### Slices 9 to 14, and later

| Slice | Brings in | Done when, in one line | Leaves out |
|---|---|---|---|
| 9 The factory page on the web | W1: endpoints for the task list, open asks and the log from the `Engine` interface. W2: one page in the daemon's web. Browser enrolment if spike S0.9 shows it is missing | The page shows each task with what it waits on and why; a card answered there counts only when the browser holds a session ticket (design D15) | The phone and iPad boards |
| 10 Evidence by the engine | N21, N27, the check runner, a judgement per outcome in the verdict (design 11.6) | A task whose own check fails goes back to the worker with no review round spent; the accept bar needs passing evidence on the final head | The milestone verify task |
| 11 Plan review | The plan reviewer run and template, `plan_review_rounds`, the milestone design note (prompts.md 3.2, 3.3) | A plan with an "optional" backlog task comes back with "delete" | — |
| 12 The retro round | L2 to L6, L11, N28, the retro template | The SDF-16 history draws one retro at round 3; the second FIX ends in a recap with no `continue` (KB-RET-1, KB-RET-4) | — |
| 13 Rotation and the journal | J11; design 11.1 | A fresh coordinator continues from the entry; an entry exists with facts even when the coordinator does not answer | — |
| 14 Settings by voice and card | The `rule` ask, read-back, delegation (A1) | A setting changes only when the owner answers its card; `sheepdog factory set` only raises the card | — |
| later | Alerts to push and voice and the digest; milestones, critical path and the verify task; agy (spike S0.1); the design loop; cheap sync and a cap on PRs in CI; secrets; the approved phone and iPad boards; background-job detection (needs an hc change); token budgets; machine load holds | | |

## 5. Spikes, each run only before the slice that needs it

Each is a short script against real CLIs in a private herdr-core, with the captured output kept as a fixture.

| Spike | Question | Before slice |
|---|---|---|
| S0.3 | Can the last message of a Claude Code reviewer be read whole from the terminal, with a one-time code in it? (Codex: before slice 5b) | 1 |
| S0.6 | A real PR snapshot for each checks class, as JSON. Is gh's JSON cut short when fetched through herdr-core? | 1 (the four simple classes); 4 (the rest) |
| S0.5 | The sheepdog repo's branch protection, required reviews and merge-queue settings | 4 |
| S0.2 | Where each CLI's session id comes from under herdr-core; does a resume keep model and effort? | 5a |
| S0.4 | Does the daemon run under launchd here, and can `hc exec` read a project folder the daemon cannot? | 5b |
| S0.8 | What herdr-core can report: output activity, child processes, "the owner typed", "the owner quit it"; does killing a terminal end its process group? | 5a |
| S0.10 | Can the supervisor's existing card show an override of two tasks with their commits and check states? | 6 |
| S0.9 | Can a browser on this computer enrol with the daemon and hold a session ticket? | 9 |
| S0.7 | Time to run a real task's proofs in a clean checkout | 10 |
| S0.1 | Does agy report status to herdr-core like Claude Code and Codex? | the agy slice |

## 6. Cutover

**Where things stand** (from the 2026-11-18 journal; not re-checked by me):

- All sheepdog-factory engine work is paused. The old coordinator and the hourly wake are still alive and should only report the pause.
- Paused tasks keep their worktrees, branches and records. PR #672 (SDF-74) is open.
- The engine that is installed and running is a separate copy under the owner's home, not the tree on main.
- *Unverified:* whether other projects run on that same installed copy.

**Steps.**

| Step | What | Who decides |
|---|---|---|
| C0 | Freeze. The old factory starts no engine tasks. Turn off its hourly wake for the sheepdog space so it spends nothing | owner or coordinator |
| C1 | Build slices 1 to 7 (sections 3 and 4). The new engine runs inside the daemon from slice 1, but only for a project started with the new `sheepdog factory start` on a scratch repo; no existing project, command or release path uses it. From slice 6 the supervisor also lists the new engine's asks | — |
| C2 | Prove it: the exit test of slice 8 (section 4), and the knowledge-base behaviours mapped to version 1 rows all have passing tests | — |
| C3 | The switch, one PR: the supervisor takes the `Engine` interface in place of the CLI client; delete `client.go`, `engine.go`, `python.go`, `tick.go`, `factory_embed.go` (at the repo root; it embeds `factory/`, so the tree and this file go together), the loopback proof route, the `python-*` commands, the Makefile target and the CI job; update `owner-contract.md`, `CONFIG.md`, `architecture.md` and the glossary; change the board text that says "engine and Python" | owner approves the board text |
| C4 | The old `factory/` tree leaves main | **owner** (below) |
| C5 | Open work from the old store | owner or coordinator (below) |
| C6 | Other projects on the old installed copy move when the slices through 12 have run on sheepdog for a week | owner |

**C4: how the old code leaves main.** Decided by the owner on 2026-11-18 (design.md D12): tag the last commit that has the old engine (`4df3a038` today), then delete `factory/` and the Go glue in the switch PR (C3). The tag keeps every file and line that knowledge-base.md cites reachable. Deleting the tree earlier does not compile, because `factory_embed.go` embeds it.

Before C3, two things are checked and written down: whether any other project runs on the installed copy of the old engine, and whether the daemon without `python.go` can still drive that copy. A revert of C3 restores code, not state: the new engine's state lives under `<data_dir>/factory/projects`, the old one's under `<data_dir>/tasks`, and terminals started by the new engine would be left without an owner.

**C5: open work.** No importer is planned. The old store's shape is the thing being replaced, and most open SDF tasks patch the old engine and are now moot.

- Save one export of the old task list as a reference file.
- The new coordinator re-plans what still matters from that list and the approved addendum.
- PR #672, and the paused tasks' worktrees and branches, are closed or removed only after the owner says so.

**Rollback.** Before C3 there is nothing to roll back. After C3, revert the switch PR; the old code is in history and under the tag.

## 7. Slice log

Filled in at each review point. An empty table is the honest state today: nothing has been built.

| Slice | PRs | Lines, estimate | Lines, real | Review rounds per PR | What the demo taught | What changed in the plan or the design |
|---|---|---|---|---|---|---|
| 1 | | 2,300 | | | | |

## 8. Risks

| Risk | Sign | Response |
|---|---|---|
| The skeleton's stubs harden into the design | A later slice builds around a hard-coded value | Each stub is listed in section 3 with the slice that removes it; the review point checks the list |
| Row order hides a problem | A task sits on a row that names the wrong thing to wait on | The histories as tests; a review of the table at each review point with real rows |
| Sizes are wrong again | Slice 1's real lines differ from 1,700 by more than half | Re-plan every later slice from the real cost per row before going on |
| A slice is too big | Its PR plan passes eight PRs | Split it at the review point before it, as slice 5 was split into 5a and 5b |
| A spike fails (the report tag cannot be read, a browser cannot hold a ticket) | The spike's fixture | The fallbacks are named in design.md 9.3 and 7.1; decide before that slice is planned |
| A real pitfall was lost in extraction | A field failure the knowledge base does not list | Add the row and the behaviour test first, then fix; the tag keeps the old tests readable |
| The engine builds itself and a bug stalls it | After slice 8 | Engine work stays hand-run until a week passes with no intervention |

## 9. Pictures that lag the tables

The review ladder has 13 rows since the second review (L10 is the reject row). `transitions.html` and the "Engine" page of the design canvas (the EngineReviewLadder board) still show 12. They are redrawn from design.md at the first review point, when the tables have met real code and will change again anyway.
