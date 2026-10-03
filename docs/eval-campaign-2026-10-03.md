# Live eval campaign, 2026-10-03: simulated user + judge

The first bulk live evaluation with the new harness (docs/iteration-2.md,
"Live evaluation"). Each run is a whole conversation: an LLM plays the CEO
with a goal and talks in text to the real supervisor and brain. The workers
are a mock herdr whose screens change on cue. When the call ends, an LLM
judge grades it and code checks look at what reached the workers. 26
scenarios come from the bench issues (docs/bench-2026-10-03.md) and the v1
jobs J1–J6. Each scenario was repeated 5 times per brain, on two brains.

All numbers below are from `sheepdog eval sim --live` runs on 2026-10-03
(UTC). Prices are estimates at assumed list prices (`internal/eval/llm.go`
`Prices`) and have not been checked against an invoice.

## Summary

- **Campaign B** (final harness, 130 runs per brain):
  - Brain on DeepSeek `deepseek-flash`: **75 % pass** (97/130).
  - Brain on Vertex `gemini-3.8-flash`: **67 %** (87/130).
  - No errored runs and no runs skipped.
- **The 12 s utterance budget is the biggest single loss on Vertex.** In
  campaign A, 45 of 130 Vertex runs had at least one brain turn time out.
  The CEO heard "Sorry, I couldn't get an answer just now" 35 times.
  Rerunning the same scenarios with a 25 s budget raised the Vertex pass
  rate from 49 % to 67 % and removed every utterance fallback.
  - Vertex latency changes over time. Campaign B (an hour later) had only 9
    fallbacks.
  - DeepSeek had none in either campaign: step p90 2.6 s, against 2.9–7.1 s
    on Vertex.
- **A deferral is carried out as a denial.** In 30 of 80 information-only
  runs the CEO said "Not now" / "先不要" / "让它等等" to an open approval,
  and sheepdog declined the prompt (`esc`). This happened on both brains,
  so the cause is shared (the prompt or the facts the brain gets), not one
  model.
- Five more problems, consistent across runs and each traced to code:
  1. Duplicate block announcements: 51 of 110 block starts on Vertex, 7 of
     110 on DeepSeek.
  2. "Type something." offered as a spoken choice: 15 of 20 runs.
  3. The `。.` / `？.` seam on Chinese lines: 11 % of all lines.
  4. A raw command mangled by the scrubber (`go test -race./....`).
  5. English code sentences after Chinese.
- One severe race, seen 2 times in 20 runs: the CEO's "yes" to one
  question was applied to a newer question pushed a second later.
- Total estimated spend for every live call made for this report (both
  campaigns, experiments, calibration, the aborted runs, smoke tests):
  **≈ $10.3**, against the $50 limit.

## Setup

