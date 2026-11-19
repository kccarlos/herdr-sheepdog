# Audit of the engine-v2 rule tables, by simulation

2026-11-18. Independent audit of [../design.md](../design.md), sections 0 to 8 (with 9.1, 9.4, 10.1, 11.2, 11.5, 11.6, 16 as needed). Method: concrete histories walked pass by pass through N1..N36, J1..J13, R1..R34, A1..A5, the ladder (4.6), the restart table (4.8), the acceptance table (7.2) and section 8. Only faults are listed. Default settings are assumed unless said.

Notation: `c1, c2…` worker commits; `m` a merge commit made by GitHub's update-branch; `H` as in 3.1; `p` the PR snapshot.

## Summary

| Class | Count |
|---|---|
| UNSAFE | 5 |
| DEAD-END | 6 (plus F4.2 and F6.1, filed as MIS-ORDER and UNDEFINED, which end in dead ends) |
| HOT-LOOP | 3 |
| NO-RULE | 3 |
| MIS-ORDER | 5 |
| CONTRADICTION | 9 |
| UNDEFINED | 11 |
| **Total** | **42** |

---

## History 1: the happy path

Walk (only the passes that matter):

| Pass | Records / facts | First match | Asks for | Next |
|---|---|---|---|---|
| 1 | planned, no attempt, deps done, slot free | N11 | new attempt | worktree exists |
| 2 | attempt, no ready | N15 (but N7 is evaluated first, see F1.1) | worker run alive (4.8 "none yet") | worker commits c1, `ready` c1 → H=c1 |
| 3 | no PR | N21 (ladder = review, N17–N20 false) | push + PR | p.head=c1 |
| 4 | no evidence | N22 | checks at c1 | evidence pass |
| 5 | ladder review | N23 | reviewer round 1 | round 1 changes |
| 6 | ladder fix | N18 | tell worker once | worker commits c2 → N15; `ready` c2 → H=c2 |
| 7 | p.head=c1 behind H | N21 | push | ; then N22, N23 round 2 pass |
| 8 | ladder passed, CI pending | N30 | — | CI green |
| 9 | all of N35 | N35 | accept + merge | |
| 10 | merge asked, snapshot open | N33 | wait/back-off | snapshot merged |
| 11 | merged | N2 → N1 | done, cleanup | |

No pass lands on N36 and nothing is asked twice (N16/N21 double push is a no-op by key). Faults found on this path:

