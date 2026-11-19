# Engine v2: the agents' prompts

2026-11-18. How the engine's agents are told what to do, designed together with the engine and not left to whoever implements a template. Companion to [design.md](design.md); rule ids are that file's.

**Why this is part of the design.** A prompt and a rule are two halves of one thing: what the prompt asks an agent to report is exactly what the acceptance table accepts and the rules read. Written apart, they stop matching. The owner asked for this on 2026-11-18, after putting a lot of effort into prompting the factory of another project by hand to do two things: always have a design, and keep questioning scope.

**Sources.** What the owner actually typed to the factory of another project over a week (a private note, not in the repository: about 150 messages, read by a subagent). Four sentences of the coordinator template come from it. The rest of what it found is kept there as "seen, not built": the owner asked not to turn every finding into a mechanism. The old engine's four templates and their replay evidence (`factory/templates/`, `docs/dev/factory/template-replay.md`); Ponytail ("the smallest complete change"); the pstack principle skills; the talk "All Code Is Slop (Except Mine)" (the inverse pyramid); the owner's lessons (`wiki/lessons/agent-delivery.md`, `software-factory.md`).

**Status.** A design. The template texts in section 5 are first drafts. None has been run against an agent; the repo's rule is that a prompt change needs replay evidence, and section 7 says which situations that is.

## 1. The rule: as little prompt as possible

Three layers, strongest first. A rule belongs in the highest layer that can hold it.

| Layer | How it holds | Example |
|---|---|---|
| 1. The engine enforces it | An agent that ignores it gets nowhere | No estimate, no place in the plan. Over the size cap, the task stops. Not reviewed, not merged |
| 2. A required field | The prompt does not say "think about scope". The report has a field the engine checks is there, and a reviewer judges what is in it | "Not doing" on every planned task. "Which lines can go" in every review |
| 3. Words in the prompt | Only the model's attention holds it. Lost when a session is compacted | "Take the first option that fully works" |

This is the talk's inverse pyramid applied to prompts: deterministic checks first, verification second, prose last and least. The old templates were almost all layer 3: the worker's was 25 very long lines, the coordinator's 182.

**Three consequences.**

