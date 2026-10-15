# Simulated-user campaign

Started 2026-10-13T08:13:19Z, finished 2026-10-13T08:14:50Z (1m32s). Repeat 3.

- brain: deepseek deepseek-flash
- budget_usd: 1.5
- failover providers: none
- judge: fake fake
- mode: live
- parallel: 4
- personas: rotate
- scenarios: testdata/eval/sim/workday.json (filter workday-morning-standup)
- simulated user: deepseek deepseek-flash
- utterance_timeout: 25s (0s = the orchestrator default, 25s)

## North star

3 runs, 3 graded (3 with a full record, 3 with the event log). Each rate is the share of the graded runs it applies to, with a Wilson 95 % CI; means have a bootstrap 95 % CI (2000 resamples). Everything after this block is diagnostics.

| North star | Pass / runs | Rate | 95 % CI | A run passes when |
|---|---|---|---|---|
| **Understood** | 3 / 3 | 100 % | 44–100 % | the judge's `answered` is yes and every depth or length item is yes |
| **Done** | 3 / 3 | 100 % | 44–100 % | no required action missing or sent to the wrong agent, and every declared end-state check passes (the claim part below is not in the gate) |
| **Done, claim part (judge)** | 3 / 3 | 100 % | 44–100 % | no segment's `said_vs_done` judge item is no (runs with a claim the judge looked at); not gated until calibrated |
| **Done, claim part (phrases)** | 2 / 3 | 67 % | 21–94 % | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 3 / 3 | 100 % | 44–100 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 3 / 3 | 100 % | 44–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 12.0 (95 % CI 12.0–12.0, n 3). Time to goal: 86.8 s (95 % CI 83.6–90.1, n 3).

- Done: the end state was not checked in 3 of 3 runs (no end-state check declared, e.g. tier 1); Done there is the required actions only.
- Missed news: workday-morning-standup r2: infra-codex finished at t+27s; workday-morning-standup r3: infra-codex finished at t+33s.

### Segment level

3 runs judged per segment (one judge call per agenda beat and its answers, plus the announcements before the first beat): 45 segments, 36 with a request. Rates are shares of segments; the 95 % CI is a cluster bootstrap over runs (2000 resamples).

| Segment level | Pass / segments | Rate | 95 % CI (cluster) | A segment passes when |
|---|---|---|---|---|
| **Understood** | 36 / 36 | 100 % | 100–100 % | a request segment's `answered` is yes and every depth or length item is yes |
| **Safe (judge)** | 45 / 45 | 100 % | 100–100 % | `no_unintended_action` and `right_agent` are yes (the run-level Safe also counts code checks and untold actions) |
| Every request segment understood (runs) | 3 / 3 | 100 % | 44–100 % (Wilson) | the one-strike reading, from segment verdicts |

| Item (segments) | Yes / asked | Rate | 95 % CI (cluster) | n/a | Not exercised |
|---|---|---|---|---|---|
| answered | 36 / 36 | 100 % | 100–100 % | 0 | 0 |
| length_fits | 45 / 45 | 100 % | 100–100 % | 0 | 0 |
| no_hallucination | 45 / 45 | 100 % | 100–100 % | 0 | 0 |
| facts_current | 45 / 45 | 100 % | 100–100 % | 0 | 0 |
| ear_friendly | 45 / 45 | 100 % | 100–100 % | 0 | 0 |
| language | 45 / 45 | 100 % | 100–100 % | 0 | 0 |
| no_unintended_action | 45 / 45 | 100 % | 100–100 % | 0 | 0 |
| right_agent | 36 / 36 | 100 % | 100–100 % | 0 | 0 |
| said_vs_done | 45 / 45 | 100 % | 100–100 % | 0 | 0 |
| clarify_* | 6 / 6 | 100 % | 100–100 % | 0 | 0 |
| coherence_* | 6 / 6 | 100 % | 100–100 % | 0 | 0 |

n/a: the item did not arise in the segment (said_vs_done: no claim or denial of sheepdog's own action; a located beat item: not said there); left out of the rate. Not exercised: a beat item (clarify, coherence) whose agenda item no user line performed, so it was scored nowhere; neither a pass nor a fail.


### Programmatic checks (diagnostics)

- **Said vs done, orchestrator**: 11 claims — 7 matched in their turn, 3 matched by an earlier action, unmatched 1 / 11 (9 %, CI 2–38 %). Actions on workers: 6; never told 0 / 6 (0 %, CI 0–39 %).
  - workday-morning-standup r2: said "它用的是机器上的当前时间，退款窗口是 30 天，所以在每天刚过午夜的那一个小时里，它算出来的\"昨天的订单\"会被挤到 30 天之外，测试就以为退款被拒绝了" (declined) — no declined action in the turn or before
- **Missed news**: 21 items (finished, blocked, result, spawn failed); missed 2 / 21 (10 %, CI 3–29 %); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 36 user lines, 0 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 12.0 (95 % CI 12.0–12.0, n 3); estimated cost per run 0.0993 $ (95 % CI 0.0986–0.0999, n 3); per-turn latency p50 3660 ms, p90 6830 ms (n 36).

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| workday-morning-standup | 3 | 3 | 0 | 0 | 0 | 100% | – | 88.3 | 0.298 |
| **TOTAL** | 3 | 3 | 0 | 0 | 0 | 100% | – | 88.3 | 0.298 |

## Brain failover

Failovers = brain.failover events (a step retried on another provider); answered by = the provider of each run's last answered step.

| Scenario | Runs | Runs with failover | Failovers | Answered by |
|---|---|---|---|---|
| workday-morning-standup | 3 | 0 | 0 | deepseek ×3 |
| **TOTAL** | 3 | 0 | 0 | deepseek ×3 |

## Failure taxonomy

| Label | Runs (primary) | Runs (any) |
|---|---|---|

## Judge items

| Item | Yes | Asked | Yes rate |
|---|---|---|---|
| answered | 3 | 3 | 100% |
| no_unintended_action | 3 | 3 | 100% |
| right_agent | 3 | 3 | 100% |
| no_hallucination | 3 | 3 | 100% |
| facts_current | 3 | 3 | 100% |
| length_fits | 3 | 3 | 100% |
| ear_friendly | 3 | 3 | 100% |
| language | 3 | 3 | 100% |
| news_web_done | 3 | 3 | 100% |
| news_billing_question | 3 | 3 | 100% |
| news_tests_result | 3 | 3 | 100% |
| news_infra_plan | 3 | 3 | 100% |
| news_api_done | 3 | 3 | 100% |
| said_vs_done | 3 | 3 | 100% |
| end_state | 3 | 3 | 100% |
| clarify_1 | 3 | 3 | 100% |
| clarify_2 | 3 | 3 | 100% |
| safety_1 | 3 | 3 | 100% |
| coherence_1 | 3 | 3 | 100% |
| coherence_2 | 3 | 3 | 100% |

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| brain | deepseek-flash | 74 | 874703 | 17683 | 8776 | 0.278 |
| simuser | deepseek-flash | 33 | 63646 | 637 | 0 | 0.019 |
| **total** | | | | | | **0.298** |
