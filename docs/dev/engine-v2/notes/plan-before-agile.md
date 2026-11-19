# Engine v2: build plan and cutover

2026-11-18. How to build the engine in [design.md](../design.md), smallest first, and how to replace the old one. Sizes are lines of Go without tests and are estimates. Rule ids (N1, J3, R12, A2) refer to the tables in design.md.

## 1. Rules for the build

| Rule | Why |
|---|---|
| Tables before code. A change starts by editing rows; the generated table document shows the reviewer every changed row | The last three engine tasks grew 10 to 20 times because behaviour had no single table |
| Every PR is at most 300 lines of code and ends in a state that can be tested | "Size tasks as one complete change"; a first overrun is a question, not a target |
| Each package has the budget from design.md section 13. Passing 1.5 times it stops the work and asks | The engine obeys its own size rule |
| Every behaviour test names a `KB-…` id | The knowledge base replaces the old tests |
| Unverified facts (design.md section 17.1) are checked by a spike before the code that leans on them | "Test the real thing, not a fixture you wrote" |
| No flag that keeps two engines alive. New code is unused until one switch PR | Migrate callers, then delete, in one wave |
| Get the inner loop right before automating more of it | Automating a loop that produces poor work produces more of it |
| The tables are run before they are trusted, and run for real early: a real agent at PR 13 and a real merged PR at PR 22, before the rest of the rules are written | Three careful readings of the design each found new faults in the tables (design.md section 17.5). Reading does not converge; running does |
| A gap found in review is sorted first (version 1, later, not carried), then built or not | Fixes for review findings are where designs grow most; the first design check produced 181 gaps |

**Who builds it.** A hand-run orchestrator with Codex or Carbon workers and an outside review per PR, starting each PR from the rows it implements (the first review agreed; the owner agreed with its answers). The new engine does not build its own code until it has run a week on other work with no intervention.

## 2. Step 0: spikes (before any engine code)

Each is a short script run against real CLIs in a private herdr-core, with the captured output saved as a fixture. About a day in total.

| Spike | Question | Decides |
|---|---|---|
| S0.1 | Does agy report status to herdr-core like Claude Code and Codex? | Whether agy can be a worker without screen reading |
| S0.2 | Where does each CLI's session id come from under herdr-core, and does a resume keep model and effort? | resume |
| S0.3 | Can the last message of a Codex and a Claude reviewer be read whole from the terminal, with a one-time code in it? Can each run a build in a scratch copy while unable to write the branch? | The report tag or the session-file fallback; what "read-only" means per CLI |
| S0.4 | Does the daemon run under launchd here, and can `hc exec` read a project folder the daemon cannot? | design.md section 2.4; whether the volume probe is needed |
| S0.5 | The sheepdog repo's branch protection, required reviews and merge-queue settings (`gh api`, read-only) | Whether merge-queue handling is built at all; the doctor checks at start |
| S0.6 | A real PR snapshot for each checks class, captured as JSON. Is gh's JSON cut short by hc's exec result limit? | The rollup's fixtures; how gh output is fetched |
| S0.7 | Time to run a real task's checks in a clean checkout of the sheepdog repo | Whether evidence per task is affordable (increment 1) |
| S0.9 | Can a browser on this computer enrol with the daemon and hold a session ticket (the `sheepdog_session` cookie), as a paired phone does? | Whether the factory web page can answer gated asks (design.md 7.1, D15), or shows them read-only |
| S0.10 | Can the supervisor's existing card (one line, 2 to 9 options) show an override of two tasks with their commits and check states? | How version 1's exit test answers an override before the web page exists |
| S0.8 | What herdr-core can tell the engine: output activity, child processes, "the owner typed", "the owner quit it", and whether killing a terminal ends its whole process group | Which run rows can be built now, and which wait for an hc change |

## 3. Version 1: one project, end to end

**Size.** About 8,000 lines of Go without tests, in 34 PRs of at most 300 lines each. The estimate has been raised three times ("about 3,600", "about 4,000 in 14 PRs", "about 6,000 in 23 PRs"); each time a reviewer redid the arithmetic on design.md section 13's own bases and found PRs of 500 to 900 lines. It is still an estimate. Two checkpoints compare real size with it and re-plan: after PR 4 (the first rule rows: is a row about 20 lines?) and after PR 12 (the loop, store and report path). The rule "stop at 1.5 times" applies to every PR.

