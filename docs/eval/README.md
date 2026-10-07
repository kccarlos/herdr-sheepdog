# Eval index

Every eval experiment run on sheepdog so far, oldest first. Read the row for
your topic and its report before proposing an eval or a prompt change: most
obvious ideas have been measured, and several "improvements" lost.

How the suite works: [../bench/README.md](../bench/README.md) (tiers 1–3,
workday sessions, north star, `bench rejudge`, `northstar --vs`). The
default gate today: tier 1 workday ×5 (35 runs), DeepSeek `deepseek-flash`
brain and simulated user, chattiness `chatty`, Claude Sonnet segment judge,
compared on **segment Understood** with `sheepdog bench northstar --vs`;
merge only without a Safe regression.

## Standing lessons

- **Noise.** 35 runs give ±15–30 pp on run-level Done / Low effort / Safe;
  only segment-level rates (~420 segments, cluster bootstrap) separate
  arms by 3–5 pp. A difference whose CI spans 0 is not a result.
- **Judge.** Claude Sonnet via `claude -p` (kappa ≈ 0.72 against
  ground truth). Never flash-lite (kappa 0.45 on the first calibration);
  Haiku 5.5 is close but did not pass. Never ask the CEO to label by
  default; a ~10-item spot check when a rubric changes.
- **Length.** Every "say more / fit the content" prompt change so far
  lowered segment Understood or `no_hallucination`; short by default has
  held up.
- **Safety.** Status and `ask` wording changes can make the orchestrator
  approve `rm -rf ./data` in `workday-scale-ten`; any such change needs
  ≥ 15 scale-ten runs.
- **Chattiness.** Evals before 2026-10-07 ran at `normal`, whose code
  ceiling dropped about half the finishes; the default (and the CEO's
  setting) is now `chatty`. Compare only arms run at the same level.
- **Outputs live in the repo** (`docs/bench/results/<date>-…`), never in a
  temp dir.

## Experiments

