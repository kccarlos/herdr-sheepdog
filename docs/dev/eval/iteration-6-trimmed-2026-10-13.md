# Harness iteration 6 (trimmed): facts before explaining on current main, 2026-10-13

The CEO approved merging iteration 6 ([iteration-6-facts-before-explaining-2026-10-13.md](iteration-6-facts-before-explaining-2026-10-13.md)) once it was shown to have no side effects (2026-10-13: "验证没有副作用后就合并第6轮"). On current main, iteration 6's RULES.md change no longer fits the prompt-size test, so it needs a trim. This run checks the trimmed version against main under a pre-registered merge rule.

## The change (branch `iter/6-trimmed`, not merged)

- **Iteration 6's wording**, applied by hand onto main b0c5716 (the branch's rebase conflicts): the Intent sentence on detail and follow-ups, and the "Explaining" edit.
- **The trim.** Main plus iteration 6 is 5,047 tokens in `TestAnnouncePromptBudget` (limit 4,975). Four clauses of the Tools paragraph were removed. Each repeats a tool declaration in `internal/brain/tools.go` word for word:
  - read_screen session: "its last message for a human in full, its last instruction, the files it edited";
  - run_cli: "phrase the question with the context the CLI needs, it has not heard this conversation";
  - recall: "last results, last tasks, last questions, notes, earlier transcript lines";
  - investigate: "and the answer reaches the user on its own when the job returns".
- **Not touched.** Record and Fallback stay as they are. Nothing was taken from iteration 6b. The result is 4,969 tokens, which leaves 6 tokens of headroom.

## Setup

- **Models.** Tier 1. DeepSeek `deepseek-flash` runs the orchestrator (`--fallback-provider none`) and the simulated user. Workday is kept off Google.
- **Arms.** main b0c5716 against fix 85464f7. Main's eval harness as is, without 6b's `offer_yes`.
- **Runs.** explain.json ×5 per arm (70 detail lines each), judged by iteration 6's Sonnet fact judge (whole screens). Workday ×5 per arm, with code checks and then the Sonnet segment judge on both arms.
- **Pre-registration.** The metrics and the merge rule were committed before any comparison run ([STATUS.md](../bench/results/2026-10-13-iter6t/STATUS.md)). CIs on explain are a per-run cluster bootstrap. Raw data: [../bench/results/2026-10-13-iter6t/](../bench/results/2026-10-13-iter6t/).

## Results

| Explain, per detail line (70 per arm) | main | fix | fix − main (95 % CI) | gate |
|---|---|---|---|---|
| **Restating follow-ups (primary)** | 57 % | 49 % | **−9 pp (−26 to +9)** | fails: needs the CI below 0 |
| Any wrong fact | 20 % | 26 % | +6 pp (−8 to +20) | passes: not up beyond the CI |
| No fresh look | 80 % | 51 % | −29 pp (−38 to −18) | |
| New facts per answer | 1.17 | 1.51 | +0.34 (−0.19 to +0.92) | |
| Detail-turn median / p90 | 4.6 / 8.2 s | 6.1 / 9.0 s | | 0 fallbacks in both arms |

| Workday ×5 (35 runs per arm) | main | fix | fix − main (95 % CI) | gate |
|---|---|---|---|---|
| **Segment Understood (Sonnet)** | 375/417 (90 %) | 379/418 (91 %) | **+1 pp (−3 to +5)** | passes |
| Safe, run level (code checks) | 32/35 | 33/35 | +3 pp (−11 to +17) | passes |
| Segment Safe (Sonnet) | 98 % | 98 % | | passes |
| Held out: explain-deep | 5/5 | 5/5 | | passes |
| **Held out: adversarial-afternoon** | 5/5 | 4/5 | | fails by the count (see below) |
| Missed news | 18 % | 16 % | −2 pp (−9 to +5) | |
| Actions never told | 7/95 | 2/91 | −5 pp (−12 to +1) | |
| Low effort | 28/35 | 25/35 | −9 pp (−28 to +11) | |
| Brain fallbacks | 1 | 2 | | |

- **The adversarial-afternoon failure.** The failing fix run fell back on a DeepSeek model error: a `read_screen` call whose arguments were not a JSON object. It was not an unsafe action, and segment Safe is equal in both arms. The pre-registered rule counts failures as recorded, so it is reported as a regression.

**Side-effect check of the trim, tier 1.** The fix arm was compared with iteration 6's own untrimmed fix arm: same judge, same rubric, base d52defc.

- Wrong facts are 13 % untrimmed and 26 % trimmed: +13 pp, CI +0.4 to +26, just beyond the CI.
- Restates are 44 % and 49 % (+4 pp, n.s.). No fresh look is 41 % and 51 % (+10 pp, −1 to +21).
- The two main arms (then and now) agree within 3 pp on every metric, so the base itself moved little.
- The wrong claims are embellishments of the same kind as before: invented causes, an example presented as real, "a quarter of an hour and a half". There is one "that is all it reported, the rest was said".
- 6b's b arm, iteration 6 plus a larger trim and B, also showed more wrong facts than iteration 6 alone (+13 pp).
- On the workday measure the trim shows no side effect: segment Understood is +1 pp and Safe is equal.

## Decision

**Not merged.** The merge rule fails on two items:

- **The primary.** Restating follow-ups fell 9 pp. The CI (−26 to +9) does not exclude 0. Iteration 6 measured −16 pp on d52defc, and 6b's exploratory comparison measured −20 pp. This run reproduces the direction, not the size.
- **One held-out.** adversarial-afternoon went 5/5 → 4/5 through a provider model error.

Segment Understood (+1 pp, −3 to +5), Safe and wrong facts against main pass. The mechanism holds: no fresh look −29 pp. The branch `iter/6-trimmed` is kept.

## Next

1. **Is the trim costing accuracy?** Wrong facts against untrimmed iteration 6 are +13 pp, just beyond the CI. Two later runs point the same way: this one, and 6b's b arm, which had a larger trim. One option is a run of iteration 6 with room made elsewhere: a test-budget raise approved by the CEO, or trimming a non-Tools section. It would tell whether the Tools clauses carry weight at the moment the model answers.
2. **n.** With ×5 per arm, the restates effect has drifted from −9 to −20 pp across three runs, inside overlapping CIs. Pooling the three main-vs-iteration-6 comparisons is possible, but the arms and rubrics differ, so it would be exploratory.
3. **The model error.** A `read_screen` call with non-JSON arguments failed a held-out run. A tolerant argument parser (information or rail, not prompt) would remove this noise from the gate.

## Spend

- **DeepSeek.** 23.63 → 5.35 CNY (18.28 CNY; the runs' own estimate ≈ $9.5 at list prices) for 60 explain runs and 70 workday runs. Workday cost about 8.6 of that. No other API spend.
- **Sonnet.** The local Claude subscription ran 60 fact-judge calls and 1,033 segment-judge calls (520 + 513). The 5-hour session went from 100 % to 11 % left, the weekly from 44 % to 37 %.
