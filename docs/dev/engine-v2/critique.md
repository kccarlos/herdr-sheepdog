# Engine v2: critique of the old engine

2026-11-18. What is structurally wrong with the inherited engine, with evidence, and what the new engine must not carry over. The behaviours worth keeping are in [knowledge-base.md](knowledge-base.md); the new design is in [design.md](design.md).

**The old engine:** the Python package `factory/mods/tasks/bin/herdr_task/` in the sheepdog repo at `4df3a038`: 44 modules, 21,460 lines (`wc -l`), plus 19,692 lines of tests in `factory/test/`.

**How this was checked.** An Opus subagent read the code and counted with grep; its full notes, with a line reference for every claim, are in [notes/critique-notes.md](notes/critique-notes.md). The lead re-opened the lines marked ✔ below. Everything else is the subagent's reading and is **not re-checked by the lead**. Paths are relative to `herdr_task/`. Counts are lower bounds from grep.

## 1. The verdict in one paragraph

The old engine has no state machine. It has one stored `state` word per task with 6 values, about 109 side fields that carry the real state, and a table (`stage.py`) that guesses a status from those fields at read time. Every command, the tick, and the GitHub code write those fields directly, each with its own idea of which fields belong together. Launch intent is stored as a finished command line in three places and re-derived by four functions with four different rules. Recent engine tasks (SDF-54, 73, 74) tried to patch single cases of this and grew 10 to 20 times past their size, because each patch has to reach every place that holds a copy. The fault is in the shape, so the new engine takes ideas only.

## 2. Structural faults, most costly first

### F1. No single owner of a task's state

| Evidence | Where |
|---|---|
| `machine.py` is not a state machine. It samples CPU, memory and disk and reads the CLIs' plan limits | `machine.py:1-9` ✔ |
| The stored `state` field has 6 values (todo, scheduled, queued, running, ready, done), written at 15 sites in 4 modules | `commands.py` (11 sites), `tick.py:306`, `:328`, `automations.py:387`, `github.py:1348` |
| The real state is about 109 side fields with 304 direct writes across 17 modules | grep count, notes "Symptom 6" |
| The status the owner sees is derived at read time: 8 status words plus a 9-row stage table, first match wins | `agents.py:162`, `stage.py:346-356` ✔ |
| Sub-states with their own `state` words live inside the task: `backup.state`, `after_ext[].state`, CI mirrors | `takeover.py:215-224`, `queues.py:89`, `proof.py:290-292` |