**F1.1 UNDEFINED — N7, N16 (and N15's second clause) before the first ready report.** `H` is "the commit of the latest ready report" (3.1); on pass 2 there is none, yet N7 ("tip diverged from H") is evaluated before N15. *Fix:* in 3.1, `H` is the attempt's base commit until the first ready report.

**F1.2 CONTRADICTION — N18 vs 11.5.** N18 says every "fix" tells the worker "with `size_cap` set to lines now + `continue_lines`". Read literally, an ordinary review fix at pass 6 lowers the cap (estimate 200 → cap 400; lines now 150 → cap 210) and can trip N12 during a normal fix; 11.5 says the cap changes only at start and after `continue`. *Fix:* attach the cap clause to the retro's bounded fix only.

**F1.3 CONTRADICTION (minor) — ladder table vs ladder words.** Level 0 is "asked when evidence covers H", the words say "review: otherwise". Harmless today only because N22 precedes N23; it matters for N19 (F2.3). *Fix:* write the evidence clause into "review".

## History 2: review never converges

Rounds 1, 2, 3 at c1, c2, c3 all "changes" (7.2 makes every "changes" carry a major). After round 3: level-0 budget (2+1) spent, last 2 rounds majors, round ≥ 3 → ladder "retro" → N19 retro at c3 → verdict FIX → ladder "fix" → N18 tells the worker the retro's change → worker commits c4, `ready` c4 → H=c4.

**F2.1 UNDEFINED — ladder after a retro FIX (4.6 level 1).** At c4 no round and no retro covers H. The words give: not passed, not fix (no round at H; whether "the retro said FIX" still holds at a later head is unsaid), level 0 spent → level 1 "only if fewer than `retros_per_task` used" → 1 of 1 used → **recap**. Literal reading: the bounded fix is never checked and `retro_fix_verdicts` is dead. The other reading ("retro said FIX" not scoped to H) keeps saying "fix" at c4 → N18 already told → the task waits on the worker forever. Neither is the intended "delta check". *Fix:* in the ladder, "retro" also when the newest retro is FIX, a ready report is newer than it, and FIX verdicts used < `retro_fix_verdicts`; "fix" from a retro only when that retro's head covers H.

**F2.2 CONTRADICTION — continue after a bounded retro fix.** Level 1 says "level 2, with continue not allowed"; but if the bounded fix overflows its cap, N12 opens a `size` recap and section 4.3 allows `continue` (it bans it only "after a review trigger"). *Fix:* "never after a review trigger, nor while the newest retro verdict is FIX".

**F2.3 MIS-ORDER — N19 (and N20) above N21/N22.** Under the intended F2.1 reading, the retro check at c4 is asked by N19 before N21 (push) and N22 (evidence) are reached, so a retro verdict is spent on code whose own checks may fail; level 0 never has this problem because "review" needs evidence. *Fix:* order N21, N22, N19, N23 (move N19 below N22). N20 may stay.

Second FIX → budget spent → N20 recap `review` (split, new, drop) → N6 wakes coordinator → split → N1 closes PR. Fine.

Split child: rounds 1–3 changes, child's `retros[]` empty.

**F2.4 CONTRADICTION — level 3 vs N20.** The ladder table says the owner is asked when "a split child of a task that had a retro reaches level 1 again" (i.e. instead of a retro); N20 opens the owner ask only when the ladder says **recap**, and the child has 0 retros used, so the ladder says **retro** and runs one. *Fix:* in the ladder, level 1 is not available to a task whose `split_from` parent had a retro (ladder says recap, N20 asks the owner).

**F2.5 UNDEFINED — answers to engine-opened asks (A3, N20, N25).** A3 delivers an answer "to the asking run"; the N20 and N25 asks have no asking run, and an owner-kind ask without a task cannot be "held on the task". The owner's "drop it" reaches nobody by rule. *Fix:* A3: an ask opened by a rule delivers its answer as a coordinator wake (or a record the rule reads).

## History 3: size cap

N12 opens recap → N6 (wake, tell worker to write recap) → `continue` → cap = lines + 60 → N15 → over again → N12 with split/new/drop. Fine. With the owner's "merge it as it is":

**F3.1 HOT-LOOP — N12 after the owner closes a recap.** The owner's yes closes the recap and writes an accept, but changed lines are still over `size_cap`; N12 has no "since" clause, so the next pass opens a new size recap at once, and N12 sits above landing, so the owner's decision can never merge. (N13/N14 have "since the last recap decision", but it is unsaid whether the owner's close is a decision.) *Fix:* N12–N14 do not hold while an owner accept covers H (equivalently: the owner's close counts as a recap decision, and N12 gets the same "since" clause).

**F3.2 UNDEFINED — "a gate ask" for merge-as-is.** 4.9 says the owner answers "a gate ask 'merge it as it is'", but an ordinary size/CI/time recap opens no owner ask, and 7.2 forbids the coordinator from accepting; the owner's spoken "merge it as it is" has no defined path. *Fix:* say which ask carries it (e.g. the coordinator's owner ask of a named kind whose yes is the owner accept).

## History 4: the base moves

Ladder passed at c2, CI green, `behind` → N34 asks update for H → next passes: snapshot still old → N34 "asked already: wait" → snapshot head m, `head_by` engine, merge_only → H=m (rounds and evidence cover) → N30 pending → green → N35. Worker tip c2 is `behind` H: no row cares, fine.

**F4.1 DEAD-END — N34 "asked already: wait" has no limit.** If GitHub accepts update-branch but no new head appears (silent failure, eventual 422 lost), the task waits on "GitHub" forever with no wake and no alert (`ci_stalled` is only for N30). *Fix:* same shape as N29: asked less than `checks_grace_minutes` ago → wait; otherwise wake the coordinator once.

**F4.2 MIS-ORDER (→ DEAD-END) — N33 above N34.** Base moves after N35 asked the merge: GitHub refuses ("branch out of date"), the snapshot shows open and behind, N33 matches first and re-asks the merge `merge_tries` times, then wakes the coordinator, which has no command that updates the branch (and `retry` is not said to reset merge tries). *Fix:* N33's condition gets "and not behind (or a merge queue is on)"; N34 then updates, H moves, N33's key (per H) no longer holds, N35 asks again.

