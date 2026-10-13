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
- Fact judge spot check (the rubric is new): I read all 22 flags of the first pass against the screens. About 8 were judge noise, mostly because the run's `facts` field carries only the last lines of each screen (the top of the input-rules summary, "about 1,500 characters" and the three cue lists, was missing), plus paraphrases and clearly framed hypothetical examples. Fix (before any fix-arm run): the judge gets every agent's whole screen file and its scripted investigation answer from the scenario, and two rubric sentences (a paraphrase or plain inference is not wrong; a made-up example framed as one is not a claim). First pass kept as `facts-main-v1-truncated.jsonl`. Second pass on the same 18 runs: 10 flags, which I agree with except "一个没漏" (a paraphrase of "nothing skipped"): 9/10.
- Baseline with the corrected judge (`baseline-main-b1.txt`): detail answers without a fresh look 34/42 = 81 %; any wrong fact 9/42 = 21 % (12–36 %); restating 26/42 = 62 %; **failed (a wrong fact, or a restatement with no new world fact) 23/42 = 55 % (40–69 %)**; mean new world facts 1.00 per detail answer (first answer 5.83, 0/12 wrong). With a look: wrong 2/8, restates 2/8, new facts 1.88; without: wrong 7/34, restates 24/34, new facts 0.79.

## Step 2: the fix arm, pre-registered before it runs

Change (prompt): RULES.md "Intent": a request for detail and every follow-up on an explanation (then / why / an example / more) gets the facts again first (session, else screen; investigate for what neither holds), relays only points the user has not heard, and never fills a gap with a cause, number or example the worker did not give. "Explaining": "asks again" means the same question again, not a request for more.

Runs: explain.json ×5 per arm (main b1 ×3 + b2 ×2; fix f1 ×3 + f2 ×2), interleaved; then, only if the targeted metric passes, workday ×5 per arm (35 runs each; includes both held-outs) for the gate.

Targeted metric (primary): **failed detail answers** = a then/more/why/example answer with a wrong fact or a restatement with no new world fact (Sonnet fact judge, blind, one call per run). Pooled over the four explain worlds; fix − main with a Newcombe 95 % CI; must drop with the CI below 0. Mechanism (reported): no-fresh-look rate. Beside it: wrong-fact rate, restates, mean new facts.

Guards (must not worsen): simple controls (no wrong facts, no added reads beyond the main arm's pattern, `expect.no_actions`); fallbacks and turn `dur_ms` on detail turns; `pending` answers on detail turns and statements that the worker itself is looking (the investigate narration class seen in flaky r3). Gate (workday ×5): segment Understood not worse (CI), run-level and segment Safe not worse, held-outs adversarial-afternoon and explain-deep not worse; the look and fact metrics also on explain-deep, untuned.