**Missing abstraction:** one place that decides what a task needs next. (The new design goes one step further than a state field: it stores no phase and reads the task's situation from its records each time; see design.md section 0 for why.)

**Seen in the field.** A task that was out of review rounds showed "Working", and its red check never surfaced, because the stage row order hid it (retro doc §0). SDF-16 still read "Needs you" after it was finished outside the engine.

### F2. Launch intent is stored as argv, in three places, and re-derived four ways

| Evidence | Where |
|---|---|
| Three homes: the task's fields, the frozen `t["queue"]["argv"]`, and each agent record's own argv. A fourth copy sits in a terminal label for revive | `commands.py:466-479`, `revive.py:172` |
| Four re-derivations with different guards: edit, refresh of worker plans (stricter), retarget, and "resolve again" (decided by timestamps) | `commands.py:1825-1827`, `:1877-1881`, `:2111-2113`, `:1999-2005` |
| `plan amend` changes a task's settings and never refreshes the queued argv | `plan.py:369-370` |
| Because argv is the stored form, code parses argv back into intent: per-CLI tables of which flags take a value | `revive.py:41-48`, `settings.py:555` |
| About 24 sites build or rewrite an agent argv | [notes/adapters-notes.md](notes/adapters-notes.md) §2a, §9a |
| "Which model runs" has about 8 sources, ending in a silent fallback to the first installed CLI | `settings.py:360-375`, `:521-552`, `:24-26` |

**Missing abstraction:** store the request (what the task asks for), resolve it to an intent once, at launch, and build argv in one function. Keep the argv only as a record of what ran.

**Seen in the field.** 6 of 11 tasks ran on the wrong model because an argv left the model out and Codex took it from the owner's `~/.codex/config.toml` (SDF-53/54). The SDF-54 outside review found the "resolution table" was decorative: reversing its rows kept every test green, because the real logic was hand branches (`sdf54-split.md`).

### F3. Eleven spawn paths, four spawn mechanisms

| Path | How it spawns | Where |
|---|---|---|
| Worker, reviewer, fix round, retro, designer | `launch()`, branching inside on role and CLI | `launch.py:770-830` |
| Revive after a crash | types a shell line into the terminal with `hc send` | `revive.py:358` ✔ |
| Coordinator | a one-off template file per start | `lease.py:235-252` |
| Ask pane, check runner | `hc run` directly | `ask.py:136`, `checks.py:97` |
| Takeover by another CLI | uses that CLI's default model, ignoring the task's choice | `takeover.py:140-146` |

The two resume builders disagree: one restates the permission mode on a Claude resume, the other does not (`launch.py:327`, `revive.py:115`).

**Missing abstraction:** one "start a run" operation that takes an intent. Resume, takeover and a fresh start with a handoff are the same call with a different "continue from" value.

### F4. The size cap cannot fire on most tasks

| Evidence | Where |
|---|---|
| The estimate is read from the brief's prose by a regex that needs a number directly before the word "lines". "About 150 Python lines" does not match; "250 code + 300 tests" does not match | `budget.py:21` ✔ (regex run by the lead) |
| No estimate means the check returns nothing, silently | `budget.py:437` ✔ |
| The size check runs only when the worker says ready. The tick's budget pass never looks at size | `commands.py:535` ✔, `budget.py:398-427` |
| After one `continue` decision the check is off for that task | `budget.py:438-439` ✔ |
| SDF-54, 73 and 74 had no parsed estimate | task records, notes "Symptom 4" |

**Missing abstraction:** the size estimate is a required number on the task, checked by the plan before the task exists, and the cap is tested on an event every task passes.

### F5. A reset is a hand-kept list of fields to forget

Reopen clears 25 named fields; begin clears 7; they are different lists (`commands.py:1771` ✔, `:416-417`). A field someone adds later and forgets to list survives the reset. That is the SDF-67/74 bug (review rounds and the extra-round grant surviving a re-plan).

**Missing abstraction:** progress lives in an attempt record, as records tied to a commit. A reset starts a new attempt; nothing has to be listed.

### F6. Control flow keyed on free text

`attention` sentences are matched by prefix to decide what happens next (`commands.py:1819`, `:1828`, `:1883`, `:2039`, `:2114`). Recap reasons are matched by the substring "twice its size" (`budget.py:438` ✔). The non-convergence sentence is used as a key (`budget.py:381`, `:391`). Verdicts arrive by three protocols: a CLI verb, a code-fenced block scraped from the screen, and a `<retro>` JSON tail (`commands.py:911-928`, `retro.py:1-4`).

**Missing abstraction:** typed reasons and one structured report channel.

### F7. Hand-kept lists

Model-family regexes (`settings.py:398`, `:743`), hard-coded model names (`settings.py:1144`, `commands.py:682`, `project.py:11-12`), per-CLI flag tables (`revive.py:41-48`), lists of generated file names and doc suffixes (`budget.py:69-104` ✔), and 47 inline tuples of state words. Each is a second source of truth that goes stale when a CLI or a model changes.

### F8. Polling everywhere, and facts cached in several homes

Pull requests are re-read every 60 s, or 20 s while checks run (`base.py:102`, `tick.py:49`). 14 sites in 9 modules read a terminal's screen. Agent liveness has four homes: the task's agent list, hc's live list, revive records in labels, and the coordinator lease. A stored `start` commit gave a stale diff base after a rebase (trial log). Work done outside the engine left the store stale (retro doc).

**Missing abstraction:** read facts from the system that owns them (GitHub, git, hc) through one observer, and store only what nothing else knows.

### F9. Errors swallowed

About 319 `except` clauses, 98 of them going straight to `pass`, `continue` or `return`. Each tick pass is wrapped in `except Exception` that only adds a line to a report (`tick.py:375-378`). A failure is therefore not an event anyone must handle.

### F10. Many doors into the store

48 places open the store; some read the JSON file without the lock (`commands.py:1236`). More than 20 side files sit beside it, each with its own lock or none: news, held messages, retries, watchdog state, autopilot state, a copy of the settings last told to the coordinator ([notes/critique-notes.md](notes/critique-notes.md) table (c)). 122 imports are done inside functions to get around import cycles.

### F11. The loop-breaker was blind and could be bypassed

From the retro doc (2026-11-17, read by the lead): the non-convergence triggers counted only rounds the engine started, and the default mode was worker-run review, so they never fired. `review-more` had no limit. A round record was one free-text line. The "5-round cap" on SDF-16 and SDF-31 was 2 rounds plus 3 the coordinator added one at a time.

### F12. Inherited weight

407 ticket references in comments, 15 "as core/*.js" mirrors of the old TypeScript app, `space` parameters still threaded through, and an environment variable for a browser view that no longer exists set on every launch (`launch.py:804-806`).

## 3. The owner's reported symptoms, checked

| Symptom | Result | Fault |
|---|---|---|
| State changed in one place and not others | Confirmed | F1, F5, F8 |
| Launch plan stored twice, re-derived in four places | Confirmed, and it is three homes | F2 |
| About 8 launch paths | Confirmed: 11 paths, 4 mechanisms | F3 |
| Tasks growing 10 to 20 times; size recap never firing | Confirmed, three separate causes | F4 |
| Hand-kept lists | Confirmed | F7 |
| "`machine.py` is the state machine" | The premise is wrong: there is none | F1 |

## 4. What not to carry over

| Do not carry | Because | The new engine does instead |
|---|---|---|
| A state word plus side fields plus a derived status | F1 | No stored phase at all. Records of what happened, and one ordered rule table that gives both the next action and what the owner sees |
| argv as stored intent; any re-parsing of argv | F2 | Store the request; resolve and build argv at launch |
| More than one spawn path | F3 | One way to start a run, from an intent |
| Estimates and limits read from prose | F4 | Required typed fields, checked by the plan |
| Reset-by-list | F5 | Attempts; records tied to a commit stop counting when the commit changes |
| Sentences as keys; verdicts scraped from screens | F6 | Typed reasons; one report channel |
| Model, flag, file-type and state-word lists | F7 | A probe, the platform's own marking, or a table row per CLI |
| Per-feature polling and side files | F8, F10 | One observer, one store, one writer |
| `except: pass` | F9 | Every executor result, including failure and timeout, is a record; repeated failure lands the task on a row that waits on the coordinator |
| Worker-run review | F11 | The engine starts every review and records every round |
| A separate Python program and its CLI contract | process boundary cost: a 2 s poll that spawns Python, a loopback proof round-trip, a managed interpreter | The engine is a Go package inside the daemon |

Whole modules with no place in the first versions: `machines` (983 lines, multi-machine sync), `link`, `devices`, `simulators`, `automations` and `autopilot` as a second scheduler with its own time parser, `takeover`, `revive`, `assistant_settings`, `cli`, `output`. Simulators and devices are used by iOS work, so they are a later addition and not proven dead.

## 5. Ideas worth keeping (as ideas)

| Idea | Old home | Why it is right |
|---|---|---|
| A stage table: one ordered table says what a task waits for | `stage.py` | The right instinct, applied only to reading, while the actions went through other code. The new engine makes this one table drive both |
| The escalation ladder: rounds, one extra round, retro, recap, owner | `budget.py`, `retro.py`, review-rounds.md | Measured against real tasks |
| A lease for the coordinator, with start limits | `lease.py` | "Exactly one coordinator" needs an owner in code |
| Proof of the owner's answer | Go `proof.go` | The rail that stops an agent approving its own work |
| Plan apply and amend with checks | `plan.py` | The graph is validated as a whole |
| "Believe the system": re-read the PR after a merge call | `github.py` | Twice a PR was reported merged and was not |
| Delta review, merge-only commits exempt from rounds | `budget.py` | Review loops were the largest controllable cost |
| A timed probe of the project volume before starting agents | Go `doctor.go` | Two overnight stalls on macOS consent prompts |

## 6. Limits of this critique

- Counts come from grep and are lower bounds.
- The subagent did not open SDF-53's record or every module; the module table in the notes marks which "real job" lines come from the earlier audit.
- Whether any live project uses machines, devices, simulators or automations was not checked.
- The live task store is under `~/.local/share/shepherdr/tasks`, not the path the code comments name; it was not inspected beyond `herdr-task show`.
