# Gate evidence contract

What the merge gate (docs/factory.md section 6) reads from a task's store entry `t`, and what its producers
(herdr-task, the checks runner, the verifier) must write. G4 consumes it; G2, G3 and G5 produce it. The validator is
`test/contract_gate_evidence.py`; each producer's test runs it on the records its own code really writes.
Unknown extra keys are rejected. `review_runs[].model` is a non-empty string, or null when the CLI's default model ran. `sha`/`head` are 40-hex SHAs. Times (`at`, `started_at`, `finished_at`) are ms ints.

## `t["review_runs"][]`: a reviewer herdr-task started
```json
{"round": 1, "head": "0123456789abcdef0123456789abcdef01234567", "address": "term-7", "cli": "agy",
 "model": "gemini-4-argon-high", "at": 1762600000000}
```

## `t["reviews"][]`: a review round
```json
{"round": 1, "head": "0123456789abcdef0123456789abcdef01234567", "by": "term-7", "started_by": "herdr-task",
 "verdict": "pass", "must_fix": 0, "report": "No findings.", "at": 1762600600000}
```
`verdict` is `pass` or `changes`. `must_fix` is the number of findings at or above the task's must-fix level.
`report` is at most 2000 chars; `must_fix` is 0 on a pass. This shape is only for rounds herdr-task recorded
(`started_by` is "herdr-task"). A round a worker recorded itself (`herdr-task reviewed`, `started_by` absent, its
existing shape) is not covered here and never counts for the gate.

## `t["checks"][]`: a check run by herdr-task
```json
{"sha": "0123456789abcdef0123456789abcdef01234567", "command": "python3 -m unittest discover -s test", "exit": 0,
 "output_tail": "OK", "started_at": 1762600700000, "finished_at": 1762600720000, "by": "term-9",
 "ci": {"state": "success", "url": "https://github.com/o/r/actions/runs/1"}}
```
`output_tail` is at most 4000 chars. `ci` is null, or `state` is `success`, `failure`, `pending` or `none` and `url` is a string or null.

## `t["verdicts"][]`: a verifier verdict (used by G5)
```json
{"sha": "0123456789abcdef0123456789abcdef01234567", "by": "term-11", "verdict": "pass",
 "commands": ["bin/shepherdr --version"], "output_tail": "2.0.1", "at": 1762600800000}
```
`verdict` is `pass` or `fail`; `output_tail` is at most 4000 chars.

## Counting rule (G4)
For the SHA being merged and `review.min` > 0:
- A review round counts only if `started_by` is "herdr-task", a `review_runs` entry has the same `round`, `head`
  and `address` (= its `by`), no entry serves two rounds, and its `head` is the SHA being merged.
- At least `review.min` rounds count, and the last counting round has verdict `pass` and `must_fix` 0.
- The latest `checks` record for that SHA has `exit` 0, and its `ci.state` is `success` where the repo has CI.
- Folders without `review.min` > 0 skip the gate.
