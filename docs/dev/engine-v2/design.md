# Engine v2: design

2026-11-18. The new sheepdog-factory engine, designed from scratch. Companion files: [knowledge-base.md](knowledge-base.md) (what it must do), [critique.md](critique.md) (what the old engine got wrong), [plan.md](plan.md) (how to build it and cut over).

Words: the **owner** is the person. The **coordinator** is the one Opus agent per project. A **run** is one agent process in one herdr-core terminal. The **engine** is the Go code described here.

## 0. Decisions, and how this design meets them

**Owner decisions and steers (2026-11-18).** "Brief" means the task brief or a message relayed by the coordinating agent; "chat" means the owner wrote it to the lead directly during the work.

| # | What the owner said | Source | Effect on this design |
|---|---|---|---|
| D1 | The engine is written in Go, inside the sheepdog codebase, with no Python and no managed interpreter. Take only ideas from the old engine | relayed, binding | The engine is a Go package in the daemon process. Nothing is ported; the old tests are replaced by [knowledge-base.md](knowledge-base.md) |
| D2 | "The core of this engine is that every state transition is defined and planned up front. Make design.md centre on complete transition tables for each state machine (task lifecycle, review/gate, recap/retro, merge, agent run): every state, every event, the guard, the action and the next state, with no transition handled ad hoc in code outside the table. Unknown event in a state means a defined refusal, never a fallthrough. The code is then an interpreter of these tables plus effects. Every behaviour in knowledge-base.md should map to a table row or an effect." | relayed, binding | Met in a different form after D8; the table below shows how, clause by clause |
| D3 | herdr-core only; ignore herdr-tui | chat | The engine calls `internal/hc` directly |
| D4 | The approved phone and iPad design stays; the engine serves the space-page factory section, asks as cards, the start row and shell start, debug and log | relayed, binding | Section 12 |
| D5 | The coordinator is always Claude Opus, one per project; more hands-off than before | brief | Section 10 |
| D6 | "The table's structure is fixed, but the numbers in the rows' conditions are parameters"; "I want those numbers to be adjustable while it runs, this is very important" | chat | Section 10.1 |
| D7 | Asked whether the transitions were too complex and whether there is a simpler abstraction, "a discussion, not a decision"; later: "I don't want to make this loop too complex; over-engineering is not good either" | chat, a preference | Section 17.3 sorts every gap found into "version 1", "later" or "not carried" |
| D8 | Offered two forms (A: keep the transition tables; B: one ordered table of "what does this task need next"), the owner answered "I think B is more reasonable" | chat, decision | Sections 2 and 4. **This changes the form D2 asked for** |
| D9 | "I want a mechanism that proves the task really met its goal"; pointed to pstack's "prove it works". Later agreed: the mechanism of section 11.6, as the first increment after version 1 | chat, decision | Section 11.6. Version 1's bar is green CI, two passing reviews, and the worker's own proofs run by the report client on its ready report; the engine running them again is increment 1 |
| D10 | "Sometimes I need to override: several PRs are stacked and I tell the orchestrator to merge them all and fix problems afterwards." Agreed: a one-off card listing each PR, approved only by the owner, never delegable; red CI may be merged if the card names it; a conflict or a changed commit is not merged | chat, decision | Sections 4.3b and 4.9 (the override) |
| D11 | Agreed: a setting is changed only through a card the daemon proves, never directly from a terminal | chat, decision | Sections 7.2 and 10.1 |
| D13 | "The system prompts have to be designed together with the engine; left to the implementing agents they will not see the whole." Learn from the old engine's good prompts, and from pstack, Ponytail and the factory talk. Always a design first, and keep questioning scope | chat, decision | [prompts.md](prompts.md); section 11.7 |
| D16 | Build it as agile, vertical slices: a walking skeleton that merges one real PR within about 5 to 8 small PRs, then one visible capability per slice, with a review point after each and later slices only outlined | relayed by the main session as the owner's instruction | plan.md |
| D15 | The factory web page may answer every ask, the never-delegable ones included, once the browser holds a daemon session ticket, as a paired device does. A local request with no ticket is never the owner. An agent gaming this is judged unlikely; the log names the answering device | relayed by the main session as the owner's decision after the second review (not said to the lead directly) | Section 7.1; plan.md |
| D17 | A browser on the same computer pairs with a short code shown in the Mac menu-bar app and typed into the page by the owner, not with a link any local program can fetch. This narrows how a browser gets D15's ticket. Reading the screen or the menu bar is accepted as the remaining risk | the owner, 2026-11-20, after the W3 reviews | Section 7.1; plan.md slice 6 |
| D14 | The factory's owner interface is built for the web first (the daemon's own web client); the phone and iPad follow once the web version works. Review stays at two passing rounds | chat, decision | Section 12.1; plan.md slice 9 |
| D12 | Agreed with the first review's answers: "2 reviews" counts passing rounds on the final head; a repo with no CI is refused at start; the old code leaves main in the switch PR after a tag | chat, decision | the ladder rows of 4.6; section 6; plan.md |

**Why form B replaced the first form.** The first draft was a transition table: state, event, guard, next state, about 150 rows. It was checked against the 599 behaviours in the knowledge base: 181 were not covered. Most had one shape: "in state X, event Y was not written", or "a remembered fact went stale": a blocked task with no way out when the cause went away, alerts that were raised and never cleared, a closed pull request handled in three states out of sixteen, five different tests for "the head moved". The lint "every cell is defined" did not catch them, because a forgotten cell fell on a default. Patching took the table to about 190 rows. Form B removes the cause: nothing remembers a phase, so nothing can be stuck in one.

**How D2's clauses are met under form B:**

| Clause of D2 | In form B |
|---|---|
| Every transition defined up front | Every situation a task can be in is one row of the rule table (section 4). The table is total: its last row is "no rule matched: do nothing, wake the coordinator" |
| Guard, action, next state | Each row has a condition, the thing the engine makes sure of, and who the task now waits on. The "state" is the row that matches |
| Nothing ad hoc outside the table | The loop only reads the tables. Executors do one thing and report |
| Unknown event is a defined refusal | The acceptance table (section 7.2) lists every report and command, who may send it, when it is valid, and the refusal code otherwise |
| Code is an interpreter plus effects | Section 2 |
| Every knowledge-base behaviour maps to a row or an effect | knowledge-base.md, "Maps to" column; counts in section 17.2 |

Agent processes and owner asks are different: they really are driven by events. Runs and asks keep small transition tables, written as full matrices (sections 5 and 7.1).

**My assumptions (a reviewer should challenge these):**

| # | Assumption | If wrong |
|---|---|---|
| A1 | A project is a GitHub repo with CI. No local-merge mode in the first versions | Add rules for local merges. The old engine's local mode once merged into local main and pushed nothing (field note 57) |
| A2 | The engine starts every review. Workers never run their own reviewer | The loop-breaker goes blind again (critique F11) |
| A3 | Review passes when `review_rounds` passing rounds cover the head (default 2), the second by another reviewer candidate when one resolves. Decided by the owner (D12) | One setting |
| A4 | The retro fires when rounds 2 and 3 both report majors, as `docs/dev/factory/review-rounds.md` says today | Two settings |
| A5 | One engine loop for all projects is enough (a few projects, events seconds apart) | One loop per project; nothing else changes |
| A6 | JSON files are enough for storage. The daemon has no SQLite today | Swap the store only |
| A7 | Reviewers end their last message with a report tag that the engine reads from the terminal | Section 9.3 lists the fallback |
| A8 | The engine runs each task's own checks and keeps the result as evidence (section 11.6). Confirmed by the owner as the first increment after version 1 (D9). Its cost on a real project is not measured | It stays out, and the bar stays green CI plus reviews |

## 1. The shape in one page

```
 owner ─ phone, voice ─┐                                  ┌─ hc terminals: coordinator, workers, reviewers
                       ▼                                  ▼
  reports, commands ─► acceptance table ─► records ─┐
                                                    ├─►  ONE LOOP: for each task, first matching rule
  observers (hc, git, GitHub, pools) ──► facts ─────┘        │
                                                             ▼
                                              "make sure of X"  ─►  executors (hc, git, gh)
                                                             │           │ result or failure
                                              state.json, log.jsonl  ◄───┘  comes back as a record
                                                             │
                                                             ▼  the same rule
                                              what the owner sees: who the task waits on, and why
```

| # | Rule of the design | Answers (critique.md) |
|---|---|---|
| 1 | **One writer.** A single goroutine owns all engine state | F10 |
| 2 | **Records, not phases.** The engine stores what happened and what was decided (a ready report, a verdict, evidence, an accept), each tied to a commit. It does not store "where the task is" | F1, F5 |
| 3 | **One ordered rule table per task.** What the task needs next is the first row whose condition holds. The same row gives the action and what the owner sees | F1, F6 |
| 4 | **Make sure, don't remember.** A row names something to make sure of ("a reviewer is running for round 2 at this commit"). The engine compares that with the world and does only the difference. Doing it twice changes nothing | F3, F9 |
| 5 | **Facts are read, not stored.** CI, mergeable, heads, live terminals and diff size come from GitHub, git and hc | F8 |
| 6 | **Intent is resolved at launch,** in one function, and argv is built in one function. Nothing stores or re-parses argv | F2, F3 |
| 7 | **Typed fields, not prose.** Size estimate, outcomes, reasons, verdicts and decisions are fields with fixed values | F4, F6 |
| 8 | **Every number is a setting** the owner can change (section 10.1); every rail is code | F7 |

## 2. How it works

### 2.1 Records, facts, rules, "make sure"

| Thing | What it is | Example | Who writes it |
|---|---|---|---|
| **Record** | Something that happened or was decided, kept for good. Most are tied to a commit | "round 2 at commit 3f9c: changes, 2 majors"; "the owner answered yes"; "re-run used for commit 3f9c" | Only the loop, from an accepted report, command or executor result |
| **Fact** | What the world looks like now. Replaced whenever an observer reads again | the PR's checks; the worktree's tip; a run's status; a pool's reading | Observers |
| **Rule** | One row: a condition over records and facts, what to make sure of, and who the task waits on | "no passing evidence covers the head → make sure the checks run → waits on the engine" | Code (the tables) |
| **Make sure of** | A desired thing with a key. The executor first checks whether it already holds | "a reviewer run exists for round 2 at 3f9c" | The loop hands it to an executor |

A rule's condition is a pure function. It sees the task's records, the facts, the settings and the current time. It cannot read a file or the network.

### 2.2 One pass of the loop

```
something changed (a record was accepted, a fact was replaced, a clock tick)
  → for each task it touches: find the first rule whose condition holds
  → compare what that rule wants with what exists; hand the differences to executors
  → append to log.jsonl: task, rule id, the facts it saw, what was asked for
  → write state.json
executor finishes → its result is a record (done, or failed with a class) → another pass
```

- One input and the records it produces are **one commit** of `state.json`.
- The loop never calls git, gh or hc itself. It cannot hang.
- A missed or repeated input does no harm: the next pass reads the same records and facts and lands on the same row. A daemon restart is one pass over every open task.
- The log line for each pass is the debug view: which row matched, and for each earlier row, which part of its condition was false.

### 2.3 What makes the tables safe

| Risk | Rule |
|---|---|
| Nothing matches | The last row of every rule table is "no rule matched": do nothing and wake the coordinator. Merging is never a fall-through; it has its own row with every condition stated (N42) |
| Order hides a problem (the old stage table hid an out-of-rounds task behind "Working") | Every row names who the task waits on. Every row has a test case that lands on it. For every pair of rows that can both hold, a test case fixes which one wins; for neighbours the suite is also run with the pair swapped and must fail (section 16) |
| A thing to make sure of keeps failing | Section 8: a pending attempt counts as present, retries back off per key, and repeated failure becomes a record that row N5 turns into "waiting on the coordinator". Nothing re-fires in a loop |
| An irreversible action on stale facts | Merge, branch delete, worktree removal and PR close read the facts again and test their row's condition immediately before acting |
| A number inside a condition | Conditions take numbers only from the settings table. A test fails on a numeric literal in a rule |
| A report nobody planned for | The acceptance table (section 7.2). Not listed, or not valid: refused with a code and one plain sentence, and logged |

### 2.4 Where executors run

The daemon can be a launchd service (*not verified by me; `internal/service` exists*), and a launchd process has no macOS consent for `~/Documents` or a removable volume: git hangs there. So every git, gh and codexbar call goes through herdr-core (`hc exec`), which runs in the owner's login session, with a time limit. This is one helper in `world`; nothing calls `os/exec` on a project folder. A timeout is a failed result like any other.

### 2.5 The clock

Stored timestamps and the engine's `now()` stay in wall time (the same clock herdr-core's `status_ms` uses). The loop ticks every 15 s. The two durations that must not include machine sleep — worker working time (N14, `build_hours`) and reviewer age (N29, `review_minutes`) — are accumulated per pass from Go's monotonic clock (`time.Since`, which does not advance while macOS sleeps), capped per pass at 2 × the tick interval (30 s), and stored in `state.json` so restarts keep them.

## 3. Records

| Entity | Lives in | Stored state |
|---|---|---|
| Project | `projects/<id>/state.json` | marks: started, stopped by the owner; the plan; settings; held wakes |
| Task | same file | plan fields, marks, attempts (below). **No phase field** |
| Run | same file | one of `starting, working, idle, blocked, ended` (section 5) |
| Ask | same file; asks for "everywhere" in `engine.json` | one of `open, answered, cancelled` (section 7.1) |

**Task, by scope.** The scope column is the answer to "what does a new attempt clear".

| Records | Scope | Comes from |
|---|---|---|
| Plan: `id, title, kind, milestone, after[], priority, for_whom, if_skipped, smallest, not_doing, estimate_lines, outcomes[], brief, boards[], request{role → candidates}` | task | `plan apply`, `plan amend` |
| Marks: `parked`, `dropped{why}`, `split{children}`, `closed{why}`, `done{merged commit}` | task | coordinator commands; the loop (done) |
| `retros[]`, `recaps[]`, `split_from` | task: survive a new attempt | reports; the loop |
| `attempts[]`; each: `branch, worktree, pr`, `ready[]`, `rejects[]`, `evidence[]`, `rounds[]`, `overrides[]`, `accepts[]`, `size_cap`, `acts[]` | attempt | reports; executor results; observers |

- `ready{head, proofs[outcome → command, exit, output_path, or artifact], touched, skipped}`: the worker said "done" at this commit. The report client runs each proof command in the worktree itself and stores the exit code and the output; a proof that exits non-zero refuses the report. The client runs each proof at the worktree root, ends its whole process group after 5 minutes (exit 124), and keeps at most 64 KiB of output in a file under the worktree's git directory, so the tree stays clean. When the task's outcomes carry proofs, `touched` must say something and `skipped` must be given (`--skipped ""` means nothing was skipped); the client checks both before it runs any proof. The engine takes the exit codes the client sends; it runs the checks itself only from the evidence increment (11.6).
- `reject{head, round, why}`: the worker rejected every finding of a reviewer's round with a reason and made no new commit. The ladder then asks for another review (L10). A reject is not a round.
- `override{head, checks_shown, spent}`: the owner's yes to an override card for this task, at the commit the card showed, with the checks state the card showed. `spent` is set when the override was used for a merge or turned out stale (N19b); a spent override covers nothing.
- A retro record carries the attempt it was given in and its confidence (low, medium, high).
- `evidence{head, outcome, command, exit, output_path}`: what the engine saw when it ran the task's checks (section 11.6).
- `round{n, kind: full|delta|retro|send_back, head, reviewer{cli, model, effort}, verdict, majors, report_path}`. Every round is recorded, whoever the reviewer is.
- `accept{head, by: bar|override, rounds, check runs, evidence, waived, owner proof}`. An accept by the bar is written at `H` in the same step as the merge. An accept by the owner's override is written at the commit the override card named.
- `recap{trigger, allowed[], decided{choice, why}}`.
- `acts[]` is the engine's own ledger: each thing it did or tried, with a key and a result. "Told the worker about failing run 812", "re-run asked for commit 3f9c", "nudged at turn 14", "start of run r7 failed: timeout". Rules read it to do a thing once, to count tries, and to back off.
- `last_pr`, the latest PR snapshot, is not stored: the loop keeps it in memory per attempt and reads GitHub again after a restart.
- `last_tip`, the latest worktree reading (the changed lines, and whether the tip is ahead of `H` by merges of the base only), is not stored: the loop keeps it in memory per worktree, keyed by the tip and `H`, so git is read again only when one of them moves. A restart reads it again. A read that times out counts as over `size_cap`, so a huge commit cannot slip past N12 (owner decision at review point 2).

A project's id is written into the repo's own git config at start, so renaming or moving the folder does not orphan its tasks.

**Not stored anywhere:** a phase or status word, argv as intent, a queue, a "needs you" flag, a blocked reason, a list of live agents.

### 3.1 The current commit

| Name | What |
|---|---|
| `H` | the attempt's head. It starts as the attempt's base commit. It moves forward, never back, to: the commit of a ready report (accepted only at a commit ahead of `H`); the result of the engine's own branch update; a merge-only tip (N20); a merge-only advance of the PR head made on GitHub by anyone (a person pressing "Update branch") |
| tip | the tip of the task's worktree (a fact) |
| `p.head` | the PR's head on GitHub (a fact) |

A relation between two commits is `same`, `ahead` (a descendant), `behind` (an ancestor) or `diverged`. A delta from X to Y is `merge_only` when every new commit is a merge of the base branch and the merged tree equals git's own merge of its parents (nobody edited it by hand); otherwise it is `changes`. The observers compute both with git.

**One function decides whether an old record still counts:** a record made at commit X **covers** `H` when X is `H`, or `H` is ahead of X by a `merge_only` delta. Rounds, evidence, the owner's yes to a gate and the owner's override all use it. So an answer the owner gave for one commit never passes a later commit he did not see.

