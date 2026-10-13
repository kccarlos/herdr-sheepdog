# Harness iteration 1: "never compress" voice rule (nc), 2026-10-12

Branch `iter/1-nc`. One labelled change (prompt): arm `nc` adds one paragraph to `prompts/assistant.md`:

> Never compress or shorten the orchestrator's spoken text: say its substance fully in your own spoken words (reasons, examples, numbers); keep it short only when the briefing itself is short.

Arm `a` is main's prompts (871fe38). Prompt snapshots: [prompt-arms/2026-10-11/](prompt-arms/2026-10-11/). Raw data, scripts and run log: [../bench/results/2026-10-12-nc/](../bench/results/2026-10-12-nc/) (`STATUS.md`).

## Decision: merged

CEO rule for this iteration: merge `nc` unless segment Understood or Safe is clearly worse, or the voice-share effect of 2026-10-11 reverses. None of the three happened:

- Segment Understood **71 % vs 66 % (+5 pp, CI −5 to +15)**: not worse.
- Safe: run level 38/42 in both arms; segments 154/162 (95 %) vs 153/162 (94 %).
- Voice share of the orchestrator's key points **0.50 vs 0.38 (+0.13, CI +0.01 to +0.25)**: the 10-11 effect (+0.29, CI +0.01 to +0.56) replicates, smaller, with the production DeepSeek brain.

`prompts/assistant.md` now carries the paragraph. `assistant-gptlive.md` (GPT-Live voice) was not changed and not measured.

## Setup

- Tier 2, production config: brain DeepSeek `deepseek-flash` (Vertex `gemini-3.5-flash` as failover), voice Vertex Live `gemini-3.8-live`, Vertex TTS, chattiness `chatty`, simulated user Vertex `gemini-3.5-flash`. Scratch daemons on :7911/:7912, ≤ 2 concurrent. No adversarial scenario was sent to Google.
- Per arm 42 runs: explain category (4 explain scenarios + 2 controls) ×5 = 30, 6 non-adversarial workday scenarios ×2 (`--personas rotate`) = 12. Chunks interleaved a / nc.
- Judge: Claude Sonnet, `bench rejudge --orchestrator` (run level, orchestrator items, segments), then `bench report --rescore-explain`. Binary built from this branch (code = main 871fe38).
- Spend: Vertex bench meter ≈ $19.77 (a $9.48, nc $10.29), under the $25 cap. DeepSeek 42.5 → 39.3 CNY during the runs (floor 32).
- Direction: every difference is **nc − a** (`bench northstar --from a --vs nc`).

## North star (42 runs per arm; segment rows from the 12 workday runs)

| Metric | a | nc | nc − a (95 % CI) |
|---|---|---|---|
| **Understood, segments** | 88/134 (66 %, 59–73) | 95/134 (71 %, 64–78) | **+5 pp (−5 to +15)** |
| Understood, run level | 8/42 (19 %) | 6/42 (14 %) | −5 pp (−21 to +12) |
| **Safe, run level** | 38/42 (90 %) | 38/42 (90 %) | **0 pp (−14 to +14)** |
| **Safe, segments** | 153/162 (94 %, 91–98) | 154/162 (95 %, 91–99) | overlapping |
| Done (required actions) | 11/12 | 12/12 | +8 pp (−17 to +35) |
| Low effort | 23/42 (55 %) | 24/42 (57 %) | +2 pp (−18 to +23) |
| Unmatched claims (heard) | 5/36 (14 %) | 12/46 (26 %) | +12 pp (−6 to +28) |
| Actions never told | 3/27 (11 %) | 7/30 (23 %) | +12 pp (−8 to +31) |
| Missed news | 25/80 (31 %) | 22/81 (27 %) | −4 pp (−18 to +10) |
| Time to goal | 121 s | 178 s | +57 s (−33 to +146) |

Segment items (a → nc): answered 76 → 82 %, length_fits 82 → 83 %, **no_hallucination 89 → 82 %** (CIs 83–94 / 75–89), facts_current 89 → 88 %, said_vs_done 25/28 → 27/27, ear_friendly, language, right_agent, no_unintended_action flat. Run-level Understood is low in both arms because the explain scenarios' depth and padding items almost always fail; use the segment row. Full table: `northstar-a-to-nc.md` (`.json`).

## Explanation (explain scenarios, 20 runs per arm, controls excluded, bootstrap over runs)

| Measure | a | nc | nc − a (95 % CI) |
|---|---|---|---|
| **Voice share of the orchestrator's points** | 0.38 | 0.50 | **+0.13 (+0.01 to +0.25)** |
| Voice coverage of the world's points | 0.22 | 0.28 | +0.06 (−0.02 to +0.14) |
| Orchestrator coverage | 0.59 | 0.56 | −0.03 (−0.15 to +0.10) |
| Voice follow-up depth pass | 0.66 | 0.51 | −0.14 (−0.28 to 0.00) |
| Voice ear comprehension | 0.93 | 0.93 | 0.00 |
| Voice words per answer, en | 33.9 | 43.6 | +9.7 (+2.9 to +16.0) |
| Voice words per answer, zh | 56.8 | 55.1 | −1.7 (−9.5 to +6.3) |

Table: `explain-coverage-a-to-nc.md`.

## Watch items (not gates)

- **Voice follow-up depth** drops 0.66 → 0.51; the CI touches 0. On 10-11 (Gemini brain) it went the other way (+0.10, n.s.). With the voice saying more in the first answer, "why / example / then?" follow-ups may get a repeat instead of new depth.
- **no_hallucination** 89 → 82 % and unmatched claims 14 → 26 % (both n.s.): longer retellings give more room for a wrong detail. This is the pattern of the standing "length" lesson; the next iteration's runs should keep an eye on it.
- Voice answers are longer in English (+10 words); Chinese is unchanged.

## Caveats

- n: 12 workday runs (134 request segments) and 20 explain runs per arm. With ~25 comparisons, one CI excluding 0 could be chance; the voice-share effect is the pre-registered one and replicates 10-11.
- `workday-scale-ten` r1 got no Sonnet segment verdicts in either arm: the segment judge kept answering n/a for `right_agent` on beat 13, which the parser rejects (failed on all 2–3 rejudge attempts per arm). Its run-level and orchestrator items were judged (`--segments=false`) and its fake-judge segments were removed, so segment rows use 11 runs per arm. Worth fixing in the harness (allow n/a for `right_agent` on a beat that names no agent).
- **Stale binary in the repo**: `./sheepdog` at the repo root is a binary committed in #166 (built from a0dbf05, 2026-10-06, no segment judging, pre-#189 rubric). The first judging pass used it by mistake and was discarded (`*-stale-binary.log`); the reported numbers come from a binary built from this branch. The live runs were not made with it (their records carry segment verdicts). The file should be removed from git.
- The orchestrator is sampled per run; its coverage differs by −0.03 between arms here.
