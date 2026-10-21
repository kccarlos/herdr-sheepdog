# North star: A vs B

- A: /Users/kc/Documents/herdr-sheepdog/.claude/worktrees/agent-a828dcb9b89e08095/docs/dev/bench/results/2026-10-21-spawn/main/merged (18 runs, 18 graded)
- B: /Users/kc/Documents/herdr-sheepdog/.claude/worktrees/agent-a828dcb9b89e08095/docs/dev/bench/results/2026-10-21-spawn/spawn/merged (18 runs, 18 graded)

Difference = B − A with its 95 % CI (Newcombe's hybrid score interval for rates, an independent bootstrap for means). A CI that spans 0 is no evidence of a difference.

| Metric | A | B | B − A | 95 % CI |
|---|---|---|---|---|
| Understood | 7 / 18 (39 %, CI 20–61 %) | 8 / 18 (44 %, CI 25–66 %) | +6 pp | -24 to +34 pp |
| Done | 18 / 18 (100 %, CI 82–100 %) | 18 / 18 (100 %, CI 82–100 %) | +0 pp | -18 to +18 pp |
| Done, claim part (judge) | 18 / 18 (100 %, CI 82–100 %) | 17 / 18 (94 %, CI 74–99 %) | -6 pp | -26 to +13 pp |
| Done, claim part (phrases) | 12 / 17 (71 %, CI 47–87 %) | 10 / 17 (59 %, CI 36–78 %) | -12 pp | -40 to +19 pp |
| Low effort | 12 / 18 (67 %, CI 44–84 %) | 11 / 18 (61 %, CI 39–80 %) | -6 pp | -34 to +24 pp |
| Safe | 14 / 18 (78 %, CI 55–91 %) | 14 / 18 (78 %, CI 55–91 %) | +0 pp | -27 to +27 pp |
| Understood (segments, cluster bootstrap) | 196 / 215 (91 %, CI 87–95 %) | 200 / 220 (91 %, CI 88–94 %) | -0 pp | -5 to +5 pp |
| Turns to goal | 12.5 (95 % CI 12.1–13.0, n 15) | 12.8 (95 % CI 12.1–13.5, n 14) | +0.25 | -0.58 to +1.15 |
| Time to goal | 93.9 s (95 % CI 84.7–104.8, n 15) | 95.2 s (95 % CI 87.3–103.3, n 14) | +1.27 | -11.88 to +13.50 |
| Unmatched claims (heard) | 6 / 62 (10 %, CI 5–20 %) | 8 / 79 (10 %, CI 5–19 %) | +0 pp | -11 to +10 pp |
| Actions never told | 2 / 43 (5 %, CI 1–15 %) | 1 / 49 (2 %, CI 0–11 %) | -3 pp | -14 to +7 pp |
| Missed news | 20 / 121 (17 %, CI 11–24 %) | 14 / 124 (11 %, CI 7–18 %) | -5 pp | -14 to +4 pp |

Pairwise judging (A vs B on depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