Worker later commits once more. If it pulled m: c3 ahead of H with changes → N15 → `ready` → new round (safe). If it did not pull:

**F4.3 CONTRADICTION — 7.2 `ready` vs 3.1.** 3.1 says H moves forward only by the engine; 7.2 accepts a ready report at any new commit ahead of the base, so `ready` at c3 (child of c2, not of m) sets H=c3, p.head m is now `diverged` from H → N7 → coordinator. *Fix:* `ready` valid only when the commit is H or ahead of H (refusal tells the worker to pull first).

**F4.4 DEAD-END — N26 after an engine update.** N27 tells the worker "pull first" after an engine update, N26 does not. The worker merges the new base into its stale c2: the tip is `diverged` from H=m → N7, coordinator only (its effective exit is `say`). *Fix:* N26's message says "pull first" when the tip is behind H, as N27's does.

**F4.5 NO-RULE — PR head ahead of H, not pushed by the engine.** A person presses GitHub's "Update branch", or someone pushes a commit to the PR branch: `p.head` is ahead of H, the worker's tip is behind. N7 (diverged) no; N21 (GitHub lacks H) no; review runs at H; landing: N35 needs `p.head` is H → falls to **N36**. Safe (no merge) but a normal event lands on "no rule matched". *Fix:* 3.1: a `merge_only` advance of `p.head` moves H whoever made it (as N16 does for the tip); N7 also holds when `p.head` is ahead of H with `changes` and `head_by` other.

## History 5: CI

- No checks right after push: `absent` within grace → N30; past grace → N29 dispatch once → wait → wake once. Fine.
- Red once then green: N27 re-run (key per H) → pending N30 → green → N35. Fine.
- Red twice: N27 tells worker per failing run. Fine.
- Cancelled from outside: N28 re-run if re-runs left, shared with N27. Fine.
- Own time limit: N28 wakes coordinator. Fine.
- Unreadable: N30 → `ci_stalled` after 90 min. Fine.

**F5.1 UNSAFE (low) — rollup "all SKIPPED is green" (7.3).** A PR whose every check is SKIPPED (path filters) is `green`, so N35 merges with no CI run at all, while 10.1 says "no checks is never green". The engine's own evidence still ran, which limits the damage. *Fix:* the `green` row requires at least one SUCCESS; all skipped/neutral is `absent`.

**F5.2 CONTRADICTION — section 10 wake list vs N30–N32.** Section 10 says rules N28–N33 wake the coordinator; N30 waits on CI, N31 on "a GitHub approval", N32 on the base, and none wakes anyone. *Fix:* for N30 and N32, drop them from the list; for N31 see F5.3.

**F5.3 DEAD-END — N31.** "Nobody in the factory can give it"; the task waits on a GitHub approval with no wake and no owner ask, for ever. *Fix:* N31 wakes the coordinator once (as section 10 already claims), which can open an owner ask.

## History 6: failures

- Worker terminal dies twice: R30 → 4.8 "any other end", restarts 1 and 2. Fine.
- git push transient ×3 → back-off 1, 2, 4 → N5; settled → N5 at once. Fine.
- Worker usage limit with a second candidate: R28 → handoff → next candidate. Fine.

**F6.1 UNDEFINED (→ DEAD-END) — 4.8 "it counts as a failure (N5)".** Reviewer exits without a report three times: tries 1, 2 use `reviewer_retries`, then "counts as a failure". If that is one failure toward `ensure_failures` (3), the row starts nothing more, the key is not re-tried, and the task sits at N23 "waits on the reviewer" with no reviewer and no wake. Same for the worker's last row. *Fix:* "it is a `settled` failure" (section 8 then goes to N5 at once).

**F6.2 HOT-LOOP — quota restarts with an unknown pool.** 4.8: an end on a usage limit is "not a failure, for any role", and the next candidate that resolves starts; 7.3: a pool whose reading is unknown is usable. A CLI that is out of quota but whose reading cannot be read starts, shows the limit screen (R5 → `ended(quota)`), resolves again, starts again, every pass, never counted. The same shape for "signed out" when the CLI's sign-in check says yes but the screen says no. *Fix:* an `ended(quota)` (or `signed_out`) run makes its pool (or CLI) unusable to Resolve until the reported reset or the next `pool_minutes` reading.