A plan amend that changes a task's brief or outcomes is also a boundary: rounds and evidence from before it stop covering, so code reviewed against the old brief is reviewed again.

Consequences, with no rule needed: a new commit by the worker makes earlier rounds and evidence stop covering the head; bringing the branch up to date does not; a rebase or force push (`diverged`) is row N7.

### 3.2 What is remembered until something closes it

Form B has few latches. Each is listed with what closes it.

| Remembered | Scope | Closed by |
|---|---|---|
| `done`, `dropped`, `split`, `closed` | task | never |
| `parked` | task | `unpark` |
| An open recap | task | the coordinator's decision, or the owner's override |
| A "nothing to change" report | attempt | a later ready report, `send-back` or `close` |
| The owner's yes to a gate, or an override | the commit the card named | a commit it does not cover: the gate asks again; the override no longer applies |
| The owner's no to a gate | attempt | a new attempt asks again |
| Owner stop | project | the owner starts it again |
| "Done once" keys in `acts[]` (told, re-run, update, nudge) | the commit, check run or turn in the key | a new commit, run or turn makes a new key |
| Failure counts in `acts[]` | attempt, per key | success, or the coordinator's `retry` |
| Restarts, reviewer failures, rounds, size cap | attempt | a new attempt |
| Retros used, recaps and continues used, split lineage | task | never |
| Alerts | project | the observer that raised them (section 6) |

## 4. The task rules

First match wins. "Waits on" is what the owner's surfaces show. Numbers are settings (section 10.1). A dash in "make sure of" means: nothing to do but wait.

### 4.1 Ended, held, or needing a decision

