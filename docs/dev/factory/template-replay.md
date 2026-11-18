# Template replay: eval evidence for the factory templates

Sheepdog's rule is that prompt changes need eval evidence ([eval index](../eval/README.md)). The factory's role templates (`factory/templates/assistant.md`, `worker.md`, `reviewer.md`, and the built-in worker and reviewer defaults in `factory/mods/tasks/bin/herdr_task/templates.py`) are not routed by the [orchestrator](../glossary.md), so `make eval` does not cover them. Design section 7 asks for a small replay set instead: about ten scripted situations from the field notes, run against the suite's `fake_cli`. This page records that set and its results before and after SDF-11's trim.

## How it works

`factory/test/test_replay.py` with the situations in `factory/test/replay/situations.json`.

- **Prompts come from the engine's own code**, on a clean install with an empty templates folder. The worker's prompt comes from `launch.brief`, the reviewer's from `instructions("reviewer")`, and the Assistant's from the bundled `assistant.md` plus its settings block, the way `project.py` starts it. The `--files` arm instead installs the bundled `worker.md` and `reviewer.md` as the owner's templates.
- **The fake CLI runs as a real process** (`factory/test/fake_cli`, `FAKE_MODE=replay`) with the prompt plus the situation's message. It splits the prompt into rules (a bullet, a numbered step or a paragraph) and takes the first rule that holds every word of the situation's first matching cue set. It acts on that rule alone and logs the rule and the commands the rule names.
- **A situation passes** when that rule names the expected command (`run`), holds the expected words (`says`) and names none of the forbidden commands (`never`).
- **Two checks run on each whole prompt.** First, every `herdr-task …` command it names, in backticks or a code block, must be accepted by the engine's real CLI (`herdr-task <command> --help`, with each flag looked up there). Second, the whole built prompt (including the Settings block) must not mention a Shepherdr-only surface (window, web app, chat UI, browser view, upstream feedback).
- **A mutation test proves the replay can fail.** It removes the Assistant's "Never approve a delete" sentence from a copy of the template and checks that A3, and only A3, fails.

**No model is in the loop.** The replay proves what each prompt holds and that what it names exists in the engine. It does not prove how a model behaves on these prompts.

Run it: `cd factory && python3 test/test_replay.py --score` (add `--files` for the second arm). The test class runs in `make test-factory`.

For the structured review-round wording, both the built-in and bundled-template arms passed all 14 situations on 2026-11-17. This checks the existing situations and command names; it does not measure whether a model reports the new count correctly.

## Situations

| ID | Field notes | Role | Situation |
|---|---|---|---|
| W1 | 31, 51 | worker | A worker needs the owner's choice between designs |
| W2 | 27, 42 | worker | A worker's change passes its size limit |
| W3 | 45, 46 | worker | A worker's pull request falls behind main with a conflict |
| W4 | 26 (FAC-110) | worker | A worker's iPhone check fails because the phone is locked |
| W5 | SDF-55 | worker | A worker starts without quota information |
| R1 | #840 | reviewer | A read-only reviewer meets a check it can't run |
| R2 | 32 | reviewer | A reviewer is tempted to raise a finding the worker already rejected |
| A1 | 36 | assistant | A release fails with HTTP 403 from App Store Connect |
| A2 | 46, 49 | assistant | A task runs out of review rounds after merging main in |
| A3 | 12, 13 | assistant | A worker's permission prompt asks to run rm inside its worktree |
| A4 | 28 | assistant | A task's CI keeps failing on flaky tests |
| A5 | 30, 53 | assistant | An Autopilot wake arrives |
| A6 | 47, 48, 50 | assistant | A task is Ready with its rounds passed and CI green |
| A7 | 17, 18, 27 | assistant | The owner gives a goal with several parts (SDF-31) |
| A8 | 27, 42 | assistant | A recap says a task is two jobs: split it (SDF-31) |

## Before: the templates on main at 4f22cede

Clean install (built-in worker and reviewer defaults):

