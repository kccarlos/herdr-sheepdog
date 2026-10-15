# Simulated-user campaign

Started 2026-10-13T20:45:35Z, finished 2026-10-13T20:45:54Z (19s). Repeat 1.

- brain: deepseek deepseek-flash
- budget_usd: 4
- failover providers: none
- judge: fake fake
- mode: live
- parallel: 4
- personas: rotate
- scenarios: testdata/eval/sim/unanswered.json (filter ua-fail-en)
- simulated user: deepseek deepseek-flash
- utterance_timeout: 25s (0s = the orchestrator default, 25s)

## North star

1 runs, 1 graded (1 with a full record, 1 with the event log). Each rate is the share of the graded runs it applies to, with a Wilson 95 % CI; means have a bootstrap 95 % CI (2000 resamples). Everything after this block is diagnostics.

| North star | Pass / runs | Rate | 95 % CI | A run passes when |
|---|---|---|---|---|
| **Understood** | 1 / 1 | 100 % | 21–100 % | the judge's `answered` is yes and every depth or length item is yes |
| **Done** | – / 0 | n/a | – | no required action missing or sent to the wrong agent, and every declared end-state check passes (the claim part below is not in the gate) |
| **Done, claim part (judge)** | – / 0 | n/a | – | no segment's `said_vs_done` judge item is no (runs with a claim the judge looked at); not gated until calibrated |
| **Done, claim part (phrases)** | – / 0 | n/a | – | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 1 / 1 | 100 % | 21–100 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 1 / 1 | 100 % | 21–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 4.0 (95 % CI 4.0–4.0, n 1). Time to goal: 17.2 s (95 % CI 17.2–17.2, n 1).

- Done: 1 runs had nothing to get done (no required action, no end-state check and no action) and are left out.

### Programmatic checks (diagnostics)

- **Said vs done, orchestrator**: 0 claims — 0 matched in their turn, 0 matched by an earlier action, unmatched – / 0 (n/a, CI –). Actions on workers: 0; never told – / 0 (n/a, CI –).
- **Missed news**: 1 items (finished, blocked, result, spawn failed); missed 0 / 1 (0 %, CI 0–79 %); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 4 user lines, 0 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 4.0 (95 % CI 4.0–4.0, n 1); estimated cost per run 0.0127 $ (95 % CI 0.0127–0.0127, n 1); per-turn latency p50 1534 ms, p90 5641 ms (n 4).

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| ua-fail-en | 1 | 0 | 1 | 0 | 0 | 0% | brain_fallback ×1 | 18.7 | 0.013 |
| **TOTAL** | 1 | 0 | 1 | 0 | 0 | 0% | brain_fallback ×1 | 18.7 | 0.013 |

## Brain failover

Failovers = brain.failover events (a step retried on another provider); answered by = the provider of each run's last answered step.

| Scenario | Runs | Runs with failover | Failovers | Answered by |
|---|---|---|---|---|
| ua-fail-en | 1 | 0 | 0 | deepseek ×1 |
| **TOTAL** | 1 | 0 | 0 | deepseek ×1 |

## Failure taxonomy

| Label | Runs (primary) | Runs (any) |
|---|---|---|
| brain_fallback | 1 | 1 |

## Judge items

| Item | Yes | Asked | Yes rate |
|---|---|---|---|
| answered | 1 | 1 | 100% |
| no_unintended_action | 1 | 1 | 100% |
| right_agent | 1 | 1 | 100% |
| no_hallucination | 1 | 1 | 100% |
| facts_current | 1 | 1 | 100% |
| length_fits | 1 | 1 | 100% |
| ear_friendly | 1 | 1 | 100% |
| language | 1 | 1 | 100% |

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| brain | deepseek-flash | 6 | 38900 | 1179 | 543 | 0.013 |
| **total** | | | | | | **0.013** |

## Failed runs

### ua-fail-en r1 — brain_fallback (end_call, 4 turns, 18.7 s)

- **brain_fallback**: model_error: eval: injected model failure (sim faults)

```text
[t+0s] CEO: Why did bench-codex's tests fail?
[t+0s] Sheepdog: Sorry, I couldn't get an answer just now. If you say it again, I'll try once more.
[t+1s] CEO: Okay. What is bench-claude doing right now?
[t+7s] Sheepdog: The bench-codex test failed because its add function returns the wrong number: it hands back the second number and ignores the first. Its earlier all-pass report was wrong, that run only covered part of the code. bench-claude is writing a README and is now checking the links in it.
[t+8s] (The CEO waits in silence.)
[t+10s] Sheepdog (unprompted): The README is done, covering what calc does, how to build it and how to run the tests. While writing it, bench-claude also noticed two dead links in the contributing notes, and left them alone. Want it to fix those links?
[t+11s] CEO: Alright, anything else I should know?
[t+14s] Sheepdog: One thing is still open. bench-codex found a real bug in the add function, and nobody has fixed it yet. The README and the two dead links are the only other items, and the links are still waiting on your call. Shall I have bench-codex fix the add bug? Options: Fix the add bug; Also fix the two dead links.
[t+15s] CEO: Thanks, that's all for now.
[t+17s] Sheepdog: You are all set. Both agents are idle, and the add bug is still on the list whenever you want it.

```