| Date | Experiment | Question | Method | Result (one line) | Decision | Report · raw |
|---|---|---|---|---|---|---|
| 10-03 | Live eval campaigns A, B, C | Does the live tier-1 harness (simulated CEO + judge) find real bugs; do the H1–H7 fixes help? | tier 1, 26 scenarios ×5, DeepSeek and Vertex brains, flash-lite judge | pass DeepSeek A 79 % → B 75 % → C 87 %; Vertex C 73 % | H1–H7 fixed (#99); tier 1 adopted | [eval-campaign-2026-10-03.md](eval-campaign-2026-10-03.md) · [2026-10-03/](2026-10-03/) |
| 10-03 | Tier-2 voice bench, first campaign | How much does the voice layer lose against tier 1? | tier 2, 34 scenarios ×3, phone emulator, Gemini Live on Vertex | 58 % (62 % without echo); the same worlds pass 89 % in tier 1 | voice-layer fixes (#108 onward) | [../bench/tier2-2026-10-03.md](../bench/tier2-2026-10-03.md) · [../bench/results/2026-10-03/](../bench/results/2026-10-03/) |
| 10-03 | Tier-3 bench, first runs | Does the daemon work against real herdr and real Claude/Codex workers? | tier 3, 12 Claude + 2 Codex scenarios, two runs | A 9/12, B 8/12; trust prompt and naming bugs found | fixes in #107 | [../bench/tier3-2026-10-03.md](../bench/tier3-2026-10-03.md) |
| 10-03 | Campaign D | Main after #100–#108, all tiers | tiers 1–3 | t1 85 %, t2 61 % (no echo), t3 8/12: flat | baseline only | [campaign-d-2026-10-03.md](campaign-d-2026-10-03.md) · [2026-10-03/](2026-10-03/) `D-*` |
| 10-03 | Campaign E | Main after #109–#115, all tiers | tiers 1–3, incl. tier-3 voice | t1 88 %, t2 67 % (+6 pp), echo −40 dB clean | baseline only | [campaign-e-2026-10-03.md](campaign-e-2026-10-03.md) · [2026-10-03/](2026-10-03/) `E-*` |
| 10-04 | Dual brain vs main | Gemini Live as the fast brain with briefings, vs relaying the brain's lines | tier 2 ×3 interleaved, both arms | non-echo pass 51 % → 68 % (Fisher p ≈ 0.03); unheard answers 26/178 → 5/139 | merged (#122) | [dual-brain-2026-10-04.md](dual-brain-2026-10-04.md) · [2026-10-04-dual-brain/](2026-10-04-dual-brain/) |
| 10-04 | Live mid-answer pauses | Is the 7 s mid-answer stall specific to Vertex `gemini-3.8-live`? | tier 2, 8 talk-heavy scenarios, 3 voice configurations | stall in ~3 % of answers on Vertex 3.8 only; 0 in ~60 each on AI Studio and native audio (not conclusive); native audio breaks Chinese | no switch; AI Studio trial later | [../research/live-pauses-2026-10-04.md](../research/live-pauses-2026-10-04.md) |
| 10-04 | Resume-context study | Does a Live session keep its context across resumes? | `cmd/livecanary`, ~35 canary runs, 8 facts probed | first resume lossless; a handle from a resumed connection restores only that connection (later resumes ratio 0.73) | never resume a resumed session (#139) | [../research/live-resume-context-2026-10-04.md](../research/live-resume-context-2026-10-04.md) · [../research/live-resume-context-results/](../research/live-resume-context-results/) |
| 10-04 | Fast brain toward facts, prompt v4 / v5 / v5.1 | Should the voice hand status questions to the orchestrator instead of answering? | tier 2, 6 facts cases ×3 + suite ×1 per arm | facts 50 → 61 %, hand-off 75 → 90 %, ambiguous "rewind it" prompts 4 → 0; suite flat | v5.1 merged (#141) | [fast-brain-facts-2026-10-04.md](fast-brain-facts-2026-10-04.md) · [fast-brain-facts-2026-10-04/](fast-brain-facts-2026-10-04/) |
| 10-05 | Baseline on main 5d8ec20 | Fresh numbers for all tiers after #139 / #141 | tier 1 ×1, tier 2 ×3, tier 3 text + voice | t1 78 %, t2 54 % (58 % old shape), t3 7/12; 0 unauthorised actions | baseline only | [baseline-2026-10-05.md](baseline-2026-10-05.md) · [../bench/results/2026-10-05/](../bench/results/2026-10-05/) |
| 10-05 | AI Studio vs Vertex Live | Does AI Studio Live remove the 7 s stall? | one tier-2 pass (40) vs 120 Vertex runs | stalls 0 vs 4 %, pass 62 vs 54 %, but 3/91 lines unheard ("Stop.") and resume fails 3/3 | stay on Vertex (AI Studio only as the Vertex-ban fallback) | [baseline-2026-10-05.md](baseline-2026-10-05.md#item-4-ai-studio-live-geminibackendaistudio-gemini-38-live-vs-vertex) · [../bench/results/2026-10-05/t2-aistudio/](../bench/results/2026-10-05/t2-aistudio/) |
| 10-05 | Information fixes (overnight item 5) | Do the baseline's information fixes help? | tier 2 ×2 on 14 scenarios, before/after | pass 14/28 → 13/28 (flat); stray questions 2/15 → 0/15 | merged (#146) | [../status.md](../status.md) · [../bench/results/2026-10-05-info5/](../bench/results/2026-10-05-info5/) |
| 10-05 | Explain evals | Does the voice lose detail when explaining? | `explain` category, tier 1 ×12, tier 2 ×18, key-point coverage | voice says 0.64 of the orchestrator's points (0.37 of the world's); rephrases on "more detail" | led to #155 (unified roles + length) | [explain-baseline-2026-10-05.md](explain-baseline-2026-10-05.md) · [../bench/results/2026-10-05-explain/](../bench/results/2026-10-05-explain/) |
| 10-05 | Judge calibration | How often does the production judge (flash-lite) agree with the CEO? | 30 items / 126 pairs; Opus reference labels, CEO reviewed 41 | flash-lite vs reference 75 %, kappa 0.45; CEO sided with Opus 27/31 and 10/10 spot checks | labelling and judging by Claude, not the CEO or flash-lite (#157, #161) | [calibration/README.md](calibration/README.md) · [calibration/2026-10-05/](calibration/2026-10-05/) |
| 10-05 | North-star report | Can the four north-star numbers (Understood, Done, Low effort, Safe) come from recorded runs? | `bench northstar` offline on 10-05 results, programmatic checks | works at $0; said-vs-done, missed news, end state added | merged (#159) | [northstar-baseline-2026-10-05.md](northstar-baseline-2026-10-05.md) |
| 10-05 | Workday suite baseline | Long multi-worker sessions: where does main fail? | 7 workday scenarios, tier 1 ×5, tier 2 ×3, personas | (flash-lite) Understood t1 66 % / t2 62 %, missed news 40 → 60 % in voice, one `rm -rf ./data` approval | suite adopted (#158) | [workday-baseline-2026-10-05.md](workday-baseline-2026-10-05.md) · [../bench/results/2026-10-05-workday/](../bench/results/2026-10-05-workday/) |
| 10-06 | Judge bake-off (flash-lite → Sonnet) | Which judge matches ground truth? | 126 truth pairs; Sonnet, Pro preview, flash-lite | Sonnet kappa 0.72 (length_fits 0.70–1.00); flash-lite 0.65; Pro 0.69 | Sonnet default judge; `bench rejudge` (#166) | [judge-bakeoff-2026-10-06.md](judge-bakeoff-2026-10-06.md) · [calibration/2026-10-05/bakeoff/](calibration/2026-10-05/bakeoff/) |
| 10-06 | Segment judging | One verdict per session hides everything; does per-segment judging work? | one Sonnet call per agenda beat; calibration and workday rejudge | kappa 0.69–0.77; segment Understood t1 83 %, t2 73 % (voice −10 pp) | segment Understood is the primary metric (#170) | [workday-baseline-2026-10-05.md](workday-baseline-2026-10-05.md) · [../bench/results/2026-10-05-workday-rejudged/](../bench/results/2026-10-05-workday-rejudged/) |
| 10-06 | Step 4: prompt arms A0 / A / B / C (#155) | Do unified roles + fit-the-content length (B) or an STE-like style (C) beat main? | tier 1 workday ×5 + safety ×8 + explain, tier 2 ×2, `normal` | B −5 pp segment Understood (CI −9 to −0), `no_hallucination` 88 → 79 %; C approved `rm -rf` 3/5; safety A0 0/80, others 1/80 | no arm merged; no action rails | [prompt-arms-2026-10-06.md](prompt-arms-2026-10-06.md) · [../bench/results/2026-10-06-arms/](../bench/results/2026-10-06-arms/) |
| 10-07 | Harness iteration 1–2 | Status naming every agent; `options` only once | tier 1 workday ×5 vs A0, extra scale-ten runs, `normal` | 1: −4 pp and `rm -rf` 5/15; 2: +3 pp (CI −1 to +7), `rm -rf` 2/15 | both not merged | [harness-iteration-2026-10-07.md](harness-iteration-2026-10-07.md) · [../bench/results/2026-10-07-iter1/](../bench/results/2026-10-07-iter1/), [iter2](../bench/results/2026-10-07-iter2/) |
| 10-07 | Normal vs chatty filtering | Do evals at `normal` match the CEO's real `chatty` use? | A0 step 4 (`normal`) vs A0 chatty (cloud), tier 1 workday ×5 | missed news 34 → 19 % (−16 pp, CI −23 to −8); segment Understood +3 pp (n.s.); binaries differ by a day | default chattiness `chatty` (#178); evals at `chatty` | [harness-iteration-2026-10-07.md](harness-iteration-2026-10-07.md) · [northstar-a0-normal-vs-chatty.md](../bench/results/2026-10-07-chatty/northstar-a0-normal-vs-chatty.md) |
| 10-07 | Haiku 5.5 as judge | Can Haiku replace Sonnet as judge? | 126 truth pairs, two Haiku passes + Sonnet control | kappa 0.62 / 0.69 (length_fits 0.44 / 0.74) vs Sonnet 0.71 | stay on Sonnet (#183) | [haiku-judge-2026-10-07.md](haiku-judge-2026-10-07.md) · [haiku-2026-10-07/](haiku-2026-10-07/) |
| 10-07 | Roles arms r / rl (cloud) | Do unified roles cost understanding without the length change (r); does a gentle length rule help (rl)? | Claude Code cloud sessions, tier 1 workday ×5, `chatty`, Sonnet segment judge | A0 92 %, r 89 % (−3 pp, CI −8 to +1), rl 88 % (−4 pp, CI −9 to 0); missed news ~18–19 % all | r merged for the single framing (#185); rl dropped | [roles-arms-2026-10-07.md](roles-arms-2026-10-07.md) · [../bench/results/2026-10-07-chatty/](../bench/results/2026-10-07-chatty/) |

Older material (bench issues and dives before 2026-10-03) is in
[../history/](../history/).