**What version 1 does.** `sheepdog factory start` in a GitHub repo with CI and a PR workflow: doctor, a coordinator (Claude Opus), a plan of tasks with dependencies, scope fields and outcomes, workers (Claude Code) that prove each outcome before they report, engine-run review by another model (Codex), two passing rounds, green CI, squash merge, cleanup, owner asks as cards through the existing phone path, the owner's override, stop and start again, the factory log in the terminal. It survives a daemon restart at any point.

**What version 1 leaves out,** each with what happens instead:

| Left out | In version 1 |
|---|---|
| The engine running each task's proofs itself | Outcomes and how they are proved are required in the plan (PR 2); the worker's `ready` report runs each proof in its worktree and is refused if one fails (PRs 5 and 12); the reviewer re-runs them. What waits for increment 1 is the engine running them again in a clean checkout, and a per-outcome judgement in the verdict |
| The retro round | The ladder goes from review to recap: the coordinator picks split, new or drop |
| Plan review by another model | The plan checks only; increment 1a |
| Coordinator rotation and the journal | One long session; the engine restarts it from the store |
| agy; a second worker CLI | One worker CLI and one reviewer CLI |
| Milestones, critical path, digest | Tasks and dependencies only |
| Changing a setting | Settings exist with defaults and ranges, only those version 1's rows use. Changing one (a `rule` card the owner answers) is increment 4. There is no terminal shortcut: a terminal is never taken for the owner |
| The approved phone boards and the factory web page | The phone shows asks as the supervisor's existing cards; start, log and status are terminal commands. The web page comes first and the phone after it (section 4, steps W1 to W3) |
| Alerts to push and voice; the volume probe; the sweep; merge queue; owner touch; background-job detection | Alerts are raised and cleared and shown in the log; the rest waits for its spike or its increment |

**PRs, in order.** Rule ids are those of design.md. Two stages. A real agent is started at PR 13; the first real merge is at PR 22. (Earlier drafts promised a real merge at PR 6, then PR 14. With PRs honestly under 300 lines the thin slice is 22 PRs. Saying so now is better than the first builder finding out.) The "Lines" column is an estimate for that PR, on section 13's bases.

*Stage A: the thinnest thing that merges a real PR.*