| Situation | Notes | Role | Result |
|---|---|---|---|
| W1: A worker needs the owner's choice between designs | 31, 51 | worker | FAIL: doesn't run herdr-task ask; rule lacks "--option" |
| W2: A worker's change passes its size limit | 27, 42 | worker | FAIL: doesn't run herdr-task ask; rule lacks "end your turn" |
| W3: A worker's pull request falls behind main with a conflict | 45, 46 | worker | pass |
| W4: A worker's iPhone check fails because the phone is locked | 26 (FAC-110) | worker | FAIL: doesn't run herdr-task device-wait |
| R1: A read-only reviewer meets a check it can't run | #840 | reviewer | FAIL: names herdr-app check; names shepherdr doctor |
| R2: A reviewer is tempted to raise a finding the worker already rejected | 32 | reviewer | FAIL: no rule covers it |
| A1: A release fails with HTTP 403 from App Store Connect | 36 | assistant | pass |
| A2: A task runs out of review rounds after merging main in | 46, 49 | assistant | pass |
| A3: A worker's permission prompt asks to run rm inside its worktree | 12, 13 | assistant | pass |
| A4: A task's CI keeps failing on flaky tests | 28 | assistant | pass |
| A5: An Autopilot wake arrives | 30, 53 | assistant | pass |
| A6: A task is Ready with its rounds passed and CI green | 47, 48, 50 | assistant | FAIL: rule lacks "Auto-accept" |

Passed 6 of 12.

| Role | Commands the engine refuses | Shepherdr-only words |
|---|---|---|
| worker | `herdr-task feedback` | `Tasks panel`, `<hc suggest`, `herdr-task feedback` |
| reviewer | none | `herdr-app` |
| assistant | `herdr-task config --max-running`, `herdr-task feedback`, `herdr-task home`, `herdr-task resume --space`, `herdr-task start --space` | `herdr-app`, `Tasks panel`, `<hc suggest`, `herdr-task feedback`, `herdr-task home`, `the switch in your header`, `Show things in the chat`, `Usage insights`, `--space` |

With the bundled `worker.md` and `reviewer.md` installed:

| Situation | Notes | Role | Result |
|---|---|---|---|
| W1: A worker needs the owner's choice between designs | 31, 51 | worker | pass |
| W2: A worker's change passes its size limit | 27, 42 | worker | pass |
| W3: A worker's pull request falls behind main with a conflict | 45, 46 | worker | pass |
| W4: A worker's iPhone check fails because the phone is locked | 26 (FAC-110) | worker | pass |
| R1: A read-only reviewer meets a check it can't run | #840 | reviewer | FAIL: names herdr-app check; names shepherdr doctor |
| R2: A reviewer is tempted to raise a finding the worker already rejected | 32 | reviewer | pass |
| A1: A release fails with HTTP 403 from App Store Connect | 36 | assistant | pass |
| A2: A task runs out of review rounds after merging main in | 46, 49 | assistant | pass |
| A3: A worker's permission prompt asks to run rm inside its worktree | 12, 13 | assistant | pass |
| A4: A task's CI keeps failing on flaky tests | 28 | assistant | pass |
| A5: An Autopilot wake arrives | 30, 53 | assistant | pass |
| A6: A task is Ready with its rounds passed and CI green | 47, 48, 50 | assistant | FAIL: rule lacks "Auto-accept" |

Passed 10 of 12.

| Role | Commands the engine refuses | Shepherdr-only words |
|---|---|---|
| worker | `herdr-task feedback` | `herdr-app`, `herdr-browser`, `Tasks panel`, `<hc suggest`, `herdr-task feedback`, `Show it in the chat` |
| reviewer | none | `herdr-app` |
| assistant | `herdr-task config --max-running`, `herdr-task feedback`, `herdr-task home`, `herdr-task resume --space`, `herdr-task start --space` | `herdr-app`, `Tasks panel`, `<hc suggest`, `herdr-task feedback`, `herdr-task home`, `the switch in your header`, `Show things in the chat`, `Usage insights`, `--space` |

Findings before the trim:

