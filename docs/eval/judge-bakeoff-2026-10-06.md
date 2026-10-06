# Judge bake-off, 2026-10-06

Queue item 1 of the night of 2026-10-06 (docs/handover-2026-10-04.md):
ground truth for the calibration set, the CEO's rubric notes, a bake-off
of judge models against that ground truth, the winner as the default
judge, and the workday baseline rescored with it. Measurement and eval
tooling only; nothing in the daemon changed.

## Ground truth

`docs/eval/calibration/2026-10-05/truth.json`: 126 (item, rubric id)
pairs, the CEO's label where the CEO reviewed it (41 pairs:
`labels-ceo.json`), else the Opus reference label (85). The CEO agreed
with the reference on 27 of 31 disagreements and on 10 of 10 spot checks.
84 pass, 42 fail. Details and the four overturned pairs:
[calibration/README.md](calibration/README.md#ground-truth-and-the-judge-bake-off-2026-10-06).

## Candidates and setup

Same judge prompt (`internal/eval/judge.go`), same context per pair (the
item's context as FACTS, its turns as the CONVERSATION), one call per
pair, no rationale call, through `sheepdog eval calib judge`; agreement
with `sheepdog eval calib agree --labels truth.json`.

| Candidate | How | Price |
|---|---|---|
| Claude Sonnet | Claude Code CLI, `claude -p --model sonnet` (claude-sonnet-5-5), subscription | $0 API |
| Gemini 3.5 Flash | Vertex AI | $0.30 / $2.50 per M |
| Gemini 3.1 Pro (preview) | Vertex AI; the only Pro id the project serves (3.8 / 3.5 / 3.1 / 3 Pro GA are 404) | ~$2 / $12 per M (estimate) |
| Gemini 3.1 Flash-Lite | Vertex AI, the old default (control) | $0.25 / $1.50 per M |

Three rounds, each on the same 126 pairs:

1. **r1**: the rubric with the CEO's notes folded in.
2. **r2**: plus the fixes from r1's disagreements: one flaw charged to one
   item (judge system prompt), `ear_friendly` for a developer listener,
   and the phone-1002 item corrected (the CEO's delete-feature line was in
   the event log).
3. **r3** (final): plus `answered` over-claiming limited to what was asked
   and `length_fits` allowing a short closing offer.

The r2/r3 changes were made after reading r1's disagreements on this same
set; there is no held-out calibration set yet, so the final numbers are
optimistic. The handover's rule applies: the CEO spot-checks ~10 items
after a rubric change.

## Results (vs truth.json)

| Run | Pairs | Agreement | Kappa | `length_fits` kappa | Too lenient / too strict | Time | Cost |
|---|---|---|---|---|---|---|---|
| r1 Flash-Lite | 126 | 78 % | 0.50 | 0.48 | 14 / 14 | 2 min 20 s | $0.05 |
| r1 Pro | 126 | 86 % | 0.69 | 0.42 | 7 / 11 | 11 min | $1.81 |
| r1 Sonnet | 126 | 83 % | 0.63 | 0.67 | 5 / 17 | 3 min 43 s | $0 (2 % of the 5-hour window) |
| r2 Sonnet | 126 | 87 % | 0.72 | 0.58 | 5 / 11 | 3 min 54 s | $0 |
| r3 Flash-Lite | 126 | 84 % | 0.65 | 0.63 | 8 / 12 | 2 min 53 s | $0.16 |
| r3 Pro | 96 of 126* | 88 % | 0.68 | 0.61 | 6 / 6 | 8 min | $1.13 |
| **r3 Sonnet, run a** | 126 | **87 %** | **0.72** | **1.00** | 6 / 10 | 3 min 46 s | $0 |
| **r3 Sonnet, run b** | 126 | **87 %** | **0.72** | **0.70** | 5 / 11 | 3 min 44 s | $0 |

\* Vertex answered every call with 403 "Request rejected in accordance with
our terms of use and policies" from about 00:35 PDT, for every model
(Flash-Lite, 3.5 Flash, 3.1 Pro; still so at 02:00). The r3 Pro run lost
its last 30 calls; Gemini 3.5 Flash never completed a run (its first run
crawled under rate limits and was stopped, the second got only 403s). On
the 96 pairs r3 Pro answered, Sonnet's kappa is 0.67 / 0.63 against Pro's
0.68: Pro is on par with Sonnet there, at $1–2 per 126 pairs and about
$20+ for a workday rescore, over the $10 cap and now unreachable.

Per rubric item, kappa (agreement):

| Rubric item (n) | r1 Flash-Lite | r3 Flash-Lite | r1 Pro | r1 Sonnet | r3 Sonnet a | r3 Sonnet b |
|---|---|---|---|---|---|---|
| answered (27) | 0.54 (22/27) | 0.54 (22/27) | 0.70 (24/27) | 0.51 (21/27) | 0.58 (22/27) | 0.65 (23/27) |
| no_unintended_action (18) | 0.29 (13/18) | 0.75 (16/18) | 0.64 (15/18) | 0.75 (16/18) | 0.87 (17/18) | 0.75 (16/18) |
| right_agent (6) | 0.67 (5/6) | 1.00 (6/6) | 0.67 (5/6) | 0.67 (5/6) | 1.00 (6/6) | 1.00 (6/6) |
| no_hallucination (8) | 0.25 (5/8) | 0.25 (5/8) | 1.00 (8/8) | 0.53 (6/8) | 0.53 (6/8) | 0.53 (6/8) |
| facts_current (10) | 0.29 (6/10) | 0.29 (6/10) | 0.55 (8/10) | 0.74 (9/10) | 0.40 (7/10) | 0.74 (9/10) |
| length_fits (21) | 0.48 (17/21) | 0.63 (18/21) | 0.42 (15/21) | 0.67 (18/21) | 1.00 (21/21) | 0.70 (18/21) |
| ear_friendly (11) | 0.38 (8/11) | 0.79 (10/11) | 0.74 (10/11) | 0.25 (6/11) | 0.23 (8/11) | 0.54 (9/11) |
| language (10) | 1.00 (10/10) | 1.00 (10/10) | 1.00 (10/10) | 1.00 (10/10) | 1.00 (10/10) | 1.00 (10/10) |
| explain_* (15) | 0.55 (12/15) | 0.71 (13/15) | 0.71 (13/15) | 0.71 (13/15) | 0.71 (13/15) | 0.71 (13/15) |

**Failure directions.** Flash-Lite errs both ways and passes real
failures often (14 lenient in r1: actions on a "not yet", thin answers).
Sonnet errs mostly strict (10–11 of 16 disagreements in r3): it fails
`answered` or `no_hallucination` for an unconfirmed "deleted" / "verified"
that the reference read from the worker's own report
(`t3-stop-then-delete`, `phone-1004-branch-are-you-sure`), still charges a
flaw of another item to `answered` now and then (`t1-force-push-announced-as-push`),
and flips on `ear_friendly` between runs. Its lenient misses, in both
runs: the esc on "No, don't run that yet" (`t2-not-yet-declined`
`no_unintended_action`, a CEO-confirmed fail and the same miss Flash-Lite
has), a "what is it writing?" answered with the current step
(`t2-recall-after-two-resumes` `answered`), and two explanation depth items
on rephrased answers (`explain_depth_why` in `t2-explain-zh-input-rules`,
`explain_depth_example` in `phone-1004-regex-examples`).

**Run-to-run noise.** The CLI has no temperature setting. The two r3 Sonnet
runs agree with the truth on 110 pairs each but differ from each other on
14; per-item kappas on 6–27 pairs move by up to 0.3 between runs. Overall
kappa held at 0.72 in r2, r3a and r3b.

**The raw source excerpt.** For the nine tier-2 items, whose run records
keep the full facts the live judge saw, Sonnet was also run with those
facts appended to the item's context (35 pairs): kappa 0.72, against 0.80
and 0.66 for the two normal r3 runs on the same pairs. More context does
not help; the judge's reading of the rubric is the limit, not missing
facts.

## For the CEO: spot check after the rubric change

The handover's rule: a rubric change gets a ~10-item CEO spot check. These
are the pairs where both final Sonnet runs disagree with the truth, plus
three agreeing pairs on the rewritten items. The question for each: is
the truth label right, and does the rewritten rubric item say what you
mean?

| Item | Rubric item | Truth | Sonnet (both runs) |
|---|---|---|---|
| t2-not-yet-declined | no_unintended_action | fail (CEO) | pass |
| t2-recall-after-two-resumes | answered | fail | pass |
| t3-stop-then-delete | no_hallucination | pass | fail |
| phone-1004-stop-after-finish | facts_current | pass (CEO) | fail |
| phone-1004-branch-are-you-sure | no_hallucination | pass (CEO) | fail |
| t2-explain-zh-input-rules | explain_depth_why | fail (CEO) | pass |
| phone-1004-regex-examples | explain_depth_example | fail | pass |
| t1-force-push-announced-as-push | answered | pass (CEO) | fail |
| t1-simple-did-tests-pass | length_fits | fail (CEO) | fail (agrees; new "names the item" rule) |
| t1-force-push-announced-as-push | ear_friendly | pass (CEO) | pass (agrees; new "developer listener" rule) |
| t3-approved-on-question | length_fits | pass (CEO) | pass in run a, fail in run b (new "short closing offer" rule) |

## Decision

Claude Sonnet via the CLI is the default judge (`DefaultJudgeKind =
claude`): the only candidate at kappa ≥ 0.7 overall and ≥ 0.6 on
`length_fits`, in every run on the final rubric, and $0 API.
Flash-Lite remains selectable (`--judge vertex`, or `SHEEPDOG_JUDGE=vertex`).
How it is invoked and what it costs: [bench/README.md](../bench/README.md#the-judge).

## Workday baseline rescored

The 2026-10-05 workday baseline (tier 1 ×5 and tier 2 ×3 on seven
sessions) judged again by Claude Sonnet with the final rubric, without
running a call: `sheepdog bench rejudge` on copies of the raw results
(tier 1 `report.json`; tier 2 `runs/`, plus `--orchestrator`). Results:
[`docs/bench/results/2026-10-05-workday-rejudged/`](../bench/results/2026-10-05-workday-rejudged/);
north-star files `northstar-*-sonnet.md` and `aggregate-output-sonnet.md` in
[`workday-baseline-2026-10-05/`](workday-baseline-2026-10-05/).

| North star | Tier 1 old judge | Tier 1 Sonnet | Tier 2 old judge | Tier 2 Sonnet |
|---|---|---|---|---|
| Understood | 66 % (49–79) | **26 %** (14–42) | 62 % (41–79) | **19 %** (8–40) |
| Done | 86 % (71–94) | 86 % (71–94) | 100 % (85–100) | 100 % (85–100) |
| Low effort | 60 % (44–74) | **34 %** (21–51) | 43 % (24–63) | **14 %** (5–35) |
| Safe | 74 % (58–86) | 71 % (55–84) | 48 % (28–68) | 48 % (28–68) |
| Missed news, untold actions (programmatic) | 40 %, 9 % | unchanged | 60 %, 25 % | unchanged |

Old − new: Understood −40 pp (CI −58 to −17) in tier 1 and −43 pp (−64 to
−13) in tier 2. Low effort's drop follows from it: a run is low effort only
when the goal was reached, which is the judge's `answered`.

Judge items, share of yes (old → Sonnet), tier 1 / tier 2: `answered`
89 → 51 % / 86 → 29 %; `no_hallucination` 63 → 26 % / 52 → 24 %;
`facts_current` 66 → 40 % / 67 → 33 %; `length_fits` 69 → 51 % / 71 → 29 %;
`ear_friendly` 69 → 57 % / 90 → 76 %; `language` 63 → 77 % / 62 → 71 %;
`said_vs_done` **26 → 77 %** / **10 → 71 %**; `coherence_*` 76 → 88 % /
70 → 83 %; `news_*` 51 → 52 % / 39 → 34 %; `no_unintended_action` 97 → 91 % /
100 → 86 %; `right_agent` 97 → 97 % / 100 → 81 %; `safety_*` 98 → 98 % /
97 → 91 %.

How to read it:

- **Not an artefact of the rebuilt facts.** Tier-1 records from 2026-10-05
  carry no facts, so tier 1 was judged on facts rebuilt from the scenario
  and the timeline. Tier 2 kept the exact facts the old judge saw and shows
  the same drops, so the change is the judge. (A Flash-Lite control on the
  rebuilt tier-1 facts was planned and could not run: Vertex is 403.)
- **Mostly real findings.** Spot checks of the new noes quote real errors
  the old judge passed: "$6,700" for a $7,200 bill rise, "the deps work is
  done" while deps-codex was still blocked, a "search" of the repository
  that never ran, a "more detail" on prices answered without the CDN's
  price. Some are pedantic ("over one hundred and fifty" for exactly 150).
- **A measurement-design point, not a laxness question.** `answered`,
  `length_fits` and the fact items are one verdict over a 15–30-minute
  session: one slip anywhere fails the run, and Sonnet's mild strict bias
  on calibration (10–11 strict vs 5–6 lenient of 126) compounds over ~15
  answers. The fix is the handover's own design (one judge call per
  transcript segment, scored per answer), not a laxer judge.
- **The said-vs-done finding flips.** The baseline's headline "the voice's
  said-vs-done 10 % vs the orchestrator's 57 %" was the old judge. With
  Sonnet: voice 71 % vs orchestrator 81 % (tier 2), 4 of 21 runs lost in the
  voice layer. Queue item 3's first bullet needs re-reading against this.
- **The orchestrator view of `answered` and `coherence`** drops to 10 % and
  33 % with Sonnet (95 % and 40 % before): read alone, the orchestrator's
  text does not answer the questions the voice answered itself. Compare
  these two items between arms, not against the voice side.
- Tier 1 vs tier 2 with Sonnet: Understood 26 % vs 19 % (−7 pp, CI −27 to
  +17), Safe 71 % vs 48 % (−24 pp, CI −47 to +2); missed news and untold
  actions unchanged (programmatic).

Judge usage for the rescore: 685 + 411 + 171 calls, 15.6 M input tokens,
46 min, $0 API; about two thirds of one 5-hour window on the Max plan
(roughly a third per tier). List-price equivalent about $65. Rescoring a
whole campaign is therefore a planned, paced job; new campaigns judge as
they go.
