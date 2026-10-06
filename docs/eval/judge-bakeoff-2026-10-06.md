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

## Decision

Claude Sonnet via the CLI is the default judge (`DefaultJudgeKind =
claude`): the only candidate at kappa ≥ 0.7 overall and ≥ 0.6 on
`length_fits`, in every run on the final rubric, and $0 API.
Flash-Lite remains selectable (`--judge vertex`, or `SHEEPDOG_JUDGE=vertex`).
How it is invoked and what it costs: [bench/README.md](../bench/README.md#the-judge).

## Workday baseline rescored

WORKDAY_SECTION