- On a clean install, workers get the built-in default in `templates.py`. That default lacks the `herdr-task ask` rule (W1), the size-overrun rule (W2) and `device-wait` (W4), which only the bundled `worker.md` has. The built-in reviewer default lacks the evidence rules (R2).
- Accept is "never accept", with no rule on the Auto-accept policy the engine applies (A6).
- The prompts name commands the engine no longer has: `herdr-task feedback`, `herdr-task home`, `start/resume --space` and `config --max-running`.

## The trim (SDF-11)

The trim follows design section 7 and the SDF-11 rows of the [engine audit](engine-audit.md) (sections 4.1 to 4.3).

- **Assistant** (219 to 180 lines). The identity is now "you coordinate one project; the owner doesn't read this pane; you reach them through `herdr-task ask`, `herdr-task alert` and the digest". Accept is now a policy kept in code: the task's Auto-accept line, otherwise the person, or a delegated `--owner-approved`. Autopilot is how the Assistant works, with no header switch. Dropped: Feedback for Shepherdr, Usage insights, Home space, suggested next message, showing media in chat, the Tasks panel, `herdr-app ls`, space and tab labels, and `--space`. `config --max-running` became `config --set tasks.max_running=N`.
- **Worker** (34 to 24 lines). Dropped: the two herdr-app mod rules, artifact preview, chat media, the Browser view, Shepherdr feedback and suggestions. Its reviewer's labels are now `sheepdog.task=$SHEEPDOG_TASK` and `sheepdog.role=reviewer`. Proof and secrets no longer name the artifact viewer or the masked box. Proof is now files committed on the branch, which is what SDF-10's engine checks, and `herdr-task ready --help` says where it looks.
- **Reviewer** (16 to 15 lines). Dropped the Shepherdr feedback line. The sandbox rule keeps "Not run in my sandbox:" without the `herdr-app check` and `shepherdr doctor` examples.
- **Built-in defaults.** `WORKER_MD` and `REVIEWER_MD` in `templates.py` are now the bundled files word for word, and `test_templates.py` keeps them equal. A clean install therefore gets the ask, size-overrun, device-wait and evidence rules. An unedited earlier default is upgraded to today's; an edited one is left alone.
- **Recap rail.** `herdr-task recap --decide` refuses a reviewer or the task's own agent by either label prefix (`budget.py`). It used to read only `herdr.*`, so a reviewer labelled `sheepdog.role=reviewer` by the new worker template would have passed it.

## After

Clean install:

| Situation | Notes | Role | Result |
|---|---|---|---|
| W1: A worker needs the owner's choice between designs | 31, 51 | worker | pass |
| W2: A worker's change passes its size limit | 27, 42 | worker | pass |
| W3: A worker's pull request falls behind main with a conflict | 45, 46 | worker | pass |
| W4: A worker's iPhone check fails because the phone is locked | 26 (FAC-110) | worker | pass |
| R1: A read-only reviewer meets a check it can't run | #840 | reviewer | pass |
| R2: A reviewer is tempted to raise a finding the worker already rejected | 32 | reviewer | pass |
| A1: A release fails with HTTP 403 from App Store Connect | 36 | assistant | pass |
| A2: A task runs out of review rounds after merging main in | 46, 49 | assistant | pass |
| A3: A worker's permission prompt asks to run rm inside its worktree | 12, 13 | assistant | pass |
| A4: A task's CI keeps failing on flaky tests | 28 | assistant | pass |
| A5: An Autopilot wake arrives | 30, 53 | assistant | pass |
| A6: A task is Ready with its rounds passed and CI green | 47, 48, 50 | assistant | pass |

Passed 12 of 12.

| Role | Commands the engine refuses | Shepherdr-only words |
|---|---|---|
| worker | none | none |
| reviewer | none | none |
| assistant | none | none |

With the bundled files installed: Passed 12 of 12, with no refused commands and no Shepherdr-only words.

## Follow-up (SDF-42): full-prompt check including Settings block

`test_replay.py` now checks the whole built prompt (`launch.brief` / `assistant.md` plus `settings_block` / `assistant_settings.block`) rather than only the template body. Before SDF-42, `worker` and `assistant` still had `Tasks panel` in their Merge line (`settings.MERGE_WORDS` and `assistant_settings.MERGE_SHORT`); after SDF-42 they say "by voice or on the phone's ask card":

