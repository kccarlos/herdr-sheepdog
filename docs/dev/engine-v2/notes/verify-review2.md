# Verification of the review-2 fixes

2026-11-18. An independent verifier. Read-only, except for this file. What I read: review-2.md in full; the three diffs against notes/*-before-review2.md; design.md in full except sections 9.1, 9.2 and 14 to 16 (skimmed); plan.md in full; prompts.md sections 3.2 to 9 in full and 1 to 3.1 by grep. I ran no code. I did not check knowledge-base.md or notes/renumber.tsv.

**Verdict: CHANGES. 0 blockers, 4 majors, 22 minors.** (Revised after a second pass: M5 and M6 were moved down to minors 18 and 19, and M2's history was redone under the strict reading of 7.2.) NB1 is fixed. Of the other findings, four are partly fixed: NM2 (it opens a new hole), NM3 (the arithmetic still does not follow section 13), m1 (now a dead end) and m4 (the cancel happens too late).

## Part 1. Each finding against its fix

| Finding | State | Where it is fixed, quoted | Tested against the reviewer's own history |
|---|---|---|---|
| NB1 | **FIXED** | design 7.1: "**A request from this computer with no ticket is never the owner**, whatever page or origin it claims … This keeps the guard the daemon has today". D15 is in section 0. plan PR 20 done-when: "**A local answer with no session ticket to a gated ask is refused**". plan S0.9. 7.1 also states the threat model ("It stops an agent taking a shortcut. It does not stop a hostile process…") | A worker's shell posts to 127.0.0.1 with no ticket: it is not the owner, so `proof_required`. Fixed for answers to asks. The wording is wider than its scope (minor 18) |
| NM1 | **FIXED** | design 7.2 `verdict`: "(A separate judgement per outcome … becomes a required field with the evidence increment. Until then … the tag of prompts.md 5.4 is the whole report)". prompts 3.5 agrees. N42 already says "(from the evidence increment on)" | The version 1 tag (`code, round, head, verdict, majors, findings, self_check_misses, class, deletable, not_run`) now meets every clause of 7.2: one-time code, round and commit, pass or changes by majors, `deletable`, `self_check_misses`. It is no longer `invalid_report` |
| NM2 | **PARTLY** | 7.2: "`ready --reject "<why>"` … the newest round covering `H` asks for changes; there is no new commit". Section 3: "`reject{head, round, why}`". L10: "it asks for changes, and the worker rejected its findings after it … review". prompts 5.3 and 6 | The reviewer's history, where the worker rejects every finding and makes no commit: the reject is accepted, L10 says review, and the next round runs. Fixed. **Missing:** (a) a send-back or a retro FIX is also a "round-like record that asks for changes", and nothing refuses a reject against one (M2 below); (b) no text says a reject is not a round and does not count toward the streak or `max_rounds`; (c) section 9.4's delta range is empty after a reject; (d) the attempt field list in section 3 has no `rejects[]` |
| NM3 | **PARTLY** | plan 3: "About 6,000 lines of Go without tests, in 23 PRs of at most 300 lines each. This follows design.md section 13's own bases". design 13: "about 6,000 for version 1" | The PR budgets add up to 6,250, which matches the claim. They do not match section 13's bases: at least seven PRs are 1.6 to 2.8 times their 300 lines, and `world` is about 1,400 lines short (M4 below) |
| m1 | **PARTLY** | 4.3b: "A yes writes … an override at that `H`, together with the checks state the card showed". N19: "the checks at `H` are no worse than the card showed". N19b | The card shows green; checks are red at the tap. N19 does not hold, N19b tells the owner, and nothing red is merged: safe. But the task then sits on N19b for ever (M1). There is no order among green, running, red and absent, so "worse" is undefined |
| m2 | **FIXED** | 4.3b: "**The engine fills the card**, whoever asked for it: for each task its `H` … Nobody types a commit. A task with no ready report cannot be listed, and a task whose worktree has work it has not reported is marked 'has unreported work'" | The card is made at c2 and the worker reports ready at c3: the override covers c2, not c3, and the task goes on by the normal rules. Correct |
| m3 | **FIXED** | N18: "GitHub refused because the branch must be up to date: bring it up to date once …". 4.3b: "one result line per card … (a conflict, a newer commit, checks worse than shown, GitHub refused)" | The update gives a merge-only head that the override covers. M1 shows the fresh checks on that head can still fall into N19b |
| m4 | **PARTLY** | N31: "A gate ask whose commit no longer covers `H` is cancelled first" | N31 is reached only once the ladder says passed. While c3 is being reviewed and fixed, the c2 gate ask stays open and the owner can still answer it. That is harmless (the yes does not cover c3) but it is what m4 asked to stop. Fix: cancel the ask on the pass where its commit stops covering `H` (a row above N12, or the ask table) |
| m5 | **FIXED** | prompts 8: "One statement of where each thing lands (plan.md has the same)": PR 2, PRs 5 and 9, increment 1. plan "left out" and increment 1 agree, and design 17.7 agrees | The three documents give the same PR numbers |
| m6 | **FIXED** | Section 3: "`ready{head, proofs[outcome → command, exit, output_path, or artifact], touched, skipped}` … The report client runs each proof command in the worktree itself". 7.2 `ready` agrees | Gap left: the worker template has no syntax for a manual outcome's artifact (m-list) |
| m7 | **FIXED** | prompts 5.1: "Before you submit, ask of every task what could be deleted or made smaller". The text about the plan reviewer moved to "From increment 1a" | — |
| m8 | **FIXED** for `set` | 7.2: "`sheepdog factory set` from a terminal that carries no run's token (a worker's or reviewer's terminal is refused …)" | The same refusal is missing for `sheepdog factory override` (7.2 `ask` row)  |
| m9 | **FIXED** | 7.2: the "plan review report (from increment 1a)" line and "the coordinator's answer to a plan finding" line | The plan reviewer's tag in prompts 5.2 has every field the 7.2 line names. One spelling differs: `design-first` against "design first" |

## Majors

**M1. N19b is a dead end and the override is never spent** (design 4.3b). N19b's condition, "an owner override covers `H` (the checks turned worse…)", holds on every pass as long as `H` stays put. It sits above N20 to N43, so "The task goes on by the normal rules" never happens: the first match is N19b every time. N33, which re-runs a red check and tells the worker, cannot be reached. The worker is never told, so `H` never moves, and no wake goes to the coordinator. 4.11 has no stage for N19b. The m3 path makes it worse: when N18 updates the branch, the new head's checks are `pending`, and whether pending counts as "worse than green" is not defined. If it does, every override of a behind PR on a repo that requires up-to-date branches lands here. Section 3 also defines no override record (with its `checks_shown`) for N15 to N19b and L1 to read. The only record there is `accept{by: override}`, and N19 writes that at merge time.
*Fix:* in section 3, add `override{head, checks_shown, spent}`. N19b sets `spent`, and a spent override covers nothing, so N12 to N19 and L1 stop holding and the normal rules run. State the order: green < pending < red, with absent and unreadable counted as red, and pending allowed after the engine's own N18 update. Add N19b to the 4.11 table, to the 4.9 row and to the heading of 4.3b.

**M2. `ready --reject` against a coordinator `send-back`: 7.2 and L10 disagree, and the L10 reading reopens an "unsafe" fault from the 17.5 audit.** The two texts:
- 7.2: "the newest **round** covering `H` asks for changes".
- L10: "**it** asks for changes", where "it" is "the newest **round-like** record covering `H`". 4.6 says a send-back "is stored as a round-like record" but "is not a review round".

History, under both readings: round 1 asks for changes at c1, then the coordinator sends the task back at c1, then the worker rejects. 7.2 accepts the reject, because the newest *round* (round 1) asks for changes. L10 matches the newest round-like record (the send-back) "with a reject after it", so the ladder says review. Rounds 2 and 3 pass at c1, so L9 says passed and the task merges. The send-back's why was never addressed. The audit called exactly this unsafe ("a coordinator's send-back ignored"). A retro FIX is similar but harmless: L4 sits above L10, so the reject does nothing, and the worker is nudged and then the coordinator is woken.
*Fix (7.2 and L10, one clause):* a reject is valid only when the newest round-like record covering `H` is a reviewer's round; against a send-back or a retro verdict it is refused with `nothing_to_reject`.

**M3. The coordinator template does not know the override** (prompts 5.1 against design 4.3b and D10). 4.3b says "The coordinator can open the card when the owner says so". D10's own case is "I tell the orchestrator to merge them all". Template 5.1 says only "you never merge" and offers `sheepdog factory ask` for decisions. It never says the coordinator may raise an `override` card and cannot answer it, and 7.2 gives no syntax for the ask's kind or its tasks. In version 1 there is no web page, and no PR adds the `sheepdog factory override` verb (PR 14 adds start, stop, log and report; PR 22 adds "the coordinator commands"). So the exit test's step "one override of two stacked PRs" depends on a coordinator that was never told how.
*Fix:* one line in 5.1: "The owner says merge these now: `sheepdog factory ask --kind override --task A --task B`; the engine fills the card; only the owner answers it." Then name the verb in 7.2 and in plan PR 18 or PR 22.

**M4. NM3's re-split still does not follow section 13's bases** (plan 3 against design 13). Section 13 says: "about 65 rule rows and 36 run rows at about 20 lines; adapters … 900 [three]; gh 800; git and worktrees 400; start, deliver, stop 400; observers, pools, sweep 600; check runner and tag reader 200; loop 350; store 200". The largest PRs, on those bases:
- PR 10: the Claude adapter (900 / 3 = 300) plus start, send and stop (400) comes to **about 700**.
- PR 6: the `starting` and `working` run rows (R1–R23, R25, R26, R31–R35) are 30 rows × 20 = **about 600**. At the reviewer's 10 lines a row it would be 300.
- PR 12: the hc observer (about 200 of the 600) plus git and worktrees (400) comes to **about 600**.
- PR 13: gh (800 in full; about 500 thin, with no merge queue) plus the rollup comes to **about 550**.
- PR 8: the loop (350) plus the store (200), keys, back-off and the clock comes to **about 550**.
- Also over: PR 11 (about 500) and PR 7 (7 run rows, 10 rows for keeping a run alive, and Resolve, about 490).

By package: the plan gives `world` 1,200 lines (PRs 10 to 13). Section 13 gives 3,300, less agy (300), the check runner (about 100), the sweep and probe (about 150) and the pools (about 150, in PR 19): about 2,600 for version 1. Version 1 is nearer **7,500 to 8,000 lines in 28 to 30 PRs**. "Stop at 1.5 times" would trip at PRs 6, 8, 10, 11, 12 and 13.
*Fix:* split PRs 6, 8 and 10 to 13 again, or state a per-row basis lower than section 13's and change section 13 to match. Then correct "6,000 / 23" in plan 3, design 13 and design 17.7.

## Minors (22)

1. plan increment 2 cites "ladder L2–L6, L10" for the retro. The retro row is now L11, and L10 is the reject row.
2. No text says a reject is not a review round and is not counted for `review_rounds`, the streak of L11 or `max_rounds`. Add a bullet under the level table, beside the one about send-back.
3. Section 9.4's review diff: "A delta round: the task's own changes since the last reviewed commit". After a reject the head has not moved, so that range is empty. State that a round after a reject reviews the rejected round's range.
4. The attempt field list in section 3 (line 149) has no `rejects[]`, although the `reject{}` bullet defines one.
5. The reviewer template 5.4 has `{brief} {not_doing} {diff_range} {earlier_rounds}` but no `{proofs}` or `{rejections}` (or `{facts}`). Yet it tells the reviewer "Read the worker's proofs…", and prompts 6 says the reviewer is given both.
6. The worker template 5.3 has no syntax for a manual outcome's artifact, so `outcome_missing` cannot be avoided for a look or device outcome. Add `--artifact <outcome>=<path>`.
7. In the 7.2 `ask` row, the owner raises an override "from the phone or `sheepdog factory override`". A terminal is taken as the owner for raising one, and a worker's terminal is not refused (the m8 pattern).
8. The heading of 4.3b, "(rows N15–N19)", and the 4.9 effect "N15–N19" both leave out N19b.
9. The 4.11 stage table has no row for N19b. The table is printed from the rows and checked in `go test`, so as written that check would fail.
10. The wake list in section 10 leaves out N2, which wakes the coordinator once on `merged_outside_bar`.
11. In the 10.1 "Used by" column: `merge_tries` leaves out N18; `background_job_minutes` names N23, but N29 and the 4.4 preamble use it; `checks_grace_minutes` leaves out N40; `review_rounds` names L9, which does not read it. The 10.1 bullet "`sheepdog factory set` raises it from a terminal" also leaves out the run-token refusal that 7.2 now has.
12. D10's "Effect" column, "Sections 4.5 and 4.10 (the override)", should read 4.3b and 4.9. N35's "(section 4.9)" would be clearer as 4.3b.
13. O6 says the order tests are "to be judged at PR 3 of plan.md". Real rows now arrive at PRs 4 and 15, as plan 6 says.
14. Stale text. 17.6 says "**These changes have not been reviewed.** A second review is expected." 11.6 is headed "*Proposed … assumption A8, not yet confirmed.*", against D9 and A8. D9's "Version 1's bar is green CI plus reviews" leaves out the proofs on the ready report. 17.6's M8 row, "about 4,000", reads as current: mark it as superseded by 17.7.
15. plan risk table: "fallbacks are named in … increment 3". Increment 3 is rotation; the fallback for slow checks is increment 1 and for agy increment 8.
16. The plan's L1 (override, so passed) is in no version 1 PR. Name it in PR 18.
17. The exit test's "two stacked PRs": N9 holds a dependent task until its dependency is done, so two dependent PRs are never open at once, and N17's dependency clause cannot fire. Say "two independent PRs", or accept it. If S0.10 shows the card cannot show two tasks, no fallback is named.

18. (Was M5.) 7.1's bold sentence, "A request from this computer with no ticket is never the owner", reads as global. But the 7.2 row "start, stop, say, `touch` | the owner, through the daemon" and plan PR 14 (`sheepdog factory start, stop`) take a ticketless terminal as the owner for start and stop in version 1. So a worker's shell could run `stop`, which holds the project at J2. *Fix:* limit the sentence to answers to gated asks, where its bullet already puts it.
19. (Was M6.) plan PR 8's done-when, "a failing executor ends at N5", needs N5, which is built in PR 15. Move N5 into PR 4, or reword the line. This predates the re-split.
20. Fields the ladder reads that have no home. L2 reads "low confidence", but the retro record in 11.2 has no confidence field. L2 to L5 read "this attempt's" retro, but `retros[]` is task-scoped and has no attempt id. A send-back is a "round-like record", but `round.kind` is only `full|delta|retro`.
21. N6, "(or the owner, while the N25 ask is open)": N25 opens a recap, not an ask. The owner ask is N24, and N24 opens no recap, so the parenthesis can never hold.
22. `sheepdog factory show <task>` (template 5.1) has no 7.2 line. 9.3 lists "reads", but 7.2 says "Not valid, or not listed: refused". State that reads are not checked by the acceptance table.

## Part 2. Consistency

- **a. 7.2 against the templates.** The worker's `report ask`, `report ready --proof/--touched/--skipped` and `report ready --reject` all have 7.2 lines with the same names. The 5.4 tag against the `verdict` line: every required field is carried (code, round, head, verdict, majors, deletable, self_check_misses), and `class` and `not_run` are extra but expected by plan PR 5. The 5.2 tag against the plan-review line matches. Refusals a template does not prepare its agent for: `outcome_missing` on a manual outcome (minor 6); `nothing_to_reject` after a send-back (M2); the coordinator's `not_delegable` on an override (M3).
- **b. The ladder.**
  - History (i), version 1 (no L11): round 1 changes (L13, fix; N22 tells the worker). The reject is valid, and L10 says review (N29, round 2 at c1, with an empty delta range: minor 3). Round 2 changes: L10 does not hold (no reject after round 2); L12 does not, because the streak needs round ≥ `retro_from_round` = 3; so L13, fix. The worker commits c2 and reports ready: L7, round 3 passes; L8, one pass < 2, so review; round 4 passes; L9, passed. The retro (full design) is correctly not reached: rounds 2 and 3 are not both majors. Rounds used: 4, within `max_rounds` 5. The reject is not a round in the record model, but no text says so (minor 2).
  - Worker that rejects for ever: every `changes` verdict carries at least one major (7.2), so rounds 2 and 3 form the streak. At round 3, L11 (retro) or L12 (recap) holds on the same pass, before any reject can be filed. With alternating pass and changes, L12's `max_rounds` fires the same way. Bounded.
  - History (ii): round 1 passes; L8 says review; round 2 passes; L9, passed.
  - Records: L10 reads `reject` (defined; minor 4). L1 reads an override record that section 3 does not define (M1). L2 to L5 and the send-back have field gaps (minor 20).
  - Settings: every ladder setting exists with a default and a range; the "Used by" slips are in minor 11.
- **c. The override.** 7.1 and 7.2 agree that it is never delegable and that only the owner answers (`answer`: the coordinator answers only `decision`). The 10.1 rail, "unless the owner overrode that very commit", agrees with N19 through `covers`. Gaps: the override record (M1), the coordinator template (M3; prompts.md never contains the word "override"), the terminal raise (minor 7).
- **d. plan.md.** The PR that adds outcomes is named the same everywhere: PR 2, then PRs 5 and 9, then increment 1. The increment numbers in design 17.3 (4, 5, 7, 9) and prompts 8 (1, 1a) match plan 4. Wrong cites: minors 1, 13, 15 and 16. Arithmetic: M4. Done-when needing a later PR: minor 19. Exit test: M3 and minor 17.
- **e. Cross-references.** Minors 8 to 13. A full extraction of every N, L, J, R, A, D, I, O, S0 and W id in the three files finds each one defined. The faults are in what the ids point at (minors 1, 8 to 13, 21).
- **f. Stale text.** Minor 14. I found none of: page origin as sufficient, "4,000" or "14 PRs" as the current plan (apart from 17.6, marked as history), a required per-outcome verdict, `extra_rounds` (it is named only as removed), ladder ids D1 to D12, a `rules` package (named only as absent), or "the owner does not read the coordinator's terminal".

## Part 3. What the fixes broke

- **`ready --reject`:** no endless loop (the streak and `max_rounds` stop it). Under L10's reading it defeats a send-back (M2).
- **N19 and N19b:** a dead end, and the override is never spent. N19b's "tell once" does not repeat, but the row holds on every pass (M1).
- **Cancelling a stale gate ask:** no loop. N31 cancels, then opens the ask at `H`. A merge-only update keeps it covering, and N40 sits below N31. It is only late (m4).
- **The session-ticket rule in version 1:** gated answers are fine (a paired phone, or live voice). Its sentence is worded more widely than its scope (minor 18). If S0.10 fails, the override has no named fallback (minor 17).

## Not checked

Sections 5 (run table) and 9.1, 9.2 line by line; section 14 onward beyond the cross-references; knowledge-base.md's "Maps to" column; notes/renumber.tsv; prompts.md sections 1 to 3.1 beyond a grep; any code in `internal/web` or `internal/supervisor` (I took review-2's reading of it); whether a local process can feed the voice path.
