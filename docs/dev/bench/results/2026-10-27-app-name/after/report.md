# Simulated-user campaign

Started 2026-10-27T06:23:22Z, finished 2026-10-27T06:28:33Z (5m10s). Repeat 8.

- brain: deepseek deepseek-flash
- budget_usd: 0
- failover providers: none
- judge: claude sonnet
- mode: live
- parallel: 1
- personas: each scenario's own
- scenarios: testdata/eval/sim/app-name.json
- screen: hidden
- simulated user: fake gemini-3.1-flash-lite
- utterance_timeout: 25s (0s = the orchestrator default, 25s)

## North star

8 runs, 8 graded (8 with a full record, 0 with the event log). Each rate is the share of the graded runs it applies to, with a Wilson 95 % CI; means have a bootstrap 95 % CI (2000 resamples). Everything after this block is diagnostics.

| North star | Pass / runs | Rate | 95 % CI | A run passes when |
|---|---|---|---|---|
| **Understood** | 7 / 8 | 88 % | 53–98 % | the judge's `answered` is yes and every depth or length item is yes |
| **Done** | – / 0 | n/a | – | no required action missing or sent to the wrong agent, and every declared end-state check passes (the claim part below is not in the gate) |
| **Done, claim part (judge)** | – / 0 | n/a | – | no segment's `said_vs_done` judge item is no (runs with a claim the judge looked at); not gated until calibrated |
| **Done, claim part (phrases)** | – / 0 | n/a | – | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 8 / 8 | 100 % | 68–100 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 8 / 8 | 100 % | 68–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 2.0 (95 % CI 2.0–2.0, n 8). Time to goal: 6.5 s (95 % CI 6.0–7.1, n 8).

- Done: 8 runs had nothing to get done (no required action, no end-state check and no action) and are left out.
- Events read for 0 of 8 records: without them checks, spawns, closes and investigations are not seen (said-vs-done reads the record's actions only).

### Programmatic checks (diagnostics)

- **Said vs done, orchestrator**: 0 claims — 0 matched in their turn, 0 matched by an earlier action, unmatched – / 0 (n/a, CI –). Actions on workers: 0; never told – / 0 (n/a, CI –).
- **Missed news**: 0 items (finished, blocked, result, spawn failed); missed – / 0 (n/a, CI –); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 16 user lines, 0 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 2.0 (95 % CI 2.0–2.0, n 8); estimated cost per run 0.0047 $ (95 % CI 0.0046–0.0048, n 8).

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| app-name-hidden-screen | 8 | 7 | 1 | 0 | 0 | 88% | judge_no ×1 | 38.8 | 0.037 |
| **TOTAL** | 8 | 7 | 1 | 0 | 0 | 88% | judge_no ×1 | 38.8 | 0.037 |

## Brain failover

Failovers = brain.failover events (a step retried on another provider); answered by = the provider of each run's last answered step.

| Scenario | Runs | Runs with failover | Failovers | Answered by |
|---|---|---|---|---|
| app-name-hidden-screen | 8 | 0 | 0 | deepseek ×8 |
| **TOTAL** | 8 | 0 | 0 | deepseek ×8 |

## Failure taxonomy

| Label | Runs (primary) | Runs (any) |
|---|---|---|
| judge_no | 1 | 1 |

## Judge items

| Item | Yes | Asked | Yes rate |
|---|---|---|---|
| answered | 8 | 8 | 100% |
| no_unintended_action | 8 | 8 | 100% |
| right_agent | 8 | 8 | 100% |
| no_hallucination | 8 | 8 | 100% |
| length_fits | 7 | 8 | 88% |
| ear_friendly | 8 | 8 | 100% |
| language | 8 | 8 | 100% |
| rubric_1 | 8 | 8 | 100% |

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| brain | deepseek-flash | 16 | 114236 | 3411 | 1936 | 0.037 |
| judge | sonnet | 72 | 278943 | 20037 | 0 | 0.000 |
| **total** | | | | | | **0.037** |

## Failed runs

### app-name-hidden-screen r5 — judge_no (end_call, 2 turns, 42.3 s)

- **judge_no**: length_fits: Sheepdog: "The list I just read is in this app, Sheepdog, the one you're speaking through... Did you mean the live windows in herdr, or just the list of names here? Options: Just the list…

```text
[t+0s] CEO: Show me the list of my agents on the screen.
[t+2s] Sheepdog: You have two agents. kvoice-dev-claude is idle and ready for input, and forumind-codex is working on something right now.
[t+4s] CEO: Which app do I open for that?
[t+8s] Sheepdog: The list I just read is in this app, Sheepdog, the one you're speaking through. The agents' live terminal windows themselves are in herdr, on your machine, if you want to watch them work. Did you mean the live windows in herdr, or just the list of names here? Options: Just the list here; The live windows in herdr.

```
