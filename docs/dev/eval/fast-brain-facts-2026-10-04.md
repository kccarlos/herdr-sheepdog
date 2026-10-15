# Fast brain toward facts: prompt v5 A/B (2026-10-04 night)

Item 1 of the overnight plan (docs/dev/handover-2026-10-04.md). PR #141.
Design: docs/dev/design/dual-brain.md ("The fast-brain prompt", v5, and
the briefing payload). Raw reports, the analysis script and its output:
[`fast-brain-facts-2026-10-04/`](fast-brain-facts-2026-10-04/).

## Setup

- Tier 2 (`sheepdog bench run --tier 2`), Vertex brain
  `gemini-3.5-flash` (DeepSeek failover), Vertex Live
  `gemini-3.8-live`, judge and simulated CEO Vertex
  `gemini-3.1-flash-lite`, three scratch daemons (7790–7792).
- **Both arms run the same binary** (this branch, with all the new
  information: briefing age and agent state, `not_verified`, unvoiced
  facts, roster fix, pinned names, spawn check). The arm is the prompt
  only: `--prompts-dir` with main's `assistant.md` (v4) as an override,
  or none (the embedded v5). So this measures the prompt, not the
  information.
- Interleaved: the six new `facts` cases (from the phone-test failures)
  old, new, old, new, old, new (3 runs each per arm); then the existing
  36-scenario suite once per arm in ABBA halves.
- v5 named only Chinese scripts in the language line, and the voice
  then answered English speakers in Chinese (below). v5.1 restores "The
  shepherd may speak English or Chinese" and keeps the script request;
  it was run afterwards (facts ×3, suite ×1), not interleaved. The PR
  ships v5.1.
- Hand-off and self-answer are read from the events per CEO line: a
  `to_supervisor` call before the next line is a hand-off; bot speech
  with no call is a self-answer (`stop()` counts as self). Answer
  latency is the end of the CEO's line to the first speech of the
  answer (for a hand-off, the first speech after the tool result).
  Self-answers were graded for grounding by Vertex `gemini-3.5-flash`
  against what the voice had been told (pushes, tool results, its own
  earlier lines); the grader is strict (it marks "Codex" for
  "bench-codex" as ungrounded).

## Results

| | v4 (main) | v5 | v5.1 (shipped) |
|---|---|---|---|
| **facts cases** pass | 9/18 (50 %) | 10/18 (56 %) | 11/18 (61 %) |
| hand-off rate | 38/51 (75 %) | 46/51 (90 %) | 46/51 (90 %) |
| self-answers grounded | 9/12 | 5/5 | 5/5 |
| answer latency p50: all / self / hand-off | 5.05 / 1.22 / 5.63 s | 5.30 / 1.07 / 5.45 s | 4.88 / 1.10 / 5.50 s |
| **existing suite** pass (1 run each) | 21/36 (58 %) | 17/36 (47 %) | 21/36 (58 %) |
| hand-off rate | 58/78 (74 %) | 50/77 (65 %) | 54/82 (66 %) |
| self-answers grounded | 16/20 (80 %; 18/20 without the "nickname" nitpicks) | 26/27 | 19/28 (68 %; 23/28 without them) |
| answer latency p50: all / self / hand-off | 5.06 / 1.19 / 5.54 s | 4.69 / 1.17 / 5.29 s | 4.52 / 1.20 / 5.44 s |
| English-speaker lines spoken in Chinese (all runs) | 0/114 | **6/101** | 0/108 |
| Chinese lines with Traditional characters | 0/23 | 0/25 | 1/28 |

Per facts case (pass, 3 runs each, v4 → v5 → v5.1):