| ID | If | Make sure of | Waits on |
|---|---|---|---|
| N1 | marked `done`, `dropped`, `split` or `closed` | no run of this task is alive; its `decision` asks are cancelled; a dropped or split task's PR is closed with a note; `Cleanup` has finished | nobody |
| N2 | the PR is merged | mark `done`; if there is no accept for the head the PR had when it merged (a squash makes a new commit, so the PR's own head is compared), wake the coordinator once and log `merged_outside_bar` (a person or an agent merged it by hand; it cannot be undone, and it is never silent) | nobody |
| N3 | marked `parked` | no run of this task is alive | the coordinator |
| N4 | the PR was closed without merging, and no `retry` since | no reviewer runs; wake the coordinator once. The engine never reopens it, and never opens a second PR for a branch (section 9.4): a `retry` closes the attempt, and N11 starts a new one on a new branch | the coordinator |
| N5 | something this task needs has failed `ensure_failures` times in a row, and no `retry` since | wake the coordinator once with what failed and its last words | the coordinator |
| N6 | a recap is open (not decided, and not closed by the owner's override) | wake the coordinator once with the allowed choices; if the trigger is size, CI or time, tell the worker once to write its recap; no reviewer runs | the coordinator |
| N7 | the tip or the PR head has `diverged` from `H`, or the PR head is ahead of `H` with changes the engine did not push | tell the worker once what happened and to restore the branch (a pull, when its tip is only behind). If it still holds at the worker's next turn end, wake the coordinator once, naming the way out: `say` to the worker, then `retry`. Never merge | the worker; then the coordinator |
| N8 | the worker reported "nothing to change" with a reason, and no ready report, `send-back` or `close` came after it | wake the coordinator once (it may `close` the task or send it back) | the coordinator |

### 4.2 Not started

| ID | If | Make sure of | Waits on |
|---|---|---|---|
| N9 | no open attempt; a dependency is not done, or was dropped | — | those tasks, by name |
| N10 | no open attempt; the project is not running, the milestone is not open, no slot is free, or no worker candidate resolves | — | the project, the milestone, a slot, or quota (the first that fails) |
| N11 | no open attempt | a new attempt: a worktree and branch from the fresh remote base (section 9.4) | the engine |

### 4.3 Limits

| ID | If | Make sure of | Waits on |
|---|---|---|---|
| N12 | changed lines at the tip are over `size_cap`, and no owner override covers `H` | open a recap, trigger `size` | the coordinator |
| N13 | commits with a failing check run, in the last hour and since the last recap decision, reached `ci_fails_per_hour` (one bad commit counts once, however many jobs failed), the ladder does not say passed for `H`, and no owner override covers `H` | open a recap, trigger `ci` | the coordinator |
| N14 | the time the worker run has spent in the `working` state in this attempt, since the last recap decision, is over `build_hours`; the ladder does not say passed for `H`; and no owner override covers `H` | open a recap, trigger `time` | the coordinator |

The row that opens a recap also stores its allowed choices. `continue` is allowed only while the task has used fewer than `continues_per_task`, never after a review trigger, and never while this attempt's newest retro verdict is FIX.

### 4.3b The owner's override (rows N15–N19b)

**Owner decision D10.** The owner can say "merge these now" for one or several tasks. It is one card of its own kind, `override`, which only the owner can answer and which no delegation rule can ever answer. **The engine fills the card**, whoever asked for it: for each task its `H` at that moment and its checks at that moment (green, running, red, not reviewed). Nobody types a commit. A task with no ready report cannot be listed, and a task whose worktree has work it has not reported is marked "has unreported work" (that work is left out). A yes writes, for each task, an override at that `H`, together with the checks state the card showed. It waives waiting for checks, being behind the base, review, evidence and the limits of section 4.3. It does not waive a conflict, and it does not reach a commit the card did not name.

| ID | If | Make sure of | Waits on |
|---|---|---|---|
| N15 | an owner override covers `H`, and GitHub does not have `H` | `H` is pushed and one PR is open for the branch | the engine |
| N16 | an owner override covers `H`, and the PR conflicts with the base | tell the worker once: merge the base into the branch, never rebase. The owner's result line says this task was not merged. (Resolving the conflict makes a new commit, which the override does not cover) | the worker |
| N17 | an owner override covers `H`; a task it depends on is not done, or another merge of this project is in flight | — (overridden tasks merge one at a time, in dependency order) | those tasks |
| N18 | an owner override covers `H`, a merge was asked for `H`, and the PR is still open | GitHub refused because the branch must be up to date: bring it up to date once (the result is a merge-only head, which the override still covers). Tries below `merge_tries`: ask again after the back-off. Otherwise wake the coordinator once with GitHub's reason, and say so in the owner's result line | GitHub; then the coordinator |
| N19 | an owner override covers `H`, and the checks at `H` are no worse than the card showed (red merges only if the card showed red) | record the accept for `H` (by override, with what was waived); merge `H` (squash, pinned to `H`) | GitHub |
| N19b | an owner override covers `H` (the checks turned worse than the card showed) | mark the override spent, and say so in the owner's result line: "not merged: checks got worse after the card was made". A spent override covers nothing, so from the next pass the task is on the normal rules (N33 tells the worker about the red check) | the engine |

- The coordinator can open the card when the owner says so (it knows what is queued): `sheepdog factory ask --kind override --task A --task B`. The owner can also raise it without any agent, from the phone or with `sheepdog factory override` in a terminal that carries no run's token. A worker's or reviewer's terminal cannot raise it. Nobody but the owner can answer it.
- "Worse" is ordered: green, then running, then red; no checks and unreadable checks count as red. Checks that are running again because the engine itself updated the branch (N18) do not count as worse.
- The owner gets one result line per card: which tasks merged, and for each that did not, why (a conflict, a newer commit, checks worse than shown, GitHub refused).
- After an override the base is watched as always. If it turns red the coordinator is woken to open a fix task, and the log names the override behind each merge.
- A worker's new commit after the card was made is not covered: that task goes on by the normal rules.

### 4.4 The worker's turn

**In every row that waits on the worker** (here, and N32 and N33) the engine also makes sure the worker run is alive (section 4.8), and watches for a worker that ignores it: if the worker's turn ended with no background job younger than `background_job_minutes` and no open ask, it is nudged once for that turn; a second such turn wakes the coordinator once. A worker stopped at a permission or question dialog is ended after `blocked_minutes` (R29) and restarted like any other run that ended; workers run with full permission in their worktree, so a dialog is a fault, not a question for anyone. No row can stall silently on the worker. Every message to the worker starts with "pull first" when `H` is ahead of the worker's tip.

| ID | If | Make sure of | Waits on |
|---|---|---|---|
| N20 | the tip is ahead of `H`, `merge_only` | push it; `H` moves to the tip. No round | the engine |
| N21 | (from the evidence increment on) the newest evidence covering `H` has a check that failed or ran out of time, and no owner override covers `H` | tell the worker once: which check, and where its output is | the worker |
| N22 | the ladder (section 4.6) says "fix" for `H` | tell the worker once: the findings file; or the coordinator's why; or, for a retro's bounded fix, the retro's majors and its one change, with `size_cap` set to lines now + `continue_lines` | the worker |
| N23 | no ready report in this attempt, or the tip is ahead of `H` with `changes` (the worker is working) | — | the worker |

### 4.5 The engine's turn

| ID | If | Make sure of | Waits on |
|---|---|---|---|
| N24 | the ladder says "recap" for `H`; the task is a split child of a task that already had a retro; and the owner has not answered for this task | one owner ask: "drop it, or tell me how to go on?". No recap yet: there is one decider at a time. "Drop" marks the task dropped | the owner |
| N25 | the ladder says "recap" for `H` | open a recap, trigger `review` (allowed: split, new, drop), with the owner's words when N24 came first | the coordinator |
| N26 | GitHub does not have `H` (no open PR for this attempt, or the PR head is behind `H`) | `H` is pushed and one PR is open for the branch (section 9.4) | the engine |
| N27 | (from the evidence increment on) no evidence covers `H`, and no owner override covers `H` | the task's checks run at `H` (section 11.6) | the engine |
| N28 | the ladder says "retro" for `H` | a retro run for `H` (a first retro, or the delta check of a bounded fix) | the retro reviewer |
| N29 | the ladder says "review" for `H` | a reviewer run for that round at `H` (section 4.8); its report is read when it goes idle; with no report and no background job of its own younger than `background_job_minutes`: one nudge, then stop it; a run older than `review_minutes` is stopped | the reviewer |

Gap (N29 background jobs): herdr-core does not yet report a run's background jobs, so `background_job_minutes` and the young-background-job hold are not shipped yet; an idle reviewer with no report tag is nudged once and then stopped (a reviewer whose tag was refused waits until `review_minutes`).

Red checks while a review runs are not acted on. Telling the worker at once would move the head under the reviewer; N33 acts when the review has passed.

### 4.6 The review ladder

`Ladder(task, H)` answers "what does review need next at `H`": **passed**, **fix**, **review**, **retro** or **recap**. It is a rule table like the others (first match wins, tested the same way) over round, retro and accept records. "Covers" is the function of section 3.1. A coordinator `send-back` is stored as a round-like record that asks for changes.

| ID | If | Answer |
|---|---|---|
| L1 | an owner override covers `H` | passed (the owner chose to skip review for that commit) |
| L2 | this attempt's newest retro verdict is REDESIGN, or has low confidence, and no recap was decided since | recap |
| L3 | this attempt's newest retro verdict is FIX, FIX verdicts in this attempt have reached `retro_fix_verdicts` (with the default 2: the second FIX is the cap), and no recap was decided since | recap |
| L4 | this attempt's newest retro verdict is FIX, and no ready report came after it | fix (the bounded fix) |
| L5 | this attempt's newest retro verdict is FIX, a ready report came after it, and no retro record covers `H` | retro (the delta check) |
| L6 | the newest round-like record covering `H` is a retro verdict SHIP | passed |
| L7 | no round-like record covers `H` | review |
| L8 | the newest round-like record covering `H` is a pass, and fewer than `review_rounds` passing rounds cover `H` | review (by another reviewer candidate when one resolves) |
| L9 | the newest round-like record covering `H` is a pass | passed |
| L10 | it is a reviewer's round that asks for changes, and the worker rejected its findings after it (a `reject` record at `H`, section 7.2). (A coordinator's send-back or a retro verdict cannot be rejected this way) | review (the next reviewer is given the rejections; a finding comes back only with a run that proves it) |
| L11 | it asks for changes; the last `retro_after_rounds_with_majors` rounds all had majors and this is round `retro_from_round` or later; the task has used fewer than `retros_per_task`; the retro has not failed to run (section 4.8 gave up on it); and it is not a split child of a task that had a retro | retro |
| L12 | it asks for changes, and that streak holds, or `max_rounds` rounds were run | recap |
| L13 | it asks for changes | fix |

The same loop at four levels, for reading:

| Level | Who checks | Budget (settings) | When the budget is spent |
|---|---|---|---|
| 0 Review | a reviewer on another model | changes may be asked until the streak of L11 (with the defaults: rounds 2 and 3 both with majors); `max_rounds` in all | level 1 (L11), or level 2 (L12) |
| 1 Retro | a stronger model in a fresh session | `retros_per_task` retros; `retro_fix_verdicts` FIX verdicts, each a bounded fix checked as a delta | level 2, with `continue` not allowed (L2, L3) |
| 2 Recap | the coordinator | `continues_per_task` | split, new attempt, or drop |
| 3 Owner | the owner | one question | drop, or the owner's words go to the coordinator (N25) |

- "Two reviews" means two passing rounds on the final head. A task that needed one fix therefore has three rounds: changes, pass, pass. `review_rounds` set to 1 gives the cheaper bar.
- With the defaults a task gets a third round by itself after two that asked for changes; there is no separate "extra round" to grant.
- A reviewer or retro report that is invalid (section 7.2) is not a round and uses no budget.
- A coordinator `send-back` is not a review round either: it makes the ladder say "fix", and does not count for `review_rounds`, the streak of L11 or `max_rounds`.
- A worker's `reject` is not a review round. It does not count for `review_rounds`, the streak of L11 or `max_rounds`. What bounds it: the round that follows is a real round, and a worker that keeps rejecting meets the streak at round `retro_from_round` (every "changes" verdict has at least one major) or `max_rounds`.
- A retro that cannot be run at all (no candidate, or it failed out) does not hold the task: L12 sends it to a recap.
- A new commit by the worker makes L7 hold again: the next round reviews the new head as a delta.
- Retro verdicts steer only the attempt they were given in (L2–L5). The count of retros a task may have (`retros_per_task`, L11) is kept across attempts, so a new attempt does not earn a second retro.

### 4.7 Landing

Reached only when the ladder says "passed" for `H`. `p` is the latest PR snapshot (section 7.3).

| ID | If | Make sure of | Waits on |
|---|---|---|---|
| N30 | an owner gate applies to this task (the setting `design_gate` for a design task, or the plan field `gate: owner`), the owner answered no in this attempt, and no recap was decided since | open a recap, trigger `owner_no` (allowed: split, new, drop), with the owner's words | the coordinator |
| N31 | an open owner ask names this task, or an owner gate applies and no yes covers `H` | the gate ask is open, naming `H` | the owner |
| N32 | conflict with the base | tell the worker once: merge the base into the branch, never rebase | the worker |
| N33 | checks red at `H` | re-runs used for `H` below `flake_reruns`: re-run the failed jobs once (a check that turns green by itself costs no round; set `flake_reruns` to 0 to never re-run a red check). Otherwise tell the worker once per failing run ("pull first" if the engine updated the branch) | CI; then the worker |
| N34 | checks stopped: cancelled from outside, or a job hit its own time limit | cancelled from outside with re-runs left: re-run. Otherwise (and always for a job's own time limit): wake the coordinator once | CI; then the coordinator |
| N35 | no checks, past `checks_grace_minutes` | dispatch not used for `H`: dispatch the `dispatch_workflow` setting's workflow if it is set and exists at `H` (else skip to the wake). Dispatched less than `checks_grace_minutes` ago: wait. Otherwise wake the coordinator once, naming the ways out: a change that makes a workflow run for this PR, the owner's override (section 4.3b), or drop. "No checks" is never green. Doctor refuses the start of a repo with no PR workflow, and warns when path filters can leave a PR with none | CI; then the coordinator |
| N36 | checks pending, unreadable, or absent within `checks_grace_minutes`; or mergeable unknown | — (when checks are proven `pending` on the current head `H` for `ci_stalled_minutes`, raise the alert `ci_stalled` once for `H` and wake the coordinator once per `H`; clear `ci_stalled` when checks on `H` complete or `H` changes, so a new head starts a new episode only if its checks also stay pending for `ci_stalled_minutes`) | CI |
| N37 | GitHub requires an approving review | wake the coordinator once. Nobody in the factory can give the approval; doctor lists the repo setting at start, and review comments on the PR are passed to the worker as a file | a GitHub approval |
| N38 | the base is red (the runs on the base's head commit), and the PR is behind or the comparison is unknown | — until the base has been red for `main_red_minutes`. Then, once: tell the worker of the oldest task held here to merge the base into its branch and say ready (N20 or a normal round follows; a PR that is no longer behind may merge and so repair the base), and wake the coordinator with that way out named | the base branch; then the worker |
| N39 | the worker run is `working`, or its last turn left a background job younger than `background_job_minutes` | — (it may be about to commit, and a merge would end its job; a stray turn does not undo a pass) | the worker |
| N40 | behind the base, no merge queue | update not asked for `H`: bring the branch up to date with a merge commit (the snapshot's new head moves `H`). Asked less than `checks_grace_minutes` ago: wait. Otherwise it is a failed try (section 8) | GitHub |
| N41 | a merge was asked for `H`, the PR is still open, and it is not behind the base (or a merge queue is on) | in the merge queue: wait. Tries below `merge_tries`: ask again after the back-off. Otherwise wake the coordinator once with GitHub's reason. (A merge call's own failure is not counted by section 8; only this row counts merge tries) | GitHub; then the coordinator |
| N42 | **all of:** the ladder says passed; (from the evidence increment on) evidence covers `H` with every outcome passing or marked manual and judged by the reviewer; `p.head` is `H`; GitHub is reachable; checks green; mergeable; not behind (or a merge queue is on); no open owner ask names the task; the worker run is not `working` and has left no young background job | record the accept for `H`; merge `H` (squash, pinned to `H`) | GitHub |
| N43 | none of the above | wake the coordinator once per stall, keyed by `H` and the facts no row matched: "no rule matched", with the facts. Nothing else is done. (While a slice has not built a row yet, that row's cases land here) | the coordinator |

The merge call's result is never trusted. Only a snapshot that says merged ends the task (N2).

`Cleanup` (part of N1) is a chain of small steps, each safe to repeat: tell the task's runs "merged" and stop them, the whole process group, and check nothing is left; delete the remote branch (never as part of the merge call); fast-forward the local base; remove the worktree only if it is clean and no terminal is inside; delete the local branch only if its tip is in the base. A step that cannot finish (a dirty worktree) is tried again by later passes and listed by doctor; the task is already done.

### 4.8 Making sure a run is alive

A row that needs a run (worker, reviewer, retro) names it by role, round and commit. Only a run of that role, round and commit is its run: the stopped reviewer of an earlier round reads as none yet, not as ended. When no such run is alive, the next step depends on how the last one ended. First match wins.

| Last run of that role | Then |
|---|---|
| a start is pending | wait; it counts as present |
| a stopping alert is active (section 6) | wait; it counts as present |
| the engine itself stopped the last run (a park, a changed request, a round made void by a moved tip; a touched worktree is the no-valid-report row below) | start again (resume for a worker); not counted as a restart |
| none yet | start fresh |
| ended on a usage limit, or no candidate resolves because of quota | the next candidate that resolves starts with a handoff; if none, wait for the soonest pool reset (the task waits on quota). A run that ended on a usage limit makes its pool unusable until that pool's reset time, whatever the reading says. Not a failure, for any role |
| the owner quit it in its terminal | do not restart; wake the coordinator once. Its `retry` restarts the run |
| signed out | add an at-the-Mac item; the next candidate that resolves starts with a handoff; if none, wait until doctor sees it signed in |
| reviewer or retro with no valid report (a verdict read from a touched worktree is none, section 7.2) | start again with the next reviewer candidate while tries are below `reviewer_retries`; then it counts as a failure (N5). Slice 1 marks the round's start act failed instead, so only an owner start tries the round again |
| worker, any other end, restarts below `worker_restarts` | wait until backoff has passed and a slot is free, then resume the session if it is resumable and on the same CLI, else start with a handoff |
| otherwise | it counts as a failure (N5) |

Starting or restarting a worker needs a free slot, like a first start (N10); without one it waits. A run that ends `signed_out` while the CLI's own sign-in check passes counts as a restart, so it cannot loop.

A start first delivers the messages held for that run (findings a dead worker never received).

Runs a task's matching row does not want are stopped: a reviewer whose round is over (its verdict was accepted) or void (the round is void as soon as the tip moves off the round's commit), and every run of a task at N1 to N3. **The worker run is wanted by every other row** while the attempt is open, also while a recap is open or the engine has the turn: it writes the recap and makes the fixes. A split or a new attempt stops it as part of that decision. A task uses a slot only while its worker run is `starting` or `working`.
In slice 4, an open attempt holds its slot through `idle` too (freeing it only when its worker run is gone or the task stops), because `max_running` caps concurrent tasks rather than only active worker turns.

### 4.9 What the coordinator and the owner can do to a task

Each is a record, accepted by the table in section 7.2. The rules above then land elsewhere by themselves.

| Command | Record | Effect through the rules |
|---|---|---|
| `park`, `unpark` | mark | N3 |
| `drop` (with a why) | mark | N1. Tasks that depend on it then wait at N9 "depends on dropped SDF-n" until the plan is amended. Nothing is unblocked silently |
| `close` (only after a "nothing to change" report) | mark | N1, with nothing to merge |
| `retry` | an act that resets failure, restart, reviewer-try and merge-try counts, closes an attempt whose PR was closed (N4), and allows a run the owner quit to start again. Slice 1 has no `retry`: the owner starting the project again clears every failed act, a failed merge or cleanup act included | N5 and N4 no longer hold |
| `send-back` (with a why) | a round-like record "changes" from the coordinator | N22 |
| recap decision: `continue` | closes the recap; raises `size_cap` to lines now + `continue_lines` | N23 |
| recap decision: `split` (children pass the plan checks) | mark `split`; children carry `split_from` and the retro's worker change; dependants move to the children | N1 |
| recap decision: `new` (the brief was amended since the recap opened) | closes the attempt and its PR (with a note); the old branch is kept | N11 |
| recap decision: `drop` | mark | N1 |
| `plan amend` on a started task | changed plan fields, with a why | the worker is told once what changed; a changed worker request restarts the worker with a handoff; a changed estimate applies to the next attempt and may lower the open attempt's `size_cap` when no size recap is open, never raising an open attempt's `size_cap` |
| the owner answers yes to an `override` card (section 4.3b) | an override at each named commit; an open recap of those tasks is closed | N15–N19b |

### 4.10 Scheduling

There is no queue. N10's "a slot is free" is decided by one pure function over all tasks that have reached N10, in this order: a task the owner just answered, then `priority: urgent`, then the critical path, then oldest. One slot beyond `max_running` may be used by a task the owner just answered or an urgent one. A milestone is open when every earlier milestone is done and its gate task (section 11.4) is done.

### 4.11 What the owner sees

**In code, the stage, the wake and its text are fields of each row**, beside the condition, the thing to make sure of and "waits on". The table below, the wake list in section 10 and the run matrix in section 5 are printed from the rows by the table printer and checked against this file in `go test`; nobody keeps them by hand. As printed today:

| Matching rule | Stage | Why line |
|---|---|---|
| any, with an open gated owner ask on the task (even a done task) | `owner_answer` or `owner_gate` | the question |
| N1, N2 | `done` | — |
| N3–N8, N12–N14, N25, N30, N43, and N18, N34, N35, N41 once they wait on the coordinator | `blocked_or_failed` | the rule's reason and who it waits on |
| N33–N38, N40–N42 | `ci` | "Waiting on CI, 18 min" |
| N15, N18, N19, N19b | `ci` | "Merging now, by your override" |
| N9, N17 | `waiting_on_dep` | "After SDF-53" |
| N10, N11 | `queued` | the first failing reason |
| N23 with the worker idle | `idle` | — |
| N16, N20–N23, N26–N29, N32, N39 | `working` | "Working · model · round 1 of 2", "Checking 3 outcomes" |

Stage is a field of each task row; `GraphViewOf` and `StatusViewOf` read it from the task's last matched row (with ended and parked taking precedence, and a stored-state fallback only before any row has matched), share the same why line, and do not yet emit `idle` (N23 with an idle worker) or `owner_answer`/`owner_gate`.

## 5. The run table

A run is one agent process in one hc terminal: coordinator, worker, reviewer, retro reviewer, designer, verifier. There is **one** way to start one (section 9). A run is found again by its own label (`sheepdog.run`) and the terminal's creation time, never by an address alone or a role label, so a reused address or a hand-started agent is not taken for a run.

Runs are driven by events, so this is a transition table. Every cell is written; there is no default.

| Event | From |
|---|---|
| `launched`, `launch_failed` | the start executor |
| `screen` | start watcher: the adapter's class for a screen with no status yet |
| `status` | hc: idle, working, done, blocked (permission, question, auth), error |
| `activity` | process observer: new output, or a child process started or ended |
| `exit`, `lost` | hc: the program ended (with "the owner quit it" when the owner's keys ended it); hc says the terminal does not exist |
| `deliver` | the engine has a message for this run (may be urgent) |
| `stop` | the engine |
| `pool_floor` | pools observer: this run's pool reached the floor |
| `tick` | the clock |

| ID | From | On | If | To | Do |
|---|---|---|---|---|---|
| R1 | `starting` | `launched` | — | same | keep the terminal's address, hc id and creation time |
| R2 | `starting` | `launch_failed` | — | `ended(failed)` | — |
| R3 | `starting` | `screen` | a trust, confirm or first-run screen with verified adapter keys, not answered yet for that screen class | same | press the adapter's keys for it |
| R4 | `starting` | `screen` | signed out | `ended(signed_out)` | stop it |
| R5 | `starting` | `screen` | usage limit | `ended(quota)` | stop it |
| R6 | `starting` | `screen` | — | same | — |
| R7 | `starting` | `status` | idle; the first prompt is typed, and not sent yet; no trust, confirm or first-run screen is up | `working` | send the first prompt |
| R8 | `starting` | `status` | working | `working` | — |
| R9 | `starting` | `status` | idle or done (or error on a non-reviewer/retro run), with the first prompt already given on the command line | `working` | — (R20 then decides whether it is idle) |
| R10 | `starting` | `status` | any status other than blocked or error (those are R14–R16) | same | — (does not count as a usable status for R11) |
| R11 | `starting` | `tick` | older than `start_minutes` and no usable status was ever reported | `ended(blank)` (or `ended(stuck)` on a start dialog) | stop it. Two blank starts in a row run the volume probe (section 6) |
| R12 | `starting` | `tick`, `activity` | — | same | — |
| R13 | `starting`, `working` | `exit` | a resume, within `start_minutes` | `ended(resume_failed)` | the session is marked not resumable |
| R14 | `starting`, `working` | `status` | blocked, kind auth, on a run older than `start_minutes` (a brief sign-in prompt at start is R15) | `ended(signed_out)` | stop it |
| R15 | `starting`, `working` | `status` | blocked | `blocked` | keep the question and the time as facts. What happens next is R29; nobody is woken yet |
| R16 | `starting`, `working` | `status` | error, on a reviewer or retro run | `ended(error)` | stop it |
| R17 | `working` | `status` | — | same | keep the status and its time |
| R18 | `working` | `tick` | status idle or done (or error) for `idle_grace_seconds`; a held message exists; not a reviewer or retro run whose report is still unread | same | send the next held message; the idle time starts again from the send |
| R19 | `working` | `tick` | status idle or done for `idle_grace_seconds`, and the screen is the adapter's usage-limit class | `ended(quota)` | stop it |
| R20 | `working` | `tick` | status idle or done (or error) for `idle_grace_seconds` | `idle` | reviewer or retro: read its report (section 9.3). Any role: the turn number, background jobs and their age become facts |
| R21 | `working` | `tick` | no output and no child activity for `quiet_minutes`; not interrupted yet | same | interrupt once; wait `interrupt_seconds` |
| R22 | `working` | `tick` | still quiet after the interrupt | `ended(stuck)` | stop it |
| R23 | `working` | `tick`, `activity` | — | same | — |
| R24 | `idle` | `deliver` | — | `working` | send the message |
| R25 | `working` | `deliver` | urgent | same | interrupt, then send |
| R26 | `starting`, `working`, `blocked` | `deliver` | — | same | hold the message on the run |
| R27 | `idle`, `blocked` | `status` | working | `working` | — |
| R28 | `blocked` | `status` | idle or done | `working` | — (R20 then decides) |
| R29 | `blocked` | `tick` | blocked for longer than `blocked_minutes` | `ended(stuck)`, or `ended(signed_out)` when the dialog is a sign-in | stop it. Section 4.8 then resumes it or moves to the next candidate, and counts it; the coordinator hears through N5 if it keeps happening |
| R30 | `idle`, `blocked` | `status`, `tick`, `activity`, `screen`, `launched`, `launch_failed` | — | same | — |
| R31 | `starting`, `working`, `idle`, `blocked` | `pool_floor` | worker or designer | same | urgent deliver: "write your handoff and stop"; stop it with result `quota` when it next goes idle, or after `handoff_minutes`, whichever is first |
| R32 | `starting`, `working`, `idle`, `blocked` | `pool_floor` | — | same | — |
| R33 | `starting`, `working`, `idle`, `blocked` | `exit`, `lost` | — | `ended(exited / lost)` | remove a leftover terminal; note whether it is resumable and whether the owner quit it; held messages go back to the task or project |
| R34 | `starting`, `working`, `idle`, `blocked` | `stop` | — | `ended(stopped)` | end the terminal's whole process group and check nothing is left |
| R35 | `working` | `launched`, `launch_failed`, `screen` | — | same | — |
| R36 | `ended` | `deliver` | — | same | refuse `run_ended` (the message goes back to the task or project, whose rules restart the run) |
| R37 | `ended` | every other event | — | same | — |

**R31, the floor handoff.** Each worker or designer on the affected pool gets one urgent "write your handoff and stop" per attempt. A replacement run in the same attempt on a different pool gets its own handoff when that pool reaches its floor; the same pool never gets one twice in one attempt. Remember delivery and the deadline across a restart: save the handoff marker and deadline before the send so a crash or a lost reply after the send never sends it again while the deadline still stops the run, and only a send that herdr-core refused before accepting it clears the marker. Deliver at once to a working run or to an idle run, which starts its handoff turn. Hold it for a starting run until it is ready. For a blocked run, only the deadline applies. On a pass where the floor is seen, set the handoff deadline before timeouts are judged; while a handoff deadline is pending, R29 (blocked too long), starting and quiet timeouts do not end the run, which ends with `quota` at the deadline or on the next idle. Stop with `quota` when the run next goes idle after delivery or when `handoff_minutes` have passed since the floor was seen, whichever comes first.

Resolve the next run through the pool gate. It takes over in the same worktree with its uncommitted work and held owner messages, and a launch failure while starting the replacement does not overwrite the ended run's `quota` result and reason. The handoff gives the branch commits, uncommitted files, held messages, the outgoing run's last chat lines (from its last screen read through herdr-core), `the <pool> pool reached its floor` and the worktree path; a git read that fails shows `unknown (<reason>)`, never `none`. It points to the brief. When no candidate is usable, the task waits with the soonest reset in its reason, including the day when the reset is more than a day away.

**R32, other roles.** Coordinators and reviewers keep running at the floor without a handoff. On an R5 or R19 quota end, a reviewer keeps the round and its head and spends no try. Show `All reviewers are out of quota; retries at <time>` only when every otherwise eligible reviewer is quota-blocked; a mixed set (such as one out of quota and one signed out) shows each reason. A coordinator's quota end puts its in-flight wake back on the queue, including the default Claude coordinator on an R19 quota end while working. A quota-end cooldown is per `(CLI, pool)` across projects: one recorded in one project blocks that `(CLI, pool)` in every project, also after a restart, and a later quota end never shortens an unexpired cooldown. A quota wait (task, reviewer, or coordinator J7) is logged once per episode, independent of the displayed time (the day disappearing at 24 h does not log again) and across a restart.

The matrix (row ids; every cell filled):

| | `launched` / `launch_failed` | `screen` | `status` | `activity` | `tick` | `exit` / `lost` | `deliver` | `stop` | `pool_floor` |
|---|---|---|---|---|---|---|---|---|---|
| `starting` | R1 / R2 | R3–R6 | R7–R10, R14–R16 | R12 | R11, R12 | R13, R33 | R26 | R34 | R31, R32 |
| `working` | R35 | R35 | R14–R17 | R23 | R18–R23 (with R19) | R13, R33 | R25, R26 | R34 | R31, R32 |
| `idle` | R30 | R30 | R27, R30 | R30 | R30 | R33 | R24 | R34 | R31, R32 |
| `blocked` | R30 | R30 | R27, R28, R30 | R30 | R29, R30 | R33 | R26 | R34 | R31, R32 |
| `ended` | R37 | R37 | R37 | R37 | R37 | R37 | R36 | R37 | R37 |

- The adapter names its own long-lived helper processes, so one of those does not count as child activity and cannot keep a dead run alive.
- Sending waits for a person's unfinished typing in that terminal; herdr-core does that itself (`hc send`). The engine decides "idle" from the run's own state, not from a send-only-if-idle check, because an agent can report done between steps.
- Runs are started so that the terminal ends when the program ends (no kept shell), so a dead agent always produces `exit`.
- A run's end is a fact the task and project rules read (section 4.8, section 6). The run table restarts nothing.
- herdr-core gives no output time or output events (spike S0.8), so `activity` is derived by `world` from a change in the normalised screen text (timers, digits and spinner glyphs stripped) between two reads (`hc read`), and R21's own interrupt redraw is re-baselined so R22 still fires if the run stays quiet.
- The Claude trust dialog and the Codex hooks dialog report no status, so `screen` events come from matching each adapter's whole known UI phrases in the bottom lines of the screen, never agent output above; only dialogs whose default key was verified in spikes (agy trust) are auto-answered by R3, while unverified dialogs stay on R6 and time out via R11 as `ended(stuck)`; agy also reports its trust screen as `status blocked, kind permission` (spikes S0.2, S0.8).
- A gone terminal is `state finished, status null`; `reason exited` vs `killed` tells an engine kill from anything else, but not who quit it (spike S0.8).

## 6. The project rules

First match wins.

| ID | If | Make sure of | Shown as |
|---|---|---|---|
| J1 | a start was asked and doctor has not passed since | doctor runs; on failure its fix lines are shown and the start is dropped; on success the project is marked started | start shell |
| J2 | never started, or stopped by the owner | no run of this project is alive; the plan and tasks are kept | "Stopped by you" |
| J3 | a stopping alert is active (`no_pool`, `disk`, `consent_prompt`) and no coordinator run is alive | nothing starts (every new start and restart is held; tasks wait at N10) | "Stopped", with the fix |
| J4 | a plan exists, every milestone is done, no task is open, and the owner has given no new goal since | the final journal entry; then no coordinator | "Finished" |
| J5 | the coordinator role is held on sign-out (no configured coordinator candidate has a positive sign-in check) | the alert `agents_down` with the at-the-Mac fix; recheck sign-in on schedule even with no run; use another coordinator candidate only if configured and positively signed in; hold until a positive check for the held candidate or a completed coordinator turn | running |
| J6 | no coordinator run is alive; a start is not allowed yet (`coordinator_gap_minutes`, `coordinator_starts_per_hour`) | wait; raise `agents_down` if the hourly limit is the reason | running |
| J7 | no coordinator run is alive | start one: fresh with the goal if there is no plan, else with a handoff built from the store; held wakes are kept for it | "Planning" with no plan, else running |
| J8 | the coordinator reported an error at the end of a turn | first time: tell it to go on. Again within `quiet_minutes`: stop it (J7 follows) | running |
| J9 | wakes were delivered, no coordinator turn followed for `coordinator_silent_minutes`, and no stopping alert is active | stop it (J7 follows); the second time in a row, raise `agents_down` | running |
| J10 | the coordinator's run ended as `stuck` at a dialog (R29) | J7 restarts it. The second time in a row, tell the owner (the one coordinator event that speaks) and raise `agents_down` | running |
| J11 | a rotation is due (`rotate_after_hours`, `rotate_after_turns`, or the template changed), the coordinator is idle, and no stopping alert is active | ask it for the journal entry; when written, or after `handoff_minutes`: write the fact part, stop the run (J7 follows) | running |
| J12 | wakes are held and the coordinator is idle | deliver up to 20 as one batch and keep the rest held | running |
| J13 | no wake for `heartbeat_minutes` and the coordinator is idle | a heartbeat wake | running |
| J14 | otherwise | — | running ("nothing needs you" when no owner ask is open) |

The owner is told once per alert episode, whoever is alive. While a stopping alert is active, every new start is held: coordinator (J7), worker (N11, N23), reviewer (N29), and restarts across roles. Runs already live stay managed (a live coordinator keeps getting wakes and heartbeats through J8 to J13), and the section shows "Stopped" with the alert's fix.

Held wakes merge only when their kind and their task are the same. The owner's words (`say`) are a wake marked as the owner's.

**Alerts.** An alert is a persisted episode: raised once with its cause and time, logged and shown once per episode, visible in `factory status` even with zero tasks, kept across a restart without repeating, cleared when its own clear evidence is seen, and raised again as a new episode if the cause returns. Every alert has one observer that both raises and clears it, and that keeps probing while it is active. A stopped project comes back by itself. Outage signals (`github_unavailable`, `hc_unavailable`) use the same persisted episode: raised once per outage, kept across a restart without repeating, and cleared on the first good observation.

| Alert | Stops the factory | Raised when | Cleared when |
|---|---|---|---|
| `no_pool` | yes | eligible open tasks exist and none has a usable worker candidate, or the plan is empty with no live coordinator and no usable coordinator candidate. A missing coordinator candidate alone does not stop planned work (J7 already holds the coordinator, and workers keep working). Eligible means not done (not ended), not held by a dependency, and not approved and waiting only on CI. A partial shortage holds only the affected tasks at their own rows | a usable candidate appears |
| `disk` | yes | positive disk-full evidence only: an `ENOSPC` from a store or git write, or the project folder's or data directory's volume reading at least `disk_full_percent` (default 90%, range 50–99) used; both volumes are measured and the affected volume is named in the alert | a write succeeds and both volumes read under `disk_full_percent` (an unknown reading never raises or clears it) |
| `consent_prompt` | yes | positive evidence from a protected-folder probe of the project folder through herdr-core: two consecutive probe timeouts or two consecutive blank results (the shape doctor already detects). An hc outage or another inconclusive failure is `hc_unavailable` or unknown, never `consent_prompt`. Blank starts with a healthy folder are ordinary start failures (section 4.8, J6), not this alert | the first good protected-folder probe |
| `main_red` | no: it does not stop new starts; N38's narrower hold on behind or unknown-behind merges and branch updates stays, so a task that repairs the base can start and merge | the runs on the base's head commit have been red for `main_red_minutes` | a positively known completed good base snapshot (every run on the base's head commit finished and none is red) |
| `ci_stalled` | no | checks are proven `pending` on the current head `H` at N36 for `ci_stalled_minutes` (once per `H`; unreadable, absent, or unknown checks do not raise it) | checks on `H` complete, or `H` changes (a new head is a new episode only if its checks also stay pending for `ci_stalled_minutes`) |
| `agents_down` | no | the coordinator cannot be kept alive (J5, J6, J9, J10); or agents on two different CLIs started blank in a row while the project folder answers (a herdr-core or CLI fault, counted as an outage, not as each task's failure) | J5: a positive sign-in check for the held candidate, or a completed turn of a coordinator (a check that times out or cannot run is `unknown (<reason>)`, which is not positive and never counts as signed out, and doctor prints `unknown (<reason>)` rather than "not signed in"; rechecks run on their own schedule even with no run; another coordinator candidate is used only if configured and positively signed in; a healthy unrelated worker never clears it). J6/J9/J10: a coordinator run is alive and has taken a turn. Blank starts: an agent has started |

`agents_down` is a sixth kind; the contract has five (open item O3).

**Notes on `consent_prompt` and `disk` (5b.9).**
- `consent_prompt` is raised only on positive evidence from a protected-folder probe of the project folder through herdr-core: two consecutive probe timeouts (while the temp folder answers) or two consecutive blank results (exit 0 with empty output / no probed directory). An hc outage or any inconclusive failure (for example both the project folder and temp folder timing out, or an RPC error) is `hc_unavailable` or `unknown (<reason>)`, never `consent_prompt`. It clears on the first good probe.
- `disk` is raised only on positive evidence: an `ENOSPC` from a store or git write, or either the project folder's volume or the data directory's volume reading at or above `disk_full_percent` (default 90, range 50–99). Both volumes are measured and named in the alert; an unreadable volume is `unknown (<reason>)` and never raises or clears `disk`. It clears when a write succeeds and both readings are below `disk_full_percent`.
- When the store cannot write (for example on `ENOSPC`), `disk` stays active in memory, is shown by `factory status`, and is logged to stderr; it is persisted to `state.json` on the first successful write.
- Both `consent_prompt` and `disk` are stopping alerts (they hold new starts across roles per section 6 / J3) and are probed again on every pass while active.

The coordinator is started in the project's repo folder, never a home folder or another project's.

## 7. Asks, the acceptance table, and the observers

### 7.1 Asks

| ID | From | On | If | To | Do |
|---|---|---|---|---|---|
| A1 | (new) | `ask_open` | a delegation rule covers this kind and scope, and the kind can be delegated | `answered` (by rule) | — |
| A2 | (new) | `ask_open` | — | `open` | kind `decision`: wake the coordinator. Other kinds: notify the owner |
| A3 | `open` | `answer` | — | `answered` | an ask opened by a run: deliver the answer to that run, or hold it for the task's worker if the run has ended. An ask opened by a rule: the answer is a record the rules read (N25, N30, N31) and a coordinator wake |
| A4 | `open` | `cancel` | — | `cancelled` | — |
| A5 | `answered`, `cancelled` | `answer`, `cancel` | — | same | refuse `already_closed` |

Whether an `ask_open` or an `answer` is valid at all (who may send it, the owner's proof) is the acceptance table's job, below. All six cells (two states × two events, plus the two ways to open) are rows. Delegation (`A1`) arrives in slice 14; until then every valid `ask_open` matches `A2`.

- **Ask records and kinds.** Project asks live in `State.Asks` (`projects/<id>/state.json`) and carry `id`, `project`, `folder`, `kind`, `gated` (`kind != "decision"`), `question`, `options`, `recommend`, `task`, `head`, `asked_by`, `run_id`, `at`, `status` (`open`, `answered`, `cancelled`), `answer`, `why`, `answered_at`, `answered_by` (`coordinator`, `owner`, `rule`), `proof_id` and `device_id`. The closed v1 ask kinds are `decision`, `gate`, `release`, `paid-key`, `delete`, `install`, `reset` and `rule` (`override` arrives in slice 7); any other or empty kind is refused `unknown_kind`.
- **Who an ask belongs to.** A `decision` ask (a worker asking the coordinator) belongs to its task and is cancelled when the task ends. Every other kind belongs to the project, may name the task it is about, and outlives the worker and the task: a release question once died with its worker's terminal (field note 31). When a task's original worker run has ended or been replaced before its `decision` ask is answered, `A3` delivers the answer to the task's open attempt worker (or holds it in `Attempt.Held` for the next worker start, KB-ASK-10); when the coordinator opened a gated ask, `A3` delivers/wakes the coordinator with the owner's answer.
- **Gated kinds** are all kinds except `decision`: their answer needs the daemon's proof of the owner's tap or words, or a delegation rule where one is allowed.
- **The owner's proof** means: the answer came from a device that holds a session ticket from the daemon, or from the owner's live voice. In-process (`internal/factory/proof.go`), `Engine.MintProof(askID, option, inputKind, deviceID)` mints a short-lived `AnswerProof` (`prf-` + 12 hex characters, persisted in `<data_dir>/factory/proofs.json` mode `0600` with 24 h retention), refusing `deviceID == ""` or `"local"` and any `inputKind` other than `"tap"` or `"voice"` (`proof_rejected`); `Engine.Answer(askID, option, proof)` requires a verified minted proof on every gated ask (`proof_required` when empty, `proof_rejected` when unminted, expired, mismatched on `ask_id` or `option`, `"local"`, or non-`tap`/`voice`) and logs `device_id`. A paired phone holds a ticket. So does a browser that has enrolled with the daemon: the factory web page may then answer every ask, the never-delegable kinds included, exactly as a paired phone does (owner decision D15). **An answer to a gated ask that comes from this computer with no ticket is never the owner's**, whatever page or origin it claims: the setup page's origin check stops another website in the browser, not a program on the machine, and agents run as the owner's user. This guard is not in the daemon yet (spike S0.9 found no such check: `AnswerTap` takes no device), so slice 6 builds it. It is not about which screen the owner used. **A browser on this computer enrols with a short code** that the owner reads in the Mac menu-bar app and types into the page (owner decision D17); once slice 6 is built, the daemon hands that code, or a link carrying it, only to the menu-bar app, never to another caller on this computer.
- **What this stops, and what it does not.** It stops an agent taking a shortcut. It does not stop a hostile process running as the owner's user, which could enrol its own device or edit the state file. Today an agent does not even need to be hostile: `GET /api/pair` is open to any caller on this computer and gives it an enrol code whenever the daemon can pair a phone at all (a non-loopback `web.bind`, or `web.advertise`), so four HTTP calls (pair, enroll, challenge, session) enrol a device; slice 6 closes that path (O20). The check on that is after the fact: every override and every rule change is written to the log and the owner's digest with the device that answered.
- **Browser enrolment and factory ask HTTP contract (D15, D17, O20 — slice 6, PR 6.4/6.5):**
  1. **Authenticated short browser-enrolment code (`POST /api/pair/browser-code`).** Registered at `accessDevice` and additionally refuses ticketless loopback (`deviceOf(r) == "local"`) with `403` (`device_session_required`): only a paired device session (the Mac menu-bar app in PR 6.5) may issue a browser code. Returns `200` `{"code", "expires", "expires_in": 300}`. The code is short (8 characters from an unambiguous uppercase alphabet without `0/O/1/I/L`, formatted `XXXX-XXXX`; `POST /api/devices/enroll` normalizes case, spaces and hyphens), single-use (`410` `code_used` on reuse; a new code retires the previous one as `code_expired`), expires after **5 minutes**, is never written to the event log, and enrols `kind: "browser"` only. **Phone QR pairing compatibility:** `GET /api/pair`, `GET /api/pair/qr.svg`, `GET /api/pair/qr.json`, `sheepdog pair`, the setup and debug pages, and the iOS E2E tests keep their existing 10-minute `enrollCodes` unchanged; issuing a browser code does not invalidate an active phone QR code.
  2. **Loopback rate limit on `POST /api/devices/enroll`.** Failed enrolment guesses are rate-limited on loopback as well as remote addresses (`internal/web/limits.go`): 5 wrong/used/expired guesses inside the 1-minute window return `410`, and the **6th** wrong guess inside the window gets `429` (`reason: "rate_limited"`), blocking further enroll attempts from loopback for `limitBlock` (5 minutes).
  3. **Factory ask routes (`GET /api/factory/asks`, `POST /api/factory/asks/{id}/answer`).** Both are `accessDevice`. `GET /api/factory/asks` returns `{"protocol": 1, "asks": [...]}` with open asks from `Engine.Asks(true)` (empty array `[]`, never `null`). `POST /api/factory/asks/{id}/answer` accepts `{"option"}`, refuses any request without a paired device session (`deviceOf(r) == ""` or `"local"`) with `403` (`code: "proof_required"`, KB-ASK-19), and for an authenticated browser or phone session mints an in-process `AnswerProof` (`Engine.MintProof(..., ProofInputTap, dev)`) and calls `Engine.Answer`, recording `device_id` on the ask and in the project's factory log (`log.jsonl`).
  4. **`/factory` web ask cards and automatic re-auth (`web/factory/*`).** `/factory` polls `GET /api/factory/asks` and renders open asks as cards. Answering from an unenrolled browser shows on-demand instructions to pair with a short code from the Mac menu-bar app (never fetching a code automatically). After a daemon restart clears in-memory tickets, an enrolled browser silently re-authenticates with its non-extractable WebCrypto P-256 key (`GET /api/auth/challenge` + `POST /api/auth/session`) and completes the answer without pairing again.
- **Never delegable:** an override, a paid key, a quota reset, a release with no stated scope, a rule change.
- An ask about a task stores the commit it was asked at. A yes is a record at that commit. On any pass, an open gate ask whose commit no longer covers the task's `H` is cancelled, so the owner is never asked about a commit that is gone.
- Workers and reviewers open only `decision` asks; the coordinator turns one into an owner ask when the owner must decide. (The old engine let workers open owner kinds. This is a deliberate change: the owner hears from one voice.)
- Asks never expire on a timer. The engine owns ask status; the supervisor's `fask-` card is a view of open owner asks and changes when the engine says so, not on a 2 s poll.

### 7.2 The acceptance table

Every report and command enters here. Valid: it becomes a record. Not valid, or not listed: refused with the code and one plain sentence, logged in the daemon's event log (section 14), and nothing changes. The conditions are about the report itself (who sent it, which commit and round it names, its shape), not about a phase. Reads (`show`, `status`, `log`, `graph`) change nothing and are open to every role; this table is about what changes records.

| Report or command | Who may send it | Valid when | Else |
|---|---|---|---|
| `ready` | the task's worker or designer run | the worktree is clean; the commit is ahead of `H` (if the engine moved `H` past the worker's tip, the refusal says "pull first"); for each outcome it names a proof command, which the report client runs in the worktree, keeping the exit code and the output, or an artifact (`--artifact <outcome>=<path>`) for an outcome marked manual; every proof command exits zero; it says what the change touched and what was skipped or not checked | `dirty_tree`, `nothing_new`, `pull_first`, `outcome_missing`, `proof_failed` |
| `ready --reject "<why>"` | the task's worker run | the ladder (4.6) says fix for `H`, so a reject cannot skip an open recap; the newest round-like record covering `H` is a reviewer's round that asks for changes (not a coordinator's send-back and not a retro verdict: those are not the worker's to reject); there is no new commit; a reason is given for the findings it rejects. (Findings the worker fixes and findings it rejects in the same turn: a normal `ready` at the new commit, with the rejections in the commit message) | `nothing_to_reject`, `why_missing` |
| `ready --nothing-to-change` | the task's worker run | a reason is given; no commit over the base | `nothing_new` |
| `recap-written` | the task's worker run | a recap is open; the file exists | `no_recap` |
| check results | the engine's own check run | they are for `H` | dropped |
| `verdict` | the run the engine started for that round | the tag carries that run's one-time code (text the reviewer merely printed, such as a diff or a fixture written by the worker, cannot carry it); the round and commit are that run's; pass means no majors and changes means at least one, and `majors` equals the reply's `major:` lines (section 9.3); the worktree is untouched when the tag is read (clean, its tip at the round's commit); the reply also answers which lines could go and how many findings the worker should have caught itself, as reply lines, never as tag fields: `deletable:` (text, "none" is an answer) and `self-check misses:` (a number of 0 or more; "none" reads as 0) are required; `class:` and `not run:` are kept on the round when given. (A separate judgement per outcome, "the check proves it" or not, becomes a required field with the evidence increment. Until then a proof that does not show its outcome is reported as a major, and the reply and tag of prompts.md 5.4 are the whole report) | `invalid_report`; the run counts as failed, the round is not used, and the reviewer is told why once per run so it can answer again. A touched worktree voids the round instead: logged, and the run counts as one with no valid report (section 4.8) |
| plan review report (from the plan-review slice) | the plan reviewer run the engine started | the tag carries that run's one-time code; each finding names a task and one of `delete`, `shrink`, `merge`, `split`, `design-first`, with a why | `invalid_report` |
| the coordinator's answer to a plan finding (from the plan-review slice) | the coordinator | each finding is answered done, or rejected with a why; then the plan is submitted again | `finding_unanswered` |
| `retro` | the retro run the engine started | the tag carries that run's one-time code; the commit is that run's; the cause is in the fixed set; the tag parses | `invalid_report`; the task's retro is not used |
| `ask` | a worker or reviewer (kind `decision`, `sheepdog factory report ask "<question>" --option ... --recommend ...`); the coordinator (any kind, `sheepdog factory ask --kind <kind> [--task <task>] "<question>" --option ... --recommend ...`; for an override `--kind override --task A --task B`, and the engine fills the card); the owner (kind `override`, from the phone, the factory page, or `sheepdog factory override` in a terminal with no run's token) | known kind; 2 to 9 distinct non-blank options; a recommended one among the options; one single-line question of 1 to 300 characters; when the recommended option is a negative or a wait, another option starts with `Yes` (KB-ASK-9); no option ends with a short word in parentheses such as `Switch (v2)` (KB-ASK-9) | `unknown_kind`, `invalid_ask`, `kind_not_allowed` |
| `answer` | the coordinator (kind `decision`, `sheepdog factory answer <ask> "<option>" [--why "<why>"]`); the owner, with the daemon's in-process `AnswerProof` (`Engine.MintProof` + `Engine.Answer`) of a paired-device tap or live spoken words; a delegation rule | the ask exists and is `open`; the option matches an entry of `ask.options` (or its 1-based index); an optional `--why` is at most 500 characters; a gated kind has a verified `AnswerProof` (`input_kind` in `tap, voice` and `device_id` non-empty and not `"local"`) | `not_found`, `already_closed`, `invalid_option`, `proof_required`, `proof_rejected`, `kind_not_allowed`, `invalid_report`, `not_delegable` |
| `plan apply`, `plan amend` | the coordinator | the plan checks pass (section 11.7); an amend has a why | `plan_invalid`, with every failing check |
| recap decision | the coordinator | the task is not ended; a recap is open; the choice is in its allowed set; a why is given; split: the children pass the plan checks; new: the brief was amended since the recap opened | `already_closed`, `no_recap`, `not_allowed` (naming the allowed set) |
| `park`, `unpark`, `drop`, `close`, `retry`, `send-back` | the coordinator | the task is not ended; a why is given; `close` only after a "nothing to change" report | `already_closed`, `why_missing` |
| `say <run>` | the coordinator | the run is alive | `run_ended` |
| start, stop, say, `touch` | the owner: by voice, a paired device or the factory page, or `sheepdog factory start` and `stop` in a terminal that carries no run's token | a start is always recorded (J1 then runs doctor); a run's terminal is refused, so a worker cannot stop the factory | `kind_not_allowed` |
| a setting change | the owner by voice, phone or the factory page; the coordinator; or `sheepdog factory set` from a terminal that carries no run's token (a worker's or reviewer's terminal is refused: they open only `decision` asks). Each only raises an ask of kind `rule` | the value is in its range; it is saved only when the owner answers that ask with the daemon's proof. A terminal is never taken for the owner: a worker has a shell too | `out_of_range`, `proof_required`, `kind_not_allowed` |

Two rules hold for every row. A report from a run whose task has ended, and a command on an ended task, is refused `already_closed`. Every why or reason (a reject, nothing to change, a recap decision, a task command, a plan amend) is cleaned before it is stored, logged or put in a prompt (control characters and line breaks become spaces, runs of spaces become one); an empty one is `why_missing`, and one over 500 characters is `invalid_report`.

The daemon knows which run is calling from a token in the run's environment (`SHEEPDOG_RUN_TOKEN`). The same environment carries the daemon's port (`SHEEPDOG_WEB_PORT`), so the report client needs no config file. A worker cannot record a verdict. The coordinator cannot accept, merge, add a round or mark a task done.

### 7.3 Observers

**The PR snapshot.** One reader per open PR, always pinned to the repo of the push remote. It runs every 60 s, every 20 s while checks are pending, and at once after the engine acts on the PR.

| Field | Values | How it is derived |
|---|---|---|
| `state` | open, merged (with the merged commit), closed | GitHub |
| `head`, relation to `H`, `head_by` | commit; same/ahead/behind/diverged; engine or other | GitHub and git |
| `mergeable` | clean, conflict, unknown | GitHub |
| `behind` | commits behind base, or unknown | GitHub |
| `base_red` | bool | the base observer |
| `merge_queue` | none; on; queued; removed(reason) | GitHub |
| `review_required` | bool | GitHub's review decision |
| `checks`, failing run ids | below | the rollup |

**Checks rollup,** first match wins, over the newest run per check name at `head`, with a fallback to the REST check-runs API when the first call is denied:

| Condition | `checks` |
|---|---|
| both API calls failed | `unreadable` |
| any FAILURE, ERROR, ACTION_REQUIRED, STARTUP_FAILURE | `red` |
| any TIMED_OUT, or a job cancelled at its own time limit | `stopped_timeout` |
| any CANCELLED that a newer run did not replace | `stopped_external` |
| any check queued or in progress, or a workflow run for `head` not finished | `pending` |
| no check | `absent` (with the head's age) |
| at least one SUCCESS, and the rest SUCCESS, NEUTRAL or SKIPPED | `green` |
| only NEUTRAL or SKIPPED | `absent` (nothing actually ran) |

A failed read is not a snapshot. `gh` being unreachable is the project fact `github_unavailable` (an outage episode, section 6: raised once per outage and cleared on the first good observation); it holds merges and is never remembered as "not a GitHub repo".

**Other observers.**

| Observer | Reads | Gives |
|---|---|---|
| hc events (`internal/hc` Subscribe) | terminal status, exit, and "does not exist" | run events. An hc error or outage is the project fact `hc_unavailable` (an outage episode, section 6: raised once per outage and cleared on the first good observation); it never becomes `lost` for every run |
| Start watcher | the screen of a run in `starting` with no status yet | `screen{class}` from the adapter |
| Process observer | each live run's terminal: new output (not the adapter's spinner and timer lines), child processes and terminals the run started and their ages, minus the adapter's own helpers | `activity`; the background jobs a turn left behind. **herdr-core has no API for this today** (spike S0.8). Until it has, "quiet" means no status change for `quiet_minutes`, and background jobs are not seen: a known limit of the first version |
| Git tips | the tip of each worktree with a live worker; its relation to `H`; the delta kind; the changed lines (added plus removed) against the merge base computed now, with the remote base when it is ahead, counted from the patch with `--text` so no attribute or NUL byte hides a line (`--numstat` still calls such a file binary); whether it is clean | `last_tip`, in memory (section 3) |
| Pools and sign-in | `codexbar usage --json` and each CLI's own sign-in check, every `pool_minutes` (even with no live run while J5 holds) and before each start; a check that times out or cannot run is `unknown (<reason>)`, never positive and never signed out | pool and sign-in facts; run event `pool_floor`; alerts `no_pool`, `agents_down` (J5) |
| Base branch | the runs on the base's head commit, for push events only | `base_red`; alert `main_red`; a wake on red and on green |
| Volume probe | a timed read of the project folder through hc | alerts `consent_prompt`, `disk` |
| Sweep, at daemon start and every `sweep_minutes` | hc terminals with factory labels, `git worktree list`, the local base against the remote | strays and leftovers (section 11.3) |

- **A pool is usable** when each of its windows (the 5-hour one and the weekly one) reads above `pool_floor_percent`, or when it cannot be read at all: an unknown reading is `unknown`, never "empty", and a run on it is tried. A known reading at or under the floor stays in force until its blocking window's reset even if a later refresh fails; once that reset passes, an unreadable refresh is unknown again and does not block. A pool on which a run just ended with a usage limit is unusable until the reset time it printed, or for `pool_minutes` when it printed none.
- **Remember a quota end** for the run's CLI and pool across a restart. On R5 or R19, use the time in the CLI's own limit message, or now plus `pool_minutes` if it gives no time. Only that message supplies the reset. A pool reading or quoted output elsewhere on the screen cannot set it. The cooldown holds until that time even when a newer reading says the pool is usable.
- Changed lines leave out files the repo marks generated in `.gitattributes` (`linguist-generated`). The engine keeps no list of file names; doctor offers once, at project start, to add the usual lock and project files there.

## 8. Making sure: executors, failure, and the few invariants

| Property | Rule |
|---|---|
| Key | Everything a rule asks for has a key such as `review/SDF-12/a2/round-2/3f9c`. The executor first asks the world whether it already holds (a terminal with this run's label, a PR for this branch, the PR already merged) |
| Pending counts as present | While an executor is working on a key, the rules see that thing as there. Two passes never start two reviewers |
| Time limit | Every executor runs off the loop with a time limit. A timeout is a failed result. A hung git call stalled the old factory for 3 h 15 min |
| Failure classes | `transient` (network, 5xx, rate limit), `settled` (auth, refused, not found), `timeout` |
| Back-off | A failed key is not tried again before its back-off has passed (`ensure_backoff_minutes`, doubling). The rules read this from `acts[]` |
| Outages | A failure while herdr-core or GitHub is known to be unreachable (`hc_unavailable`, `github_unavailable`) is not counted |
| Giving up | `ensure_failures` failures in a row on one key, or one `settled` failure: row N5. "It counts as a failure" in section 4.8 means a settled failure. (A task check that fails or runs out of time is evidence, N21, not an executor failure.) Nothing re-fires until the coordinator's `retry` |
| Irreversible actions | Merge, branch delete, worktree removal and PR close read fresh facts and test their rule's condition again just before acting |
| Restart | Pending work is asked for again by the first pass. Keys make that safe |

Form B needs few invariants, because runs are derived from rules instead of tracked beside them. These are checked after every pass, in tests and in production (a failure is logged as an error and wakes the coordinator):

| ID | Invariant |
|---|---|
| I1 | Every live run is wanted by its task's or project's matching rule, or is being stopped |
| I2 | A project that should have a coordinator has exactly one live coordinator run, a pending start, or a reason in J5 or J6 |
| I3 | No two live runs share a worktree unless all but one are read-only roles |
| I4 | A merge is asked only for a commit with an accept record |
| I5 | A task has at most one open attempt, and an attempt at most one PR |


## 9. Launch contract and adapters

### 9.1 Request, intent, argv

```
task.request[role]  (optional, set by the plan)   ┐
project setting models[role]                      ├─►  Resolve(role, task, settings, facts)  ─►  Intent
   each an ordered list of {cli, model, effort}   ┘        one function, at launch

Intent{role, cli, model, effort, perm, continue, cwd, prompt}  ─►  adapter.Command(Intent)  ─►  Spec{argv, env, prompt delivery}
                                                                    one function for the whole engine
```

**Resolve** walks the candidates in order (the task's list first, then the project's) and takes the first that passes every check. If none passes, the launch is refused with the first failing reason, which the task shows at N10.

| Check on a candidate | Reason shown |
|---|---|
| the CLI is installed and signed in | "codex is not signed in" |
| the model is in `Models()` for that CLI, when the CLI can list them | "model X is not offered to this account" |
| its pool is usable (section 7.3) | "waiting for quota until 6:10 PM" |
| reviewer and retro: `(cli, model)` differs from the worker's | "the reviewer must differ from the worker" |
| designer: the CLI can draw on a canvas (adapter datum) | "design tasks need Claude" |
| resume: the session's CLI is this CLI | (a handoff is used instead) |

- The lists are explicit: set by the owner at start ("confirm the pools found") or by the coordinator in the plan. There is no "first installed CLI" fallback, and nothing is read from the owner's CLI config. `plan apply` runs the same checks, so a task that could never launch is refused when it is planned.
- A list with a second entry is how a task moves to another CLI when a pool runs out (section 4.8), for workers and for reviewers. The old engine had a separate takeover module for this.
- `perm` comes from the role, not from the task:

| Role | `perm` | `continue` on first start |
|---|---|---|
| coordinator | full | fresh, or handoff |
| worker, designer | full, in its worktree | fresh |
| reviewer, retro, verifier | read-only on the branch; may build and run in a scratch copy | fresh, always |

**The run record keeps the Intent and the argv as history.** Nothing reads them back to decide anything. Editing a task changes `task.request`; there is nothing else to refresh.

Resume, a move to another CLI, and a fresh start with a handoff are the same start, differing only in `continue`: `{fresh}`, `{resume, session}`, `{handoff, file}`. A handoff file is written by the engine from the store (where the brief is, the rounds so far, the last findings) plus whatever the ending run wrote. It points to the brief; it does not copy it.

### 9.2 The adapter

One interface, three data rows (Claude Code, Codex, agy). Facts are in [notes/adapters-notes.md](notes/adapters-notes.md), taken from each CLI's `--help` and the old engine.

| Operation | Returns |
|---|---|
| `Command(Intent)` | `Spec{Argv, Env, Unset, Cwd, PromptVia: argv / flag / type-when-idle, Prepare[]}`. Always states model, effort and permission mode |
| `Screen(text)` | `trust, confirm, onboarding, signed_out, limit, none`. Used only before the agent reports status |
| `Keys(class)` | the keys that answer a trust or confirm screen |
| `Session(terminal)` | the CLI's session id, from the CLI's own files |
| `Resumable(session)` | whether that session can still be resumed |
| `Models()` | the models this account can use, or `unknown` |
| `Pool(Intent)` | which quota pool this intent spends |

Per-CLI differences that are **data**: base command, fixed environment (Claude Code's auto-updater is switched off on every start and resume, because an updated binary made macOS ask for volume access and every new agent hung), the flags for model, effort and each permission mode, how the first prompt is passed, the resume form, the session file location, the pool name. Three adapter rules from field failures: environment markers of the parent session are removed before a start, or the new agent writes no transcript and cannot be found or resumed; Codex runs on its own app server, never a shared one (a shared one once made every turn fail); and a CLI's own config file is edited only to pre-set trust for a worktree, keeping its mode and every other field, honouring a config-folder override, and leaving a malformed file alone (Claude Code; Codex needs no edit, below).

**Codex as a reviewer, as built** (opt-in with the project setting `second_reviewer`, whose row is in [CONFIG.md](../../../CONFIG.md); facts from spike S0.3's Codex half and PR #700). Codex reviews a round only when an opus pass already covers the head, so it gives L8's second passing round; every other round stays on Claude Code until candidate lists (`models`) select otherwise.

| Fact | Rule |
|---|---|
| Prompts before the first turn | Codex asks for folder trust once per folder, asks to review a repo's hooks, and offers updates. Each is answered per run by `-c` overrides that write nothing to the owner's config: the worktree trusted, `features.hooks=false` (so no project hook runs), the update check off. Codex keys trust by the resolved folder, so the path is resolved through symlinks first |
| The rest of the argv | `--no-daemon` (its own app server, never a shared one), `--no-alt-screen` (the scrollback `hc read` needs), a read-only sandbox, never an approval prompt, model and effort stated |
| The owner's own instructions | The run's `CODEX_HOME` is one folder per data dir (`<data_dir>/factory/codex-home`) holding only a link to the owner's `auth.json`, so the reviewer reads none of the owner's global instructions, config or skills. If Codex has replaced that link with a file (a sign-in refresh), the engine never touches it: that round runs on Claude opus and the daemon log names the fix |
| A repo's own `.codex/config.toml` | Still loads in a trusted folder; no per-run override turns it off without a prompt (O14) |
| Status | The report tag ends the run: the screen is read every pass, after the last prompt echo, with no wait for idle. Non-empty output moves the run to `working`, so a review with no tag that goes quiet ends via R21–R22, an idle run with no tag is nudged once then stopped by N29, and a run older than `review_minutes` is stopped by N29, each retrying under section 4.8 (`reviewer_retries`) |

**Codex as worker and coordinator** (slice 5b.5, PR #740): runs unattended with `--dangerously-bypass-approvals-and-sandbox` and the same `-c` overrides and scoped `CODEX_HOME`. Turn end is read from the current screen (the `Working (... esc to interrupt)` status line reads `working`; the composer line without it reads `idle`; an unrecognised screen or an unchanged `sent_screen_hash` stays silent and keeps the run's state), and a worker's session id is read from its unique `session_meta` rollout in the scoped `CODEX_HOME` (matching `cwd` after start; a coordinator restarts fresh).

**agy as worker, coordinator and reviewer**:

| Fact | Rule |
|---|---|
| Launch | `launchCapability` allows `agy` for worker, coordinator and reviewer. Every start runs `env -u GEMINI_API_KEY agy --model <model> --mode auto -i=<first prompt>` (the prompt attached to `-i=`; `GEMINI_API_KEY` is never set or passed, because it switches billing to pay-as-you-go). All three roles use `--mode auto` (edits and safe commands auto-approved): agy has no unattended read-only mode (`--mode plan` prompts interactively on commands outside its literal prefix allow list, and `--sandbox` cannot read a git worktree's main `.git` metadata while still allowing worktree writes), so a reviewer runs in `--mode auto` with its first prompt stating plainly `"do not modify, create or delete any files; read and run tests only"`, backed by the clean-worktree check on the verdict (section 7.2) |
| Status and turn end | Read from herdr-core's status (`app: antigravity`, spike S0.1): `working` is working, `idle` and `done` (whose `msg` carries the first line of the reply) end the turn after `idle_grace_seconds`, and `blocked` keeps its `kind` (`permission`, `question`, `auth`). A `blocked`/`auth` within the first `start_minutes` of a run (agy's brief startup sign-in check, which clears by itself within seconds) is treated by `ObserveRun` as `starting`, not `blocked` and not signed out; a later `blocked`/`auth` keeps `kind` `auth` |
| Trust | Handled only by the existing 5b.4 rule (R3 answers the verified bottom folder-trust dialog once per run); nothing pre-edits the owner's agy settings |
| Session and resume | A worker's session id is the conversation id for that worktree in `~/.gemini/antigravity-cli/history.jsonl`, read through herdr-core only. Resume adds `--conversation <id>` (`env -u GEMINI_API_KEY agy --model <model> --dangerously-skip-permissions --conversation <id> -i=<prompt>`) only when asked, never `-c`. When the id cannot be read, resume is not offered (fresh start with a handoff) |
| Pool | An `agy` candidate reads the CodexBar `antigravity` pool scoped by model family (`gemini-*` uses Gemini windows; `claude-*`/`gpt-*` uses Claude/GPT `3p` windows; KB-QTA-4); other `agy` models have no pool (`unknown`, which never blocks) |
| Doctor | `sheepdog factory doctor` checks `agy` when a role's candidates include it: the binary and status are checked through herdr-core, and a missing binary or an unreadable status is reported with its reason (`unknown` is never "fine" and never "signed out") |

Round 3 (C): `--mode auto` still stopped on permission prompts for `sheepdog` commands, so all three `agy` roles start with `--dangerously-skip-permissions` instead; the reviewer keeps its `"do not modify, create or delete any files; read and run tests only"` first-prompt instruction and `GEMINI_API_KEY` is still never set.

Round 4: for an agy reviewer, the report is the `<report>` tag when visible, or else the last bare JSON object line in agy's own reply (after the last prompt echo) whose `code` equals the round's code and that decodes as a valid report (the prompt echo's template never counts), and the reviewer's first prompt asks agy to put the `<report>` tag inside a fenced code block.

Per-CLI differences that need **code**: pre-setting trust for a worktree, the agy sandbox wrapper for read-only runs, listing models.

Read-only for Claude Code is the `dontAsk` mode with an allow list of read tools and the usual test runners, passed as one `--allowedTools=` argument (as separate values the flag swallows the prompt that follows). This is a rule on the model, backed by the clean-worktree check on the verdict (section 7.2). It is not a sandbox: a test runner runs the worker's code.

`Screen` needs text patterns per CLI. That is a kept list, and the accepted limit: it covers only the few screens that appear before status reporting starts, and each pattern has a captured real screen as its test fixture.

### 9.3 Reports from agents

| Role | Channel | Reports |
|---|---|---|
| worker, designer | `sheepdog factory report …`, a thin client of the daemon | `ready`, `recap-written`, `ask` (kind decision) |
| reviewer, retro, verifier | a `<report>{json}</report>` tag at the end of its last message, carrying the one-time code the engine put in that run's prompt, read by the engine from the terminal when the run goes idle (R20). The echoed prompt is skipped; the tag may sit in a code fence or be wrapped over lines; a read that ends mid-tag is read again, not judged. The tag holds no free text, because a model does not escape quotes in JSON reliably: a verdict's tag is `code, round, head, verdict, majors`. The findings are the reply above it, one per line starting `major:` or `minor:`, then the reply lines of section 7.2 and prompts.md 5.4. These lines are read tolerantly: a leading `⏺`, bullet or number, bold or backticks around the line, and a trailing period are stripped; lines inside a tool's output block (under Claude's `⎿`, or under Codex's `└` below a call line) are skipped, so text a tool printed is never a finding or a reply line | `verdict`, `retro` |
| coordinator | `sheepdog factory …` | the commands of section 4.9, `plan apply/amend`, `answer` (kind decision), `ask` (owner kinds), `say`, reads |

- Every report goes through the acceptance table (section 7.2).
- Reviewers use the tag because a read-only mode may deny the shell or the network; the old engine found that a Claude reviewer in plan mode could not record its verdict (field note 20). The old engine already reads the retro this way. If reading the screen proves unreliable for a CLI, the fallback is that CLI's session file (assumption A7).

### 9.4 Rules of the executors

These are mechanics: how a thing is done once a row has asked for it. They decide nothing. The decisions that look like mechanics (the checks rollup, when an existing PR may be adopted, what counts as `merge_only`) are small first-match tables in `flow`, fed with what `world` fetched. Each line has a behaviour test.

| Area | Rule |
|---|---|
| git | Every call is non-interactive, has a time limit and runs through hc (section 2.4). Never a force push, never a rebase of a task branch. A push names the branch explicitly |
| Worktree | A new attempt branches from the remote base after a fetch, so unpushed local commits never leak into a task. The remote base is the push remote's default branch (its `HEAD`), and nothing else. One branch per attempt, named `task/<project>-<task>-<attempt>-<slug>`, so a new project on the same repo never reuses a branch an old PR names. The project's sparse exclude list applies. A worktree with uncommitted work is never removed |
| gh | Every call names the repo of the push remote. A call with no repo is refused |
| PR | One PR per attempt, and never a second PR for a branch. After its own push of `H`, the engine adopts the branch's PR only if it is open, in the same repo and on the same base. The head is not compared there, because GitHub lags a re-push; N42's `p.head` check guards the merge. A closed, merged or fork PR for the branch is never adopted: the act fails. Title: the task's title, which the plan checks as a Conventional Commit subject. Body and squash message are built from task fields only: no co-author trailer, no session link |
| Merge | Squash, pinned to `H`. Never asks GitHub to delete the branch (with a merge queue that closes the PR). The remote branch is deleted only after the PR reads as merged |
| Branch update | GitHub's update-branch (a merge commit), pinned to the expected head. When and how often is rows N38 and N40's business, not the executor's |
| CI | Re-run of failed jobs; dispatch of a workflow. When and how often is rows N33 to N35's business. Dispatch is opt-in by name (`dispatch_workflow`, empty by default), because a dispatchable workflow can have side effects (a deploy or a release) |
| Review diff | Round 1: the task's own changes, from the merge base with the base branch, computed now, to `H`. A delta round: the task's own changes since the last reviewed commit, leaving out what merges of the base brought in. A round that follows a worker's reject has no new commit: it reviews the full range again (as round 1, which holds the rejected round's range), with the rejections beside it |
| Scratch copies | The clean checkout for a check run and a reviewer's scratch copy are removed when that run ends |
| Stop | Ending a run ends its whole process group, then checks nothing is left |
| Terminal names | Built from project, task, attempt and round, and fitted to herdr-core's rule `^[a-z][a-z0-9_.-]{0,31}$` (task ids come from the plan and may have capitals): lower case, other characters made `-`; a name that changed or is too long becomes its first 23 characters, `-` and 8 hex characters of its hash |
| Messages | Long content is a file path. Another agent's output is passed as a file to read, marked untrusted |

## 10. The coordinator

| Opus decides | Code enforces |
|---|---|
| The plan: milestones, tasks, order, briefs, size estimates, outcomes | The plan checks (section 11.7) |
| Which candidates each task asks for, among signed-in CLIs | The Resolve checks; model, effort and mode stated on every start |
| Answers to workers' `decision` asks | Owner kinds need the daemon's proof |
| The recap decision, among the allowed choices | The allowed set; the ladder's budgets |
| What to do about a task that waits on it: retry, send back, re-plan, drop | Every rule; the accept bar; the merge; cleanup |
| The "why" and "open risks" of each journal entry | Exactly one coordinator; rotation; the fact part of the journal |

**Wakes.** The coordinator is woken by rules, each wake once per key, batched and delivered when it is idle (J12): a task waits on it (N3–N8, N12–N14, N18, N25, N30, N34, N35, N37, N38, N41, N43), a PR was merged outside the accept bar (N2), a worker asked, a retro finished, a milestone finished, a pool or the base changed, the owner spoke. A heartbeat covers silence (J13). Each wake starts with a facts block built in code: the tasks waiting on it and why, pools, slots, every setting that differs from its default, and the retro table (section 11.2).

**Rotation.** A long coordinator session was 41.5% of day one's Claude cost. J11 gives it a fresh session at an idle moment, with a written handoff.

**Standing rules** reach it two ways: settings are enforced in code and listed in the facts block; text rules are a section of its prompt, rebuilt at every start.

### 10.1 Settings: every number is tunable by the owner

**Owner decision D6.** The tables' **rows** are code; every **number** a rule uses is a named setting.

- There is one settings table. A rule gets its numbers from it and from nowhere else: **no number is written inside a rule.** A test fails on a numeric literal in the rule tables.
- Each setting has a name, a default, an allowed range and a scope: every project, or one project (the project's value wins).
- A change is an ask of kind `rule` and nothing else (owner decision D11): the owner says it by voice, phone or the factory page, or `sheepdog factory set` raises it from a terminal that carries no run's token; the engine reads it back with its scope ("This project: review rounds 3. Save?"), and saves it only with the daemon's proof of the owner's answer. It is in force from the next pass, with no restart, and is written to the log.
- The coordinator can **propose** a change (it becomes that same ask). It cannot make one.
- A value outside its range is refused with the range in the reply.
- A running task goes on under the new value. Nothing is recomputed backwards: a round already run stays run. (The old engine kept a running task on the values it started with; this is a deliberate change that follows from D6.)

| Setting | Default | Range | Used by |
|---|---|---|---|
| `review_rounds` | 2 | 1–5 | L8 |
| `max_rounds` (per attempt) | 5 | `review_rounds`–10 | L12 |
| `retro_after_rounds_with_majors`, `retro_from_round` | 2, 3 | 2–4, 2–6 | L11, L12 |
| `retros_per_task`, `retro_fix_verdicts` | 1, 2 | 0–2, 1–3 | L3, L11 |
| `continues_per_task`, `continue_lines` | 1, 60 | 0–3, 0–500 | recaps (N12–N14, section 4.9) |
| `task_size_limit` (lines), `size_cap_factor` (× estimate) | 400, 2 | 50–2000, 1–5 | plan checks; N12 |
| `plan_review_rounds` | 2 | 0–5 | plan review (11.7) |
| `ci_fails_per_hour`, `build_hours` | 3, 4 | 1–10, 1–48 | N13, N14 |
| `flake_reruns`, `merge_tries` | 1, 3 | 0–3, 1–10 | N33, N34; N18, N41 |
| `checks_grace_minutes`, `ci_stalled_minutes`, `main_red_minutes` | 5, 90, 60 | 1–60, 10–600, 5–600 | N35, N36, N40; the checks rollup; alerts |
| `max_running` | set at start | 1–20 | N10 |
| `worker_restarts`, `reviewer_retries` | 3, 2 | 0–10, 0–5 | section 4.8 |
| `ensure_failures`, `ensure_backoff_minutes` | 3, 1 | 1–10, 1–60 | N5, section 8 |
| `start_minutes`, `idle_grace_seconds`, `quiet_minutes`, `interrupt_seconds`, `review_minutes` | 2, 20, 20, 90, 45 | 1–15, 5–120, 5–120, 10–600, 10–240 | R11, R18–R22, N29 |
| `background_job_minutes`, `blocked_minutes` | 30, 10 | 1–240, 1–120 | the worker watch of 4.4, N29, N39; R29 |
| `coordinator_starts_per_hour`, `coordinator_gap_minutes`, `coordinator_silent_minutes` | 3, 10, 30 | 1–10, 1–60, 5–240 | J6, J9 |
| `rotate_after_hours`, `rotate_after_turns`, `handoff_minutes`, `heartbeat_minutes` | 4, 150, 5, 60 | 1–24, 20–1000, 1–30, 10–600 | J11, J13 |
| `pool_floor_percent`, `pool_minutes`, `sweep_minutes` | 5, 5, 5 | 0–50, 1–60, 1–60 | section 7.3 |
| `disk_full_percent` | 90 | 50–99 | section 6, 7.3 (`disk` alert) |

Settings that are choices, not numbers, are set the same way: `design_gate` (review model or owner), `delegation` (kinds, scope, until), `notify_level`, `merge_policy`, `project_priority`, `models` per role, `verify` (section 11.6). Until `models` and candidate lists exist (slice 5b), `second_reviewer` (section 9.2) is the one choice built.

The engine's own machinery has a few fixed numbers that are not rules about work: the loop's 15 s tick, the 2 × tick per-pass duration cap (30 s), how often GitHub is read, and the report client's limits on a proof (5 minutes, 64 KiB of output). They are constants in code.

**Not a setting, ever:** the rows themselves, the acceptance table, the owner's proof, the invariants, "no checks is never green", "never merge a commit no reviewer passed, unless the owner overrode that very commit". Rails stay in code; a row change goes through review and shows up in the generated table document.

**What this costs, and how it is contained.** Every tunable number adds combinations nobody ran. Three guards: ranges are enforced; the table checks run at each setting's minimum and maximum; and the coordinator's facts block lists every setting that differs from its default, so an odd value is visible. A version ships only the settings its own rows use.

## 11. Built in: journal, retro, evidence, plan, design loop, size caps

### 11.1 Journal

One entry per coordinator rotation, per milestone, and before a stop. The engine writes the fact part from `log.jsonl` (tasks merged with PR numbers, decisions taken, rounds and retros, checks that ran, alerts). The coordinator adds why, open risks and the next first step. If it does not answer in `handoff_minutes` the entry goes out with facts only (J11). Entries are files under `projects/<id>/journal/`; the handoff for the next session is the latest entry. Whether they are also copied to the owner's wiki is a project setting.

### 11.2 Retro

The retro is level 1 of the ladder (section 4.6). Its record is stored once and never edited: verdict, majors, one cause from a fixed set, the evidence it quotes, one change for the coordinator and one for the worker, and whether each change is **a check to add to the repo** (a lint rule, a test) or words for a brief. A check is preferred: a correction that lives in code is enforced every time; one that lives in a prompt is remembered sometimes.

Nothing keeps a lessons file. The facts block derives, at each wake, the retros of the last 30 days by cause, the three latest coordinator changes, how many changes became checks, and a routing table (tasks, first-look pass rate, self-check misses, mean rounds and retros per worker model and reviewer model). **The first-look pass rate is the factory's headline quality number** ([prompts.md](prompts.md) 1.2): the owner's goal is that review and verification usually pass the first time, because the worker checked its own work. It is counted per task, on the first reviewer round of its first attempt that had one; a coordinator's send-back is not a reviewer round, and a task with no reviewer round is left out. `sheepdog factory log` prints it for the project. An outcome counter compares rounds used by tasks that followed a retro with those that did not, so the feature can be judged and removed if it does not help.

### 11.3 Sweep

Under form B most drift cannot happen: a task's situation is read from records and facts on every pass, so "approved but not merged" or "red after approval" is simply the row the task is on. What is left is things no task points at. The sweep finds them and wakes the coordinator once per item.

| Found | Means |
|---|---|
| a terminal with factory labels that no run owns | a stray agent |
| a worktree or branch no attempt owns; a done task's worktree still there | a leftover (doctor lists it) |
| a task waiting on the coordinator for `coordinator_silent_minutes` with no coordinator turn since | the coordinator is silent (J9 acts) |
| the local base ahead of the remote | someone committed to the base locally |

The sweep also lists the shared Codex home (location, size, file count) as one item per episode, never reading its files.

A probe that cannot run reports `unknown` with the reason, never a negative.

### 11.4 Design loop

No new table. A design task is a task with `kind: design`:

- its worker is the designer role, which only a CLI that can draw on a canvas may fill (section 9.1); its export folder is checked by hash as one of its outcomes;
- its reviewer must be a session that did not draw;
- its ladder settings come from the design settings (at most 2 change requests), then the same ladder;
- N31 opens an owner gate only when `design_gate` is `owner`;
- it is the gate task of a UI milestone: N10 holds that milestone's tasks until it is done, and `plan apply` refuses a UI task with no `boards`.

### 11.5 Size caps

| Where | Rule |
|---|---|
| Plan | `estimate_lines` is required. A task over `task_size_limit` is refused: split it before it exists |
| Start | `size_cap` = `size_cap_factor` × `estimate_lines` |
| Every pass | N12 reads the changed lines at the tip. No task can avoid it, and it needs no report from the worker |
| After a `continue` | the cap becomes lines now + `continue_lines`. With `continues_per_task` used up, `continue` is no longer offered |
| The engine's own code | each package in section 13 has a line budget; going over is the same stop-and-ask |

### 11.6 Evidence: proving the task met its goal

*Asked for by the owner and confirmed (D9): the worker's proofs on its ready report are version 1; the engine's own run of them is the first increment.* It follows pstack's "prove it works": check the real thing, not a self-report, and prefer a script anyone can run again.

| Step | Who | What |
|---|---|---|
| Plan | coordinator | Each task lists its **outcomes**: what is true when it is done, and how each will be proved. Required from version 1, like the estimate |
| Before writing | worker | Each proof becomes a check, run once and seen to fail. From version 1 ([prompts.md](prompts.md) 1.1) |
| Before ready | worker | It runs every check and reports the command and the real output. From version 1; the acceptance table refuses a ready report with an outcome unproved |
| Build | worker | For each outcome, a check that can be run again (a script or a test command against the real thing), committed with the code. The ready report maps outcome to check. An outcome that cannot be scripted (a look, a device) is marked `manual` with a reason and an artifact (a screenshot, a recording) |
| Evidence | **the engine** | Runs every check itself at `H` in a clean checkout (N27) and stores command, exit code and output. A failure goes back to the worker (N21) and costs no review round |
| Review | another model | Besides the code, it judges each outcome: "does this check prove it?" A check that never reaches its target is a major. It looks at the manual artifacts itself |
| Accept bar | code | N42: every outcome has passing evidence that covers `H`, or is manual and judged |
| Milestone | a verify task the engine adds | A fresh session re-runs the milestone's checks on the merged base and exercises the feature end to end with the project's verify skill, if it has one |

- The evidence is a record anyone can re-run: which commit, which command, what came out. It is not an agent's word.
- `verify` is a setting: `milestone` (evidence per task, a verify task per milestone: the default), or `task` (also a separate verifier run after review for tasks marked high risk).
- Limits, stated plainly: code can require that evidence exists and passed; it cannot tell a strong check from a weak one. That judgement is the reviewer's, on a different model from the worker, and the milestone verify task is independent of both.

### 11.7 Plan and milestones

A plan is a file the coordinator writes. `plan apply` and `plan amend` accept it only when every check passes; `plan check` runs the same checks and changes nothing.

| Check | |
|---|---|
| Shape | no unknown field; every dependency id exists; no cycle |
| Apply | refused when a plan already exists (`plan_exists`): changes go through amend |
| Amend | a list of changes (add a task, change fields of a task), never a whole new plan; a task leaves only through `drop`, so leaving one out cannot remove it |
| Scope | every task says for whom it is, what happens if it is skipped, the smallest version, and what is not being done ([prompts.md](prompts.md) 3.1) |
| Size | every task has `estimate_lines` at or under `task_size_limit` |
| Outcomes | every task has at least one outcome, and each says how it will be proved (a command, a test, or an artifact for a look or a device) |
| Title | a Conventional Commit subject |
| Launch | every task's candidates pass the Resolve checks that do not depend on quota |
| UI | a UI milestone has a design task; a UI task names its `boards` |
| Why | an amend has a why; an ended task is not changed |

**A plan is reviewed before it is accepted** (an early increment; [prompts.md](prompts.md) 3.2). When the checks pass, the engine starts a plan reviewer: another model, a fresh session, read-only, whose one question is what can be deleted or made smaller. Its findings go to the coordinator, which answers each and submits again, at most `plan_review_rounds` times (default 2). A plan that still does not pass is accepted with the open findings written to the journal and the owner's digest; planning is reversible and does not wait for the owner unless `design_gate` is `owner`. A milestone's one-page design note is part of what is reviewed.

A milestone has an id, a goal, an order, a gate (`auto`, `review` or `owner`) and, when `verify` asks for it, a verify task the engine adds. Its state is derived: planned, active, validating, done. The graph view gives dependency layers and the critical path from the estimates.

Not in the first versions (section 17.3): a `touches` field to keep two tasks off the same files at once, a "not before" time, contract tasks, a research-only task kind.

### 11.8 Foundation first

Automating a loop that produces poor code produces more of it. At project start doctor reads the repo's base: is there CI, a lint or type check, a test command, a green base branch? What is missing becomes the coordinator's first milestone ("foundation"), before feature work, and the start shell says so in one line.

## 12. Fit with the daemon and the owner's surfaces

### 12.1 What the surfaces read

Every surface reads a **view**: a pure function of the stored state. The JSON shapes stay those of owner contract v1 (`testdata/factory/owner-contract-v1.json` is kept as the golden file), plus the fields the approved addendum adds.

| Surface (canvas board) | View | Built from |
|---|---|---|
| Space-page factory section (FactoryPage, FactoryIPad*) | section state; "Milestone 3 of 4 · 7 of 12 merged"; time left along the critical path; task rows by dependency layer with stage, why, model, rounds and live terminal | the project rules (section 6); `plan` layers and critical path; section 4.11 |
| Ask cards (FactoryAsk, FactoryAskCard), on Conversation and on Agents with a badge | open owner asks | ask table |
| Start row and shell start (FactoryStartEntry, FactoryStartShell) | doctor lines, then five questions | `doctor.go`; J1 |
| Agents block, worker window (FactoryAgents, FactoryWorker) | coordinator state and age; each run's terminal, model and effort | run table; labels `sheepdog.project/role/task/model` set at launch |
| Log (FactoryTerminal) | `at, by, wake, task, decision, why, model` | `log.jsonl` (section 14) |
| Debug | for a task: the rule that matched, and for each earlier rule the part of its condition that was false; refusals with their codes | `log.jsonl`; refusals from the daemon's event log (section 14) |
| Voice digest | merged, waiting, pools, alerts; any PR merged outside the accept bar; any task that has waited on the coordinator for hours | a pure function over state and log; no model |
| Quiet rule | labels on every factory terminal | set at every start; enforced where it is today (`herdr.IsFactoryQuiet`) |

**Order of building the surfaces (owner decision D14).** The web first: one factory page in the daemon's existing web, beside setup, voice and debug, with the factory section, the ask and override cards, and the log, the same content as the approved boards. The iOS and iPad boards follow once the web version has been used. Neither client has any factory code today.

### 12.2 Today's Go code

Sources: [notes/go-surface-notes.md](notes/go-surface-notes.md). Line counts are today's.

| Code | Lines | Fate |
|---|---|---|
| `internal/factory/proof.go` | 314 | **Gone from `main`; rebuilt in slice 6.** The engine mints and verifies `AnswerProof` in process (`MintProof`, `Answer`) |
| `internal/factory/doctor.go` | 1,700 | **Keep,** minus the engine and Python checks; add the "Running now" part from the sweep |
| `internal/factory/contract.go` | 1,385 | **Gone from `main`.** Its ask, view and refusal shapes live in `flow` and `factory` |
| `internal/factory/client.go` | 728 | **Gone from `main`.** Its method set becomes the engine's Go methods |
| `internal/factory/tick.go` | 834 | **Gone from `main`.** The loop is a goroutine in the daemon. Keep its two rules: nothing runs until a project is started; tests resolve paths away from the real home |
| `internal/factory/engine.go`, `python.go`, `factory_embed.go` | 423, 368, ~20 | **Old Python glue gone from `main`** (`internal/factory/engine.go` is now the v2 engine) |
| `internal/web/factory.go`, the loopback proof route | 83 | **Loopback proof route gone from `main`** (`internal/web/factory.go` now serves v2's factory routes) |
| `internal/supervisor/factory.go` | 796 | **Gone from `main`** (spike S0.9, 2026-11-20), with the answer guard it held; slice 6 builds the guard again on the engine |
| `internal/herdr/factory.go`, `supervisor/factory_quiet.go` | 40, 50 | **Gone from `main`; rebuilt in slice 6.** Coordinator rule follows the addendum (blocked speaks; done and idle are silent) |
| `cmd/sheepdog/factory.go` | — | Keep `doctor`; `python-*` gone; add `start`, `stop`, `status`, `log`, `report` (`ready`, `ask`), `ask`, `answer`, and the coordinator verbs |
| `Makefile` `test-factory`, the CI factory job | — | **Gone from `main`** |
| `factory/` (Python, tests, templates) | ~41,000 | **Gone from `main`** |

Not built in Go today and needed by the approved design: alerts to push and voice, the digest, `say`, rules read-back, the graph endpoint, `stop`, owner touch. They become calls on the engine interface, which is cheaper than through a CLI.

### 12.3 The engine's Go interface

```go
type Change struct {
    Project string `json:"project,omitempty"`
    Kind    string `json:"kind"`
    AskID   string `json:"ask_id,omitempty"`
    Task    string `json:"task,omitempty"`
    Seq     int64  `json:"seq,omitempty"`
}

type Engine interface {
    // reads (views)
    Projects() []ProjectView;  Graph(project string) (GraphView, error);  Asks(open bool) []flow.Ask
    Digest(since) Digest;  Rules(project) []Rule;  Alerts(since) []Alert;  LogLines(project string, n int) ([]LogEntry, error)
    // owner actions (each becomes an event)
    Start(folder, goal string, maxRunning int) error;  Stop(project string) error;  Say(project, text string) error
    MintProof(askID, option, inputKind, deviceID string) (AnswerProof, error)
    Answer(askID, option string, proof AnswerProof) error;  ProposeRule(...) (flow.Ask, error);  RevokeRule(...) error
    Touch(terminal, device, kind string, n int) // owner typed into a factory terminal: logged, answers nothing
    // agent reports (from the CLI client, checked against the role table)
    Report(runToken, verb string, args json.RawMessage) error
    Changes() <-chan Change                     // one Change per ask, answer, cancel or say; supervisor and web push from this
}
```

## 13. Packages and size budgets

**Size: three estimates, each higher than the last.** The first figure in this document was about 5,300 lines. The first review measured today's code (`contract.go` alone is 1,385 lines, the old `github.py` 1,413; a rule row costs about 20 lines once each clause is named so the log can say which part was false) and put the full design at 8,500 to 10,000. The second review, and then an independent check of the PR split, redid version 1 on this section's own bases twice: 4,000 became 6,000, then about 8,000. The table below is the full design by package (about 9,500). Adding the plan's increments to its version 1 gives about 11,500. So: **about 8,000 lines of Go without tests for version 1, and 9,500 to 12,000 for the full design.** The two methods disagree by a fifth, and every earlier figure was low. They are estimates. plan.md is now cut into slices that each run end to end; after every slice the real size is compared with the estimate and the next slices are re-planned. The first slice (a walking skeleton) came in at about 2,650 lines against 2,300, 1.15 times (plan.md section 7); the next slices are planned on that factor. A package or a PR that passes 1.5 times its budget stops and asks.

| Package | Job | May import | Budget, full | Basis |
|---|---|---|---|---|
| `internal/factory/plan` | Plan file, checks, layers, critical path. A leaf: it knows tasks and milestones as plain data | nothing | 500 | the old `plan.py` is 555 lines |
| `internal/factory/flow` | Record types; the first-match interpreter (a loop) and the run table's interpreter; the task rules, the ladder, "making sure a run is alive", scheduling, `covers` and head relations; the project rules; the run and ask tables; the acceptance table (which calls `plan` for plan checks); the small decision tables for the checks rollup, PR adoption and `merge_only`; settings; `Resolve`; the views and the table printer | `plan` | 3,800 | about 65 rule rows and 36 run rows at about 20 lines; acceptance table 300; types 500; views and printer 700; settings and Resolve 300 |
| `internal/factory/world` | Executors and observers: hc start, send and stop; git; gh; the task check runner; pools; the three adapters; the report tag reader; the sweep | `flow`, `internal/hc` | 3,300 | adapters and trust pre-setting 900; gh 800; git and worktrees 400; start, deliver, stop 400; observers, pools, sweep 600; check runner and tag reader 200 |
| `internal/factory` | The loop, keys and back-off, the clock, the store, the `Engine` interface, prompt assembly, the report endpoint and run tokens; plus the kept `proof.go` and the reworked `doctor.go` | all of the above | 1,300 | loop 350; store 200; interface, endpoint and wiring 500; prompts 150; doctor changes 100 |
| `cmd/sheepdog`, `internal/web`, `internal/supervisor` | Verbs, endpoints, the notification swap | `internal/factory` | 600 | about 15 verbs and 4 endpoints |

There is no separate `rules` package: first match is a loop, and the ask table is a switch.

Rules that keep the boundaries honest:

- `plan` imports nothing from the engine. `flow` imports `plan`; nothing imports the other way.
- `flow` does no I/O. It gets the time as an argument. Every table, condition and view can be tested with plain values.
- `world` holds no decisions (section 9.4). An executor does one thing and reports what happened.
- Only `internal/factory` writes the store.
- Templates (coordinator, plan reviewer, worker, reviewer, retro, verifier) are embedded Markdown, at most 40 lines each. **They are designed in [prompts.md](prompts.md), together with the fields and checks they rely on,** and not left to whoever implements a row: which principles each role carries, the draft texts, the facts the engine fills in, and the replay situations. Each sentence in a template that states a rule cites the knowledge-base id it carries, a test checks that every command a template names exists, and a prompt change needs replay evidence, as the repo rules say. **Rules that only a prompt carries are the weakest rails.** Five safety rules are in that state today (knowledge-base rows KB-COO-41, KB-COO-55, KB-COO-61, KB-WKT-24, KB-ASK-24: among them "the coordinator never merges and never commits to the base"). The code-level backstop is GitHub branch protection on the base, which doctor checks at start.

Deleted against this: about 3,250 lines of Go glue, 21,460 lines of Python and 19,692 lines of Python tests.

## 14. Storage

```
<data_dir>/factory/
  engine.json                  project index, settings for every project, asks for "everywhere"
  projects/<id>/state.json     project, plan, settings, tasks with their records, runs, asks, alerts, pending work, seq
  projects/<id>/log.jsonl      append-only: one line per pass, with the rule that matched and what was asked for
  projects/<id>/journal/*.md   journal entries (section 11.1)
  projects/<id>/reports/       findings, recaps, retro records, briefs
```

- `state.json` is the one source of truth. It is written with a temporary file and a rename, mode 0600, as the daemon writes its other state. It keeps no run token, only its SHA-256.
- `log.jsonl` is the audit trail and the owner's factory log. A line: `{seq, at, project, by, wake, task, input, facts, rule, asked, decision, why, model}`. On start, lines past the state's `seq` are dropped (a crash between the two writes). A refused report or command changes no record, so it goes to the daemon's event log, not here.
- Settings are stored in `state.json` or `engine.json` by scope. There is no layered settings file and nothing is read from `~/.config`.
- Done tasks stay in `state.json` until their milestone closes, then move to `projects/<id>/archive.json`. *Estimate, not measured:* a few hundred tasks is about 1 MB, written a few times a minute.
- New data files are added to `CONFIG.md`'s data table.

## 15. Errors and recovery

| Failure | What happens | Why it converges |
|---|---|---|
| Daemon crash or restart | One pass over every open task reads the stored records and fresh facts and lands on the same rows | Nothing depends on what a previous pass meant to do |
| Crash after an executor acted, before its result was stored | The same thing is asked for again; the executor finds it already done | Keys; executors ask the world first |
| An executor keeps failing | Back-off per key; after `ensure_failures`, row N5: the task waits on the coordinator and nothing re-fires | Section 8 |
| Merge sent, result unknown | The next snapshot says merged (N2) or still open (N41) | The PR's state is the only proof |
| Agent crashed, terminal gone, reboot, hc restart | The run ends (R33); the task's row still wants it; section 4.8 restarts it with resume, else a handoff | Restarts are counted |
| herdr-core itself is down | The project fact `hc_unavailable`; no run is declared lost; nothing new starts | Outage is not loss |
| Coordinator gone, stuck or signed out | J5–J10, with start limits | Invariant I2 |
| git or gh hangs (macOS consent prompt, disk) | The executor times out; two timeouts on the project folder run the volume probe, which raises `consent_prompt` or `disk` and clears it when the folder answers | No call is made from the loop |
| GitHub unreachable or rate limited | Transient failures back off; `github_unavailable` holds merges; nothing is remembered as a settled fact | Failure classes |
| Quota runs out | The run ends with `quota`; the next candidate takes over with a handoff, or the task waits for the reset. At the floor, workers are told to write a handoff and stop (R31). No usable pool at all: `no_pool` | Pools are facts with reset times |
| Machine sleep | The gap is taken out of the clock (section 2.5) | One clock rule |
| Work done by hand outside the engine (a PR merged or closed by a person, a commit pushed) | N2, N4, N7, N23 | The facts are read like any others |
| A cause of waiting goes away by itself (CI appears, GitHub comes back, the base turns green) | The next pass lands on a different row | Nothing was latched |
| An input nobody planned for | Refused by the acceptance table with a code, and logged | Section 7.2 |
| A situation nobody planned for | N43 or J14: nothing is done, the coordinator is woken with the facts | The last row |
| `state.json` unreadable | The engine refuses to start that project; the section shows "couldn't load"; the last good file is kept beside it | Never guess |

## 16. Test strategy

The old tests are not ported. [knowledge-base.md](knowledge-base.md) lists each behaviour with its source; its "Maps to" column names the rule, table line, executor rule or view that carries it.

| Layer | What | How |
|---|---|---|
| Table checks | The last row of each rule table is "no rule matched"; every row names who it waits on; the run and ask matrices have no empty cell; no numeric literal in a rule; the generated table document matches `docs/dev/factory/rules.md` | `go test`; a table change must regenerate the document, so a reviewer sees it |
| A case per row | Every row has at least one case that lands on it and on no earlier row | A row with no case fails the suite |
| Order | For every pair of rows that can both hold (neighbours or not; the property generator finds the pairs), a case where both hold that fixes which one wins. For neighbours the suite is also run with the pair swapped and must fail. The ladder (L1–L13) and "making sure a run is alive" are tables and get the same tests | Catches a decorative table (SDF-54: reversing the rows kept the tests green) |
| Properties | Random histories of records and facts: the invariants hold; every row can be reached; a second pass with nothing changed asks for nothing new; an accepted record is never lost | Property-based tests over `flow`, which is pure |
| Histories | The pass-by-pass histories of [notes/table-audit.md](notes/table-audit.md) (happy path, review that never converges, size cap, the base moving, each CI class, failures, people acting outside the engine, the coordinator, the run table) are the first fixtures. Each asserts the row at every pass, that no normal pass lands on "no rule matched", and that nothing is asked for twice | Written before the rules they test |
| Behaviour | One scripted history per knowledge-base behaviour | Each test names its `KB-…` id. A script lists ids with no test and ids mapped to nothing |
| Settings | Each setting refuses a value outside its range, and the table checks pass at each setting's minimum and maximum. (Running every behaviour test at every extreme is not done: too much surface for the first versions) | — |
| Restart | Each behaviour test replayed with a restart after every step | The end must match the run without restarts |
| Adapters | The argv for every role, permission and continue kind, per CLI; the screen classes | Golden files; the flags come from each CLI's `--help`; the screens are captured real ones (the fixtures in `factory/test/fixtures` are kept) |
| World | The checks rollup and snapshot against JSON captured from real PRs (#665, #669 and #671 each have skipped checks); git against temporary repos; hc against `herdrtest` | — |
| End to end | One toy project: start, plan, one task through evidence and review to merge, one ask, stop and start again | A private hc, scripted fake agent CLIs, a fake `gh`. Then one real run on a scratch repo before cutover |

**Hygiene.** Every path comes from one function that tests override; one test fails on a personal path or name in code, docs or fixtures, and on files a test run left behind; a guard test fails if any test touches the real home, a real daemon, the owner's hc or GitHub. End-to-end runs remove what they start (terminals, worktrees, trusted-folder entries).

"A red check is a bug until shown otherwise" applies to the engine's own CI.

## 17. Unverified, open, and what was left out

### 17.1 Unverified facts

From the research notes; none was tested by running an agent.

| # | Item | Matters for |
|---|---|---|
| U1 | Whether agy reports status to herdr-core the way Claude Code does (checked on 2026-10-30). Codex's status is not reliable: spike S0.3 (2026-11-19) read none for a Codex terminal, and PR #700's run the same day read `done` for a finished one, so the engine does not use it (section 9.2) | the run table for agy and Codex |
| U2 | Where a session id comes from under herdr-core. The notes say the CLI's own files are the safe source | resume |
| U3 | Whether a changed model or effort survives a resume, per CLI | resume |
| U4 | The exact screen text when a CLI hits its usage limit; no fixture exists | R5 |
| U5 | Whether a reviewer's last message can be read whole from the terminal for agy. For Codex, yes (spike S0.3, 2026-11-19: the tag came back on one line with its code) | A7 |
| U6 | Whether the daemon runs under launchd on the owner's machine, and whether `hc exec` then has the folder access the daemon lacks | section 2.4 |
| U12 | What herdr-core can report about a terminal (output activity, child processes, the owner typing or quitting an agent) and whether killing a terminal ends its process group. The reviewer found no API for the first three | the run table's quiet and background-job rows; "the owner quit it" in 4.8 |
| U13 | Whether a Claude or Codex reviewer can build and run in a scratch copy while unable to write the branch. Only agy has a known mechanism | 9.1 |
| U7 | Branch protection and merge-queue settings of the sheepdog repo | N37–N42 |
| U8 | GitHub rate-limit behaviour through `gh` | failure classes |
| U9 | Claude Code's flag for "no special permission mode" on resume (`default` is not in the help text) | adapter |
| U10 | The storage size estimate in section 14 | A6 |
| U11 | How much the engine's own check runs cost in time on a real project | section 11.6 |

### 17.2 How the design was checked against the knowledge base

| Pass | Design | Result |
|---|---|---|
| 1 | The first draft: a transition table of about 150 rows | Of 599 behaviours: 120 on a named row, 181 not covered, 55 in conflict, 64 not needed, 28 deferred, the rest on an executor, observer, adapter or view. This result is why form B was chosen (section 0) |
| 2 | Form B, before the fixes below | 19 not covered, 19 in conflict, 143 on a named rule, 93 deferred on purpose, 26 not carried on purpose, 53 not needed, 30 details, 216 on the ladder, the acceptance table, an executor rule, an observer, an adapter, a setting or a view. The three mappers also reported about 30 faults in the new rules themselves (mis-ordered rows, a normal run landing on "no rule matched", recaps that would reopen at once) |
| after 2 | This document | The 19 gaps and the reported rule faults were fixed in the text; each of the 19 conflicts is marked fixed, deliberate, a setting, later, or open (knowledge-base.md, "Maps to"). **Not re-checked by a third mapping pass.** A separate audit by simulation is in [notes/table-audit.md](notes/table-audit.md); what it found and what was done is in section 17.5 |

### 17.3 What was left out, on purpose

The first check produced 181 gaps. Absorbing them all would rebuild the old engine. Each group was sorted once:

| Group (knowledge-base areas) | Decision | Reason |
|---|---|---|
| Stuck states, stale accepts, PR closed in any state, alerts that never clear, five head tests, drift checks | Gone with form B | The cause was remembered phases |
| Run identity, process-group stop, hc outage, quiet reset on activity, idle grace, background jobs, owner quit, failed resume, urgent messages | In (sections 5, 8) | Field failures, cheap |
| Coordinator limits, signed out, error turn, owner typing, template change | In (section 6) | Field notes 53–55 |
| Merged outside the bar, never-delegable kinds, asks outliving a worker, dropped dependencies, amend of a started task, owner's "merge as it is" | In (sections 4.9, 7) | Safety or a dead end |
| Pools: usable, floor, next candidate | In (sections 4.8, 7.3, 9.1) | Hands-off at night |
| Merge queue, required review, flake re-run, CI counting, executor rules | In (sections 4.7, 9.4) | Real GitHub behaviour |
| Evidence | The first increment after version 1 (11.6; owner decision D9) | Its cost is not measured, and version 1 should stay thin |
| Time limit per attempt | In (N14) | One row |
| Token budget per task | Later | Needs a reader per CLI |
| Cheap sync (a local test merge in place of a full CI run on update) | Later | Saves CI minutes; needs the check runner first |
| A cap on PRs in CI at once | Later | Cost control |
| Milestone gates, verify task, critical path, digest | Later slices | Not needed to run one project |
| Changing a setting, delegation, rule read-back | Slice 14 | — |
| Alerts to push and voice | A later slice | — |
| Design loop | A later slice | — |
| Secrets (a prompt at the Mac, scopes, reviewers refused) | Later, its own increment | The old engine had a separate tool; nothing in version 1 needs a new secret |
| Machine load holds (CPU, memory) | Later | — |
| Simulators, devices, other machines | Later | Needed by iOS projects only |
| Start checklist beyond the foundation check (release workflow, CI shape, iOS preflight) | Later | Doctor items, one at a time |
| File-overlap sequencing, "not before" time, contract tasks, research-only tasks, continuing an existing PR | Later | Plan features; each is a field and a check |
| A rule proposal when one retro cause repeats | Later | The retro doc itself deferred it |
| Worker-run review; a fallback to the first installed CLI; workers opening owner asks; values frozen for a running task; a docs-only exemption from review; a hand-kept list of generated files; an unbounded first `continue`; telling the worker about red checks during a review | **Not carried** | Each is a cause named in critique.md, or contradicts an owner decision |
| Merging with no CI after a grace time; merging when checks cannot be read | **Not carried** (assumption A1; owner decision D12) | "No checks is never green". A repo with no PR workflow is refused at start. The owner's override is the one way to merge without checks |

### 17.4 Open items

| # | Item |
|---|---|
| O1 | Evidence (11.6) is decided but not in version 1. Its cost on a real project is unknown (U11) and is measured by spike S0.7 before the increment starts |
| O2 | A third review round happens by itself after two that asked for changes (L13). In the old engine the coordinator granted it. Code is more hands-off; the cost is one automatic round on tasks a coordinator would have stopped. `retro_from_round` set to 2 removes it |
| O3 | The contract has five alert kinds. `agents_down` is a sixth |
| O4 | Approved board text says "engine and Python" (FactoryStartShell, FactoryTerminal). With a Go engine that line must change |
| O5 | The contract and one board still name a `/factory` page that the addendum replaced with the space-page section |
| O6 | Order is form B's failure mode. The order tests of section 16 are the guard; whether they are enough is judged at each review point of [plan.md](plan.md), with real rows |
| O7 | The ladder (4.6) treats review, retro, recap and owner as one loop at four levels. The levels are not alike (three retro verdicts, four recap choices). If the special cases grow, split the function per level rather than adding conditions |
| O8 | The picture of the tables (`transitions.html`, published privately for the owner) was redrawn for form B from the tables of this file. Its "Try it" panel is a simplified illustration, and its interactions were not tested in a browser |
| O9 | 62 of the old engine's 893 test functions are cited by no knowledge-base row (knowledge-base.md, Appendix D). They were not re-read |
| O10 | Until reviewers run in a scratch copy (9.1, U13), slice 1 runs them in the task's worktree. A round voided because the reviewer left that worktree dirty (7.2) leaves the dirt there: the worker's next `ready` is refused (`dirty_tree`) and the next verdict would be void too. The round is voided and logged, and section 4.8 retries up to `reviewer_retries` before waking the coordinator (`reviewer_failed`). Who cleans a reviewer's leftovers, and how without touching the worker's own unfinished work, is not decided (still open after slice 3) |
| O11 | The in-memory tip reading (section 3, `last_tip`) is keyed by the tip and `H` only, not by the remote base commit. A reading taken while the local base ref was stale (a false `merge_only`, or a line count) stays until the tip or `H` moves. The effect is safe: the tip goes to review instead of N20, at the cost of one round |
| O12 | After `recap decide new`, until the next attempt exists, a stopped worker's token still resolves to the closed attempt, so a stale terminal could add a ready to it. Harmless while that run is stopped; the fix is to resolve tokens only to an open attempt |
| O13 | A worker cannot answer a coordinator's send-back at an unchanged head that is not the base: `--reject` is for reviewer rounds only, and `--nothing-to-change` is refused `nothing_new`. It can only commit or sit idle while N22 waits on it, and the coordinator is not woken. How a worker says "this is already right" is not decided |
| O14 | A Codex reviewer still loads a trusted worktree's own `.codex/config.toml`, so a repo or a worker can add MCP servers or a notify command to the review. Hooks stay off. No per-run override turns that layer off without a prompt (marking the folder untrusted asks "Open restricted"). Not decided: refuse a Codex review when the worktree has that file, or test whether `-c mcp_servers={}` and `-c notify=[]` override it |
| O15 | A `plan amend` that lowers `estimate_lines` after a size recap's `continue` can drop `size_cap` below what the `continue` granted, because the decided recap is no longer open, and so open a second size recap |
| O16 | `plan amend` cannot change `for_whom`, `if_skipped`, `smallest` or `not_doing` on an existing task, only `title`, `brief`, `outcomes` and `estimate_lines`. Whether a changed `not_doing` should be a brief boundary (section 3.1) is not decided |
| O17 | The template test (prompts.md 7) checks the flags of each command a template names against the usage's global flags line, not per subcommand, so a flag that exists for another command passes. The `{ready_cmd}` line's flags are not checked there; other tests pin them |
| O18 | The base observer makes two GitHub calls per pass, and more when a run on the head commit was cancelled by hand. Owner: cheap sync |
| O19 | A workflow file that fails to start (`startup_failure`) on the base reads green: the base observer does not count it red, though the checks rollup does |
| O20 | How the menu-bar app gets a pairing code over a channel other programs of the same user cannot read, whether `GET /api/pair` and `sheepdog pair` change, and a limit on enrol attempts on this computer. **Decided (slice 6, PR 6.4/6.5, section 7.1):** separate `POST /api/pair/browser-code` (paired-device session only, 5-minute single-use short code for `kind: "browser"`); phone QR pairing (`GET /api/pair`, `sheepdog pair`, setup/debug pages, iOS E2E) stays unchanged; loopback `POST /api/devices/enroll` rate-limits the 6th wrong guess in a window with `429 rate_limited` |
| O21 | A hung coordinator can lose a wake's text when the engine replaces it. The engine has already marked the wake delivered. **Done (5b.5, #740):** requeues interrupted wakes for every coordinator CLI |
| O22 | A recorded PR number that GitHub can no longer read can look like confirmed absence to N26. Keep the PR read unknown in that case. Slice 5b, cross-PR finding #15 |
| O23 | Screen phrase matching can mistake agent output for CLI status. Moved into 5b.7a: R5/R19 classification and the reset time come only from the adapter's own UI area (Claude: the bottom area at or below the input box; Codex: the footer and composer area), never from agent output above it; if several genuine messages show, the bottom-most counts, and a quote above or below in the agent's text never ends a run or sets a time. A printed reset time already in the past (having passed during the idle grace or tick) gives now plus `pool_minutes`, never tomorrow or next year, and text in brackets before the time zone does not stop the time from being read. Slice 5b, cross-PR finding #17 |
| O24 | R21 can interrupt a worker when Claude Code folds a long command's output and only the stripped timer changes. **Handled in 5b.7b (round 3):** a working run counts as active on a pass when its agent process (the terminal's `foreground.pid` from `hc get`, falling back to `pid`) has at least one live non-zombie descendant (`ps` stat not starting with `Z`/`z`) that started at least `start_minutes` (the existing setting, default 2 minutes) after the agent process itself: `agentEtime - childEtime >= start_minutes`, with both `etime` values (`[[dd-]hh:]mm:ss`) read in the same single bounded `hc exec` of `ps -ax -o pid=,ppid=,stat=,etime=` per pass with a short `Timeout` capped by the pass context budget. No turn tracking and no hand-kept list of helper names are kept: long-lived adapter and MCP helpers started with the agent (`mcpbridge`, `codex-code-mode-host`, stdio MCP servers) are younger than `start_minutes` relative to the agent and never count (on turn 1, later turns, or after a restart), while traversal still walks through startup helpers and intermediate rows with unreadable `etime` so commands started later under them still count. When such a descendant is found on a pass with an unchanged screen, `world.ObserveRun` emits `EvActivity` before `EvTick`, so `flow.StepRun` updates `LastActivity` and clears `InterruptedAt` and R21's quiet timer does not advance (neither R21 nor R22 triggers). An unreadable answer (no PID, missing or unparseable agent `etime`, RPC error, non-zero exit, timeout, truncated or empty output) is unknown and changes nothing: no `EvActivity` is emitted and R21/R22 work as before. `RunRecord.LastActivity` persists in `state.json`, so quiet-timer progress stays restart-safe. **Accepted limit:** a process the agent leaves running in the background after startup (such as a dev server) keeps the run active |

### 17.5 The audit by simulation, and what it means

After the second check, an independent agent walked nine families of concrete histories through the tables pass by pass ([notes/table-audit.md](notes/table-audit.md)). It found 42 faults in the text as it then stood: 5 unsafe (among them: a coordinator's send-back ignored at a head that had passed; an owner's "no" forgotten by a new attempt), 6 dead ends, 3 loops, 3 normal situations with no rule, 5 order faults, 9 contradictions, 11 undefined words. The happy path was clean.

What was done: every unsafe, dead-end, loop, no-rule and order fault, and most of the contradictions and undefined words, were fixed in this text. The ladder, which the audit called "a second rule table written in prose", is now a table (L1–L13). One setting (`extra_rounds`) was removed as redundant.

What it means, plainly:

- **These tables are a specification that has been read carefully three times, not one that has been run.** Each reading found faults the one before had introduced or missed. More remain.
- Reading does not converge; running does. So the first PRs of [plan.md](plan.md) build the tables as pure, executable data with the audit's histories as tests, before any code touches a terminal or GitHub. A fault found there costs a row, not a night.
- The fixes made after the audit were **not** audited again.

**A note on numbers in 17.6 and 17.7.** PR numbers and "increment" numbers there refer to the plan as it was when each review was answered (kept as `notes/plan-before-agile.md`). plan.md has since been rewritten as vertical slices at the owner's request (D16): a walking skeleton first, a review point after every slice. Where this document says "the evidence increment", "the plan-review slice" or "the retro increment", plan.md's slices 10, 11 and 12 are meant.

### 17.6 The first independent review, and what changed

A reviewer who did not write this design returned CHANGES ([review-1.md](review-1.md)): keep form B; fix two blockers and a list of majors before any code. What was done:

| Finding | Change |
|---|---|
| B1 the owner's accept was not bound to a commit, and could be answered by a rule | The override is its own kind, never delegable (7.1); an ask stores its commit; the override and a gate's yes use `covers` (3.1, 3.2); rows N15–N19 (4.3b). The override skips review for the commit it names, and section 10.1 says so |
| B2 `factory set` trusted a terminal as the owner | A setting changes only through a `rule` ask proven by the daemon (7.2, 10.1); plan.md has no terminal shortcut in version 1 |
| M0 red main had no exit | N38 names one after `main_red_minutes` |
| M1 the report tag was not bound to its run | A one-time code in the run's prompt (9.3, 7.2) |
| M2 "2 reviews" was two rounds run | Two passing rounds covering the head (L8) |
| M3 a worker at a dialog had no exit | A blocked run ends as stuck after `blocked_minutes` (run table) |
| M4 a retro that cannot run | L11 falls to L12 |
| M5 two deciders | N24 then N25: one at a time |
| M6 waits with no command | N35's wake names its exits; doctor refuses a repo with no PR workflow or a required approving review (plan.md PR 32) |
| M7 import cycle | `plan` is a leaf that `flow` imports (13) |
| M8 size | 8,500 to 10,000 lines full; about 4,000 for a thinner version 1 (13, plan.md). Superseded twice: see 17.7 and section 13 |
| M9 decisions in two places | Stage and wake are fields of the row and the lists are printed; 9.4 holds mechanics only; the rollup, adoption and `merge_only` tables are in `flow` |
| M10, M11 missing version 1 work | plan.md PRs 12, 31, 32, 33; the phone boards are a separate, unsized track |
| M12 hc gaps | Spikes S0.3, S0.6, S0.8 |
| Thinner version 1, earlier real run | plan.md: a real agent at PR 13 and a first real merge at PR 22 (PR 6 before the re-splits); evidence, retro, settings changes and several observers moved to increments |
| Minors m1 to m8 | The landing rows are reordered; ids renumbered in table order (`notes/renumber.tsv`); pool and sign-in loops closed; the exit test widened |

Not done, on purpose: the reviewer's list of "weak" rows was worked through only where a fix was one line (working time, a passed head and the limits, background jobs at landing, the first-run screen, pool windows, a plan amend voiding rounds, the slot check on restart, the review diff, scratch copies). The rest are listed in review-1.md section 1 and go to the builders of the rows concerned.

These changes were then read by the second review (section 17.7).

### 17.7 The second review, and what changed

The same reviewer returned CHANGES, narrowly ([review-2.md](review-2.md)): every round-1 finding fixed except the size plan; one new blocker and three majors in the parts added since, all text.

| Finding | Change |
|---|---|
| NB1 the page-origin proof equals a ticketless local request | 7.1: a local request with no ticket is never the owner; the factory page answers when the browser holds a session ticket (D15); the threat model and the log line are stated. plan.md: spike S0.9 for enrolling a browser; "a local answer with no session ticket to a gated ask is refused" is a done-when line of the supervisor PR (PR 31) |
| NM1 the reviewer's tag could not satisfy 7.2 | 7.2: the per-outcome judgement is a required field only from the evidence increment; until then the tag of prompts.md 5.4 is the whole report |
| NM2 a worker that rejects every finding had no path | `ready --reject` (7.2), the `reject` record (3), ladder row L10, and the worker and reviewer templates |
| NM3 version 1's size and split | plan.md section 3 re-split twice (the independent check found the first re-split still broke section 13's bases): about 8,000 lines in 34 PRs, none over 300; section 13 says the same. PR 2 adds outcomes with their proofs to the plan checks; PRs 5 and 12 add the proofs to `ready`. A real agent is started at PR 13; the first real merge is at PR 22 |
| Override m1, m2, m3, m4 | 4.3b: the engine fills each commit with `H`; the card's checks state is kept and N19 tests it; N18 updates a branch GitHub wants up to date; one result line per card; N31 cancels a stale gate ask |
| Prompts m5 to m9 | One statement of where outcomes and proofs land (version 1; the engine's own runs are increment 1); the `ready` record's fields; `factory set` refused from a run's token; 7.2 lines for the plan reviewer; the version 1 coordinator template no longer mentions the plan reviewer |

**An independent agent then checked these fixes and the agreement of the three documents** ([notes/verify-review2.md](notes/verify-review2.md)): no blocker; four majors (an override that could not be spent after its checks got worse, a reject that could defeat a coordinator's send-back, a coordinator template that did not know the override, and a PR split that still broke section 13's bases) and 22 minors. All four majors and the minors were then fixed in the text: the `override` record with `spent` (section 3, N19b); a reject valid only against a reviewer's round (7.2, L10); the override line in the coordinator template; the 34-PR split. **Those last fixes have not been checked by anyone.**
