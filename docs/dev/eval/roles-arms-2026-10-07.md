# Roles arms r / rl vs the chatty baseline, 2026-10-07

Question: does #155's unified role framing ("one orchestration agent operating
the user's herdr", "the user" instead of "the CEO", no sheepdog metaphors) cost
understanding once its length edits are taken out, and does a gentle length
rule help? Measured at the new default chattiness `chatty` (#178).

## Arms

- **A0**: main (a378f23), prompts as on main.
- **r**: #155's role framing and wording, main's length and brevity rules
  (prompts in [prompt-arms/r](prompt-arms/r)).
- **rl**: r plus a gentle length rule (answer first, then reasons only when
  they help; "more detail" adds new content) ([prompt-arms/rl](prompt-arms/rl)).
  r and rl ran on #155's binary (f063ba8, "the user" in the Go strings) with
  the supervisor's chattiness fallback set to `chatty`.

## Setup

Two Claude Code cloud sessions (Sonnet), tier 1 only: workday suite ×5 (all 7
scenarios, 35 runs per arm), `--personas rotate`, DeepSeek `deepseek-flash` as
brain and simulated user, live runs with the fake judge, then
`bench rejudge --judge claude --judge-model sonnet --skip-run-level`
(segment level, ~515 Sonnet calls per arm). Chattiness `chatty` (built-in
default; checked in the r/rl event logs). Spend: DeepSeek ≈ $3.8 (A0) and
≈ $7.8 (r + rl); judge on the Claude subscription. Comparisons run locally
with `sheepdog bench northstar --from a0 --vs <arm>`.

## Result

| Metric | A0 | r | rl | A0 − r (CI) | A0 − rl (CI) |
|---|---|---|---|---|---|
| **Understood (segments)** | 92 % (89–95) | 89 % (85–92) | 88 % (84–91) | +3 pp (−1 to +8) | +4 pp (0 to +9) |
| Done (runs) | 63 % | 69 % | 77 % | within noise | within noise |
| Low effort (runs) | 74 % | 71 % | 83 % | within noise | within noise |
| Safe (runs) | 77 % | 83 % | 69 % | within noise | within noise |
| Missed news | 19 % | 18 % | 18 % | 0 pp | 0 pp |

Run-level Understood is 100 % for every arm because the live runs used the
fake judge; it is not a result. Segment items A0 / r / rl: answered
95 / 95 / 96 %, length_fits 94 / 91 / 90 %, no_hallucination 89 / 88 / 87 %.
Safe (judge, segments) is 99 % for all three. Run-level CIs are ±15–30 pp at
35 runs, so Done / Low effort / Safe differences say nothing.

## Decision (CEO, 2026-10-07)

- **rl dropped**: no gain on anything measured, Safe −9 pp vs A0 (n.s.).
- **r merged** despite −3 pp segment Understood: the difference is not
  significant (CI spans 0) and one unified role framing is wanted for the
  product. Merged as the unified-roles PR that replaces #155 (prompts = arm r,
  #155's Go-string changes, chatty default kept).

## Files

- Raw and merged results: [../bench/results/2026-10-07-chatty](../bench/results/2026-10-07-chatty)
  (`a0/`, `r/`, `rl/`, per-chunk raw in `chunks/`, `STATUS.md` and
  `STATUS-roles.md` from the cloud sessions).
- North-star blocks: `northstar.md` (A0), `northstar-r.md`, `northstar-rl.md`;
  comparisons `northstar-a0-vs-r.md`, `northstar-a0-vs-rl.md`,
  `northstar-r-vs-rl.md`.
- Note: `STATUS-roles.md` compares against the step-4 A0 (`normal`
  chattiness); use this report's A0 column (chatty) instead.