| Case | v4 | v5 | v5.1 | Notes |
|---|---|---|---|---|
| contradiction-linecount (85k vs 110k) | 3 | 3 | 3 | "which is right, why": **v4 answered it itself 3/3, v5 and v5.1 handed it off 3/3**; v4 got away with it because both briefings carried the counting method (the phone test's did not) |
| history-first | 2 | 2 | 2 | "what did we talk about first": v4 answered itself 2/3, v5 handed off 2/3, v5.1 3/3 |
| stale-conclusion-zh | 1 | 0 | 1 | every arm told "still working" while it worked and the cause after the finish; the failures are judge items (right_agent, rubric wording) plus one slow-brain misreading of the screen's older history (it reported an earlier "Add" fix) |
| ambiguous-rewind | 0 | 1 | 3 | **prompts sent to workers on "rewind it": v4 4, v5 2, v5.1 0**; in the failing runs the voice paraphrased "it" as both agents in `reading` and the slow brain rewound both |
| zh-simplified | 3 | 3 | 2 | the v5.1 fail is a judge item; no Traditional characters in any arm |
| timeout-not-verified | 0 | 1 | 0 | no arm claimed a verification after the timeouts ("the check is still running", true: an investigation was started); the fails are the default `facts_current`/`no_hallucination` items judging "still running" against "checks never finish". `brain.tool_timeout` hit in all 9 runs |

## Reading

- **The behaviour change happened.** On the phone-test cases the voice
  handed off 90 % of lines instead of 75 %, its remaining self-answers
  were all grounded, and the one action-shaped failure that matters
  most — workers rewound on an ambiguous "it" — went from 4 prompts
  sent (v4) to 0 (v5.1). Pass rates move 50 → 61 % on 18 runs per arm:
  direction right, not significant on its own.
- **The cost is latency on the lines that are handed off**, about
  1.1–1.2 s for a self-answer against 5.3–5.6 s for a hand-off. Overall
  p50 barely moves (5.05 → 4.88 s on facts, 5.06 → 4.52 s on the suite)
  because most lines were already handed off and the new arms ran a
  little faster; the honest per-line trade is ≈ 4 s more for each
  line that moves from self to hand-off.
- **The suite is unchanged within noise** (v4 21/36, v5.1 21/36 on one
  run each). The v5 dip to 17/36 came mostly from judge items on cut
  or echoed lines, plus two real failures: the language regression
  (status-anything-for-me opened in Chinese to an English CEO) and
  deep-ask-detail (handed off, the slow brain prompted the worker for
  its README points — an action — and the answer never came).
- **The suite still shows self-answers v5 tells the voice not to give**:
  status questions answered from the connect announcement ("Not now,
  just tell me what everyone is working on"), "先都别动" answered
  "nothing to stop". The prompt narrows them, it does not end them.
- **Language:** naming only Chinese scripts in the rule made the voice
  speak Chinese to English speakers (6/101 lines in v5, including a
  first announcement); v5.1's wording is back to 0/108. Traditional
  characters did not appear in any arm with either prompt (the phone
  test's Traditional output was not reproduced by the bench; the
  language setting, item 2, is the structural fix).

## Tier 1

The six cases as text sims (`sheepdog eval sim`, Vertex
`gemini-3.5-flash`, one run each): 4/6 on the first run, both failures
judge items (the rewind run asked "which agent", the judge wanted an
action; the timeout run said a check was running); 6/6 after the rubric
was made explicit (`reports/tier1-first.md`).

## Spend

Bench ≈ $7.6 (114 tier-2 runs), tier 1 ≈ $0.15, grounding grader and
TTS renders < $0.2. Total ≈ $8 of the $20 cap.

## Open

- The slow brain acting on two agents for an ambiguous "it" (v4 and
  v5 runs) is steered by the voice's `reading`; v5.1 asks first, but the
  slow brain's own rule is in RULES.md, outside this item.
- deep-ask-detail: after a hand-off the slow brain prompted the worker
  for detail it could have read; check with more runs.
- `assistant-gptlive.md` was not changed (GPT-Live is not the default
  voice).
- The suite numbers are one run per arm; a 3× interleaved suite run
  would separate v5.1 from v4 properly.
