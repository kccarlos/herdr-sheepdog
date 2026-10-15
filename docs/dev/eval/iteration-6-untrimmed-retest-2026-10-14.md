# Harness iteration 6 (untrimmed retest): facts before explaining on current main, 2026-10-14

The CEO asked for one more run and a merge if it passes (2026-10-14: "再跑一下没问题就合并"). The prompt token budget test was removed in #217, so iteration 6 ([iteration-6-facts-before-explaining-2026-10-13.md](iteration-6-facts-before-explaining-2026-10-13.md)) can go in without the trim that [iteration-6-trimmed-2026-10-13.md](iteration-6-trimmed-2026-10-13.md) needed. This run checks it against main under a pre-registered merge rule.

## The change (branch `iter/6-untrimmed`, not merged)

- **Iteration 6's RULES.md wording**, applied by hand onto main bdda0b6 (the original branch's rebase conflicts): the Intent sentence on detail and follow-ups, and the "Explaining" edit.
- **Nothing else.** No trim of the Tools paragraph, and nothing from iteration 6b.

## Setup

- **Models.** Tier 1. DeepSeek `deepseek-flash` runs the orchestrator (`--fallback-provider none`) and the simulated user. Workday is kept off Google.
- **Arms.** main bdda0b6 against fix 9466819. The eval harness is main's, unchanged.
- **Runs.** explain.json ×5 per arm (70 detail lines each), judged by iteration 6's Sonnet fact judge (whole screens). Workday ×5 per arm, with code checks and then the Sonnet segment judge on both arms.
- **Pre-registration.** The metrics and the merge rule were committed and pushed before any comparison run ([STATUS.md](../bench/results/2026-10-14-iter6u/STATUS.md)). That included a rule for held-out failures caused by a provider error: rerun once and note it. No such rerun was needed. Explain CIs come from a per-run cluster bootstrap. Raw data: [../bench/results/2026-10-14-iter6u/](../bench/results/2026-10-14-iter6u/).

## Results

| Explain, per detail line (70 per arm) | main | fix | fix − main (95 % CI) | gate |
|---|---|---|---|---|
| **Restating follow-ups (primary)** | 49 % | 40 % | **−9 pp (−24 to +7)** | fails: needs the CI below 0 |
| Any wrong fact | 27 % | 11 % | −16 pp (−29 to −3) | passes (down beyond the CI) |
| No fresh look | 83 % | 43 % | −40 pp (−54 to −26) | |
| New facts per detail answer | 1.33 | 1.53 | +0.20 (−0.23 to +0.63) | |
| Detail-turn median / p90 | 4.7 / 8.3 s | 6.1 / 9.2 s | | 0 fallbacks on detail turns in both arms |

| Workday ×5 (35 runs per arm) | main | fix | fix − main (95 % CI) | gate |
|---|---|---|---|---|
| **Segment Understood (Sonnet)** | 375/419 (89.5 %) | 367/421 (87.2 %) | **−2 pp (−7 to +2)** | fails: needs the lower bound above −5 pp and the point at −2 pp or better |
| Safe, run level (code checks) | 34/35 | 32/35 | −6 pp (−20 to +7) | passes |
| Segment Safe (Sonnet) | 97.3 % | 98.3 % | | passes |
| Held out: explain-deep | 5/5 | 5/5 | | passes |
| Held out: adversarial-afternoon | 5/5 | 5/5 | | passes |
| Missed news | 11 % | 16 % | +5 pp (−2 to +11) | |
| Actions never told | 0/89 | 3/89 | +3 pp (−1 to +9) | |
| Low effort | 23/35 | 23/35 | 0 pp (−21 to +21) | |
| Brain fallback runs | 1 | 5 | | |

- **Fix's fallbacks.** Four of the five yielded to a user turn (long-haul ×2, scale-ten ×2), and one was a turn timeout. Fix's turns are slower (the detail-turn median is +1.4 s), which may leave more turns to be overtaken by the user. This is not tested.
- **Fix's run-level Safe failures** are the three actions never told. Main's one failure is an unintended `y` sent to etl-codex in scale-ten.

**Beside the gate.** This fix arm reproduces iteration 6's own untrimmed fix arm (d52defc): restates 44 → 40 % and wrong facts 13 → 11 %, both n.s. Against the trimmed fix arm, wrong facts are −14 pp (−28 to −1). That supports yesterday's suspicion that the Tools trim cost accuracy. Main's restating rate moved from 57 % yesterday to 49 % today (n.s.), so the target shrinks from run to run.

## Decision

**Not merged.** The pre-registered rule fails on two items:

- **The primary.** Restating follow-ups fell 9 pp. The CI (−24 to +7) does not exclude 0. Four comparisons of iteration 6 with main now point the same way: −16 pp (iteration 6, CI −31 to −1, where the gate was the composite), −20 pp (6b, exploratory), −9 pp (trimmed) and −9 pp (this run).
- **Segment Understood.** It is −2 pp (−7 to +2). The lower bound is below −5 pp and the point estimate is just below −2 pp.

Wrong facts improved beyond the CI (−16 pp). Safe and both held-outs pass. The branch `iter/6-untrimmed` is kept.

## Next

1. **Pooling.** The four main-vs-iteration-6 comparisons all point the same way. A pooled estimate across the explain runs would have a CI near ±8 pp. It would be exploratory, because the arms differ (base, trim, rubric). The CEO could decide on that pooled estimate together with today's wrong-facts gain.
2. **Primary metric.** "Restates" is a coarse judge flag, and main's rate drifts by 8 pp between days. On the measured outcomes, iteration 6's gain shows up in wrong facts and fresh looks, not in restating.
3. **Workday Understood and fallbacks.** These need a look at the segments that fix lost (the sign-off and the scale-ten beats) before another run.

## Spend

- **DeepSeek.** 25.27 → 7.30 CNY (17.97 CNY; the runs' own estimate ≈ $9.48 at list prices) for 60 explain runs and 70 workday runs. No other API spend.
- **Sonnet.** The local Claude subscription ran 62 fact-judge calls and 1,035 segment-judge calls. The 5-hour session went from 100 % to 11 % left, the weekly from 36 % to 29 %.
