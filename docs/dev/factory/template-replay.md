# Template replay: eval evidence for the factory templates

Sheepdog's rule is that prompt changes need eval evidence ([eval index](../eval/README.md)). The factory's role templates (`factory/templates/assistant.md`, `worker.md`, `reviewer.md`, and the built-in worker and reviewer defaults in `factory/mods/tasks/bin/herdr_task/templates.py`) are not routed by the [orchestrator](../glossary.md), so `make eval` does not cover them. Design section 7 asks for a small replay set instead: about ten scripted situations from the field notes, run against the suite's `fake_cli`. This page records that set and its results before and after SDF-11's trim.

## How it works

`factory/test/test_replay.py` with the situations in `factory/test/replay/situations.json`.

- **Prompts come from the engine's own code**, on a clean install with an empty templates folder. The worker's prompt comes from `launch.brief`, the reviewer's from `instructions("reviewer")`, and the Assistant's from the bundled `assistant.md` plus its settings block, the way `project.py` starts it. The `--files` arm instead installs the bundled `worker.md` and `reviewer.md` as the owner's templates.
- **The fake CLI runs as a real process** (`factory/test/fake_cli`, `FAKE_MODE=replay`) with the prompt plus the situation's message. It splits the prompt into rules (a bullet, a numbered step or a paragraph) and takes the first rule that holds every word of the situation's first matching cue set. It acts on that rule alone and logs the rule and the commands the rule names.
- **A situation passes** when that rule names the expected command (`run`), holds the expected words (`says`) and names none of the forbidden commands (`never`).
- **Two checks run on each whole prompt.** First, every `herdr-task …` command it names, in backticks or a code block, must be accepted by the engine's real CLI (`herdr-task <command> --help`, with each flag looked up there). Second, the template's own words must not mention a Shepherdr-only surface (window, web app, chat UI, browser view, upstream feedback).
- **A mutation test proves the replay can fail.** It removes the Assistant's "Never approve a delete" sentence from a copy of the template and checks that A3, and only A3, fails.

**No model is in the loop.** The replay proves what each prompt holds and that what it names exists in the engine. It does not prove how a model behaves on these prompts.

Run it: `cd factory && python3 test/test_replay.py --score` (add `--files` for the second arm). The test class runs in `make test-factory`.

## Situations

| ID | Field notes | Role | Situation |
|---|---|---|---|
| W1 | 31, 51 | worker | A worker needs the owner's choice between designs |
| W2 | 27, 42 | worker | A worker's change passes its size limit |
| W3 | 45, 46 | worker | A worker's pull request falls behind main with a conflict |
| W4 | 26 (FAC-110) | worker | A worker's iPhone check fails because the phone is locked |
| R1 | #840 | reviewer | A read-only reviewer meets a check it can't run |
| R2 | 32 | reviewer | A reviewer is tempted to raise a finding the worker already rejected |
| A1 | 36 | assistant | A release fails with HTTP 403 from App Store Connect |
| A2 | 46, 49 | assistant | A task runs out of review rounds after merging main in |
| A3 | 12, 13 | assistant | A worker's permission prompt asks to run rm inside its worktree |
| A4 | 28 | assistant | A task's CI keeps failing on flaky tests |
| A5 | 30, 53 | assistant | An Autopilot wake arrives |
| A6 | 47, 48, 50 | assistant | A task is Ready with its rounds passed and CI green |

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
