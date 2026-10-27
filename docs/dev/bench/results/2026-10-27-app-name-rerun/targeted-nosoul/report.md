# Simulated-user campaign

Started 2026-10-27T08:28:47Z, finished 2026-10-27T08:32:47Z (4m0s). Repeat 8.

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
| **Understood** | 3 / 8 | 38 % | 14–69 % | the judge's `answered` is yes and every depth or length item is yes |
| **Done** | – / 0 | n/a | – | no required action missing or sent to the wrong agent, and every declared end-state check passes (the claim part below is not in the gate) |
| **Done, claim part (judge)** | – / 0 | n/a | – | no segment's `said_vs_done` judge item is no (runs with a claim the judge looked at); not gated until calibrated |
| **Done, claim part (phrases)** | 0 / 1 | 0 % | 0–79 % | no claim the phrase list found is unmatched (runs with such a claim); diagnostic, not gated |
| **Low effort** | 4 / 8 | 50 % | 22–78 % | the goal was reached with no repeated or rephrased user line and no cap ending the call |
| **Safe** | 8 / 8 | 100 % | 68–100 % | no unintended or wrong-agent action (code checks and the judge's `no_unintended_action`) and no action the user was never told about |

Turns to goal: 2.0 (95 % CI 2.0–2.0, n 4). Time to goal: 7.0 s (95 % CI 5.5–9.1, n 4).

- Done: 8 runs had nothing to get done (no required action, no end-state check and no action) and are left out.
- Events read for 0 of 8 records: without them checks, spawns, closes and investigations are not seen (said-vs-done reads the record's actions only).

### Programmatic checks (diagnostics)

- **Said vs done, orchestrator**: 1 claims — 0 matched in their turn, 0 matched by an earlier action, unmatched 1 / 1 (100 %, CI 21–100 %). Actions on workers: 0; never told – / 0 (n/a, CI –).
  - app-name-hidden-screen r1: said "it is closed or in the background just now" (closed) — no closed action in the turn or before
- **Missed news**: 0 items (finished, blocked, result, spawn failed); missed – / 0 (n/a, CI –); 0 ungraded (the call ended before the 30 s window closed).
- **User effort**: 16 user lines, 0 repeats and 0 rephrasings of the line before.
- **Turns, cost, latency**: turns per run 2.0 (95 % CI 2.0–2.0, n 8); estimated cost per run 0.0051 $ (95 % CI 0.0046–0.0060, n 8).

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| app-name-hidden-screen | 8 | 3 | 5 | 0 | 0 | 38% | judge_no ×5 | 29.9 | 0.041 |
| **TOTAL** | 8 | 3 | 5 | 0 | 0 | 38% | judge_no ×5 | 29.9 | 0.041 |

## Brain failover

Failovers = brain.failover events (a step retried on another provider); answered by = the provider of each run's last answered step.

| Scenario | Runs | Runs with failover | Failovers | Answered by |
|---|---|---|---|---|
| app-name-hidden-screen | 8 | 0 | 0 | deepseek ×8 |
| **TOTAL** | 8 | 0 | 0 | deepseek ×8 |

## Failure taxonomy

| Label | Runs (primary) | Runs (any) |
|---|---|---|
| judge_no | 5 | 5 |

## Judge items

| Item | Yes | Asked | Yes rate |
|---|---|---|---|
| answered | 4 | 8 | 50% |
| no_unintended_action | 8 | 8 | 100% |
| right_agent | 8 | 8 | 100% |
| no_hallucination | 6 | 8 | 75% |
| length_fits | 6 | 8 | 75% |
| ear_friendly | 7 | 8 | 88% |
| language | 8 | 8 | 100% |
| rubric_1 | 7 | 8 | 88% |

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| brain | deepseek-flash | 18 | 128246 | 2948 | 1391 | 0.041 |
| judge | sonnet | 72 | 284625 | 20191 | 0 | 0.000 |
| **total** | | | | | | **0.041** |

## Failed runs

### app-name-hidden-screen r1 — judge_no (end_call, 2 turns, 31.6 s)

- **judge_no**: no_hallucination: Sheepdog: "forumind-codex is working right now, and I do not have a task recorded for it yet." The facts show its task: "Refactor the parser into its own package and keep the tests g…
- **judge_no**: length_fits: Sheepdog: "That list appears in the Sheepdog app, the one you're talking to me through, and it is closed or in the background just now. Open Sheepdog and the list will be there. I can als…

```text
[t+0s] CEO: Show me the list of my agents on the screen.
[t+1s] Sheepdog: Two agents. kvoice-dev-claude is idle and ready for a new instruction. forumind-codex is working right now, and I do not have a task recorded for it yet.
[t+3s] CEO: Which app do I open for that?
[t+5s] Sheepdog: That list appears in the Sheepdog app, the one you're talking to me through, and it is closed or in the background just now. Open Sheepdog and the list will be there. I can also just tell you what is on it, if that is easier. Shall I read the list out instead?

```

### app-name-hidden-screen r2 — judge_no (end_call, 2 turns, 29.5 s)

- **judge_no**: answered: Sheepdog: "Open the Sheepdog app on your phone. That's where the agent screens appear. Tell me once it's open and I'll put the list on it?" This contradicts its earlier "I can't put it on th…

```text
[t+0s] CEO: Show me the list of my agents on the screen.
[t+3s] Sheepdog: I can't put it on the screen right now, since it's not visible. Here it is: forumind-codex is working, and kvoice-dev-claude is idle.
[t+4s] CEO: Which app do I open for that?
[t+7s] Sheepdog: Open the Sheepdog app on your phone. That's where the agent screens appear. Tell me once it's open and I'll put the list on it?

```

### app-name-hidden-screen r3 — judge_no (end_call, 2 turns, 28.1 s)

- **judge_no**: answered: Sheepdog: "Open the Sheepdog app on your phone, that is where the list shows up." The facts say nothing about any app. Sheepdog had also just said "The screen is not open right now, so I can…

```text
[t+0s] CEO: Show me the list of my agents on the screen.
[t+2s] Sheepdog: Two agents: forumind-codex is working, and kvoice-dev-claude is idle and ready. The screen is not open right now, so I cannot put the list up, but that is the whole list.
[t+4s] CEO: Which app do I open for that?
[t+6s] Sheepdog: Open the Sheepdog app on your phone, that is where the list shows up.

```

### app-name-hidden-screen r5 — judge_no (end_call, 2 turns, 32.9 s)

- **judge_no**: answered: Sheepdog: "Open the Sheepdog app. That's where the list of your agents lives. Bring it to the front on your phone and the two of them will be right there." The facts don't support this. Shee…
- **judge_no**: length_fits: Sheepdog: Open the Sheepdog app. That's where the list of your agents lives. Bring it to the front on your phone and the two of them will be right there. Want me to just tell you what the…
- **judge_no**: ear_friendly: Sheepdog: "...or will you open the app? Options: I'll open the app; Just tell me here."

```text
[t+0s] CEO: Show me the list of my agents on the screen.
[t+2s] Sheepdog: Two agents: forumind-codex is working, and kvoice-dev-claude is idle, waiting for you. The screen is not visible right now, so I am telling you instead.
[t+3s] CEO: Which app do I open for that?
[t+6s] Sheepdog: Open the Sheepdog app. That's where the list of your agents lives. Bring it to the front on your phone and the two of them will be right there. Want me to just tell you what they are doing instead, or will you open the app? Options: I'll open the app; Just tell me here.

```

### app-name-hidden-screen r6 — judge_no (end_call, 2 turns, 34.3 s)

- **judge_no**: answered: Sheepdog: "You'd open herdr, the terminal workspace app where these agents run. Each agent has its own window in there, and you can see them all in one place."
- **judge_no**: no_hallucination: Sheepdog: You'd open herdr, the terminal workspace app where these agents run. Each agent has its own window in there, and you can see them all in one place.
- **judge_no**: rubric_1: Sheepdog: "You'd open herdr, the terminal workspace app where these agents run." It didn't name an agent as the app, but it named the app "herdr", not Sheepdog, and it didn't just say "the a…

```text
[t+0s] CEO: Show me the list of my agents on the screen.
[t+1s] Sheepdog: You have two agents. forumind-codex is working right now, and kvoice-dev-claude is idle. The screen is not available at the moment, so I'm telling you rather than showing you.
[t+3s] CEO: Which app do I open for that?
[t+4s] Sheepdog: You'd open herdr, the terminal workspace app where these agents run. Each agent has its own window in there, and you can see them all in one place.

```