| Arm | Situation score | Worker (`GONE` in full prompt) | Reviewer (`GONE` in full prompt) | Assistant (`GONE` in full prompt) |
|---|---|---|---|---|
| Before SDF-42 (clean install & `--files`) | 12 of 12 | `Tasks panel` | none | `Tasks panel` |
| After SDF-42 (clean install & `--files`) | 12 of 12 | none | none | none |

## Follow-up (SDF-31): the coordinator plans milestones

SDF-31 rewrote the Assistant's Plan section: a goal becomes milestones in a plan file applied with `herdr-task plan apply`, and a replan or split is `herdr-task plan amend --reason` ([milestones.md](milestones.md)). Two situations were added. A7: a goal with several parts must lead to `plan apply`, with the owner's words kept as said, `ui: true` and a release. A8: a recap that says split must lead to `plan amend` with `--reason` and `split_of`, and must never name `herdr-task rm`. `plan` joined the replay's nested commands, so `plan apply` and `plan amend` and their flags are checked against the real CLI.

| Arm | Before (assistant.md at 8daf81dc) | After |
|---|---|---|
| Clean install | 12 of 14: A7 and A8 fail, "no rule covers it" | 14 of 14 |
| With the bundled files (`--files`) | 12 of 14: A7 and A8 fail, "no rule covers it" | 14 of 14 |

A1 to A6 and the worker and reviewer situations still pass in both arms. No refused commands and no Shepherdr-only words in any role.

## Follow-up (SDF-55): workers leave quota checks to the engine

W5 checks that a worker brief tells the worker to leave quota checks to the
engine. Every situation rejects a replay action that names CodexBar, and the
full-prompt check rejects it in role prompts. A negative test adds a quota
command to W5's rule and verifies that the replay catches it.

| Arm | Pinned Python 3.14.8 score | Refused commands or forbidden words |
|---|---|---|
| Clean install | 15 of 15 | None |
| With the bundled files (`--files`) | 15 of 15 | None |

## Not covered

- **Model behaviour.** No model ran on these prompts. A live replay with a real CLI is still to do.
- **Existing template files.** The engine still reads `~/.config/herdr-core/templates`. Where Shepherdr already put `worker.md`, `reviewer.md` or `assistant.md` there, as on the owner's Mac, those files win, so the trim reaches clean installs only. The upgrade matches only the engine's own earlier built-in defaults, never Shepherdr's installed files: live Shepherdr agents read those. The coordinator start ([coordinator.md](coordinator.md)) and `herdr-task machines sync` read that folder too.

## The retro template (SDF-49)

`factory/templates/retro.md` is the prompt of the [retro round](review-rounds.md#the-retro-round). The keyword replay above cannot judge it, because its output is a verdict and a cause, not a command. Its replay uses two real non-converging histories from this project's task store, each built at round 3, where the retro trigger fires (rounds 2 and 3 both had majors):

| Situation | Task | At | What the store shows |
|---|---|---|---|
| R-16 | SDF-16, coordinator lease and restart | `6e965f3a72`, 1,673 changed lines against a 600-line estimate | Every round finds new cut-short states of one handoff mechanism; earlier fixes hold |
| R-31 | SDF-31, milestones and plan amend | `830bd86886`, 942 changed lines, no estimate | Every round finds one more dependency-loop case; earlier fixes hold |

The fixtures are `factory/test/replay/retro/<task>.json`: each round's verdict, major count and note, the brief with private paths removed, and the diff range. `python3 test/replay_retro.py --show` prints each filled prompt. `--live --dir <scratch>` runs one read-only Fable session per situation in a detached worktree at that round's head, and records its answer as `<task>.out.txt`. The test suite checks that every slot is filled and that each recorded answer parses. The expected causes are not in the prompt. They come from the retro proposal, which judged both tasks after the fact: R-16 should name task-too-big or design-needed-first, and R-31 patch-shaped-fix or reviewer-narrow.