**F6.3 UNDEFINED — usage limit hit mid-turn by a reviewer.** Only R5 (starting) and R28 (worker/designer, at the floor) yield `ended(quota)`. A reviewer that runs out mid-turn goes idle with no report (R18) → "no valid report" → burns `reviewer_retries` → N5, instead of waiting for quota. *Fix:* R18: an idle run whose screen is the adapter's `limit` class ends `quota`.

**F6.4 DEAD-END (soft) — herdr-core down for ten minutes.** Every git/gh call goes through hc (2.4), so each pending key fails 3 times in about 7 minutes (back-off 1, 2, 4) and every task with pending work latches at N5 until the coordinator sends a `retry` per task; two timed-out calls also raise `consent_prompt` (stops the factory). `hc_unavailable` is a fact that no row and no part of section 8 reads. Worker starts during the outage burn `worker_restarts` too. *Fix:* section 8: a failure while `hc_unavailable` (or `github_unavailable`) holds is not counted and does not count toward the volume probe.

**F6.5 MIS-ORDER — 4.8 "any other end" catches the engine's own stops.** `park`/`unpark`, a plan amend that changes the worker request, and a reviewer void-stop end with `stopped`; for the worker this falls to "any other end, restarts below `worker_restarts`", so three parks/amends land the task at N5. *Fix:* add "or the last run was stopped by the engine" to the "none yet: start fresh" row's condition.

**F6.6 UNDEFINED — task-held messages.** R30/R33 send undelivered messages "back to the task or project, whose rules restart the run", but no task row delivers held messages to the restarted run, and the "told once" key (N17, N18, N26, N27) was already used. The findings survive only if the handoff file happens to carry them. *Fix:* 4.8: a start delivers the task's held messages first (or: a told-once key counts as used only on a delivered result).

Daemon restart at each step of history 1: every step replays to the same row (keys, world checks, irreversible re-reads). No fault beyond F4.1.

## History 7: people outside the engine

- Merged by hand before review: N2, `merged_outside_bar`. Fine.
- Owner quits the worker: 4.8 no restart, wake once. Fine (task shows "waits on the worker" though it waits on the coordinator; display only).

**F7.1 HOT-LOOP — PR closed by hand, then `retry`.** `retry` forgets the PR; N21 asks for "one PR open for the branch"; the executor first looks for "a PR for this branch" and 9.4 adopts one "in the same repo, same base, head H" — the closed PR qualifies. It is adopted again: either N4 reopens (wake again) or the task goes on against a closed PR and N33 burns merge tries. *Fix:* 9.4: adopt only an **open** PR.

**F7.2 DEAD-END (soft) — force push (N7).** N7 names no exit; no coordinator command clears it (`retry` and `send-back` do not, `new` needs a recap). The only working exit is `say` to the worker ("merge the remote branch, then ready"), which is nowhere stated; otherwise drop. *Fix:* N7's wake text names that exit; with F4.3's fix the worker's ready at the merge descends from H and N7 clears.

**F7.3 UNSAFE — `new` attempt leaves the old PR open.** A recap `new` closes the attempt and keeps the old branch; its PR stays open and N2 reads only the current attempt's PR, so a hand-merge of the old, unaccepted PR is silent, contrary to N2's "never silent". *Fix:* the `new` decision closes the old PR with a note (as N1 does for split and drop).

Owner's "merge it as it is" while a recap is open: F3.1, F3.2.

## History 8: the coordinator

- Dies: J7 (J6 gap). Signed out: J5. Turn error twice: J8 → stop → J7. Silent after a wake: J8b. Fine.

**F8.1 MIS-ORDER / CONTRADICTION — J1 hides J2.** A start asked on a never-started project matches J1 ("never started") first, and 7.2 accepts `start` only when doctor passes, so J2 ("a start was asked and doctor has not passed") cannot hold. *Fix:* delete J2 and keep the acceptance check, or move J2 above J1 and remove "start: doctor passes" from 7.2 (the acceptance table cannot run doctor, which is an effect, so the second is the real fix).

**F8.2 NO-RULE — J4 before J7.** A started project with no plan has "every milestone done and no task open" vacuously: J4 "Finished, no coordinator" matches, J7 ("Planning" with no plan) is never reached. *Fix:* J4 requires a plan with at least one milestone.

