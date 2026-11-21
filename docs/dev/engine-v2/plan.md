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
| 2 | The review loop | A task that fails review is fixed and reviewed again; two passing rounds are needed; a worker can reject a finding; a task that will not converge, or grows past its size, stops, the coordinator is woken and decides | 1,500 | — |
| 3 | Quality up front | A plan with no scope fields or no proof for an outcome is refused; a worker's "ready" runs its proofs and is refused when one fails; the real templates of prompts.md | 1,050 | — |
| 4 | Several tasks, real GitHub | Three tasks with a dependency run with a slot limit, and `sheepdog factory status` shows what each waits on; a PR behind its base is updated; a flaky check is re-run once; a red base holds merges; a PR merged or closed by hand is handled | 1,400 | S0.5 |
| 5a | Leave it alone: agents | Kill a worker, the reviewer, the coordinator or the daemon: the factory carries on. An agent stuck at a screen or a dialog is restarted | 1,450 | S0.2, S0.8 |
| 5b | Leave it alone: the machine | The daemon runs as a service; an empty quota pool stops the factory and the reset starts it; a second CLI reviews; stray terminals and leftovers are reported | 1,115–1,745 | S0.4 |
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

### Parallel tracks after slice 2

Owner decision, 2026-11-19: after slice 2 the work runs on three tracks at once.