| # | PR | Lines | Done when |
|---|---|---|---|
| 1 | `plan` (a leaf): the plan file, shape checks (unknown field, unknown dependency, cycle), layers | 200 | A plan with a cycle or an unknown dependency is refused; layers are right for a sample plan |
| 2 | `plan`: the four scope fields, **outcomes each with `proved_by`**, the size estimate and limit, the title check. `flow`: record types | 300 | A task with a missing scope field, no outcome, an outcome with no proof, or an estimate over the limit is refused, each with its own reason |
| 3 | `flow`: head relations and `covers`, the first-match loop, the table printer, the settings table with ranges (version 1's settings only) | 200 | `covers` passes its cases; a value outside its range is refused; the printer's output is checked against the doc |
| 4 | `flow`: the slice's rules (N1, N2, N5, N9–N11, N23, N26, N29, N36, N42, N43) and ladder rows (L7–L9, L13). **The happy-path history of `notes/table-audit.md` is written first, as a failing test.** Size checkpoint 1 | 300 | The history passes: the expected row at every pass, nothing asked twice, nothing on "no rule matched" |
| 5 | `flow`: the acceptance lines for `ready` (a proof or an artifact per outcome, `touched`, `skipped`), `ready --reject` with ladder row L10, and `verdict` (with `self_check_misses`, `class`, `deletable`, `not_run`); run tokens and the role check | 250 | A `ready` with an outcome unproved is refused; a reject of a reviewer's round leads to another review; a reject of a send-back is refused; a verdict from another run, or text without the one-time code, is refused |
| 6 | `flow`: the run table, `starting` rows | 300 | Every `starting` cell of the matrix has a case; a blank start and a start stuck at a first-run screen end as the table says |
| 7 | `flow`: the run table, `working` rows | 300 | Every `working` cell has a case; a quiet run is interrupted once, then ended |
| 8 | `flow`: the run table, `idle`, `blocked` and `ended` rows; the matrix check | 200 | The matrix has no empty cell; a run blocked at a dialog ends as stuck |
| 9 | `flow`: "making sure a run is alive"; `Resolve` | 250 | A candidate that is not signed in is skipped with its reason; a run that ended on a usage limit moves to the next candidate or waits for the reset |
| 10 | `internal/factory`: the store and the clock | 200 | State survives a restart; a sleep gap does not fire every timer |
| 11 | `internal/factory`: the loop, keys, back-off and giving up | 300 | A restart after every step of the history gives the same end; a failing executor ends at N5, not in a loop |
| 12 | `internal/factory`: the report endpoint and run tokens; the report client, which runs each proof command in the worktree and keeps exit code and output. Size checkpoint 2 | 250 | A report with another run's token is refused; a proof that exits non-zero refuses the `ready` report |
| 13 | `world`: start, send and stop over herdr-core, ending the whole process group | 300 | **A real program is started in a private herdr-core, sent text and stopped, leaving no process** |
| 14 | `world`: the Claude Code adapter (argv, trust pre-set, screens, session) | 300 | Golden argv per role and continue kind; a real Claude Code agent takes a prompt and reports idle |
| 15 | `world`: the Codex adapter for the reviewer | 300 | Golden argv; a real Codex reviewer starts read-only and ends a turn |
| 16 | `world`: the start watcher; the report tag reader with its one-time code | 200 | A real reviewer's tag is read whole from the terminal; a tag without the code is not a verdict |
| 17 | `world`: the hc observer (status, exit, loss, and an outage that is not a loss) | 200 | With herdr-core stopped, no run is declared lost |
| 18 | `world`: git, part 1: a worktree from the remote base, push with an explicit branch, cleanup | 200 | Tests on temporary repos; a dirty worktree is never removed; nothing is ever force-pushed |
| 19 | `world`: git, part 2: the tips observer, head relations, `merge_only`, the review diff | 200 | The four relations and the delta kind are right for sample histories; the diff base is the merge base computed now |
| 20 | `world`: gh, part 1: open or adopt a PR; the snapshot | 300 | A fake `gh` drives N26; every call names the repo; a closed PR is not adopted |
| 21 | `world`: gh, part 2: merge; the checks rollup for green, red, pending and absent | 250 | A fake `gh` drives N36 and N42; "no checks" never merges; only-skipped is not green |
| 22 | `sheepdog factory start, stop, log, report`; a first coordinator, worker and reviewer template | 200 | **First real merge:** on a scratch GitHub repo with CI, with real CLIs, one task goes from plan to merged with nobody typing in a terminal, and the log names the rule behind every step |

*Stage B: widening to the full version 1.*

| # | PR | Lines | Done when |
|---|---|---|---|
| 23 | `flow`: the held and limit rules (N3, N4, N6–N8, N12–N14, N20, N22, N25) and the ladder's recap rows. The other audit histories first, as failing tests | 300 | Every history passes; the SDF-16 and SDF-31 round histories end in a recap at round 3 |
| 24 | `flow`: the landing rules, part 1 (N30–N35) | 200 | Histories: a conflict, red then green on a re-run, red twice, checks that never start |
| 25 | `flow`: the landing rules, part 2 (N36–N41) | 200 | Histories: behind the base, a required review, a red base, a merge GitHub refuses |
| 26 | `flow`: the full checks rollup, PR adoption and `merge_only` tables, with the S0.6 fixtures | 250 | One case per rollup class |
| 27 | `flow`: the override (N15–N19b, ladder row L1, the `override` record with `spent`); `sheepdog factory ask --kind override` and `sheepdog factory override` | 250 | An override yes at one commit does not pass a later commit; checks worse than the card showed are not merged and the override is spent; a delegation rule cannot answer it; a worker's terminal cannot raise it |
| 28 | `flow`: the ask table; the acceptance lines for asks, answers and the coordinator's commands | 200 | A coordinator's accept is refused; a gate ask whose commit is gone is cancelled |
| 29 | `flow`: project rules and alerts (raise and clear) | 250 | A coordinator that keeps dying raises one alert; a stopping alert clears by itself |
| 30 | `world`: the pools and sign-in observer | 150 | An empty pool stops the factory and it starts again at the reset |
| 31 | The `Engine` interface; move `AnswerProof` out of `contract.go`; the supervisor takes the interface for asks | 300 | An ask raised by the new engine is answered from the phone with the daemon's proof. **A local answer with no session ticket to a gated ask is refused**, as today's guard does |
| 32 | Doctor: remove the engine and Python checks and the layered settings file; add the start checks (a PR workflow exists, no required approving review, branch protection on the base) | 250 | A repo with no PR workflow is refused at start with its fix line |
| 33 | The templates of [prompts.md](../prompts.md) section 5 (coordinator, worker, reviewer) with replay evidence for its section 7 situations; the coordinator commands | 250 | Each template sentence that states a rule cites its source; every command a template names exists; each is 40 lines or fewer |
| 34 | The switch (section 5, C3) | net negative | The old glue is gone and the exit test passes on the new engine |

**Exit test for version 1.** On a scratch GitHub repo with CI, with real CLIs: one goal, three tasks with a dependency. During the run: one forced review change; one reviewer that exits with no report; one owner ask answered from the phone; one override of two independent open PRs (if spike S0.10 shows the existing card cannot list two tasks, the override is raised once per task, or steps W1 and W2 land before this test); the base turned red once; one worker ended by a usage limit; one daemon restart mid-task; one worker killed by hand; one PR merged by hand. All tasks end done with nobody typing in a terminal, and the log names the rule behind every step.

## 4. Increments

Each is independent unless "needs" says otherwise. Sizes are code lines and are estimates.

| # | Increment | Design sections | Lines | Needs | Exit test |
|---|---|---|---|---|---|
| 1a | Plan review: the plan reviewer run and its template, `plan_review_rounds`, the milestone design note ([prompts.md](../prompts.md) 3.2, 3.3) | design.md 11.7 | 250 | — | A plan with an "optional" backlog task comes back with "delete"; a plan that changes saved state with no design note comes back with "design first" |
| 1 | Evidence: the engine runs each task's proofs again in a clean checkout (N21, N27), and the verdict gains a judgement per outcome | 11.6 | 400 | S0.7 | A task whose own check fails goes back to the worker with no review round spent; the accept bar needs passing evidence on the final head |
| 2 | The retro round | ladder L2–L6, L11; N28; retro template | 300 | — | The SDF-16 history draws one retro at round 3; the second FIX ends in a recap with no `continue` |
| 3 | Coordinator rotation and the journal | 11.1 | 250 | — | After 4 hours a fresh coordinator continues from the entry; an entry exists with facts even when the coordinator does not answer |
| 4 | Settings changed by the owner; delegation; rule read-back | 10.1; A1 | 350 | — | "Review rounds 1 for this project" is read back, saved with the owner's proof, and used by the next pass; `sheepdog factory set` only raises the card |
| 5 | Alerts to push and voice; the digest | section 6; 12.1 | 250 | — | An empty pool is spoken once, and the restart is not; the digest names a PR merged outside the bar |
| 6 | Foundation check at start; the sweep; doctor's "Running now" | 11.8, 11.3 | 300 | S0.4 | A repo with no lint or tests gets a foundation milestone first; a stray terminal is reported |
| 7 | Milestones, the verify task, critical path | 11.6, 11.7 | 450 | 1 | A milestone's verify task re-runs its tasks' checks on the merged base |
| 8 | agy | adapter row, sandbox wrapper for read-only runs | 400 | S0.1 | An agy worker and an agy reviewer each complete a task |
| 9 | Design loop | 11.4 | 250 | 4, 7 | A UI task cannot start before its design task is done |
| 10 | Cheap sync and a cap on PRs in CI; merge queue if S0.5 finds one | 17.3 | 300 | 1 | A behind, conflict-free PR merges after a local test merge passes the task's checks |
| 11 | Secrets | 17.3 | 300 | — | A worker asks for a secret; the owner types it at the Mac; the value never reaches a command line or a log |
| later | Token budgets; machine load holds; background-job detection (needs an hc change, S0.8); simulator and device leases; other machines; plan fields (`touches`, "not before", contract tasks) | 17.3 | — | a project that needs them | — |

**The owner's surfaces: web first, then the phone (owner decision, design.md D14).** Neither the web client nor the iOS app has any factory code today.

| Step | What | Lines (a rough estimate with nothing to calibrate against) | Needs |
|---|---|---|---|
| W1 | Daemon endpoints for the factory section, open asks and the log, read from the `Engine` interface; browser enrolment if S0.9 shows it is missing | 250 Go | PR 31, S0.9 |
| W2 | One more page in the daemon's existing web, beside setup, voice and debug: a factory page with project state, tasks by layer with stage and why, the ask and override cards with their answers, and the log. Same content as the approved boards, in plain JS | 400 JS | W1 |
| W3 | The approved iOS and iPad boards (space-page section, the card on Agents with a badge, the start row, log and debug) | not estimated | W2 in use |

W1 and W2 can land before the switch PR, so version 1's exit test can answer its ask and its override from the web page. Without them it uses the supervisor's existing cards (spike S0.10).

**The factory page answers asks once the browser holds a session ticket** (owner decision D15, design.md 7.1). What the proof has to tell apart is a person at an enrolled device from a program on the same machine: workers and the coordinator run as the owner's user and can reach the daemon's local address. A local request with no ticket is never the owner, whatever origin it claims; the second review read the setup page's origin check and found that it stops another website in the browser, not a local program. So:

- The browser enrols with the daemon once and holds a session ticket, as a paired phone does. Its answers then count for every ask, the never-delegable kinds included. Spike S0.9 checks that a browser on this computer can do that; if it cannot yet, W1 adds it, and until then the page shows gated cards read-only.
- Stated plainly: this stops an agent taking a shortcut, not a hostile process running as the owner's user. The check after the fact is the log and the digest, which name the device that answered each override and rule change.

Order I recommend for the engine: 1a first (planning quality decides everything after it: the tasks that grew 10 to 20 times were planning failures), then 1, 2, 3 (evidence, the retro and rotation are what make it trustworthy and cheap to leave alone), then 4, 5, 6, then the rest.

## 5. Cutover

**Where things stand** (from the 2026-11-18 journal; not re-checked by me):

- All sheepdog-factory engine work is paused. The old coordinator and the hourly wake are still alive and should only report the pause.
- Paused tasks keep their worktrees, branches and records. PR #672 (SDF-74) is open.
- The engine that is installed and running is a separate copy under the owner's home, not the tree on main.
- *Unverified:* whether other projects run on that same installed copy.

**Steps.**

| Step | What | Who decides |
|---|---|---|
| C0 | Freeze. The old factory starts no engine tasks. Turn off its hourly wake for the sheepdog space so it spends nothing | owner or coordinator |
| C1 | Build version 1 (section 3, PRs 1–33) in the new packages. Nothing in the daemon calls them yet, so every release in between is unaffected | — |
| C2 | Prove it: the exit test of section 3, and the knowledge-base behaviours mapped to version 1 rows all have passing tests | — |
| C3 | The switch, one PR: the supervisor takes the `Engine` interface in place of the CLI client; delete `client.go`, `engine.go`, `python.go`, `tick.go`, `factory_embed.go` (at the repo root; it embeds `factory/`, so the tree and this file go together), the loopback proof route, the `python-*` commands, the Makefile target and the CI job; update `owner-contract.md`, `CONFIG.md`, `architecture.md` and the glossary; change the board text that says "engine and Python" | owner approves the board text |
| C4 | The old `factory/` tree leaves main | **owner** (below) |
| C5 | Open work from the old store | owner or coordinator (below) |
| C6 | Other projects on the old installed copy move when increments 1, 2, 5, 6 and 7 have run on sheepdog for a week | owner |

**C4: how the old code leaves main.** Decided by the owner on 2026-11-18 (design.md D12): tag the last commit that has the old engine (`4df3a038` today), then delete `factory/` and the Go glue in the switch PR (C3). The tag keeps every file and line that knowledge-base.md cites reachable. Deleting the tree earlier does not compile, because `factory_embed.go` embeds it.

Before C3, two things are checked and written down: whether any other project runs on the installed copy of the old engine, and whether the daemon without `python.go` can still drive that copy. A revert of C3 restores code, not state: the new engine's state lives under `<data_dir>/factory/projects`, the old one's under `<data_dir>/tasks`, and terminals started by the new engine would be left without an owner.

**C5: open work.** No importer is planned. The old store's shape is the thing being replaced, and most open SDF tasks patch the old engine and are now moot.

- Save one export of the old task list as a reference file.
- The new coordinator re-plans what still matters from that list and the approved addendum.
- PR #672, and the paused tasks' worktrees and branches, are closed or removed only after the owner says so.

**Rollback.** Before C3 there is nothing to roll back. After C3, revert the switch PR; the old code is in history and under the tag.

## 6. Risks

| Risk | Sign | Response |
|---|---|---|
| Row order hides a problem | A task sits on a row that names the wrong thing to wait on (design.md O6) | The histories as tests; a review of the table at PRs 4 and 23 with real rows |
| The rule conditions grow helper functions that hide logic | Many knowledge-base items map to a helper, not a row | Turn the helper's cases into rows; the mapping count is reviewed at PRs 23 and 33 |
| Version 1 overruns like the tasks it replaces | A PR passes 300 lines, or a package passes 1.5 times its budget | Stop and ask whether the premise is wrong; do not send it for more review |
| A spike fails (the report tag cannot be read, agy reports no status, task checks are too slow) | Step 0 | The fallbacks are named in design.md section 9.3 (the report tag), increment 8 (agy) and increment 1 (slow checks: evidence per milestone only); decide before PR 13 |
| The approved UI needs a fact the views cannot derive | PR 31 or the owner-surfaces track | Add the fact to the entity that owns it; never a second copy |
| A real pitfall was lost in extraction | A field failure the knowledge base does not list | Add the row and the behaviour test first, then fix; keep the tag so the old test can be read |
| The engine builds itself and a bug stalls it | Increment 4 onward | Engine tasks stay hand-run until a week passes with no intervention |
