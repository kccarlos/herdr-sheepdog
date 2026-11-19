# Engine v2: independent review 1

2026-11-18. The reviewer did not write the design. Read-only review of design.md, plan.md, critique.md, knowledge-base.md and notes/ (table-audit.md, map/). Three Opus sub-agents did the cross-checks (two traced knowledge-base samples, one measured the Go code); the safety walks in section 2 are the reviewer's own. Row ids are design.md's.

**Verdict: CHANGES.** Form B is sound and should stay. Two blockers (B1 the owner-accept path, B2 the owner's proof on `factory set`) and four majors that must also change before code starts (M0 the red-main hold, M7 the package graph, M8 the size budgets and PR sizes, and the missing version 1 work in section 5). None needs a new mechanism; each is a row, a field or a re-budget. Ranking rule used: a blocker is a concrete history that accepts, merges or changes a rule without the required proof; a wait that has one wake but no named exit is a major.

What was not checked: the 476 knowledge-base rows outside the sample; the 42 faults of notes/table-audit.md were not re-walked one by one against the fixed text (only where a history crossed them); critique.md beyond its headings; `transitions.html`; nothing was run. The "five open questions" are not listed in any file under engine-v2/ or in today's journal; section 6 answers the five the documents name (owner questions 2, 3 and 4, A8/O1, and C4).

## 1. Soundness of form B (check 1)

Sample: 123 of 599 behaviours (58 in launch, runs, lifecycle, coordinator, quota, worktrees, environment; 65 in review, recap, retro, merge and CI, git, asks, alerts, settings). Stratified with a fixed seed, and it includes every "GAP fixed after the check", every CONFLICT and every TEMPLATE row in those areas. Each row was traced from design.md first and only then compared with "Maps to".

| Result | Count |
|---|---|
| Covered: a named row or executor holds when walked | 63 |
| Deferred, and version 1 is safe without it | 20 |
| Weak: the text is there but a walked history breaks, or a word is undefined | 28 |
| Gap | 2 (KB-MRG-65, KB-ASK-9) |
| Deferred but risky in version 1 | 1 (KB-MRG-64) |
| A safety rule left to a prompt | 5 (KB-COO-41, -55, -61, KB-WKT-24, KB-ASK-24) |
| Designer's mapping wrong or stretched (4 only that; 5 more overlap the rows above) | 4 |

Reading: no sampled behaviour needs a stored phase, so records plus first-match rows can express the knowledge base. The 28 weak rows are not in the N or D tables. They sit in the places that are not tables: the 4.4 preamble (nudges, dialogs, "pull first"), 4.8 (a third first-match table), 4.10 (scheduling), "make sure of" cells that branch inside (N23, N27, N28, N29, N33, N34), and undefined words ("working time", "a task the owner just answered", "a needed role", "gated kind", "delta", "child activity", which quota window). That is where logic is piling up. Of nine "fixed after the check" rows re-walked in sample B, four still break (KB-REV-38, KB-CAP-4, KB-CAP-12, KB-MRG-3).

Weak or gap rows with their history (the ones not already in section 2):

- KB-MRG-65 (gap), KB-MRG-64 (risky): a docs-only PR in a repo whose workflow has `paths-ignore`, or a repo with no PR workflow, gets no checks. N29 dispatches, then wakes the coordinator once. The coordinator has no command that helps; the owner's override still needs CI. Exits: hand merge or drop. The foundation check that would catch a repo with no CI is increment 7.
- KB-ASK-9 (gap): 7.2 `ask` does not refuse a recommended "No" with no "Yes", or option text the kept supervisor parser reads as a key ("Switch (v2)").
- KB-TSK-6: `plan amend` on a task that passed. An amended brief does not void passed rounds, so N35 merges code reviewed against the old brief. An added outcome makes N22 false (some evidence covers H) and N35 false, so the task lands on N36.
- KB-TSK-17 and a table fault: `unpark` and every 4.8 restart skip N9 and N10. With `max_running` 2 and two workers busy, an unpark starts a third.
- KB-CAP-12: c1 to c3 red within the hour, c4 green and passed; N13 sits above landing and opens a `ci` recap on a green head.
- KB-CAP-4: N14 "working time" is undefined; read as wall clock it opens a `time` recap on a passed task waiting on CI or on the owner.
- KB-RUN-7: N35 and N34b read run status only. A worker whose turn ended with a background build is merged and Cleanup kills the job.
- KB-RUN-8: the agent dies under a shell; the terminal lives, no `exit` comes, R30 never fires. The run is `working` until R19 and R20 (about 21 min), holding a slot and N34b.
- KB-LCH-9, KB-LCH-14: `Screen` returns `onboarding` but R3 answers only trust and confirm, so a first-run screen ends `blank` until N5. R7 sends the first prompt on any idle, even with a trust dialog up.
- KB-REV-4, KB-REV-63: "read-only on the branch; may build and run in a scratch copy" has a mechanism for agy only. For Claude and Codex it is either plan mode (cannot run checks) or the owner's own allow list (can write).
- KB-REV-12, KB-REV-13: "reviews the new head as a delta" is not defined; the review diff base is nowhere. After the worker merges the base, the delta carries the base's commits (the FAC-15 failure).
- KB-REV-26, KB-RET-13: the tag reader's tolerance (code fence, wrapped lines, an incomplete read) is not stated; each miss is `invalid_report` and spends `reviewer_retries`.
- KB-COO-4: nothing sets the coordinator's working folder (field note 55b).
- KB-COO-27: blank starts with a healthy folder are "ordinary failures", so an hc fault that starts every agent blank latches every task at N5 with no outage alert.
- KB-QTA-9: which window (5-hour or weekly) meets `pool_floor_percent` is not said.
- KB-COO-23, KB-COO-24: "not interrupted yet" has no reset; "child activity" either kills a quiet 25-minute test run or lets a resident MCP server keep a dead run alive.
- Table faults also found: J4 sits above J7 and matches when the last task merges, so a finished project that is started again can never get a coordinator to add work. `no_pool` stops the whole factory when one role's pools are empty. R17 does not reset the idle timer after sending, so R18 can record a false turn end. R14 wakes the coordinator at once while 4.4 says after `background_job_minutes`. A run blocked at auth inside `start_minutes` stays `blocked` (R27) and never falls to the next candidate. A send-back is "round-like", but whether it counts for D8, the D10 streak and `max_rounds` is unsaid. The clean checkouts of 11.6 and the reviewers' scratch copies have no removal rule.

## 2. Safety (check 2)

| History | Result |
|---|---|
| Crash mid-merge | Clean. N35 writes the accept and asks; a crash re-lands on N33 or N2; only a snapshot ends the task |
| Red main | The hold has no exit: finding M0 |
| A reviewer that stops early | No report: clean (N23, 4.8, N5). With a tag on screen: finding M1. With a lazy pass: finding M2 |
| Two owners' answers racing | Clean for one ask (one writer, A5). **Breaks** across N20's two deciders: finding M5 |
| An agent stuck on a dialog | Reviewer: clean (`review_minutes`). Worker: finding M3 |
| Quota exhaustion | Converges for workers and reviewers. Two holes: finding m3 |
| Restart during a retro | Clean. A retro that cannot run at all: finding M4 |

### Blockers

**B1. The owner's accept (4.9 last row, D1) reaches a merge without the proof the rails require.** Three histories.
- Wrong commit. A size recap is open at H=c2; the coordinator opens "merge SDF-n as it is?". The worker run is still wanted (4.8) and reports ready at c3; 7.2 accepts it, H=c3. The owner taps yes. 4.9 writes "an accept by the owner at `H`", which is now c3. D1 says passed. No round covered c3.
- No review at all. Ready at H, no round yet; the coordinator opens the ask; yes; D1 is the ladder's first row, so N35 merges a commit no reviewer saw. 10.1 lists "never merge a commit that was not reviewed" as a rail that is never a setting.
- Possibly no owner (ambiguous text, not established). The ask is kind `gate`. 7.1's never-delegable kinds are "a paid key, a quota reset, a release with no stated scope, a rule change"; A1 answers a delegable kind by rule. 7.2's `answer` line also says "a gated kind has a verified proof" and has a `not_delegable` code, and "gated kind" is never defined. If a delegated `gate` covers this ask, the coordinator opens it, a rule answers it, and D1 passes. "the coordinator opens it when the owner says so" is a prompt sentence. The text must say this ask is never delegable.
- Same shape for gates: 3.2 scopes "the owner's answer to a gate" to the attempt, and N25 asks only when the gate "has no answer in this attempt". Yes at c2; a send-back or a red check; c3 passes review; N35 merges c3 unasked.
- Fix (three lines of text): an ask stores the commit it was asked at, and the owner accept and the gate yes are records at that commit that use `covers`; the override has its own kind on the never-delegable list; decide whether D1 skips review or only skips the recap, and make 10.1 agree.

**B2. Version 1 changes a rule without the owner's proof (plan.md section 3, "Changing a setting").** "`sheepdog factory set <name> <value>`, which only the owner's own terminal may run (a run's token is refused)". The absence of an environment variable is taken as the owner. Any worker with a shell runs it with the variable unset and sets `review_rounds` to 1. This is KB-ASK-17's old pitfall, and 7.2 requires "the owner's proof verifies" for a rule change. Fix: `set` mints a `rule` ask and saves only on the daemon's proof, or version 1 has no `set`.

### Major

**M0. The red-main hold has no exit, and it holds every task at once (N32; 9.4 "never while the base is red").** The design keeps the old rule (KB-MRG-47 to -50): a behind PR is held, an up-to-date green one merges, so a repair PR that is not behind lands. What is left:
- Base red from a flaky or push-only job. Every open PR is behind the red commit; N32 holds each with "—". No new task is ready. Nothing re-runs the base's failed run, nothing merges, so no new base run ever comes. All tasks wait on "the base branch" for ever.
- Or: a green, not-behind PR Q merges while the base is red; the repair PR R is now behind; the base's new run is red too; R is held and may not be updated.
- This is not a silent wait: the base observer wakes the coordinator on red. But it is one wake, and no row names the exit (`say` the worker to merge the base, after which N16 pushes).
- Fix: N32 past `main_red_minutes` wakes the coordinator once with that exit named, as N7 does; or the engine tells the oldest held task's worker itself.

**M1. A reviewer's report tag is not bound to its run (9.3, 7.2 `verdict`).** The engine reads `<report>{json}</report>` from the terminal when the run goes idle. A reviewer that stops right after printing worker-written text (a diff, a fixture) holding a tag with the right round and commit yields a valid pass. The worker knows its commit and can guess the round. Fix: a per-run nonce in the prompt that the tag must carry.

**M2. "2 reviews" is two rounds run (D8, A3).** Round 1 at c1 asks for changes; round 2 is a delta review at c2 and passes; D9 passes. The merged head has one delta pass and no full pass. KB-REV-8 was N passing rounds on the head. See question 2.

**M3. A worker at a dialog has no engine exit (R14, R24, R27, 4.4).** R14 moves it to `blocked` and wakes the coordinator. `say` is held (R24). R19 and R20 apply to `working` only; `blocked` on `tick` is R27. `retry` only resets counts. The one exit is park then unpark, which nothing names. KB-COO-61 (what the coordinator may approve) is prompt only. Fix: `blocked` longer than a setting ends the run as `stuck`; 4.8 resumes and counts it.

**M4. A retro that cannot run never leaves the ladder (D10, 4.8, N5).** The retro run fails `reviewer_retries` times: N5. `retry`; it fails again. D10 reads only records, so the answer is still "retro"; no recap is open, so split and new are refused. With no retro candidate resolving, the task waits on quota at N19 with no wake. Fix: D10 gets "and the retro has not failed"; otherwise D11.

**M5. N20 opens two deciders for one decision.** The recap goes to the coordinator and an ask to the owner. The coordinator splits; N1 cancels only `decision` asks; the owner ask stays open on an ended task (4.11: "even a done task"); the owner answers "drop" and the children keep running. Fix: either one closes the other.

**M6. Waits with one wake and no command (N29, N31).** See KB-MRG-65. N31: "Nobody in the factory can give the approval". Both should be start-time doctor failures, not per-task dead ends.

## 3. Abstractions and size (check 3)

**M7. The package graph as written has import cycles (section 13).** `flow` "may import: `rules`", `plan` imports `flow`. But 7.2 (in `flow`) runs the plan checks, 4.10 (in `flow`) orders by critical path and open milestone, and the views (in `flow`) use plan layers. Fix: `plan` is a leaf that `flow` imports, or fold it in. (`Resolve` is fine: 9.1 passes adapter data in as facts.)

**M8. About 5,300 lines is not realistic.** Estimate, calibrated on measured code: about 8,500 to 10,000 for the full design and 6,500 to 7,500 for version 1 (plan says 3,600).
- Measured by the reviewer: `internal/factory/contract.go` is 1,385 lines and the old `github.py` is 1,413 (against 450 for gh and rollup). Measured by the sub-agent, not re-checked: about 400 lines of types and enums in `contract.go` before any builder (the views budget is 300); the old launch, agents, sessions and sandbox files are 2,638 lines (against 700). The Python numbers are an upper bound: critique.md calls that code bloated.
- Estimated: about 20 lines per N or D row (named clauses so the log can say which part was false, an ensure, a waits-on, shared helpers) gives 1,300 to 1,600 against 450. The run table has 36 rows, not 34.
- PRs 2, 3, 5 and 12 are each about 800 to 1,000 lines against the 300 cap, so the plan's own "stop at 1.5 times" rule fires four times in version 1.

**M9. Decisions live in two places, and row ids are kept by hand.** The owner's rule is one source of truth and no hand-kept lists.
- 9.4 restates N27 (re-runs), N29 (one dispatch per head) and N32/N34 (no update while red). The checks rollup, the PR adoption rule and `merge_only` are first-match decisions inside `world`, which "holds no decisions"; plan PR 4 already builds the rollup in `flow`.
- The 4.11 stage map and the section 10 wake list enumerate row ids that repeat each row's "waits on". They already disagree: N31 is staged `ci` in 4.11 and is a coordinator wake in section 10. The section 5 matrix restates the R table. Fix: `Stage` and `Wake` are fields of the row; generate the matrix.

Delete or defer for version 1 (saves an estimated 1,500 to 2,000 lines and much test surface): `rules` as its own package (first match is a loop; the ask table is a switch); settings no version 1 row uses (44 numeric and 7 choice today); testing every behaviour at each setting's minimum and maximum (test the ranges); the pair generator and swapped-pair runs (keep one case per row and the histories); production checks of I1 to I5; `archive.json`; the retro counters and routing table of 11.2; the process observer and background-job machinery (hc has no API for it); the volume probe until S0.4; `touch`; merge-queue handling until S0.5 says the repo has one.

## 4. Hands-off (check 4)

| Still needs the owner | Necessary? |
|---|---|
| Start: doctor fixes and five questions | Yes, once |
| Paid key, quota reset, release with no scope, rule change | Yes |
| At the Mac: signed out, consent prompt, disk | Yes; no code can do these |
| The override "merge as it is" | Yes, once B1 is fixed |
| Design gate, `gate: owner`, milestone gate `owner` | Only if set. Say the defaults are the review model and `auto` |
| N20: a split child of a task that had a retro | Reasonable and rare; fix M5 |
| J9: the coordinator is blocked at a question | No. Stop and restart it as J8 does; tell the owner on the second time |
| N31 and N29 at each task | No. Refuse at start instead (M6) |
| `merged_outside_bar` (N2) | It wakes only the coordinator, which may be the one that merged. Put it in the owner's digest |
| A task waiting on the coordinator for hours while the coordinator keeps taking turns | Nothing tells the owner. One digest line |
| N7 after a worker amends a pushed commit | The engine can tell the worker once itself before waking the coordinator |

The coordinator has full permission in the repo and `gh`; "never merge, never commit to main" is prompt only (KB-COO-55, KB-ASK-24). Branch protection is the only code-level rail; make it a doctor line.

## 5. Plan (check 5)

- **Not the smallest.** Version 1 carries evidence (A8, not confirmed, cost unmeasured), the settings table of 10.1 (44 numeric and 7 choice settings; the plan does not say which version 1 leaves out), the full run table, merge queue, sweep and probe. See section 3.
- **Missing from version 1 but needed by its exit test** (major): the daemon endpoint and run-token minting for `factory report` (the loopback route is deleted); the doctor rework for J2 (`DoctorOptions.Engine` comes from `engine.go`, which is deleted; doctor reads the layered `~/.config/sheepdog/settings.json`, which section 14 forbids); `AnswerProof` lives in `contract.go`, which is replaced; replay evidence for five new templates; the supervisor swap, which the phone-answer step of C2 needs but which is in C3.
- **PR 14's "done when" has no work behind it** (major). `ios/Sources`, `ios/Tests` and `web/` contain no file that mentions "factory" today (grep, re-run by the reviewer); asks reach the phone only as the supervisor's generic `fask-` cards. The space-page section, start shell, log and debug of D4 are boards. No PR budgets them.
- **hc gaps are not in the spikes** (major): no output-activity or child-process API, no "owner typed" or "owner quit it" signal, `Kill` gives no process-group guarantee, `ExecResult.Truncated` caps gh JSON. Add them to step 0.
- **First real run comes too late.** Thirteen PRs land before the exit test. The plan's own rule is "reading does not converge; running does". Run a thin slice (N11, N15, N21, N23, N35, N2, one adapter) on a scratch repo by about PR 6, then widen.
- **Cutover.** Landing unused packages is safe. C3 touches `app/wire.go`, the supervisor, doctor, the CLI, CI and docs in one PR: large for one review. A revert restores code but not state (new state under `<data_dir>/factory/projects`, old under `<data_dir>/tasks`; terminals labelled by the new engine are orphaned). Before C3, verify whether another project runs on the installed copy and whether the daemon without `python.go` can still drive it.

## 6. The five open questions

1. **Evidence (A8, O1).** Confirm the mechanism, but not in version 1: it is unmeasured (S0.7) and adds N17, N22, the check runner, outcome fields and per-outcome verdicts. Version 1's bar is CI green plus reviews; evidence is the first increment.
2. **"Tested + 2 reviews" (A3).** `review_rounds` counts passing rounds that cover `H`, the second by another candidate when one resolves. That costs one extra round on tasks that needed a fix and closes M2. The setting can lower it.
3. **Who builds it.** Agree: a hand-run orchestrator with Codex or Carbon workers and an outside review per PR, tables first. The engine does not build itself until a week runs with no intervention.
4. **No CI, or unreadable checks (A1).** Agree: never green, no local-merge mode. But refuse at start when the repo has no PR workflow or requires an approving review, and give N29 an exit for path-filtered PRs (M6).
5. **How the old code leaves main (C4).** Option a: delete in the switch PR, after tagging `4df3a038`. Not b: `factory_embed.go` at the repo root embeds `factory/` with `go:embed`, so deleting the tree first does not compile. Not c: nobody has listed the non-factory commits to re-land.

If the real five differ: O4 (board text "engine and Python") — change the text in the switch PR, the owner approves the wording. C5 (open work) — agree with no importer; export the old task list once and let the new coordinator re-plan. Also: O2 (an automatic third round) is fine. O3 (a sixth alert kind) breaks no client: only the Go decoder in `contract.go` is strict, and it is replaced.

## 7. Minor

- m1. N33 sits above N34b, and its re-ask tests N33's own condition, so a merge retry ignores "the worker run is not `working`".
- m2. N34 sits above N34b: the engine updates the branch while the worker is working; the worker then commits without pulling, the tip has `diverged`, and N7 tells the coordinator to "restore the branch" when a pull is enough.
- m3. 4.8: "unusable until that pool's reset time" is undefined when the reading is unknown. "wait until doctor sees it signed in" still restarts on every pass, uncounted, when the CLI's own check passes and the screen (R4, R13) says signed out (table-audit F6.2, half fixed; KB-WKT-17 applies to Claude and Codex, not only agy).
- m4. 3.1 says an accept "is written at `H` in the same step as the merge"; 4.9 writes the owner accept at the answer and D1 uses "covers".
- m5. Rule ids are out of order (N18 before N15, N33 after N34, J2 before J1), so ranges such as "N31–N35" are ambiguous. Renumber before code.
- m6. J3's "the owner is told once per alert" fires only with no coordinator alive (table-audit F8.4's second half).
- m7. plan.md names `factory_embed.go` as if under `internal/factory`; it is at the repo root.
- m8. The exit test has no red-main, quota or no-report-reviewer step.