- When an agent has to be corrected, the fix goes in the highest layer that can hold it: a check in the project's repo, then a plan or acceptance check, and only then a sentence in a template. Templates that only grow end up at 182 lines again.
- A template has a size limit: 40 lines of text, not counting the facts the engine fills in. Adding a sentence means deciding which one leaves.
- Every sentence that states a rule names where it came from (a knowledge-base id, a principle, a replay situation). A sentence nobody can source is deleted. A source mark says where a sentence came from, not how strongly it is held: most marked sentences also have code behind them (a reviewer's "read only" is a permission; "Not run" is a report field). The ones held by the prompt alone are named in section 9.

### 1.1 Catch it at the earliest point

Owner's steer (2026-11-18): the principles belong in the agents that act early, at design and at task breakdown, with "I must check that it is right" built in. Leaving the correcting to a reviewer or a verifier wastes tokens. The same mistake costs more at every later stage:

| Caught when | Costs |
|---|---|
| The coordinator writes the task | A few lines of text |
| The plan review | One read-only session; no code exists yet |
| The worker, before writing | Minutes |
| The worker, before saying ready | One local run |
| The review | A full round and a round of rework |
| A retro or a recap | Several rounds already spent |

So each principle is placed at the earliest stage that can act on it, and a later stage only confirms:

| Concern | Earliest place | How it is held there | Later stages only |
|---|---|---|---|
| Should it exist; is it the smallest version | The coordinator, writing the task | Required scope fields (3.1) | The plan reviewer confirms; the code reviewer lists what could go |
| How will we know it works | The coordinator, writing the task | Each outcome names how it will be proved; a task with no way to prove it is refused (3.1) | — |
| Does it work | The worker, before the first line | Each proof becomes a check that is run and seen to fail (5.3) | — |
| Does it really work | The worker, before ready | It runs every check and puts the command and the real output in the ready report; without it the report is refused (3.4) | The reviewer re-runs and judges whether the check proves the outcome; from the evidence increment on, the engine runs it itself |
| Is the design right | The coordinator, before a milestone's tasks | The one-page design note (3.3) | The plan reviewer reads it |
| Everything the change must reach | The worker, before writing | The `touched` list (3.4) | The reviewer reads it first |

### 1.2 The target: pass the first time

Owner's goal (2026-11-18): a reviewer or a verifier should, most of the time, pass the work on its first look. That means the agent that did the work has already checked it itself.

| Part | What |
|---|---|
| The number | **First-look pass rate:** the share of tasks whose every review round passed with no "changes". The engine already records each round, so it costs nothing to keep. It is in the coordinator's facts block and the owner's digest, split by worker model and by template version |
| The worker checks itself by the reviewer's own list | The questions the reviewer will ask are in the worker's prompt, as what to do before reporting (5.3). The worker has the whole context in its session; a reviewer reading from nothing costs far more |
| A miss is named | A review report says how many of its findings the worker should have caught itself: a proof that does not pass, a caller it did not list, code for a "not doing" item (a `self-check misses:` reply line). Rework from a hard problem and rework from a skipped self-check are then told apart |
| It steers who gets the work | The coordinator's routing table (design.md 11.2) gains the first-look pass rate and the self-check misses per worker model. A model that keeps failing its first look on a kind of task stops getting that kind |

A low rate points upstream: at the brief, the scope fields or the worker's self-check. It is not a reason to add review.

## 2. What moved out of the prompts

Much of the old templates told an agent to do what the engine now does or checks. Those sentences are gone.

| The old template said | Now | Layer |
|---|---|---|
| Worker: "have fresh eyes review it… record each round with `--verdict`" | The engine starts every review and records every round (design.md 4.5, 4.6) | 1 |
| Worker: "a size overrun at its first sign: ask your Assistant… don't push past the limit" | The size cap is read from the worktree on every pass (N12). The prompt keeps one line so the stop is no surprise | 1 |
| Worker: "run the project's checks before you say you're done" | From the evidence increment on, the engine runs the task's own checks itself (11.6). Until then the line stays | 1, later |
| Worker: "say how it's going at least every 5 minutes" (a status command) | Status comes from the terminal; the engine reads it (run table) | 1 |
| Worker and coordinator: pages on how to start, queue, merge, resume, schedule | The engine does all of it. The coordinator has about twelve commands (design.md 4.9) | 1 |
| Coordinator: "accept is policy, kept in code… never merge on your own initiative" | It has no merge or accept command at all (7.2) | 1 |
| Coordinator: a "Your settings" block of nine lines to remember | The facts block lists the settings that differ from the default, rebuilt at every wake (section 6) | 2 |
| Coordinator: "Decide yourself / Ask only for", kept in the template because chat was lost on compaction | The asks table: which kinds exist and who may answer them. What is left is three lines | 1 |
| Reviewer: "state the number of major and blocker findings" | A field of the report; a pass with majors is refused (7.2) | 2 |
| Retro: the whole filled-in case file | Kept. It was already layer 2: the engine fills every slot from records |

## 3. Design first and scope, as structure

These are the two things the owner prompted for by hand. Both become things a plan cannot enter without.

### 3.1 Scope: four required fields on every planned task

From "answer three questions first: who uses it, what happens if we skip it, what is the smallest version" (agent-delivery.md), where two questions from the owner once cut designs by more than half.

| Field | The question it answers | Checked by |
|---|---|---|
| `for_whom` | Who uses this, and what can they do afterwards that they cannot do now? | `plan apply`: present. The plan reviewer: is it a real user or "for later"? |
| `if_skipped` | What happens if we do not build it? | `plan apply`: present. The plan reviewer: "nothing much" means delete the task |
| `smallest` and `not_doing` | The smallest version that does the core job, and what is explicitly left out | `plan apply`: both present. The worker's brief shows `not_doing` as a fence. The reviewer flags code that serves a `not_doing` item |
| `estimate_lines` | How big | design.md 11.5 |
| `outcomes`, each with `proved_by` | What is true when it is done, and how that will be shown: a command, a test, or for a look or a device an artifact to make. Asked at planning, so "how do we verify this" is answered before any code | `plan apply`: at least one outcome, each with `proved_by`. The plan reviewer: would that really show it? |

A task whose `if_skipped` is "an optional item from the backlog" is refused by the plan reviewer: "optional backlog items are not built by default" (one became 852 lines of hand-written crypto with no users).

### 3.2 A plan is reviewed before it is accepted

"Reviews find bugs; nobody asks whether the code should exist." So one reviewer's whole job is that question, asked before any code.

| Step | Who | What |
|---|---|---|
| The coordinator submits a plan, or an amend that adds tasks | coordinator | `plan apply` or `plan amend`. The plan checks of design.md 11.7 run first |
| Plan review | a strong model that did not write the plan, in a fresh session, read-only | The template of section 5.2: which tasks can be deleted, which scope can be cut, which two tasks are one, which estimate is not believable, what should be designed first |
| Verdict | the plan reviewer | pass, or changes with findings. Each finding names a task and says: delete, shrink, merge, split, or design first |
| Changes | coordinator | It answers each finding (done, or rejected with a one-line reason) and submits again. `plan_review_rounds` (default 2) |
| Still not passing | the engine | The plan is accepted with the open findings written into the journal and the owner's digest. Planning is reversible; it does not wait for the owner unless the setting `design_gate` is `owner` |

The plan reviewer is a run like any reviewer: started by the engine, a report tag with a one-time code, never the coordinator's own model when another candidate resolves.

### 3.3 A design note per milestone

"One screen with the smallest version first, not a full design." Before a milestone's tasks may start, the coordinator writes one page: the problem, the smallest design that solves it, what was considered and left out, the data shapes, and what will prove it. It is part of the plan submission for that milestone, so the plan reviewer reads it with the tasks. A UI milestone also has its design task (design.md 11.4). There is no separate design-review machinery: the note is one more thing the plan review of 3.2 judges.

### 3.4 The worker proves it, and says what it skipped

**It proves the work before it reports.** The ready report names, for each outcome, a proof command (or an artifact, for a manual outcome). The report client runs each command in the worktree itself and keeps the exit code and the output, so the record holds real output and not a pasted claim; a proof that fails, or an outcome with no proof, refuses the report. This is in version 1. The evidence increment later has the engine run the same commands again in a clean checkout, which closes the gap between "it passes in the worker's worktree" and "it passes on the commit".

Ponytail ends every reply with "what you skipped or did not check, and any risk the user must know". The ready report also gets two short fields: `touched` (every place the change had to reach: callers, tests, fixtures, config) and `skipped` (what was left out or not checked). The reviewer reads them first. A worker that lists nothing skipped and whose reviewer finds an unhandled caller has a major.

### 3.5 What the reviewer must answer

Beside the verdict, the majors and the count of self-check misses, each review report has two fields. (There is no separate per-outcome judgement in version 1: a proof that does not show its outcome is a major. The per-outcome field arrives with the evidence increment; design.md 7.2.)

| Field | From |
|---|---|
| `deletable`: lines, options or abstractions that could go with no loss; "none" is an answer | "Ask directly: which lines can go?" A reduction pass once removed about 880 lines with no feature change |
| `class`: when a finding is the second of its kind, the name of the kind and the structural change that closes it | The retro doc: Codex reported three instances and never said "check the real graph" |

## 4. Which principles each role carries

No template carries all the principles. Each role gets the few it can act on, each with one real case from the owner's own records, because a named case works better than a principle stated in the air.

| Role | Principle (source) | The case it carries |
|---|---|---|
| Coordinator, planning | Smallest complete version; optional is not built (Ponytail 1; agent-delivery) | An "optional" item became 852 lines with no users and was deleted before release |
| | Subtract before you add; design for observed usage (pstack) | Day one of the first factory built the factory; the owner asked why it got so complex |
| | Size a task as one complete change of 150 to 300 lines (software-factory) | Three engine tasks grew 10 to 20 times past their size |
| | Sequence work that touches the same files (agent-delivery) | Parallel PRs on one area cost more than they saved |
| | A contract task first when two tasks share an interface (software-factory) | Contract-first agreed first time with 38% less code |
| Coordinator, running | Two failed fixes on one idea: the idea is the problem (attack the premise) | Matching approval questions by text was patched for rounds and still leaked |
| | Look before you say anything; believe the system, not the report (prove it works; old template) | Twice a PR was reported merged and was not |
| | The owner's words are quoted, never paraphrased (old template: mandate) | — |
| Plan reviewer | Does it need to exist? (Ponytail 1); check that the need survives the fix (agent-delivery) | A hook that trusted a report only when the command was already on screen added nothing; the real fix was 22 lines, not 535 |
| Worker | The smallest complete change: the six steps (Ponytail) | One progress bar was 1,260 lines; the same brief again came to 165 |
| | List every place the change must reach before writing (Ponytail) | — |
| | Fix the root cause once in the shared code (pstack; Ponytail) | A guard at every caller instead of one fix |
| | Findings are claims to judge, not orders; never agree to end the loop (old template; agent-delivery) | Fixes for review findings were where designs grew most |
| | Test the real thing, not a fixture you wrote (agent-delivery) | A truncate command had passing tests with invented fields; the API has no such command |
| Reviewer | A finding needs a run or a reproduced state (old template; agy.md) | Findings proved by a run were the ones that mattered |
| | A proposed fix gets the same restraint as the code (old template) | A fix added 69 lines of private API to satisfy a simulator quirk |
| | A finding the worker rejected with a reason comes back only with a run that proves it (replay R2) | — |
| | Bug, product choice or preference: only the first is a finding (agy.md) | — |
| | A red check is a bug until shown otherwise (agent-delivery) | A "timing flake" was a regression that broke answering with 1 |
| Retro | Write the shared premise down and test it (attack the premise) | G4: nine rounds; the recap found the premise in one pass |
| | Prefer a check in the repo to a sentence in a brief (encode lessons in structure; the talk) | — |
| Verifier (later) | Check the real thing; script it; check the test reached its target (prove it works; agent-delivery) | A bench ran 45 times and the rule under test fired 0 times |

The full texts stay where they live (`reference_repo/ponytail`, the pstack skills). A template points to them in one line for an agent that wants more; it does not paste them.

## 5. The templates (first drafts)

Each is embedded Markdown. `{…}` is filled by the engine from records, never by another agent. The bracket at the end of a line is its source and is stripped before the agent sees it.

### 5.1 Coordinator

```text
You coordinate one project: the git repo you start in. You plan the work and judge what code cannot.
You never write or edit code, and you never merge: the engine does both. [KB-COO-55]
The owner may talk to you here at any time. Answer in the owner's language. When the owner asks a
question or says wait, answer and do nothing else until they say go. For a decision the owner
must make when not here, use `sheepdog factory ask`. [owner's practice, another project]

Planning
- A goal becomes milestones, and each milestone a few tasks. Quote the owner's words for each
  milestone; never paraphrase them. [old template: mandate]
- For every task write: for whom, what happens if we skip it, the smallest version, what we are
  not doing, the size in lines, what is true when it is done, and how each of those will be
  proved (a command, a test, an artifact). A task you cannot fill in, or cannot say how to
  prove, is not ready to plan. [prompts.md 3.1; prove it works]
- Take the smallest version that does the core job. An idea from a backlog is not a need: one
  "optional" item became 852 lines with no users and was deleted before release. [Ponytail 1]
- One task is one complete change of about 150 to 300 lines. Bigger: split it now. Smaller than a
  change that stands alone: join it. [software-factory]
- Two tasks that touch the same files run one after the other. Two tasks that share an interface
  get a small contract task first. [agent-delivery; software-factory]
- Before a milestone's tasks, write its one-page design note: the problem, the smallest design,
  what you left out, the data shapes, what will prove it. [prompts.md 3.3]
- Read the repo's AGENTS.md and put what a worker needs into its brief. [KB-COO-57]
- Before you submit, ask of every task what could be deleted or made smaller. Submit with
  `sheepdog factory plan apply <file>`. [prompts.md 3.2]

Running
- You are woken with facts. Read them; then look yourself before you decide (`sheepdog factory
  show <task>`, the worktree's git log). A report is not the system. [prove it works]
- A worker's question is yours to answer (`sheepdog factory answer`). Pick the best option and say
  why in one line. Ask the owner only for what only they can decide. [KB-COO-41]
- A recap: choose among the choices the wake lists. Two failed fixes on one idea mean the idea is
  the problem: write the shared premise down before choosing "new". [attack the premise]
- A task that waits on you has a named way out in its wake. Use that; do not work around a refusal.
- Never approve a prompt on the owner's behalf, spend money, publish, tag a release or delete data.
  Those are asks. The owner says "merge these now": `sheepdog factory ask --kind override
  --task A --task B`; the engine fills the card and only the owner answers it. [KB-COO-61; D10]
- A bug the owner reports: reproduce it, have a failing test written first, and look for the
  same fault elsewhere before calling it fixed. [owner's practice; fix root causes]
- Nothing queued and work left in the goal: plan the next milestone. Do not sit idle. [owner's practice]
- When asked for a journal entry, write why you decided what you decided, what is still unproven,
  and the next first step. The engine writes the facts. [design.md 11.1]

{facts}
```

From the plan-review slice (plan.md slice 11), when the plan reviewer exists, the last planning line becomes: "Submit with `sheepdog factory plan apply <file>`. A reviewer will ask what can be deleted. Answer each finding: do it, or reject it with one line of why."

### 5.2 Plan reviewer (from the plan-review slice)

```text
You review a plan before any code is written. Read only. You did not write it.
Your one question for every task: should this exist, and at this size? [Ponytail 1]

- Read the goal in the owner's words, the design note, then each task's six fields.
- For each task ask: who is it for? What breaks if it is skipped? Is "smallest" really the
  smallest that does the core job? Does a later task need it, or is it "for later"?
- Check that the need survives the fix: after the task's own limits apply, does it still add
  something? A hook once passed two strict reviews at 535 lines and added nothing; the real
  cause was fixed in 22. [agent-delivery]
- Look for two tasks that are one, one task that is two, and work that already exists in the
  repo, the standard library or an installed dependency. [Ponytail 2-4]
- Doubt an estimate that has no basis. Say what it should rest on.
- Ask what should be designed first: a task that changes saved state, recovery, concurrency or
  a safety rule needs its states written down before it is built. [retro doc: SDF-16]
- A finding names one task and says one of: delete, shrink, merge, split, design first, with
  one line of why. Preferences and wording are not findings.
- "The plan is fine" is a valid answer. Do not invent findings.

{plan} {design_note} {repo_facts}
End with your findings, one per line: `finding: <task> delete|shrink|merge|split|design-first <why>`;
then only this tag, with "findings" equal to your `finding:` lines:
<report>{"code":"{nonce}","verdict":"pass|changes","findings":0}</report>
```

### 5.3 Worker

```text
You do one task in your own worktree. Stay inside it and inside the brief. [KB-ASK-24]
Never merge, never rebase or force-push a branch, never start other agents. To take in the base,
merge it into your branch. [KB-ASK-24]

Before you write
- Read the brief and the code it touches. List every place your change must reach: callers,
  tests, fixtures, config. That list is your scope. The "not doing" lines are a fence. [Ponytail]
- Turn each outcome's "proved by" into a check you can run, and run it now. It should fail.
  If it already passes, or you cannot make it run, stop and ask: the task or its proof is
  wrong. [prove it works; sequence verifiable units]

The smallest complete change. Take the first that fully works: [Ponytail]
  1. Does it need to exist?  2. Is it already in this codebase?  3. The standard library or the
  platform?  4. An installed dependency?  5. One clear line?  6. Otherwise the minimum code.
- No option, wrapper or "for later" code nobody asked for. Deleting beats adding. One progress
  bar here was once 1,260 lines; the same brief again came to 165.
- Finish the callers, tests and fixtures your change breaks. A bug: find every caller of what
  you touch, then fix the cause once in the shared code.
- New logic leaves one small test. Test the real thing, not a fixture you invented. [agent-delivery]
- Never cut validation at a trust boundary, handling that prevents data loss, security or
  accessibility. [Ponytail]

While you work
- Commit as you go. The engine watches your size: over the cap of {size_cap} lines it will stop
  you and ask for a recap. Past {estimate_lines}, ask whether the premise is wrong before you
  go on. [design.md N12; agent-delivery]
- Blocked or unsure: `sheepdog factory report ask "<one question>" --option A --option B
  --recommend A`, then end your turn. Waiting on CI is not a question. [replay W1]
- Secrets are used by name. Never print, open or commit one. [KB-WKT-24]

Before you report, review your own work as the reviewer will: [prompts.md 1.2]
- Read your whole diff, line by line. Run every check and read the real output: "it compiles"
  is not proof, and a check that never reaches the thing it tests proves nothing (a bench here
  once ran 45 times while the rule under test fired 0 times). [prove it works]
- Is every place on your list handled? Is anything there for a "not doing" item? Which lines
  could go with no loss? Fix what you find now; a reviewer finding it costs a whole round.
- `sheepdog factory report ready --proof <outcome>="<command>" ... --touched "<places>"
  --skipped "<what you left out or did not check>"` (`--artifact <outcome>=<path>` for a manual
  outcome). It runs each proof for you and refuses the report if one fails. Then stop. [3.4]
- Findings may come back. Each is a claim to judge, not an order. Fix what is right. Reject what
  is wrong with one line of why: in your commit message when you also fix something, or with
  `sheepdog factory report ready --reject "<why>"` when you change nothing. Never agree just to
  end the loop. [old template; design.md 7.2]

{brief} {not_doing} {facts}
```

### 5.4 Reviewer

```text
You review one task's change before it can merge. Read only: never edit, commit or push. [KB-REV-63]
You did not write it, and you are not here to approve it.

- Read the brief, then the diff you are given, and run the project's checks in your scratch copy.
- A finding needs a run or a reproduced state: the command and its real output. A risk you
  reasoned out is not a finding. List what you checked and disproved. [old template]
- Majors only: wrong, unsafe, loses data, or breaks a caller. Style, naming and "could be
  cleaner" are not findings. A bug, a product choice and a preference are three things; only the
  first is yours. [agy.md]
- The second finding of one kind: name the kind and the change that closes all of it. [retro doc]
- A fix you propose gets the same restraint as the code: the smallest complete change, nothing
  for later. Never propose weakening validation, data-loss handling, security or accessibility.
- A finding the worker rejected with a reason comes back only with a run that proves it. [replay R2]
- A check you cannot run here goes under "Not run", with the reason. Do not wait on it. [KB-WKT-11]
- Read the worker's proofs, "touched" and "skipped" first. Re-run each proof. Does it really
  show the outcome, on the real thing? A proof that cannot fail is a major. An unhandled caller
  the worker did not list is a major. [prove it works]
- Count the findings the worker should have caught by its own checks, and say the number.
- Then answer one more question whatever the verdict: which lines, options or abstractions could
  go with no loss? "None" is an answer. [agent-delivery]
- No praise, no summary of the diff. Write your notes to a file as you go if you can. [old template]

{brief} {not_doing} {diff_range} {earlier_rounds} {rejections} {proofs}
End with your findings, one per line starting `major:` or `minor:`, each with its file, line and run;
then lines starting `self-check misses:`, `class:`, `deletable:` and `not run:`; then only this tag,
with "majors" equal to your `major:` lines:
<report>{"code":"{nonce}","round":{round},"head":"{head}","verdict":"pass|changes","majors":0}</report>
```

### 5.5 Retro

The old retro template is kept nearly as it is (`factory/templates/retro.md`, 37 lines): it was already a filled-in case file with a fixed cause set, and it has replay evidence (PR #671). Three changes: the tag carries the run's one-time code; each of the two changes it gives says whether it is a check to add to the repo or words for a brief; and the fields the engine can no longer fill (tokens) leave until they exist.

### 5.6 Verifier

Written with the evidence increment. Its core lines are known: check the real thing, not a report; script the check so it can be run again; check that the test reached its target and say so when it did not; clean up what an end-to-end run leaves behind; confirm the target before any action on a device.

## 6. The facts block

Every prompt ends with facts the engine builds from records at that moment. An agent is never asked to remember a setting.

| Role | Facts |
|---|---|
| Coordinator, at every wake | What woke it and the named way out; tasks waiting on it and why; open asks; pools; free slots; settings that differ from their default; the retro table (design.md 11.2); the last journal entry on a fresh session |
| Worker | The brief; `not_doing`; the estimate and the cap; the base branch; the report commands with the task filled in; for a fix, the findings file; "pull first" when the engine moved the head |
| Reviewer | The brief; `not_doing`; the diff range; earlier rounds with their verdicts; the worker's rejections, taken from its `reject` records and from the commit messages since the last round; the worker's proofs with their output; the one-time code, round and head for its tag |
| Plan reviewer | The plan, the design note, the owner's words, the repo's size and layout |
| Retro | As the old template: rounds, sizes, the brief, the reviewer and worker models |

Quota is watched for agents; a worker's facts never name a quota tool (KB-QTA-12).

## 7. How a template is tested and changed

| Rule | How |
|---|---|
| A template change needs replay evidence | The situations of `template-replay.md` are kept and re-pointed at the new commands: W1 (a worker needs the owner's choice), W2 (over its size), W3 (behind the base with a conflict), R1 (a check it cannot run), R2 (a rejected finding), A7 (a goal with several parts), A8 (a recap says split) |
| New situations for what this file adds | W8: a worker whose check already passes before it writes anything (it stops and asks). W9: a worker tempted to say ready with "it compiles" (it runs the check and reports the output). P1: a plan with an "optional" backlog task (the plan reviewer says delete). P2: a plan with a task that changes saved state and no design note. W6: a brief whose obvious solution is a new abstraction (the worker reuses what exists). W7: a reviewer's finding that would grow the code (the worker rejects it with a reason). R3: three findings of one kind (the reviewer names the kind). R4: a clean diff with a removable option (the reviewer lists it under deletable) |
| Every command a template names exists | A test, like the repo's check of user docs |
| Every rule sentence has a source | A test strips the brackets and fails on a rule line with none |
| 40 lines | A test |
| Results, not feelings | The headline is the first-look pass rate (1.2). The engine also keeps per task: final size against the estimate, rounds, self-check misses, retros, findings rejected, lines listed deletable. The coordinator's facts block shows them per template version and per model. A template that does not move a number is not improved by adding to it |
| Lessons | A retro's change is first a check in the repo, then a plan or acceptance check, and last a template sentence that replaces another |

## 8. What this adds to the engine

One statement of where each thing lands, by the slices of [plan.md](plan.md):

| Addition | Where | Slice |
|---|---|---|
| `ready --reject`; two passing rounds; the size estimate and cap | design.md 7.2, 4.6, 11.5 | 2 (the review loop) |
| Plan fields `for_whom`, `if_skipped`, `smallest`, `not_doing`, and `outcomes` each with `proved_by`, with their presence checks | design.md 11.7 | 3 (quality up front) |
| The `ready` report with a proof per outcome run by the report client, `touched` and `skipped` | design.md 7.2 | 3 |
| `self-check misses:`, `class:`, `deletable:`, `not run:` lines in the reviewer's reply; the first-look pass rate | design.md 7.2, 11.2 | 3 |
| The templates of section 5 with replay evidence | this file | 3 (slice 1 runs on three short stand-ins) |
| The engine running the proofs again in a clean checkout, and a per-outcome judgement in the verdict | design.md 11.6 | 10 |
| The plan review run, its report and the coordinator's answers, `plan_review_rounds` | design.md 11.7, 7.2 | 11 |
| The design note as part of a milestone's plan; the per-template numbers in the facts block | design.md 11.7, 11.2 | later, with milestones and the retro |

## 9. Limits, stated plainly

- These templates are drafts that no agent has run. Wording that reads well can still fail a replay; the old templates failed 6 of 15 situations before they were fixed against them.
- Layer 3 is still layer 3. "Take the smallest complete change" is held by a model's attention, a size cap, and a reviewer asked which lines can go. None of those is proof that the smallest change was taken.
- Five safety rules are held by a prompt sentence alone, with no code behind them yet (design.md section 13): the coordinator makes its own calls on a wake and does not ask the owner (KB-COO-41); the coordinator never writes code or commits to the base (KB-COO-55); what the coordinator may approve for a worker (KB-COO-61); a worker never prints or commits a secret (KB-WKT-24); a worker stays in its scope and never merges or rebases a PR branch (KB-ASK-24). Branch protection on the base is the backstop for the second and the last.
- The plan reviewer can become a rubber stamp or a source of noise. Its findings are counted by outcome: if plans that passed it grow past their estimates as often as plans did before it, it is removed.