**F8.3 UNSAFE — J10 rotation during a stopping alert.** `no_pool` raised while the coordinator is alive: J3 does not match (coordinator alive). J10 rotation fires, stops the coordinator; now J3 matches and nothing starts, so the factory has no coordinator for the rest of the alert (an hour), while tasks mid-review hit N5/N28 wakes nobody reads. Same for J8b. *Fix:* J10's condition gets "and no stopping alert is active".

**F8.4 UNDEFINED — a stopping alert and runs for open attempts.** "Nothing new starts; tasks wait at N10", but N10 applies only to tasks with no attempt. For a task mid-review, 4.8 has no row for "a stopping alert holds starts", so a refused reviewer start is either a failure (N5 latch, then manual `retry` after the alert clears) or an unstated wait. *Fix:* 4.8: "a stopping alert is active → wait (counts as present, not a failure)" as the condition of the "a start is pending" row. Also, J3 tells the owner only when no coordinator is alive; move "told once per alert" to the alert observer.

## History 9: the run table

Worker with prompt on argv: R1 → R3 (trust) → R8 or R9 → R18 decides. Fine. Reviewer idle with report: R17 skipped (unread report), R18 reads. Fine. Trust screen not answered by keys: R6 → R10 blank → 4.8. Fine.

**F9.1 DEAD-END — `starting` + status blocked or error.** A run that reports `blocked` (permission, sign-in) or `error` before it ever reports working hits R9b ("same"); R10 needs "no status was ever reported", so it never fires; R13/R14/R15 are From `working` only. The run sits in `starting` until someone stops it, and holds a slot. *Fix:* R13, R14, R15 From `starting, working` (one-word change each), or R10's condition becomes "never reached `working`".

**F9.2 CONTRADICTION — R13's note and R23's From vs the matrix.** R13 says "a brief sign-in prompt at start is R14", but at start the run is `starting` and R14 is From `working`. R23 lists From `starting, working, blocked` but its condition requires `working`; the matrix omits R23 from the starting/deliver cell and lists it in blocked/deliver, where it can never fire. *Fix:* R23 From `working`; matrix blocked/deliver = R24. R13's note follows from F9.1.

**F9.3 UNDEFINED — R28 "then stop" when the deliver is held.** For a `starting` or `blocked` worker, R28's urgent deliver is held by R24 (R23 needs `working`); "then stop" has no trigger. *Fix:* R28: "stop with result `quota` after `handoff_minutes` or when the run goes idle, whichever is first".

Matrix check: every other listed row has the cell's From state and event (R11 covers starting/activity; R12 is exit-only, fine; R27 covers idle/blocked for launched/screen/activity/tick and non-working status). Note: `idle` + status `blocked` or `error` stays `idle` (R27); harmless while the task rules read the status fact.

## Cross-cutting faults

**FX.1 UNSAFE — ladder: "passed" hides a `send-back`.** "passed" is checked first; a coordinator `send-back` (a round-like "changes" record) at a head where a passing round already covers H leaves the ladder at "passed", so N35 merges despite it; 4.9 claims the effect is N18. *Fix:* "passed" requires the newest round-like record covering H to be a pass.

**FX.2 MIS-ORDER — N15 hides N18 for a send-back before any ready.** After "nothing to change" (N8) the coordinator sends back: N8 clears, N15 ("no ready report") matches before N18, and N15 makes sure of nothing, so the worker is never told why. *Fix:* N15's first clause: "no ready report and no send-back newer than the worker's last report"; N18 also holds for such a send-back.

**FX.3 UNSAFE — owner gate "no" survives a `new` attempt (N24, N25).** Gate ask answered no → N24 recap → `new`. In the new attempt N24 is false ("a recap was decided since") and N25 is false (the gate "is answered"), so the new attempt merges without an owner yes. *Fix:* N25: "an owner gate applies and has no yes for an ask opened in this attempt" (gate answers are attempt-scoped).

**FX.4 NO-RULE — N17 says "at H", the rest says "covers".** Evidence fails at c2 → N17 → the worker merges the base only → N16 pushes, H=m. N17 ("newest evidence **at** H") is false, N22 ("no evidence **covers** H") is false because the failed evidence covers m, so N23 reviews failing code and, after review passes, N35 fails on evidence and the task lands on N36. *Fix:* N17 "the newest evidence covering H".

