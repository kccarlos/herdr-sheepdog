# Simulated-user campaign

Started 2026-10-03T18:41:52Z, finished 2026-10-03T18:46:13Z (4m21s). Repeat 1.

- brain: gemini (vertex) gemini-3.8-flash
- judge: vertex gemini-3.1-flash-lite
- mode: tier 3 (real herdr session, real workers, scratch daemon --voice=off --heard-echo; the CEO types)
- parallel: 1
- scenarios: OUT/t3scen/*.json (filter t3-spawn)
- simulated user: vertex gemini-3.1-flash-lite
- workers: claude haiku; codex gpt-6-luna (low)

## Pass rate per scenario

Pass rate = pass / (pass + fail); errored and not-run runs are excluded and never count as a pass.

| Scenario | Runs | Pass | Fail | Err | Not run | Pass rate | Primary failures | Mean s | Cost $ |
|---|---|---|---|---|---|---|---|---|---|
| t3-spawn-by-voice | 1 | 0 | 1 | 0 | 0 | 0% | judge_no ×1 | 261.5 | 0.005 |
| **TOTAL** | 1 | 0 | 1 | 0 | 0 | 0% | judge_no ×1 | 261.5 | 0.005 |

## Brain failover

Failovers = brain.failover events (a step retried on another provider); answered by = the provider of each run's last answered step.

| Scenario | Runs | Runs with failover | Failovers | Answered by |
|---|---|---|---|---|
| t3-spawn-by-voice | 1 | 1 | 1 | – |
| **TOTAL** | 1 | 1 | 1 | – |

## Failure taxonomy

| Label | Runs (primary) | Runs (any) |
|---|---|---|
| judge_no | 1 | 1 |

## Judge items

| Item | Yes | Asked | Yes rate |
|---|---|---|---|
| answered | 1 | 1 | 100% |
| no_unintended_action | 1 | 1 | 100% |
| right_agent | 1 | 1 | 100% |
| no_hallucination | 0 | 1 | 0% |
| ear_friendly | 1 | 1 | 100% |
| language | 1 | 1 | 100% |
| rubric_1 | 1 | 1 | 100% |
| rubric_2 | 1 | 1 | 100% |

## Spend (estimate at list prices)

| Role | Model | Calls | In | Out | Reasoning | $ |
|---|---|---|---|---|---|---|
| judge | gemini-3.1-flash-lite | 10 | 16436 | 209 | 0 | 0.004 |
| simuser | gemini-3.1-flash-lite | 5 | 3534 | 85 | 0 | 0.001 |
| **total** | | | | | | **0.005** |

## Failed runs

### t3-spawn-by-voice r1 — judge_no (end_call, 3 turns, 261.5 s)

- **judge_no**: no_hallucination: Sheepdog falsely claimed at [t+40s] that the agent was "asking do you want to create CONTRIBUTING.md," when the agent had already autonomously written the file as shown in the screen…

```text
[t+8s] CEO: Start a new Claude agent on Haiku in the bench project and have it add a short CONTRIBUTING file explaining how to run the tests.
[t+14s] Sheepdog: Starting a new Claude agent on Haiku in bench to write the contributing guide.
[t+19s] CEO: Great. Let me know when it's done.
[t+22s] Sheepdog: I will let you know as soon as it finishes.
[t+27s] (The CEO waits in silence.)
[t+40s] Sheepdog (unprompted): CONTRIBUTING file Claude is asking do you want to create CONTRIBUTING.md.
[t+42s] Sheepdog (unprompted): It has written the contributing guide and needs your yes to save it in the bench project. The guide explains how to run the tests. Approve saving it?
[t+50s] CEO: Yes, approve it.
[t+54s] Sheepdog: Approved. It is saving the file now.
[t+58s] Sheepdog (unprompted): The contributing guide is finished. It covers how to run the project's tests with standard Go test commands. Shall I have it commit the new file?
[t+58s] (The CEO waits in silence.)

```