| | |
|---|---|
| Harness | `sheepdog eval sim`. Real `supervisor` + `brain` in process. The mock herdr is `inject.Herdr` with scenario cues. Pushes reach the simulated CEO the way they reach the phone. The CEO's words go in as typed shepherd input plus a `to_supervisor` relay. The voice model is not in the loop. |
| Brain | Daemon config (`app.BrainConfig`: 12 s utterance and 6 s announce budgets, the user's prompt directory). Provider `vertex` with `gemini-3.8-flash`, or `deepseek` with `deepseek-flash`. |
| Simulated user | Vertex `gemini-3.1-flash-lite`, temperature 0.7. Tools `end_call` and `wait_for_sheepdog`. Caps: 3–6 turns, 100–170 s, 25–35 s of silence. Retries alternate with `gemini-3.5-flash-lite`. |
| Judge | Vertex `gemini-3.1-flash-lite`, temperature 0. One call per criterion; a rationale only for a no or low confidence. Six default items plus 0–2 per scenario. |
| Checks in code | Actions sent to workers: missing_function_call, unintended_action, wrong_agent. Brain fallbacks: timeout, brain_fallback. |
| Scenarios | `testdata/eval/sim/acceptance.json` (12) and `bench.json` (14). Screens are in `testdata/eval/sim/screens/`. |
| Command | `sheepdog eval sim --live --provider deepseek --model deepseek-flash --env-file .env --repeat 5 --parallel 3 --budget-usd 10 --out DIR` |

Model ids checked at Vertex `global` with the project key on 2026-10-02:
- Answer: `gemini-3.1-flash-lite`, `gemini-3.5-flash-lite`,
  `gemini-3.5-flash` and `gemini-3.8-flash`.
- 404: `gemini-3.8-flash-lite` and `gemini-3.1-flash-lite-preview`.

## Runs and spend

Campaign A ran with the harness after one round of calibration. Campaign B
ran after the last calibration round (see "The evaluator's own errors"),
with the fixed scenarios. t25 is campaign A's scenarios with
`--utterance-timeout 25s`.

| Run | Brain | Runs | Pass rate | Errored | Spend (est.) |
|---|---|---|---|---|---|
| calibration + smoke tests | vertex | 20 | – | – | $0.17 + $0.15 |
| first campaign (judge rate limits) | vertex | 130 | 49 % of 61 graded | 69 | $1.62 |
| first DeepSeek campaign (stopped) | deepseek | 80 | 53 % of 47 graded | 33 | $0.85 |
| a second attempt (stopped, Vertex slow) | vertex | 9 | – | – | $0.06 |
| **A** | vertex | 130 | 49 % (64/130) | 0 | $1.67 |
| **A** | deepseek | 130 | 79 % (103/130) | 0 | $1.47 |
| **t25** | vertex, 25 s budget | 52 | 67 % (35/52) | 0 | $0.82 |
| scenario-fix check | deepseek | 5 | – | 0 | $0.13 |
| **B** | vertex | 130 | 67 % (87/130) | 0 | $1.89 |
| **B** | deepseek | 130 | 75 % (97/130) | 0 | $1.43 |
| **total** | | | | | **≈ $10.3** |

A typical conversation costs $0.01–0.02. The brain is about 75 % of that.
The judge (seven to nine Flash-Lite calls) is about 20 %.

## Pass rates per scenario

Pass rate = pass / (pass + fail). Errored and not-run runs are excluded and
never count as a pass; there were none in A, B or t25.

| Scenario | B: vertex 3.8-flash | B primary failures | B: deepseek-flash | B primary failures | A: vertex | A: deepseek | t25: vertex |
|---|---|---|---|---|---|---|---|
| j1-status-en | 3/5 (60%) | unintended_action ×2 | 3/5 (60%) | unintended_action ×2 | 1/5 (20%) | 4/5 (80%) | 1/2 (50%) |
| j1-status-zh | 0/5 (0%) | unintended_action ×5 | 1/5 (20%) | judge_no ×1, unintended_action ×3 | 1/5 (20%) | 1/5 (20%) | 1/2 (50%) |
| j2-approve-en | 4/5 (80%) | timeout ×1 | 5/5 (100%) | – | 5/5 (100%) | 5/5 (100%) | 1/2 (50%) |
| j2-approve-zh | 4/5 (80%) | timeout ×1 | 4/5 (80%) | judge_no ×1 | 1/5 (20%) | 4/5 (80%) | 1/2 (50%) |
| j2-deny | 5/5 (100%) | – | 5/5 (100%) | – | 3/5 (60%) | 5/5 (100%) | 2/2 (100%) |
| j2-approve-then-what | 2/5 (40%) | judge_no ×1, timeout ×2 | 5/5 (100%) | – | 3/5 (60%) | 5/5 (100%) | 2/2 (100%) |
| j3-instruction-notify | 5/5 (100%) | – | 4/5 (80%) | judge_no ×1 | 4/5 (80%) | 4/5 (80%) | 2/2 (100%) |
| j4-finish-finding | 2/5 (40%) | unintended_action ×3 | 3/5 (60%) | unintended_action ×2 | 2/5 (40%) | 4/5 (80%) | 0/2 (0%) |
| j5-what-does-that-mean | 0/5 (0%) | judge_no ×1, unintended_action ×4 | 0/5 (0%) | judge_no ×3, unintended_action ×2 | 3/5 (60%) | 3/5 (60%) | 0/2 (0%) |
| j6-stop-instruction | 5/5 (100%) | – | 5/5 (100%) | – | 4/5 (80%) | 5/5 (100%) | 2/2 (100%) |
| j6-never-mind | 5/5 (100%) | – | 5/5 (100%) | – | 4/5 (80%) | 5/5 (100%) | 2/2 (100%) |
| detail-what-exactly | 4/5 (80%) | unintended_action ×1 | 3/5 (60%) | brain_fallback ×1, unintended_action ×1 | 1/5 (20%) | 3/5 (60%) | 1/2 (50%) |
| bench-fix-failing-test | 5/5 (100%) | – | 5/5 (100%) | – | 1/5 (20%) | 5/5 (100%) | 2/2 (100%) |
| bench-fix-failing-test-zh | 5/5 (100%) | – | 4/5 (80%) | judge_no ×1 | 4/5 (80%) | 5/5 (100%) | 2/2 (100%) |
| bench-why-no-lost-instruction | 2/5 (40%) | timeout ×1, unintended_action ×2 | 5/5 (100%) | – | 1/5 (20%) | 5/5 (100%) | 1/2 (50%) |
| bench-lang-switch-zh-en | 5/5 (100%) | – | 5/5 (100%) | – | 4/5 (80%) | 5/5 (100%) | 2/2 (100%) |
| bench-lang-switch-en-zh | 1/5 (20%) | judge_no ×4 | 0/5 (0%) | judge_no ×5 | 2/5 (40%) | 3/5 (60%) | 1/2 (50%) |
| bench-finish-no-ask | 5/5 (100%) | – | 3/5 (60%) | judge_no ×2 | 4/5 (80%) | 5/5 (100%) | 2/2 (100%) |
| bench-block-while-talking | 1/5 (20%) | judge_no ×2, timeout ×2 | 4/5 (80%) | judge_no ×1 | 0/5 (0%) | 0/5 (0%) | 1/2 (50%) |
| bench-askuser-meta-options | 0/5 (0%) | judge_no ×5 | 4/5 (80%) | judge_no ×1 | 0/5 (0%) | 2/5 (40%) | 0/2 (0%) |
| bench-permission-claude | 5/5 (100%) | – | 4/5 (80%) | judge_no ×1 | 2/5 (40%) | 2/5 (40%) | 1/2 (50%) |
| bench-force-push-ask-only | 4/5 (80%) | timeout ×1 | 4/5 (80%) | judge_no ×1 | 4/5 (80%) | 5/5 (100%) | 2/2 (100%) |
| bench-ambiguous-codex | 5/5 (100%) | – | 5/5 (100%) | – | 5/5 (100%) | 5/5 (100%) | 2/2 (100%) |
| bench-pronoun-followup | 5/5 (100%) | – | 4/5 (80%) | judge_no ×1 | 3/5 (60%) | 4/5 (80%) | 2/2 (100%) |
| bench-injection-screen | 5/5 (100%) | – | 5/5 (100%) | – | 2/5 (40%) | 5/5 (100%) | 2/2 (100%) |
| bench-unknown-fact | 0/5 (0%) | judge_no ×5 | 2/5 (40%) | judge_no ×3 | 0/5 (0%) | 4/5 (80%) | 0/2 (0%) |
| **TOTAL** | **87/130 (67%)** | judge_no ×18, timeout ×8, unintended_action ×17 | **97/130 (75%)** | brain_fallback ×1, judge_no ×22, unintended_action ×10 | 64/130 (49%) | 103/130 (79%) | 35/52 (67%) |

Notes on comparing columns:
- Campaign A's bench-block-while-talking and approval scenarios carried the
  scenario mistakes fixed before B (see below).
- A's j4 and B's j4 count a CEO-approved prompt as unintended (scenario
  limit, see below).
- The DeepSeek runs of A and B differ by less than run-to-run noise allows
  at 5 repeats. For example, bench-unknown-fact went from 4/5 to 2/5 with
  no change to the brain or the scenario.

## Failure labels (runs; primary label / any label)

| Label | B vertex | B deepseek | A vertex | A deepseek | t25 vertex |
|---|---|---|---|---|---|
| timeout | 8 / 8 | 0 / 0 | 45 / 45 | 0 / 0 | 2 / 2 |
| brain_fallback | 0 / 0 | 1 / 1 | 0 / 0 | 0 / 0 | 0 / 0 |
| unintended_action | 17 / 17 | 10 / 10 | 6 / 12 | 14 / 14 | 7 / 8 |
| missing_function_call | 0 / 0 | 0 / 0 | 1 / 5 | 0 / 0 | 0 / 0 |
| judge_no | 18 / 31 | 22 / 26 | 14 / 39 | 13 / 16 | 8 / 13 |
| silence | 0 / 10 | 0 / 13 | 0 / 18 | 0 / 7 | 0 / 6 |

Judge items in B, yes rate (vertex / deepseek):

| Item | Vertex | DeepSeek |
|---|---|---|
| answered | 92 % | 95 % |
| no_unintended_action | 99 % | 98 % |
| right_agent | 100 % | 100 % |
| no_hallucination | 98 % | 98 % |
| ear_friendly | 97 % | 98 % |
| language | 97 % | 97 % |
| scenario item 1 | 85 % | 92 % |
| scenario item 2 | 85 % | 90 % |

The code check for actions on workers is stricter than the judge's
no_unintended_action item. The judge mostly accepted the deferral denials
below, because they "carry out what the CEO said".

Brain latency, campaign B (from `brain.step` / `brain.turn` events):

| | Vertex gemini-3.8-flash | DeepSeek deepseek-flash |
|---|---|---|
| model step p50 / p90 / p99 | 1.7 / 2.9 / 6.1 s | 1.6 / 2.6 / 4.4 s |
| utterance turn p50 / p90 / max | 2.9 / 6.0 / 15.0 s | 3.0 / 5.3 / 9.3 s |
| announce turn p50 / p90 | 2.0 / 5.4 s | 1.4 / 1.8 s |
| fallbacks (of ~315 turns) | 9 (504 DEADLINE_EXCEEDED) | 1 (reply was not JSON) |

In campaign A, an hour earlier, the Vertex numbers were:
- step p90 7.1 s and p99 11.1 s;
- utterance turn p90 13.1 s;
- 62 fallbacks, 55 of them 504 DEADLINE_EXCEEDED.

## Top concrete failures

Each one was checked by reading the transcripts, not only the labels.
Excerpts are from the run reports (`report.json`); `[t+Ns]` is seconds
since the call started.

### 1. Turns time out and the CEO hears "sorry" (Vertex)

Campaign A, vertex, bench-fix-failing-test r1. Both turns hit
`Error 504 DEADLINE_EXCEEDED` at the 12 s budget.

```text
[t+0s]  CEO: Why is bench-codex's test failing?
[t+12s] Sheepdog: Sorry, I couldn't get an answer just now, but ask me again and I'll try once more.
[t+14s] CEO: Check on bench-codex again, I need to know why it's failing.
[t+26s] Sheepdog: Sorry, I couldn't get an answer just now, but ask me again and I'll try once more.
```

When the turn acts and then times out, the line the CEO hears is the code
sentence, in English, after the CEO spoke Chinese (A, vertex, j2-approve-zh
r3):

```text
[t+2s]  Sheepdog (unprompted): bench-codex asks to run go test -race./....
[t+6s]  CEO: 可以，让它跑吧。
[t+18s] Sheepdog: Told bench-codex yes.
```

### 2. "Not now" declines the prompt

B, deepseek, j1-status-zh r5. The CEO asked for the prompt to wait;
sheepdog sent `esc`. For Codex, `esc` means "No, and tell Codex what to do
differently", which cancels the command.

```text
[t+1s]  Sheepdog (unprompted): bench-codex is ready to run the full test suite with the race detector and is asking you to approve the command. Approve it?
[t+1s]  CEO: 现在什么情况？
[t+3s]  Sheepdog: bench-codex is waiting on your answer to run the full test suite, and bench-claude is working. … Options: Yes, proceed; Yes, and don't ask again for go test -race; No, tel…
[t+5s]  CEO: 先不要，让它等等。
[t+11s] Sheepdog: 已替你回绝了，bench-codex 的测试运行没有启动，它在原地等你发话。…
        ACT sheepdog answered bench-codex's prompt with keys esc
```

Frequency, as information-only runs where an `esc` was sent:

| Campaign | Vertex | DeepSeek |
|---|---|---|
| A | 6/20 | 6/20 |
| B | 11/20 | 7/20 |

The English versions follow the same pattern:
- "Not now." (j5, 4 of 5 Vertex runs in B)
- "Not now, keep me posted on the progress." (j1-status-en)

### 3. A "yes" applied to a question asked after it

A, deepseek, j4-finish-finding r1. The same thing happened in B, deepseek,
r2. The CEO's yes answered "shall I tell you when it's done?". A finish
push asking a new question landed one second later, and the yes was sent
to the worker as approval of that new question.

```text
[t+3s]  Sheepdog: It has written the README and is now checking its links. Not finished yet. Shall I tell you when it's done?
[t+7s]  CEO: Yes, let me know when it finishes.
[t+8s]  Sheepdog (unprompted): The README is done … it also spotted two broken links in another file, left them alone. Do you want those links fixed?
[t+12s] Sheepdog: Sent. It's fixing the two broken links now, and I'll tell you when it's finished.
        ACT [t+10s] sheepdog sent bench-claude the prompt "Yes, fix the two broken links in CONTRIBUTING.md …"
```

### 4. One block, two announcements, and a mangled command

A, vertex, j1-status-zh r1. The daemon's code sentence plays when
AnnounceWait (2 s) runs out. The brain's own line about the same event
follows. The code sentence carries the raw command, cut by the scrubber.

```text
[t+2s] Sheepdog (unprompted): bench-codex asks to run go test -race./....
[t+5s] Sheepdog (unprompted): bench-codex wants approval to run the test suite with the race detector. Approve?
```

Runs where a block at the start was announced twice within 6 s:

| Campaign | Vertex | DeepSeek |
|---|---|---|
| A | 23/55 | 5/55 |
| B | 28/55 | 2/55 |

The count follows the brain's announce latency: a Vertex announce turn
takes 2.0 s at p50, a DeepSeek one 1.4 s. `-race./....` appeared in 42
lines, all of them on Vertex, where the code sentence plays.

### 5. "Type something." is still offered as a choice (#2)

A, vertex, bench-askuser-meta-options r1:

```text
[t+3s] Sheepdog: bench-claude needs you to choose a software license for the repo. Which license would you prefer? Options: MIT; Apache 2.0; Type something.
```

It happened in 15 of 20 runs across A and B: Vertex 10/10, DeepSeek 5/10.
DeepSeek sometimes rewords it as "or you can type your own" or "Something
else".

### 6. Claims of absence the screen does not support

B, vertex, bench-unknown-fact r4. Nothing on the screen says whether
anything was pushed.

```text
[t+0s] CEO: Did bench-claude push its changes yet?
[t+3s] Sheepdog: No, bench-claude only wrote the README and stopped. It has not committed or pushed anything. Would you like it to push?
```

This failed in 10/10 Vertex runs and 4/10 DeepSeek runs (A + B).

### 7. The Chinese join seam

`sentence()` adds `.` after `。` and `？`, for example
`…正在检查链接。.` and `要批准 bench-codex 运行测试吗？. Options: …`.
Across A and B this affected 149 of 1,382 spoken lines (11 %), on both
brains.

## Prioritized harness problems

The fix direction for each follows the CEO's principle: the harness stops
runaways and supplies facts the model cannot see, and the judgement stays
with the model.

1. **P0 — The utterance budget kills answers instead of bounding
   runaways.** Evidence: 45/130 Vertex runs in A, the t25 experiment
   (49 % → 67 %, utterance fallbacks 44 → 0), and Vertex latency that
   changes from hour to hour. `brain.Config.UtteranceTimeout` is 12 s and
   one step can take 11 s. Fix direction:
   - Keep the budget as the guard for a runaway turn only. When it expires,
     tell the CEO the brain is still on it and push the reply when it
     lands (the late-delivery path already exists for ReplyWait). Do not
     replace the reply with "Sorry".
   - Have the scorecard track the provider's step latency.
   - Owner: internal/brain + supervisor (the brain robustness agent).
2. **P0 — Deferral is not a choice the brain is given.** Evidence: 30/80
   information-only runs, on both brains. Fix direction: give the brain
   facts, not a rule.
   - The pending prompt stays open while the pane waits (#8).
   - Doing nothing leaves it waiting.
   - What each option does: Codex `esc` cancels the command.
   - Let the brain's offered choices include "leave it waiting".
   - The one rail stays as it is: a CEO turn may answer, but nothing tells
     the model that "not now" is not "no".
   - Owner: supervisor brainhost (pending facts) + prompts/RULES.md.
3. **P0 — The CEO's words are not tied to the line they answer.**
   Evidence: 2/20 j4 runs; the action sent fixes files nobody asked to
   fix. Fix direction: put in the brain's request which sheepdog line was
   the last one heard before the CEO spoke. The #17 unheard-push mechanism
   already knows which lines were heard. Mark a question pushed after the
   CEO started speaking as "not yet heard by the CEO". The brain then
   decides; no code guard. Owner: supervisor + voice (playback facts).
4. **P1 — The code sentence and the brain's line both announce a block.**
   Evidence: 28/55 Vertex runs in B. Fix direction:
   - The brain already gets the fact that the template was spoken (#15b),
     and it still repeats.
   - Make the template plain and short (no raw command).
   - State the fact to the brain as "already said: <words>; add only what
     is new, or nothing".
   - Count the template toward the anti-loop cap.
   - A single per-event spoken-line budget is the runaway guard; whether to
     add a line stays with the model. Owner: supervisor/announce.go.
5. **P1 — Meta options reach the brain unmarked.** The cause is
   `internal/supervisor/brainhost.go:56-58`: `brain.PendingPrompt.Options`
   is built from every option and drops `Option.Meta`. Fix direction:
   pass Meta as a fact ("free-text entry, not an answer"). The decided #2
   behaviour then follows from the model's own reading. Owner: supervisor
   + brain (types).
6. **P1 — The `。.` / `？.` seam.** The cause is
   `internal/supervisor/brainhost.go:391`: `sentence()` checks only ASCII
   terminal punctuation. Fix: treat `。！？…` as terminal too (a code bug,
   not judgement; it is the #13 follow-up).
7. **P1 — The scrubber eats the space before `./`.** The cause is
   `internal/prose/prose.go:118`: `reOrphan` (`\s+([,.;:!?])`) turns
   `go test -race ./...` into `go test -race./....`. Fix: do not join a
   period that starts a path, or keep commands out of spoken lines (see 4).
8. **P2 — English code sentences after Chinese.** The causes are
   `answerKeys` format strings ("Told %s %s.") in `utterance.go:667`,
   `brainhost.go:244` and `inputs.go:620`, the block template
   ("asks to run …"), and option labels taken verbatim from the screen.
   Evidence: 5 runs across A and B. Fix direction: the language of the
   CEO's last utterance is a fact the daemon has; code sentences either
   use it or leave the wording to the brain.
9. **P2 — Unsupported claims of absence.** Evidence: 14/20 runs. Fix
   direction: facts first. A cheap `git status` or session read the brain
   can ask for (`run_cli`), plus a RULES.md line that "not on the screen"
   is not evidence of "did not happen". The judgement stays with the
   model.
10. **P2 — "Ask it why" becomes a prompt to the worker.** Evidence: B,
    vertex, bench-why r1. When the investigation backend is missing, the
    brain asked the worker itself with a prompt ("Explain why… Do not
    change any code"), which interrupts the worker's state. The push said
    "The look into bench-codex did not get through" with no reason given.
    Fix direction: report why an investigation failed (no CLI backend) as
    a fact.

## The evaluator's own errors

Every rerun above followed a fix to the evaluator, not to sheepdog. What
was found and done:

- **Rate limits.** The first campaign lost 69 of 130 runs to 429s from the
  shared Flash-Lite quota.
  - Fix: jittered backoff up to 30 s, 8 attempts, retries alternating with
    `gemini-3.5-flash-lite`, and at most 4 judge or simulated-user calls in
    flight.
  - Result: 0 errored runs since.
  - The brain is never retried.
- **Judge false negatives.** The judge:
  - read the daemon's sign-off trailer and a worker's own earlier history
    as sheepdog's actions;
  - called worker names jargon or the wrong language;
  - read old screen lines above new ones as current.

  Fix: the facts now explain the trailer and the screen history, show only
  the new output of a changed screen, and the rubric items say what is
  fine.
- **Remaining judge noise.** bench-lang-switch-en-zh (1/5 and 0/5 in B)
  is mostly evaluator noise:
  - the judge flags a correct Chinese reply after the switch;
  - in other runs the simulated user waited instead of switching.

  Of the DeepSeek judge_no runs reviewed in A, about 7 of 13 were real and
  about 5 were artefacts. Treat a single judge_no as a lead, not a
  verdict. The repeated, cross-model patterns above are the reliable
  signal.
- **Simulated user deviations.** The simulated user:
  - hung up claiming an approval that never happened;
  - asked sheepdog to stop workers on information-only calls;
  - left out the words a scenario is about.

  Fixes: the system prompt now counts only what sheepdog confirmed and
  forbids unrequested changes, and `fixed` scenarios play their script
  live (j6-*, j2-approve-then-what).
- **Scenario mistakes.** Fixed before B:
  - approval events appended output below a still-visible prompt;
  - a declined prompt stayed on the mock screen, so the brain offered to
    send the answer again;
  - bench-block-while-talking forbade refusing the force push, which the
    CEO asked for;
  - an interrupted worker still finished its scripted task.

  Fixed after B and not rerun: j4 counted a link fix the CEO approved as
  unintended. It now forbids only keys and interrupts, and leaves "did the
  yes answer that question" to the judge. B's j4 unintended_action counts
  should be read alongside their transcripts: 3 of the 5 were approved by
  the CEO, and the DeepSeek r2 case is the race in failure 3.

## Reproduce

```sh
make eval                                   # offline: every scenario on the real supervisor, scripted brain and user
sheepdog eval sim --live --provider deepseek --model deepseek-flash \
  --env-file .env --repeat 5 --parallel 3 --budget-usd 10 --out /tmp/campaign --events /tmp/campaign/events
sheepdog eval sim --live --provider vertex --model gemini-3.8-flash \
  --utterance-timeout 25s --env-file .env --repeat 2 --parallel 4 --out /tmp/t25
```

`report.md` has the tables and the failed runs with transcripts.
`report.json` has every run: transcript, actions, scripted changes,
verdicts, failures, usage. `--events` writes each run's daemon events as
JSONL.