**FX.5 CONTRADICTION — ladder "fix" says "newest round at H".** Same shape: a pending fix plus a merge-only push (N16) makes the ladder say "review" (round 1 still covers H, no round is "at" H), so a new round is spent on unfixed code and can bring the retro forward. *Fix:* "the newest round covering H".

**FX.6 CONTRADICTION — I1 vs 4.8 on reviewers.** I1: every live run is wanted by its task's matching row. 4.8 stops only reviewers "whose round is over or void". While a reviewer runs, the worker commits (no ready): the task moves to N15 (or N12–N14), which does not want the reviewer, yet its round (at unchanged H) is neither over nor void, so I1 fires an error and a coordinator wake. *Fix:* 4.8: a reviewer is wanted while its round's head is H, whatever row matches (and I1 says so).

**FX.7 UNDEFINED — `github_unavailable` "holds merges".** 7.3 says so, but no row reads it; N35 does not list it (only the pre-merge re-read protects it). *Fix:* add "GitHub is available" to N35's "all of".

**FX.8 UNDEFINED — "a typed rule" in N24.** No section defines what a typed owner-gate rule is or where it is stored. *Fix:* name the record (a setting or plan field).

---

## (a) Rules that could be deleted or merged with no loss

- **N28 into N27:** "red or `stopped_external`: re-run while re-runs are left; else red → tell the worker, stopped → wake". `stopped_timeout` stays a one-line row.
- **N29 into N30:** N29 already branches on time; "no checks" within and past grace is one row with three time branches.
- **4.8 "usage limit" and "signed out"** are the same row (next candidate with a handoff, else wait) apart from the at-the-Mac item.
- **R6, R9b, R11** (starting no-ops), **R21**, **R27**, **R32**, **R29**, **R34** are all "same, do nothing"; one catch-all row per state, placed last, loses nothing and makes the matrix shorter. R8 and R9 merge ("working, or idle with the prompt on argv → working").
- **J2:** delete (see F8.1) unless moved above J1.
- N34b looks redundant with N35's last clause but is not: without it a working worker at landing falls to N36. Keep.

## (b) Would the section 16 order tests catch each MIS-ORDER?

The order test swaps **neighbouring** rows and requires the suite to fail. It proves the order matters, not that it is right: a case written with the wrong expectation passes.

| Fault | Neighbours? | Caught? |
|---|---|---|
| F8.1 J1 hides J2 | yes | Only indirectly: "a case per row that lands on no earlier row" cannot be written for J2 from a never-started project, so the per-row test (not the order test) would flag it |
| F2.3 N19 above N21/N22 | no (N20 between) | No |
| F4.2 N33 above N34 | yes | Only if the case's author expects N34; the swap test alone does not decide which is right |
| F6.5 4.8 "any other end" vs engine stop | no row exists for the case | No |
| FX.2 N15 above N18 | no (N16, N17 between) | No |
| FX.1 ladder passed before fix (UNSAFE, an order fault inside a function) | not a table | No |
| F8.2 J4 above J7 (NO-RULE, order-caused) | no (J5, J6 between) | No; the property "every row can be reached" would catch J7 unreachable only from a no-plan start, if the generator makes one |

Order tests should cover every pair of rows that can both hold, not only neighbours, and the ladder needs its own ordered table.

## (c) Three conditions that lean on a helper hiding real logic

1. **`Ladder(task, H)`** — a second rule table in prose: budgets per level, how retros and FIX verdicts are counted, delta checks, split-child level 3, send-back handling, "at" vs "covers". F2.1, F2.4, FX.1, FX.5 all live here, and section 16's table tests do not reach it.
2. **`H` and "covers" / `merge_only` / `head_by`** — when `H` moves (ready report, N16 push, a snapshot's "new head" attributed to the engine), and what `head_by` is computed from. F1.1, F4.3, F4.5, FX.4 come from this.
3. **"make sure the worker run is alive" plus "counts as a failure"** (4.4, 4.8, section 8, `acts[]` keys) — restart policy, nudge-per-turn, back-off, failure counting, outage handling and held-message delivery are all behind one phrase in every worker row. F6.1, F6.2, F6.4, F6.5, F6.6, F8.4 come from this.
