# Iteration 6 tier-1 runs (2026-10-13): facts before explaining

CEO product note: when the user asks for more detail ("tell me more", "explain exactly what happened", "具体讲讲"), the orchestrator gathers the facts first (read_screen / session, or investigate) and thinks before explaining; the voice may say "let me look" first. Change type: prompt (RULES.md), only if the baseline shows a gap.

Setup: tier 1, brain DeepSeek `deepseek-flash`, `--fallback-provider none`, simulated user DeepSeek `deepseek-flash`, judge fake, then Sonnet (`claude -p --model sonnet`) where the decision needs it. Chattiness chatty. Main = d52defc.

## Step 1: baseline (main only), pre-registered before the first run

Runs: `explain.json` (4 explain worlds + 2 simple controls) ×3 on main.

Metric A, **detail answered without a fresh look** (code check over the events): a detail turn is a scripted line labelled then / more / why / example in the scenario's explain block. It counts as "no fresh look" when the orchestrator turn that answers it calls none of read_screen, run_cli, investigate, recall. Reported per turn kind and pooled, Wilson 95 % CI.

Metric B, **facts right on detail turns**: the Sonnet explain rescore (follow-up depth: new content vs rephrasing) and per-answer key points; plus a check by reading the answers against the world (screen and scripted answer) for stated facts the world does not hold.

Gap rule: if detail turns are already answered after a fresh look in nearly all cases (no fresh look ≤ 10 %), or the answers given without a look are as right and as deep as those with one, there is no gap: stop and report.

## Log
- Baseline b1 (main ×3 on explain.json, 18 runs, ≈ $0.25 list): detail turns (then/more/why/example) answered without a fresh look 34/42 = 81 % (CI 67–90 %); the first "explain" turn always looks (12/12). Sonnet fact judge (`factjudge.py`, one call per run, blind): detail answers with at least one wrong or unsupported fact 16/42 = 38 % (25–53 %), restating earlier answers 24/42 = 57 %, mean new world facts 1.07 per answer; the first explain answer is wrong in 1/12. Gap exists: follow-ups mostly re-use context, restate, and add invented detail (a 25-minute re-run became "a quarter of an hour"; "depending on how loaded the machine was" is nowhere in the world). The extra facts the investigation holds (200 runs, 17 failures, since a change three weeks ago) were reached once. Output: `baseline-main-b1.txt`, `facts-main.jsonl`.