| Track | Order | May touch | Leaves alone |
|---|---|---|---|
| Core | Slices 3 → 4 → 5a → 6 → 7 | `internal/factory/flow`; the engine's files in `internal/factory` (`engine.go`, `report.go`, `land.go`, `wake.go`, `store.go`, the three templates); `internal/factory/world`; `cmd/sheepdog/factory.go`; design.md and this file at each review point | `internal/web` beyond its factory POST routes; `doctor.go` |
| Front end | First one small core PR: the factory view API and its JSON per design.md 12.1 and the reads of 12.3 (owner contract v1 shapes plus the addendum's fields). Then slice 9's page, built from the reviewed canvas boards; spike S0.9 comes first, and the ask cards wait for slice 6's asks. The phone and iPad later, with iOS tests on the second Mac | a new view file in `internal/factory`; `internal/web` (the factory page and its routes in `server.go`); the web assets; later `ios/` | `flow` and the executors |
| Adapters and doctor | Codex as a second reviewer, from slice 5b (the adapter, `Resolve` and candidates; it needs S0.3's Codex half); slice 8's doctor checks. The switch-off of the old Python glue (C3) stays last, after the core track's slice 7 and the exit test | the adapter code in `internal/factory/world` (a new `codex.go` beside the Claude argv builder, and its tests); `internal/factory/doctor.go`; the `doctor` subcommand in `cmd/sheepdog/factory.go` | the rows and the acceptance table |

- At most two PRs in flight per track. Merges go one at a time across all tracks.
- Demos never overlap: each runs on its own daemon port and its own clone of the scratch repo.
- The shared files are `engine.go`, `report.go`, `world/world.go`, the route table in `internal/web/server.go`, `cmd/sheepdog/factory.go`, design.md and this file. A track keeps its change to them small and merges it on the latest main.
- The rest of slice 5b (launchd and git through herdr-core, pools and sign-in, alerts, the sweep) is on no track yet; it is placed at the review point after slice 5a.
- `internal/factory/world` and `cmd/sheepdog/factory.go` are shared by the core and adapters tracks: the adapters track adds files and one subcommand there and does not edit the core's functions.
- Slices 10 to 12 (evidence by the engine, plan review, the retro round) go to the core track after slice 7; slices 13 and 14 are placed at the review point after slice 7. C3 stays last, after the exit test.
- State at review point 3: the front end is paused after its page (PR #699) until slice 6's asks; the adapters track is done except C3 (section 7).

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
- **PRs.** Six, each at most 300 lines. Each brings in the settings its rows use. A split's children pass the same shape-only plan check as slice 1; the full plan checks arrive in slice 3.

| # | PR | Lines | Done when (and what can be watched) |
|---|---|---|---|
| 2.1 | Waking the coordinator for real (J12): the wake, held until the coordinator is idle, starting with the facts block of design.md 10; N43's wake, once per stall, keyed by `H` and the stall's facts. A stand-in "stuck" row until slice 5a: a task whose row has waited `stuck_minutes` on a run that never answered (a failed start, a voided round, an idle reviewer with no tag, a failed merge or cleanup act) wakes the coordinator once with the facts. The row sits above N1, since a failed cleanup act belongs to an ended task; its clock is the act's `At`, or a time 2.1 stores when it first reads the reviewer idle with no tag | 250 | **A reviewer left idle with no tag wakes the coordinator once, with the facts, after `stuck_minutes`;** later passes do not wake it again; a new `H` or another stall does; the log shows each wake (the demo sets `stuck_minutes` to its minimum, 5); a wake that arrives while the coordinator is working is held and delivered when it goes idle |
| 2.2 | L8 with `review_rounds` (default 2), by another reviewer model when one resolves; full and delta rounds; the review diff of design.md 9.4, from the merge base computed now | 200 | **A task passes only after two passing rounds on the final head** (design D12); a fix is reviewed as a delta; the diff leaves out what the base brought in (KB-REV-13, KB-REV-12) |
| 2.3 | `ready --reject "<why>"` and the `reject` record; L10; the next reviewer's prompt carries the rejections (the range it reviews: design.md 9.4) | 200 | **A worker rejects a finding and the next reviewer is given the rejection;** a rejected finding comes back only with a run that proves it (KB-REV-62); a reject uses no round; one with no why is refused (`why_missing`) |
| 2.4 | Non-convergence: L12 without the retro row (the streak from `retro_from_round`, and `max_rounds`); N25 opens a recap, trigger `review`; N6; the `recap` record and `recap-written`; the coordinator's `recap decide` with `split` and `drop`; a split or dropped task's PR closed with a note | 300 | **A task forced to fail three rounds stops; the coordinator is woken with the allowed choices and splits it.** From round 3, two rounds in a row with majors open the recap (KB-CAP-1, KB-CAP-9); a choice outside the allowed set is refused (`not_allowed`) |
| 2.5 | The coordinator's task commands of design.md 4.9: `send-back`, `close`, `park`, `unpark`, `drop`, `plan amend` (a changed brief stops earlier rounds from covering) and with it `recap decide new`; N3; `ready --nothing-to-change` and N8 | 300 | **A task sent back by the coordinator stays with the worker until it says ready again** (KB-TSK-3, KB-RUN-6); `close` before a "nothing to change" report is refused; a parked task's runs stop; after `plan amend` of a task's brief the same head is reviewed again; `new` is refused until the brief was amended since the recap opened |
| 2.6 | N12 with `estimate_lines`, `size_cap_factor` and `task_size_limit`; the recap's `continue` with `continue_lines`; the `merge_only` table and its use in `covers` (design.md 3.1; `covers` exists since slice 1, treating only the same commit as covering) and N20 | 250 | **A task that grows past twice its estimate stops at a recap;** a missing estimate does not turn the check off (KB-CAP-18); a merge of the base into the branch is pushed with no new round, and the passing rounds still cover it |

- **Done when.** A pass needs `review_rounds` passing rounds on the final head (design D12). From round 3, two rounds in a row with majors stop the task for a recap (KB-CAP-1, KB-CAP-9). The size check cannot be turned off by a missing estimate (KB-CAP-18). The review diff is the task's own changes from the merge base computed now (KB-REV-13, KB-REV-12). A rejected finding comes back only with a run that proves it (KB-REV-62). A task sent back by the coordinator stays with the worker until it says ready again (KB-TSK-3, KB-RUN-6). An amended brief needs new rounds. A task that waits on a run that never answered wakes the coordinator once. The histories "review never converges" and "size cap" of the audit pass.
- **Its review question.** Did the coordinator, woken with facts and allowed choices, decide well without being told how?
- **Size guess.** 1,500 in 6 PRs (slice 1 ran 1.15 times its estimate). **Leaves out.** The retro (slice 12); the owner's ask for a split child (N24: slice 6); the CI and time limits N13 and N14 (slices 4 and 5a); `retry` and N5, so the owner's start stays the retry (slice 5a).

### Slice 3: quality up front

- **Goal.** What the owner used to prompt for by hand is required by the engine: scope, proofs, self-review.
- **Demo.** A plan missing "for whom" or "not doing", or an outcome with no proof, is refused with its reason. A worker's `ready` runs its proof commands and is refused when one fails. The coordinator, worker and reviewer run on the templates of prompts.md section 5.
- **Brings in.** The plan checks of 11.7 (scope fields, outcomes with `proved_by`, the title; the estimate and its limit are in since PR 2.6); `ready` with proofs run by the report client, `touched`, `skipped`; the reviewer's reply lines `self-check misses:`, `class:`, `deletable:`, `not run:` (the tag stays `code, round, head, verdict, majors`); the three templates with replay evidence (prompts.md section 7); the first-look pass rate in the log; every start states model and effort (KB-LCH-4).
- **PRs.** Six, each at most 300 lines. 11.7's Launch and UI checks wait for the slices that bring candidates (5b) and milestones (later).

| # | PR | Lines | Done when (and what can be watched) |
|---|---|---|---|
| 3.1 | The scope fields `for_whom`, `if_skipped`, `smallest`, `not_doing` required on `plan apply`, on tasks an amend adds and on split children; the title checked as a Conventional Commit subject; `not_doing` shown to the worker. Two small fixes from review point 2: a `plan amend` of `estimate_lines` no longer raises the cap around `continues_per_task` or leaves an open size recap open beside the new cap; a changed-lines read that times out (the 15 s git limit) no longer skips the size check: it counts as over the cap (design.md section 3, `last_tip`) | 200 | **A plan missing "for whom" or "not doing" is refused with its reason, and nothing changes** (KB-PLN-4); a title that is not a Conventional Commit subject is refused; an amend cannot raise the cap more often than `continues_per_task` allows; a timed-out read stops the task at a size recap |
| 3.2 | `outcomes` each with `proved_by` (a command, a test, or `manual` with the artifact to make); the plan check "at least one outcome, each with `proved_by`"; worker and reviewer prompts show them | 150 | **A plan with an outcome that has no proof is refused with its reason;** the worker's and the reviewer's prompts list each outcome with its proof |
| 3.3a | `ready --proof <outcome>="<command>" …` (`--artifact <outcome>=<path>` for a manual outcome): the report client runs each proof in the worktree and keeps the exit code and the output file; the `ready` record holds them; refusals `outcome_missing` and `proof_failed` | 200 | **A worker's `ready` runs its proof commands and is refused when one fails;** a ready that leaves an outcome unproved is refused (`outcome_missing`) |
| 3.3b | `--touched` and `--skipped` on `ready`, kept on the record; the reviewer's prompt carries the proofs with their real output, `touched` and `skipped` | 150 | **The reviewer is given the real proof output** and the worker's touched and skipped lists, and a worker that lists nothing skipped while its reviewer finds an unhandled caller gets a major (prompts.md) |
| 3.4 | The reviewer's reply lines `self-check misses:`, `class:`, `deletable:`, `not run:` read beside the tag and kept on the round; the first-look pass rate (tasks whose first round passed) in `sheepdog factory log`; every start states model and effort for every role (KB-LCH-4) | 200 | **The log shows each round's self-check misses and the first-look pass rate;** a reply without `deletable:` or `self-check misses:` is `invalid_report` and its round is not used (design.md 7.2); no start falls back to a CLI default |
| 3.5 | The three templates of prompts.md 5.1, 5.3 and 5.4 in place of slice 1's stand-ins, without the lines that name commands of later slices (`ask`, `answer`, `report ask`, `override`, `show`); the tests of prompts.md 7: at most 40 lines, every command named exists, every rule line has a source | 150 | **The coordinator, worker and reviewer run on the new templates in a live demo, and a task passes review on its first look;** replay evidence for the situations of prompts.md 7 whose commands exist by then (read [docs/dev/eval/README.md](../eval/README.md) first); a reviewer's finding needs a run behind it and the reviewer stays read-only (KB-REV-63) |

- **Done when.** A plan with any problem is refused whole (KB-PLN-4). Every start states model and effort and never falls back to a CLI default (KB-LCH-4). A reviewer's finding needs a run behind it and the reviewer is read-only (KB-REV-63). Each template is 40 lines or fewer and every command it names exists.
- **Its review question.** What share of tasks passed review on the first look, and what did the reviewers count as self-check misses?
- **Size guess.** 1,050 in 6 PRs (slice 2 ran 0.93 times its estimate). **Leaves out.** The plan reviewer (slice 11); the engine's own run of the proofs (slice 10); 11.7's Launch and UI checks.

### Slice 4: several tasks, real GitHub

- **Goal.** A real plan of several tasks lands on a real repo that other things also touch, and the owner can see where each task stands.
- **Demo.** Three tasks with one dependency run with `max_running` 2; `sheepdog factory status` lists each with what it waits on and why. A PR that falls behind is brought up to date with a merge commit and merges when its new checks pass. A check that fails once is re-run and passes. With the base red, merges wait. A PR merged by hand ends its task; a PR closed by hand wakes the coordinator.
- **Brings in.** N9, N10 and scheduling (4.10), with the plan field `priority`; the landing rows N32 to N38 and N40; N13 (too many CI failures); the full checks rollup, PR adoption and `merge_only` tables; N4, N7; the executor rules of 9.4; the base observer; the stage and "why" view of 4.11 as a terminal command. It ends slice 1's "one task at a time": today nothing holds a task for its `after` or caps the running tasks, so every planned task starts at once, though the coordinator template already plans with `after`.
- **First.** Spike S0.5 (the sheepdog repo's branch protection, required reviews and merge queue) and the rest of S0.6 (a real snapshot for each checks class not yet captured, as JSON) run before 4.3. Where `behind` is read from is decided from S0.6's fixtures.
- **PRs.** Eight, each at most 300 lines. Each brings in the settings its rows use.

| # | PR | Lines | Done when (and what can be watched) |
|---|---|---|---|
| 4.1 | N9 and N10 with scheduling (4.10): a task waits on its dependencies by name, and on a dropped one until the plan is amended; `max_running`, set by a flag on `sheepdog factory start`; the plan field `priority` (urgent or not); the pure slot function: urgent first, then the critical path (from the estimates and `after`), then oldest, with one slot beyond `max_running` for an urgent task | 200 | **Three tasks with one dependency and `max_running` 2: two start, the third waits on its dependency by name, and no queued task starts at the cap** (KB-TSK-16); a task after a dropped one waits at N9 |
| 4.2 | `sheepdog factory status`: one line per task with its stage, the row it matched on the last pass and that row's why (4.11). Status and the `/factory` page must show the same stage, worked out in one place: today the view of PR #695 derives it from stored state alone (`view.go`), while 4.11 makes it a field of each row. The brief chooses between moving the stage onto the rows (the view then reads each task's last matched row) and status reusing the view's function | 150 | **`sheepdog factory status` lists each of three tasks with what it waits on and why,** with the same stage the `/factory` page shows; a task no pass has reached yet says so |
| 4.3 | The PR snapshot of 7.3: the full checks rollup (unreadable, red, stopped by a time limit, stopped from outside, pending while a workflow run is unfinished, absent with the head's age, green; only neutral or skipped is absent), with the REST check-runs fallback when the first call is denied; `behind` (N40 reads it; `PR.Behind` is never set today); `review_required`; `merge_queue` only if S0.5 finds a queue. A failed read is the project fact `github_unavailable`, which holds merges | 200 | **Each checks class reads right from a recorded real snapshot** (S0.6); a network failure is never remembered as "not a GitHub repo" (KB-GIT-20); a PR the base moved past reads as behind |
| 4.4 | The CI rows N33 to N35 with `flake_reruns`: red checks re-run once, then the worker told once per failing run; a check cancelled from outside re-run, a job's own time limit a coordinator wake; no checks past `checks_grace_minutes` (this PR brings the setting in) dispatched once, then a coordinator wake naming the ways out. The executors' re-run and dispatch (9.4) | 200 | **A check that fails once is re-run and passes, with no review round spent;** the same failing check on the same commit is reported once (KB-GIT-14); a PR with no checks is never merged |
| 4.5 | N13 with `ci_fails_per_hour` (a recap with trigger `ci`; one bad commit counts once); N32 (a conflict: tell the worker once to merge the base into the branch, never rebase); N37 (GitHub requires an approving review: wake the coordinator once) | 150 | **A task whose commits keep failing CI stops at a recap** and the coordinator is woken with the allowed choices; a conflicted PR's worker is told once to merge the base |
| 4.6 | N40 (reusing 4.4's `checks_grace_minutes`): a PR behind its base is brought up to date once per `H` with GitHub's update-branch (a merge commit, pinned to the expected head); the new head moves `H` as a `merge_only` delta, so the passing rounds still cover it. The `merge_only` decision becomes a first-match table in `flow` (9.4), fed by `world.MergeOnly` | 200 | **A PR behind its base is updated once and merged only when the new head's checks pass, with no review round spent** (KB-MRG-43); a wake with no new commit does not undo a pass (KB-MRG-6) |
| 4.7 | The base observer (the newest finished run on the base, for push events: `base_red`, and a coordinator wake on red and on green) and N38 with `main_red_minutes`: a red base holds updates and merges; after `main_red_minutes` the worker of the oldest held task is told once to merge the base and say ready | 150 | **With the base red, updates and merges wait** (KB-MRG-47), and go on when it turns green; `sheepdog factory status` names the base as what each held task waits on |
| 4.8 | N4 (a PR closed by hand: no reviewer runs, the coordinator is woken once, the PR is never reopened) and N7 (the tip or the PR head diverged from `H`, or the PR head moved with changes the engine did not push: tell the worker once, then wake the coordinator; never merge). PR adoption becomes a first-match table in `flow` (9.4): only the attempt's own open PR, in the same repo and on the same base, after the engine's own push. A bound on the title's length (a minor from PR 3.1's review; the brief takes the number from GitHub's limit) | 150 | **A PR merged by hand ends its task (N2, since slice 1); a PR closed by hand wakes the coordinator once;** the engine never adopts a fork's PR or one on another base (KB-GIT-2); a force-pushed branch is never merged |

- **Done when.** No queued task starts at the running cap (KB-TSK-16). A PR behind its base is updated once and merged only when the new head's checks pass, with no review round spent (KB-MRG-43). A red base holds updates and merges (KB-MRG-47). A network failure is never remembered as "not a GitHub repo" (KB-GIT-20). A wake with no new commit does not undo a pass (KB-MRG-6). The same failing check on the same commit is reported once (KB-GIT-14). The engine never adopts a fork's PR or one on another base; it adopts only its own open PR after its own push, and N42 checks the head before any merge (KB-GIT-2; design 9.4).
- **Its review question.** Which GitHub answers did the real repo give that the rollup and the landing rows did not expect?
- **Size guess.** 1,400 in 8 PRs. Slice 3 ran 0.81 times its estimate in added lines (0.65 net), but its rows were checks on reports the engine already reads; GitHub's answers are less known, so the guess is kept. **Leaves out.** Merge queue handling unless S0.5 finds one; cheap sync; the owner's gates (N30, N31) and the slot for a task the owner just answered (slice 6); N10's milestone condition (milestones, later) and its "no worker candidate resolves" (`Resolve`, slice 5b); the alerts `ci_stalled` and `main_red` (the alerts table, slice 5b); `say` and `retry` as the ways out of N4 and N7, and the back-off and N5 for a failed update (slice 5a; until then the owner's start).

### Slice 5a: leave it alone, agents

- **Goal.** An agent that dies, hangs or gets stuck does not stop the factory.
- **Demo.** With a plan running, the owner kills a worker, then the reviewer, then the coordinator, then the daemon: each comes back and the work finishes. An agent left at a first-run screen or a dialog is restarted.
- **Brings in.** The run table (section 5) with its matrix, replacing slice 1's status reading; "making sure a run is alive" (4.8), replacing the stand-in row and slice 2's stuck row; keys, back-off and N5 with `retry` (section 8); N3; N14 (the time limit); the project rules for the coordinator (J1 to J10, J12 to J14; rotation is slice 13); the invariants I1 to I5; `say`; the clock rule.
- **Done when.** A project has exactly one coordinator, and a replacement starts in the repo folder (KB-COO-1, KB-COO-4). A restart ends the replaced agent's whole process group (KB-COO-12). A worker that died mid-turn is seen as gone (KB-RUN-8). A start stuck at a trust or sign-in screen is told apart from a blank one (KB-RUN-28, KB-RUN-43). A restart of the daemon after every step of every history gives the same end. A failing action backs off and ends at N5, never in a loop.
- **Its review question.** How many restarts did a night's run need, and which of them were the engine's own doing?
- **Size guess.** 1,450. **Spikes first.** S0.2 (session ids and resume), S0.8 (what herdr-core can report).
- **From review point 2.** Three stalls of slices 1 and 2 end here. S1 asks `stop_task`, which is not built, so a task whose worker vanished waits silently, and S2 does not see it: the run table and 4.8 replace both. A coordinator that answers a wake with a question and applies no plan waits forever: the project rules (J9, J13) must catch it. A failed merge, cleanup or PR close act is tried again only by the owner's start: `retry` and N5 replace that.

- **From review point 4.** Cut into PRs below; spikes S0.2 and S0.8 run before 5a.1.

| # | PR | Lines | Done when (and what can be watched) |
|---|---|---|---|
| 5a.0 | Two slice 4 leftovers: each row's stage comes from the row itself, so `status` needs no hand-kept map (N13, N32, N37 and N40 were missing one); N36 wakes the coordinator once when a check stays pending past `ci_stalled_minutes` | 80 | **A fresh line from any row is never shown as stale; a check that never finishes wakes the coordinator once** |
| 5a.1 | The run table (section 5) with its matrix, replacing slice 1's status reading | 250 | **A worker that died mid-turn is seen as gone** (KB-RUN-8); a start stuck at a trust or sign-in screen is told apart from a blank one (KB-RUN-28, KB-RUN-43) |
| 5a.2 | "Making sure a run is alive" (4.8), replacing the stand-in row, S1's `stop_task` and slice 2's stuck row; N3 | 200 | **The owner kills a worker, then a reviewer: each comes back and the task finishes** |
| 5a.3 | Keys, back-off and N5 with `retry` (section 8) for every act: merge, cleanup, PR close, update-branch, tells. Also: a red, green, red base at one commit wakes again; two failed N38 tells wake once per spell; a second N7 problem at one `H` is told; a PR on another base stops being pushed; failing commits from an old attempt do not count for N13; two runs of one workflow on a head stay apart in the rollup | 250 | **A failing action backs off and ends at N5, never in a loop** |
| 5a.4 | `say` and `retry` as coordinator commands, the ways out N4 and N7 name | 150 | **A task whose PR was closed by hand is retried on a new branch, with a new PR** |
| 5a.5 | N14, the time limit: a run hung in "working" (it also ends N7's wait) | 120 | **A worker that never ends its turn is stopped at its time limit and the coordinator is woken** |
| 5a.6 | The project rules for the coordinator (J1 to J10, J12 to J14; rotation is slice 13) | 250 | **A project has exactly one coordinator; a replacement starts in the repo folder** (KB-COO-1, KB-COO-4); a restart ends the replaced agent's process group (KB-COO-12); a coordinator that answers with a question and no plan is woken again (J9, J13) |
| 5a.7 | The invariants I1 to I5 and the clock rule | 150 | **A restart of the daemon after every step of every history gives the same end** |

### Slice 5b: leave it alone, the machine

- **Goal.** The factory runs as a service and copes with quota, a second CLI and its own leftovers.
- **Demo.** The daemon runs under launchd and still reads the project folder. A pool at its floor stops new starts and the reset starts them again. The reviewer runs on Codex. A stray terminal and a leftover worktree are reported.
- **Brings in.** git and gh through herdr-core (design 2.4); pools and the sign-in check; the alerts table (raise and clear); `Resolve` and candidate lists; the Codex adapter; the sweep of 11.3.
- **Done when.** git runs in the user's session and every call has a time limit (KB-ENV-7, KB-GIT-22). A task never starts on an empty pool and waits for the reset (KB-QTA-2, KB-QTA-3). A stopping alert clears by itself when its cause is gone. A done task's leftover worktree is listed, not silently kept (KB-WKT-5).
- **Its review question.** Did anything on the machine (a consent prompt, an update, a full disk) stop the run in a way no alert named?
- **Size guess.** 1,115–1,745 net Go lines across the table below. **Spike first.** S0.4 (the daemon under launchd).
- **From review point 3.** Codex as a reviewer is built (PR #700, opt-in `second_reviewer`, design.md 9.2); this slice still brings `Resolve`, candidate lists and Codex in other roles. The sweep also lists the shared Codex home (`<data_dir>/factory/codex-home`), which gathers Codex's session and log files with no cleanup, and which concurrent Codex runs share. One 15 s git budget covers a pass's worktree inspection and its changed-lines read, so a slow inspection leaves the read little time, and a timeout inside the inspection skips the pass instead of counting as over `size_cap`: the per-call time limits of this slice must cover both.

- **From review point 5a.** The table is in execution order. Run the 5b.11 launchd checkpoint as soon as both transports land, then repeat the service demo at the end with a synthetic quota reset, a stopping alert that clears, and the sweep report. Split a row into a/b before it exceeds 300 net Go lines.

| # | PR | Lines | Done when (and what can be watched) |
|---|---|---|---|
| 5b.1 | Bounded git through hc, including project-folder access for path resolution and proof reads | 145–220 | **A hung git child in one scratch folder times out and a task in a healthy folder still starts.** A large review patch comes back intact, and a changed-lines deadline alone feeds N12 (KB-ENV-6, KB-ENV-7, KB-GIT-22) |
| 5b.2 | Bounded GitHub calls through hc, pinned to the supported origin, with a ceiling on the whole snapshot | 75–120 | **One scratch PR is opened, checked and squash-merged on the intended repo even with a different local gh default.** An unreadable snapshot holds the merge, and an unreadable recorded PR stays unknown for N26 (O22, KB-GIT-7, KB-GIT-10, KB-GIT-20) |
| 5b.11 | Early launchd verification of the existing daemon service. Use a checkpoint if the job works as-is | 0–25 | **A temporary launchd job finds hc without interactive-shell exports and merges a PR from a scratch repo under Documents.** The log shows both transports using hc and the temporary job is booted out (KB-ENV-7) |
| 5b.3 | `Resolve` at launch for all three roles, with actual candidate facts and the existing reviewer ladder | 145–205 | **A scratch PR shows the resolved worker and two distinct eligible reviewers in its run log.** An impossible task is refused before worktree creation, and legacy `second_reviewer` still selects Codex (KB-LCH-4, KB-REV-42, KB-REV-68) |
| 5b.4 | Start-dialog keys for verified screens and selected actions | 45–75 | **A captured trust dialog gets one verified key.** Sign-in, ordinary permission requests, quoted dialog text and a recycled terminal get no keys (KB-RUN-28, KB-RUN-43) |
| 5b.5 | Codex worker and coordinator adapters, including turn observation, delivery and continuation | 160–240 | **A Codex worker reports ready and its scratch PR merges. A Codex coordinator saves a plan and completes a second turn after a wake.** A restart preserves a held instruction and recovers an interrupted coordinator wake (O21, KB-LCH-2, KB-LCH-4, KB-LCH-15, KB-RUN-31, KB-COO-46) |
| 5b.6 | Local pool observer and launch gate before allocating a worktree or slot | 150–220 | **A queued task at its pool floor starts on the first eligible pass after a scripted reset.** Healthy and unknown pools still start, and an exhausted reviewer pool spends no retry (KB-QTA-1, KB-QTA-2, KB-QTA-3, KB-QTA-8, KB-QTA-9, KB-QTA-10, KB-QTA-11) |
| 5b.7 | Floor handoff and quota recovery, with persisted deadlines and cooldowns | 100–170 | **An affected worker gets one handoff and resumes on a usable candidate in the same worktree, or waits for reset.** Held messages, dirty files and the pinned review round survive a restart. Quoted quota text cannot end a run, and a live foreground child counts as activity (O23, O24, KB-QTA-4, KB-QTA-5, KB-QTA-6, KB-QTA-8) |
| 5b.8 | Alert raise and clear, J3 and J5, and the existing CI, base and outage signals | 155–235 | **A stopping alert holds new starts, survives restart without repeating, and clears when its cause recovers.** A signed-out coordinator stays held until a positive sign-in check, and a pending check alerts once for the current head (KB-COO-27, KB-COO-31, KB-ENV-5) |
| 5b.9 | Consent and disk observers with bounded project-folder probes and disk-full evidence | 95–155 | **A simulated folder stall or disk-full write shows its own alert and clears on recovery.** Disk-full remains visible when the store cannot persist the alert (KB-ENV-4, KB-ENV-5, KB-ENV-9) |
| 5b.10 | Sweep at startup and on its configured interval, including the shared Codex home | 45–80 | **A stray terminal and a retained done-task worktree appear once per episode and stay in place.** The sweep also lists the shared Codex home and explains an unknown result or the basis of a base-ahead comparison (KB-WKT-5, KB-ENV-9) |

**Decisions for the briefs**

- Run git and gh through hc at the daemon boundary. Include `factory start` preflight and the run-local `report ready` git lookup. Keep login-session CLI operations local.
- Give each git stage its own time limit under the pass limit. Distinguish an RPC failure from a child deadline, feed N12 only on a changed-lines deadline, and suppress repeated failures per folder (KB-GIT-22).
- Support one authoritative `origin` with matching fetch and push targets. Defer remote reconciliation.
- Feed the ladder's round eligibility into `Resolve`, compare reviewers by (CLI, model), and keep `second_reviewer`.
- Use a temporary, uniquely labelled LaunchAgent with a scratch binary, config, data and port, then boot it out (owner approved 2026-11-20). Leave production `service install` and the Mac app's login item untouched.
- Count each sweep item once per observed episode. Match runs by label and creation time.

### Slice 6: the owner's asks

- **Goal.** The owner hears only what needs him, and his answer counts only when it is his.
- **Demo.** A worker's question is answered by the coordinator. A release question appears as a card on the phone; the tap unblocks the task. A task with an owner gate waits for his yes. The owner says "stop the factory" and later starts it; the plan is kept.
- **Brings in.** The ask table (7.1) without delegation (A1 is slice 14) and its acceptance lines; the `Engine` interface; `AnswerProof` moved out of `contract.go`; the supervisor listing the new engine's asks beside the old client's (the old client is removed in slice 8); the gates N24, N30, N31; owner stop and start; the quiet rule's labels.
- **Done when.** An owner's answer counts only from a paired device or live voice; a local answer with no session ticket to a gated ask is refused (KB-ASK-19; design 7.1). An open question outlives the worker that asked it (KB-ASK-10). A gate ask whose commit is gone is cancelled.
- **Its review question.** How many cards did the owner get in a day, and which could a rule have answered?
- **Size guess.** 900. **Spike first.** S0.10 (what the supervisor's existing card can show). **Leaves out.** Delegation rules and rule read-back (slice 14); push and voice for alerts; `touch`.
- **From review point 3.** A worker that has committed cannot ask the coordinator anything: `--nothing-to-change` is refused over the base, so the worker template tells it to take its pick and name the question in `--skipped`, which only the reviewer reads. `report ask` closes this. The front-end track's ask cards wait on this slice.

- **From PR W3's reviews (closed, 2026-11-20).** The owner-proof guard of design 7.1 is not in the code: build it here (a gated answer from this computer with no ticket is refused). A browser on this computer enrols with a short code from the Mac menu-bar app (design D17, O20). A paired browser signs in again with its stored key after a daemon restart and on a 401 from `/factory`, instead of offering to pair again; the code is fetched when the owner asks for it, not on page load.

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
- **From review point 3.** The start checks are built on the adapters track (PRs #693, #697; see `sheepdog factory doctor` in [docs/commands.md](../../commands.md)). They check `claude` only, and must follow the resolved CLI once Codex or agy can be a worker. Gaps from their reviews: a `pull_request` inside an `if:` passes the workflow check, an empty repo is not handled, and there is no warning yet for path filters that can leave a PR with no checks (N35). Doctor's canary starts do not state model and effort; decide whether KB-LCH-4 covers a probe. What is left here is the deletion (C3).

**What "version 1" covers.** knowledge-base.md marks 426 of its 599 rows as wanted in the first version. That marking was made before the owner moved two things later: the retro round (its 13 rows are met at slice 12) and the engine's own run of proofs (4 rows, slice 10). About 20 rows about what the phone shows are met in part by `sheepdog factory status` in slice 4 and in full by the web page in slice 9. Everything else among the 426 is named by a slice from 1 to 8 above. The checker of this plan matched rows to slices by script and by hand for the misses, not by reading all 426.

- **From review point 4.** `git merge-tree --write-tree` (the `merge_only` check) needs git 2.38 or later; doctor checks the version on every machine.

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

From review point 3:

- **Slice 9.** W1 is built as a read-only view API (PR #695: `GET /api/factory/projects`, `…/{id}/graph`, `…/{id}/log`, for this computer or a paired device), and W2 as a read-only `/factory` page for this computer (PR #699). Left: the ask cards (after slice 6), browser enrolment after S0.9, the phone and iPad.
- **Slice 10.** Until then the engine takes the exit codes, commands and output paths the report client sends. Nothing compares a proof command with its outcome's `proved_by`, and any existing file passes as an `--artifact`; the reviewer re-runs proofs meanwhile.
- **Slice 14.** `Settings.Validate` has no caller outside tests: a typo in a setting edited by hand in `state.json` (`"Codex"` for `second_reviewer`) silently means the default. The settings command wires it in.

## 5. Spikes, each run only before the slice that needs it

Each is a short script against real CLIs in a private herdr-core, with the captured output kept as a fixture.

| Spike | Question | Before slice |
|---|---|---|
| S0.3 | Can the last message of a Claude Code reviewer be read whole from the terminal, with a one-time code in it? (Codex: run on 2026-11-19 for the adapters track; yes) | 1 |
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

Filled in at each review point.

| Slice | PRs | Lines, estimate | Lines, real | Review rounds per PR | What the demo taught | What changed in the plan or the design |
|---|---|---|---|---|---|---|
| 1 | 10 (#673 #674 #675 #677 #676 #678 #680 #681 #682 #683) | 2,300 | About 2,650 added code lines (Go without tests, blanks or comments); about 4,000 test lines | 1.1a 1 · 1.1b 2 · 1.2 2 · 1.3a 2 · 1.3b 2 · 1.4 2 · 1.5 2 · 1.6 3 · 1.7 3 · 1.8 1 (each later round includes a confirm of the last small commit) | Ten real bugs that only a live run showed (below). The full demo took 91 s from `factory start` to cleanup on a one-task goal | The reviewer runs in `dontAsk` with an allow list; the report tag carries no free text; branches carry project and attempt; a voided round is marked failed and logged; an owner start clears failed acts (design.md 4.8, 4.9, 7.2, 9.2 to 9.4). Slice 2 gains a stuck wake before L8 (PR 2.1) |
| 2 | 6 (#685 #686 #687 #688 #689 #690) | 1,500 (2.1 250 · 2.2 200 · 2.3 200 · 2.4 300 · 2.5 300 · 2.6 250) | About 1,390 added and 1,210 net code lines (same count as slice 1; net per PR: 2.1 173 · 2.2 139 · 2.3 100 · 2.4 280 · 2.5 299 · 2.6 220); about 3,250 added test lines | 2 each, 2.1 to 2.6 (2.6 passed its first round; its second came from a gap its demo found) | A worker on a small task finishes in about 10 s, before an owner's note lands; the reliable window to steer it is while a review runs. A clean base merge must not restart the round in flight. A rejected finding is hard to provoke live. The coordinator handles wakes well with no step-by-step rules | A reject is accepted only when the ladder says fix; every why is cleaned and capped at 500 characters; no report is taken for an ended task; a `merge_only` move keeps the round in flight; changed lines are counted from the patch with `--text`; a round after a reject reviews the full range; `last_tip` is kept in memory (design.md 3, 7.2, 7.3, 9.4; open items O11 to O13). Slice 3 cut into PRs; three parallel tracks (section 2) |
| 3 | 6 (#694 #696 #698 #701 #702 #703); beside it on the other tracks: #695 #699 (front end), #693 #697 #700 (adapters), #692 (CI) | 1,050 (3.1 200 · 3.2 150 · 3.3a 200 · 3.3b 150 · 3.4 200 · 3.5 150) | About 850 added and 680 net code lines (same count; net per PR: 3.1 82 · 3.2 79 · 3.3a 239 · 3.3b 76 · 3.4 187 · 3.5 16, plus 34 net lines of the three templates); about 2,030 added test lines, counted the same way. Tracks, net: #695 247 · #699 0 Go and 236 lines of page · #693 196 · #697 20 · #700 145 | 3.1 3 · 3.2 1 · 3.3a 2 · 3.3b 2 · 3.4 2 · 3.5 2 (3.4 and 3.5: one first reviewer passed, the other asked for changes). Tracks: #695 2 · #699 2 · #693 2 · #697 1 · #700 3 (the third confirmed a small fix) · #692 2 (the second after an added commit; both passed) | All three live runs passed review on the first look (test_repo #42 on PR 3.4's templates; #43 and #44 on PR 3.5's new templates), each a one-function task, about two minutes from start to merge with two review rounds. A worker that reports ready at once leaves no failing proof to show live: refusals were proven by tests. Codex needs three prompts answered per run and gives no reliable status. A headless browser cannot take a true phone-width screenshot. With the worker on sonnet, both review rounds run on opus in fresh sessions | The ready record's proof mechanics, the required reply lines and their tolerant reading, a refused verdict told to the reviewer once, Codex as a reviewer as built, the first-look count (design.md 3, 7.2, 9.2, 9.3, 10.1, 11.2, 17.1; open items O14 to O17). Slice 4 cut into 8 PRs; notes added to slices 5b, 6, 8, 9, 10 and 14 |
| 4 | 8 (#705 #706 #707 #708 #710 #711 #712 #713) | 1,400 (4.1 200 · 4.2 150 · 4.3 200 · 4.4 200 · 4.5 150 · 4.6 200 · 4.7 150 · 4.8 150) | About 1,970 added and 1,530 net code lines (same count; net per PR: 4.1 230 · 4.2 197 · 4.3 227 · 4.4 230 · 4.5 87 · 4.6 160 · 4.7 191 · 4.8 204); about 3,720 added test lines | 4.1 3 · 4.2 2 · 4.3 3 · 4.4 8 · 4.5 1 · 4.6 2 · 4.7 2 · 4.8 3 | Real GitHub answers differ from what the docs suggest: `behind` is readable only through compare, a re-run leaves the finished-run list, a second workflow can hide a red one, the default check-run filter hides an earlier timed-out attempt, and `gh pr view <branch>` can pick a PR on another base. Demo flags left on test_repo's main leak into later demos | Every unknown read now waits and has a bounded way out (a wake after `checks_grace_minutes`); the base is judged by the runs on its head commit and its red spell is kept in state (design.md N38, 7.3); the PR is read by its number; open items O18 and O19; slice 5a cut into PRs; a note in slice 8 |
| 5a | Core plan rows: 5a.0 #715 #716 · 5a.1 #720 · 5a.2 #723 · 5a.3 #726 #729 · 5a.4 #727 · 5a.5 #728 · 5a.6 #730 · 5a.7 #731. Core work brought forward from 5b: `Resolve` preflight #721 and the sweep #724. Adapters and doctor: #719 #722 #725. Cross-PR fixes: 5a.8a #732 and 5a.8b #733 | 1,450 | 3,551 added and 2,390 net code lines on main (same count). Net by plan row: 5a.0 3 + 24 · 5a.1 298 · 5a.2 241 · 5a.3 225 + 38 · 5a.4 90 · 5a.5 125 · 5a.6 278 · 5a.7 153 · 5a.8a 178 · 5a.8b 64. Work brought forward: #721 285 · #724 193. Adapters and doctor: #719 178 · #722 9 · #725 8 | #715 2 · #716 3 · #719 3 · #720 2 · #721 2 · #722 1 · #723 3 · #724 2 · #725 2 · #726 3 · #727 2 · #728 2 · #729 2 · #730 2 · #731 4 · #732 3 plus 2 merge rounds · #733 2 | R21 interrupted a worker running a long command. J6 held the coordinator restart for its 10-minute gap. Demo scripts found runs through the engine log and `hc get`. The sweep flagged the coordinator as stray and status flashed "round 3". Both were fixed | 5a.8a and 5a.8b came from the cross-PR review. Four gaps go to slice 5b (design.md O21 to O24). The remaining minors are recorded below |

**Review point 1.**

| Question | Answer |
|---|---|
| Real lines per PR against the estimate; one rule row's cost | 140 to 416 added code lines per PR. Two PRs passed 450 and were split (1.1 at 520 raw, 1.3 at 535 after fixes). The slice is about 1.15 times the 2,300 estimate. Tests ran about 1.5 times the code. A rule row with its named clauses cost about 15 to 20 lines, close to design.md 13's 20 |
| Review rounds each PR took, and what the reviewers found | Most PRs took 2 rounds; 1.6 and 1.7 took 3. Every first round found at least one real major: an untested guard, a silent stall, a guess treated as a fact (N26 on an unanswered snapshot), adopting the wrong PR, a JSON tag a model cannot write reliably. Two independent models found mostly different things; they overlapped on the parser and the stalls |
| What the demo showed that the tables did not expect | (1) herdr-core terminal names must match `^[a-z][a-z0-9_.-]{0,31}$`, and task ids have capitals; (2) a new project reused task branch names, and an old closed PR blocked N26; (3) Claude's plan mode waits about 20 minutes on a shell prompt; (4) `--allowedTools` swallows the positional prompt; (5) a model writes unescaped quotes inside JSON; (6) a draft typed in a reviewer's input box looks like a prompt echo; (7) a reviewer can dirty the worktree (`go build`), which voided the round and deadlocked the task; (8) GitHub lags the head after a re-push; (9) the privacy scanner reads `git@github.com` as an email; (10) Claude asks folder trust and the bypass warning once per folder and machine (pre-trusted now) |
| Did reading the reviewer's tag from the terminal work every time? | After the fixes, yes: 5 live runs, 7 verdicts. Before them it failed three ways: the prompt never arrived, the JSON did not parse, a draft hid the echo. The session-file fallback of design.md 9.3 was not needed |
| How long one task took end to end, and what it cost | 91 s for a one-file task when nothing waits; the reviewer took 15 to 20 s. Per task: one coordinator turn, one worker turn and one reviewer turn per round, on Claude (Max subscription). A stuck prompt is the real cost: 20 minutes of an idle reviewer |
| Which stub hurt most | No wake and no restart. Four of the ten live bugs ended as a task waiting silently (a failed start, a voided round, an idle reviewer with no tag, a failed N22 send); only the log showed them. So a small "wake the coordinator on a stuck task" row moves into slice 2 (PR 2.1), ahead of the full slice 5a |

Not built as planned: the PR snapshot does not read `behind` yet (N40 is slice 4); N42 stays safe through its `p.head` check and the merge pinned to the head. A failed merge or cleanup act is not tried again until the owner starts the project again.

**Review point 2.**

| Question | Answer |
|---|---|
| Did the coordinator, woken with facts and allowed choices, decide well without being told how? | Yes, in every live run; its template gained only the new commands, no rules on how to decide. It refused to plan a goal already met on main; woken for an idle reviewer, it proposed nudge-then-replace and would not merge an unreviewed head (2.1). It rewrote an impossible outcome ("say so instead of faking it") rather than plan a task that could not pass (2.4). It split a task that failed three rounds into one fix task, which then merged (2.4). It read the README before closing a nothing-to-change task (2.5). It chose `continue` on a size recap (2.6). Its `close` before a nothing-to-change report, tried at the owner's request, was refused by the engine as designed (2.5) |
| Real lines per PR against the estimate | 100 to 299 net code lines per PR. By net none passed the 300 cap; by added lines 2.4 (319) and 2.5 (357) did, both under 450, so none was split. The slice is about 0.93 times its 1,500 estimate in added lines (slice 1: 1.15), about 0.8 net. Tests grew faster than code: about 2.3 times the added code, many of them whole-loop tests on hctest and a fake `gh` |
| Review rounds each PR took, and what the reviewers found | Every PR took 2 rounds. First rounds found real majors in five of six PRs: a stale failed act that woke the coordinator again at every new head (2.1); a hand-edited base merge left out of the review range (2.2); an uncapped worker reason with control characters reaching a command line (2.3); `recap decide` on an ended task (2.4); uncapped whys, and `recap decide new` stopping the runs before closing the PR, so a failed close left the task half-stopped (2.5). 2.6 passed its first round; its live demo found the gap its second round fixed. Reviewers on other models than the writer (Neon, Sonnet, Opus) agreed on the main major in most PRs, and each found majors the other missed (2.1, 2.2, 2.5) |
| What the demos showed that the tables did not expect | (1) An owner's message cannot steer a fast worker: it finishes first. Steering works while a review runs, because the worker is idle then. (2) Esc on a starting Claude Code worker exits it; S1 then asks `stop_task`, which is not built, so the task waits silently. (3) A clean base merge during a round voided that round (fixed in 2.6). (4) A delta round still caught a bug that an earlier commit left in the code. |
| Did the demos run without a hand on a terminal? | The engine needed none: plan, work, review, merge and cleanup ran by themselves. The owner typed only to push agents onto the path under test (a note to the worker, Esc, asking the coordinator to try a command) and, with no `factory set` yet, set `stuck_minutes` in `state.json` while the factory was stopped (2.1) |
| Which stub or left-out item hurt most | S1's `stop_task`, and a coordinator that answers with a question and applies no plan: both leave work waiting silently, and S2 does not see a vanished worker. Next: a failed merge, cleanup or PR close act is tried again only when the owner starts the project again (`retry` and N5, left to slice 5a) |

Where the gaps found in slice 2 went: the text-only ones are fixed in design.md (sections 3, 7.2, 7.3, 9.4); those with no owner slice are open items O11 to O13 there; O10 (a reviewer's dirt in the worktree) stays open; the rest are notes in the slices that build them (3, 4 and 5a in section 4). Not carried: tasks created before PR 2.6 have a size cap of 0 and go to a size recap on their first pass (scratch projects only; start them again); `TestInstallNowInstallsThenRestarts` is flaky under load (passes 10 of 10 alone) and is not engine code.

**Review point 3.**

| Question | Answer |
|---|---|
| Real lines per PR against the estimate | 16 to 239 net code lines per PR, 26 to 273 added; none passed 300 by either count. The slice is about 0.81 times its 1,050 estimate in added lines (slice 2: 0.93; slice 1: 1.15), about 0.65 net. 3.5's 16 lines are small because its work is in the three templates (`coordinator.md`, `worker.md`, `reviewer.md`: 79 lines together) and the replay test. 3.3a, the report client that runs proofs, was the only PR above its estimate by net (239 against 200); by added lines 3.4 was too (221 against 200). Tests ran about 2.4 times the added code. Counted per merged commit with `git diff -U0 <sha>^ <sha>`: Go files outside `_test.go`, leaving out blank lines and lines that start with `//`, the same way as review point 2 (this count gives 2.6 274 added and 220 net, as review point 2 reported) |
| Review rounds each PR took, and what the reviewers found | Most PRs took 2 rounds; 3.1 took 3 and 3.2 passed its first. First rounds found real majors in five of the six core PRs, in three themes. **Text a worker controls reaching a prompt or a command line:** an outcome holding `=` or a line break, and `$` or backticks in a proof, broke or expanded the ready line (3.3a: outcomes refuse both, arguments are single-quoted); a proof command or artifact path could carry a fake `Rule:` line into the reviewer's prompt (3.3b: both are quoted on one line); a Codex reviewer read the owner's global instructions, and still loads a repo's own Codex config (#700; O14). **Timeouts and caching:** a timed-out changed-lines read was cached, so later passes stayed over the cap, and `continue` failed on a failed read and then sent with an expired context (3.1); a proof's time limit did not end its child processes, and its output had no bound (3.3a). **Reading reply lines:** bullets, bold, a trailing period and "none" were refused, and a tool's printed output was counted as reply lines (3.4); indented bullets in tool output, wrapped Codex commands and a table border fooled the reader (3.5); a Codex `• major:` line was not counted, so every Codex "changes" verdict was refused (#700). Also: proofs ran in the caller's folder, not the worktree root (3.3a); `--touched` and `--skipped` were checked after the proofs ran (3.3b); a refused verdict was never told to the reviewer, which then waited out `stuck_minutes` (3.4) |
| What the demos showed that the tables did not expect | (1) A fast worker cannot be steered into a failure: in 3.3a's demo the worker reported ready with passing proofs at once, and a second ready at the same commit is refused `nothing_new` before any proof runs, so `proof_failed` and `outcome_missing` are proven by tests, not live. (2) Codex asks, per run, for folder trust, for a review of the repo's hooks and to update; trust is keyed by the folder with symlinks resolved (`/tmp` is `/private/tmp`). herdr-core gave a Codex terminal no status in the spike and `done` in #700's run, so the engine reads the tag instead (design.md 9.2). (3) Headless Chromium on macOS keeps its window at least about 500 px wide, so a "390 px" screenshot showed an overflow that was not there; a true phone-width frame needs device emulation (#699). (4) A private repo on a free plan answers 403 ("Upgrade to GitHub Pro") to a read of its rulesets; doctor reads that as no rulesets (#693). (5) With the worker on sonnet, both review rounds ran on opus in fresh sessions; a second model reviews only with `second_reviewer = codex` |
| Its review question: what share of tasks passed review on the first look, and what did the reviewers count as self-check misses? | Every task did, but the sample is three one-function tasks (test_repo #42, #43, #44, the live runs of 3.4 and 3.5; only #43 and #44 used the new templates): six rounds, all passing, 0 majors and 0 self-check misses, about 111, 106 and 127 s from start to merge. No live reviewer counted a self-check miss, so the second half of the question has no data yet. The replay of 3.5 checks that each rule is in the templates, not that a model follows it; W8, W9 and the W1 wording have no live run yet. For comparison, the hand-run reviews of slice 3's own PRs passed on the first look for 3 of 12 PRs (3.2, #697, #692) |
| Did the demos run without a hand on a terminal? | The engine needed none. The owner typed only to push agents onto the path under test (asking the coordinator to amend the plan with a bad task in 3.1 and 3.2, the worker to try a failing proof in 3.3a), and, as #700's demo steps say, set `second_reviewer` in `state.json` by hand, since no command changes settings yet |
| How did the three parallel tracks work? | From the review point's merge to the last PR: 11 PRs in about 4 h 55 min (#691 at 18:53 to #703 at 23:46 on 2026-11-19; #692 merged just before), against slice 2's 6 in about 6 h 10 min (#684 at 11:45 to #690 at 17:56). Merges stayed one at a time. Each track touched the other's files only a little: #700 changed how `report.go` counts `major:` lines and added a setting; 3.4 (#702) gave `world/codex.go` an effort argument; #695 added three routes to `server.go`. No conflict between tracks is recorded. Inside the core track, PRs stacked on an unmerged PR conflicted after that PR was squash-merged; the way out, used for 3.3b and 3.4 (the `-v2` branches), was to replay the PR's own commits onto a fresh branch from main and open the PR from there, never a force push. Midway, the agy Claude quota ran out, and Claude writers moved to subagents (from the journal) |
| Which stub or left-out item hurt most | The live run cannot provoke a refusal or a reject against a fast worker: `proof_failed`, `outcome_missing` and `--reject` were proven by engine and CLI tests, not live. Next most: no hc status for Codex terminals (decided: the tag ends a Codex run) |
| Where the tracks stand | The front end is paused after W2 (#699): its ask cards wait on slice 6's asks, and the phone and iPad on the web page being used. The adapters track is done except C3, which stays last, after slice 7 and the exit test |

Where the gaps found in slice 3 went: the text-only ones are fixed in design.md (sections 3, 7.2, 9.2, 9.3, 10.1, 11.2, 17.1); those with no owner slice are open items O14 to O17 there; the rest are notes in the slices that build them (4, 5b, 6, 8, 9, 10 and 14 in section 4). Today every planned task starts at once, with no dependency hold and no cap: PR 4.1 ends that. Not carried: a reviewer may write `self-check misses:` above its count of majors, and the last of two such lines wins; neither changes a verdict.

**Review point 4.**

| Question | Answer |
|---|---|
| Real lines per PR against the estimate | 87 to 230 net code lines per PR, 135 to 322 added; none passed 300 net. The slice is about 1.41 times its 1,400 estimate in added lines and 1.09 net (slice 3: 0.81 and 0.65). GitHub's answers cost more than reports did: most rows needed a snapshot field, an executor and a wait. The fix rounds of 4.6 to 4.8 raised the cap to 200 and 220, under the 300 stop |
| Review rounds each PR took, and what the reviewers found | 4.5 passed its first round; 4.4 took eight (seven fix rounds); the rest took two or three. First rounds found real majors in seven of eight PRs, in three themes. **An unknown read as a known value:** a failed compare counted as not behind, so a behind PR could merge (4.6); a failed merge-tree check counted as a foreign push (4.8); the newest finished run stood for the base, so a re-run or a second passing workflow read as green (4.7); a run-list error picked a re-run target (4.4). **State only in memory:** the red spell (4.7) and a worker's turn end (4.8) were lost on a restart, doubling wakes or holding forever. **Re-runs:** a guessed workflow could deploy (dispatch is now opt-in by exact file name), and a timed-out earlier attempt was hidden by the default check-run filter (4.4). Codex found the 4.4 edge cases one per round; later rounds were asked to block only on a wrong merge or a wrong re-run |
| What the demos showed that the tables did not expect | (1) 4.4's demo PRs merged `ci/flaky` into test_repo's main, so every later PR failed its first attempt until it was removed: the engine handled it (N33), but demo flags must stay off main. (2) A red base needed a push-only flag (`ci/main-fail`), because PR checks run the same workflow file. (3) The coordinator decided every wake well without being told how: it dropped a task at a `ci` recap and the engine closed its PR, refused to plan a function that already existed, held on a red base without touching the owner's flag, and parked a task whose PR was closed by hand. (4) A worker told about a foreign commit pulled it in; the new head was reviewed twice before anything could merge. (5) agy's sandbox cannot read a worktree's git metadata, so the orchestrator creates each writer's branch |
| Its review question: which GitHub answers did the real repo give that the rollup and the landing rows did not expect? | (1) `behind` only through REST compare, never on the PR (S0.6). (2) The test_repo token cannot read checks; the daemon uses the owner's `gh` login. (3) A re-run leaves the `status=completed` run list, so the previous run decides. (4) A second push workflow that passes can have the newer run id. (5) check-runs defaults to the latest attempt, hiding a timed-out one (`filter=all` now). (6) `gh pr view <branch>` takes any PR from that branch, including one on another base (now read by number). (7) update-branch makes a clean base merge, which `merge_only` covers, so no round is spent. (8) A path-ignored push has no run at all; the base keeps its last value |
| Did the demos run without a hand on a terminal? | The engine needed none. The owner side was scripted through the API, as each PR's demo steps say: flag files, a moved base, a PR closed by hand, a foreign commit. N38's escalation after `main_red_minutes` (60 by default) is proven by tests only |
| Which "leaves out" items turned out to be needed now | `say` and `retry` as the ways out of N4 and N7: the wakes name them but only `drop`, `park` and the plan commands exist. Retrying a failed act: a failed update-branch or PR close is marked failed and waits for `stuck_minutes` (only N38's tell is tried a second time). A wake for a check that never finishes: N36 waits on a pending check forever and S2 does not see it, which `ci_stalled` was meant to cover. All three go to slice 5a (PRs 5a.0, 5a.3, 5a.4); the alerts table stays in 5b |
| Which stub or left-out item hurt most | No retry of a failed act, the same stub as review points 1 and 2: every failed act ends as a wait that only S2 or the coordinator ends. Next: `sheepdog factory status` takes each rule's stage from a hand-kept map, which has no entry for N13, N32, N37 or N40, so their fresh lines show as stale; the stage belongs to the row (design.md M9), PR 5a.0 |
| How did the tracks work? | Only the core track ran: the front end waits on slice 6's asks and the adapters track is done except C3. 8 PRs in about 6 h 35 min (#705 to #713), merges one at a time, each with a live demo on its final head |

Where the gaps found in slice 4 went: the text-only ones are fixed in design.md (N38, 7.3); the two with no owner slice are open items O18 and O19 there; everything slice 5a owns is in its PR table below; the git version that `merge-tree --write-tree` needs is a note in slice 8 (doctor). The pictures of section 9 are still not redrawn.

**Review point 5a.**

| Question | Answer |
|---|---|
| Real lines per PR against the estimate | The 17 squash commits on main added 3,551 code lines and netted 2,390, or 2.45 and 1.65 times the 1,450 estimate. The planned core rows alone added 2,378 and netted 1,475 (1.64 and 1.02 times). The other lines came from work brought forward from 5b, doctor work and the two cross-PR fix PRs. The largest PR netted 298 |
| Review rounds each PR took, and what the reviewers found | The rounds are in the log above. Codex read 5a.0 to 5a.6 together and raised 17 issues. An Opus subagent checked each. Six were confirmed, ten were partial and zero were refuted. 5a.8a fixed seven. 5a.8b fixed seven minors and made the deletions. Three findings carry forward. The 5a.7 invariant checks found two production bugs in tests. A recap left a reviewer running and a diverged PR head kept its reviewer |
| What the demos showed that the tables did not expect | (1) Claude Code folds the output of a long command. Only the stripped timer changes, so R21 interrupts the worker. Count a live foreground child as activity in 5b. (2) J6 correctly held a killed coordinator's restart for `coordinator_gap_minutes` (10). The demo used 1 to show the replacement. (3) `hc ls --json` carries no labels. Demo scripts use the engine's J7 log and `hc get`. (4) The sweep flagged the coordinator as stray. #724 fixed it in its own demo rerun. (5) Status flashed "round 3". 5a.0b fixed it |
| Its review question: how many restarts did a night's run need, and which of them were the engine's own doing? | The demos give no overnight count. The demo script killed a worker and a reviewer in the 5a.2 demo (`hc kill`). The engine brought each back once and the task merged. In 5a.6 the script killed the coordinator and the engine started one replacement. Those recoveries followed deliberate kills. The R21 interrupt during live work came from the engine. 5a.7 tested daemon restarts after every step of four histories |
| Did the demos run without a hand on a terminal? | The engine needed none. The owner side was scripted: the kills, and one setting changed before the first attempt (`coordinator_gap_minutes` 1, to show the replacement within the demo) |
| Which "leaves out" items turned out to be needed now | The demos need a settings command. The 5b pre-check found no CLI for the start settings the owner confirms. The foreground-child signal and dialog keys carry into 5b |
| Which stub or left-out item hurt most | R21 uses the screen as its activity signal. Folded output made a live worker look quiet, so the engine interrupted it |
| How did the tracks work? | #719, #722 and #725 were on adapters and doctor. The other PRs were core. Most writers used Gemini Neon, Carbon or Hydrogen through agy. Codex wrote 5a.8a and 5a.8b. Claude Sonnet and Opus subagents reviewed the earlier work. The owner chose to spend Codex quota on second reviews starting with 5a.7 |
| What changed in the review process | The first fix round of 5a.7 dropped the `coordErr` guard. The 5a.8a merge brief narrowed a fix that had already passed review. Review every round and every merge against main, including changes outside the latest delta. Keep the Go cache under the system temp directory. A cache inside the worktree trips the privacy check |

Where the gaps found in slice 5a went. Cross-PR findings #3, #15 and #17 and the R21 foreground-child idea are open items O21 to O24 in design.md for slice 5b. J6's wait text was hidden when a stop error was shown; #733 (5a.8b) now shows the allowed time with the error.

Other minors carry to slice 5b or later. A recurrence while its wake is held merges into that wake. An I1 whose text includes heads wakes again when `H` moves. The parked-task check fires only after exhaustion. A coordinator stop on J4, J8 or J9 returns silently after its budget is exhausted and its one wake is sent. Start with an empty goal sets `NewGoal`. A new goal served by reusing an open task never clears. A goal changed twice keeps the first held wake text. Stops skip a terminal with our run label and a different creation time. Workers and reviewers are still adopted by name.

**Next.** Follow the PR table in [slice 5b](#slice-5b-leave-it-alone-the-machine).

## 8. Risks

| Risk | Sign | Response |
|---|---|---|
| The skeleton's stubs harden into the design | A later slice builds around a hard-coded value | Each stub is listed in section 3 with the slice that removes it; the review point checks the list |
| Row order hides a problem | A task sits on a row that names the wrong thing to wait on | The histories as tests; a review of the table at each review point with real rows |
| Sizes are wrong again | A slice's real lines differ from its estimate by more than half (slice 1: 1.15 times) | Re-plan every later slice from the real cost per row before going on |
| A slice is too big | Its PR plan passes eight PRs | Split it at the review point before it, as slice 5 was split into 5a and 5b |
| A spike fails (the report tag cannot be read, a browser cannot hold a ticket) | The spike's fixture | The fallbacks are named in design.md 9.3 and 7.1; decide before that slice is planned |
| A real pitfall was lost in extraction | A field failure the knowledge base does not list | Add the row and the behaviour test first, then fix; the tag keeps the old tests readable |
| The engine builds itself and a bug stalls it | After slice 8 | Engine work stays hand-run until a week passes with no intervention |

## 9. Pictures that lag the tables

The review ladder has 13 rows since the second review (L10 is the reject row). `transitions.html` and the "Engine" page of the design canvas (the EngineReviewLadder board) still show 12. They were not redrawn at review point 1 or at review point 2. Slice 2 changed L8, L10 and L12 in code, so the redraw is due now, from design.md as it stands after review point 2; it is not done in the review point 2 docs. It was not done at review point 3 either, and is still due.
