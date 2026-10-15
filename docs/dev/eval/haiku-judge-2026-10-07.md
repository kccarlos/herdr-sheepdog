# Haiku 5.5 as the eval judge, 2026-10-07

Question: can Claude Haiku 5.5 replace Sonnet as the calibration judge?
Same set as the 2026-10-06 bake-off: 126 pairs against
`calibration/2026-10-05/truth.json`, segment mode (`--segment`, one call
per item), final rubric and prompt, Claude CLI judge. Raw verdicts and
`agree` reports: `docs/dev/eval/haiku-2026-10-07/`.

The `haiku` alias resolves to **`claude-haiku-5-5`** (checked with
`claude -p "reply OK" --model haiku --output-format json`, `modelUsage`).

## Overall

| Run | Agreement | Kappa | `length_fits` kappa | Too lenient / too strict | Time | Tokens in / out | Cost |
|---|---|---|---|---|---|---|---|
| Haiku a | 83 % (104/126) | 0.62 | 0.44 | 7 / 15 | 2 min 21 s | 261 k / 77 k | ≈ $1.30 (list estimate) |
| Haiku b | 86 % (108/126) | 0.69 | 0.74 | 5 / 13 | 2 min 28 s | 289 k / 78 k | ≈ $1.35 (list estimate) |
| Sonnet control (same day) | 87 % (109/126) | 0.71 | 0.70 | 5 / 12 | 1 min 30 s | 193 k / 24 k | $0 (subscription; tool shows no estimate) |

Haiku wrote about three times Sonnet's output tokens per pass and was
slower, so it is not cheaper in wall-clock time here. Its cost is the CLI's
list-price estimate; the Sonnet pass runs on the subscription and reports
no price. The 2026-10-06 Sonnet segment runs were kappa 0.69 to 0.77, so
today's control (0.71) is in range.

## Per rubric item, kappa (agreement)

| Rubric item (n) | Haiku a | Haiku b | Sonnet control |
|---|---|---|---|
| answered (27) | 0.75 (24/27) | 0.82 (25/27) | 0.67 (23/27) |
| no_unintended_action (18) | 1.00 (18/18) | 1.00 (18/18) | 1.00 (18/18) |
| right_agent (6) | 1.00 (6/6) | 1.00 (6/6) | 1.00 (6/6) |
| language (10) | 1.00 (10/10) | 0.74 (9/10) | 0.74 (9/10) |
| length_fits (21) | 0.44 (16/21) | 0.74 (19/21) | 0.70 (18/21) |
| no_hallucination (8) | 0.33 (5/8) | 0.33 (5/8) | 0.25 (5/8) |
| facts_current (10) | 0.29 (6/10) | 0.19 (5/10) | 0.55 (8/10) |
| ear_friendly (11) | 0.08 (7/11) | 0.08 (7/11) | 0.00 (8/11) |
| explain_* (15 pairs, 7 ids) | 12/15 | 14/15 | 14/15 |

Failure directions per item (lenient / strict): Haiku `facts_current` is
0 / 4 and 0 / 5 (it hedges away or fails facts the truth passes; Sonnet
0 / 2); `no_hallucination` 0 / 3 in both runs; `length_fits` 2 / 3 in a and
2 / 0 in b (Sonnet 0 / 3); `answered` 0 / 3 and 0 / 2 (Sonnet 0 / 4).
Overall Haiku errs strict like Sonnet (15 and 13 strict vs 7 and 5 lenient).
The explanation items have 2 to 3 segments each and are anecdotal; their
per-item kappa is not meaningful (`explain_depth_why` is wrong in all three
runs, as for Sonnet in 2026-10-06).

Single-run noise is large (the CLI has no temperature): Haiku's overall
kappa moved 0.62 to 0.69 and `length_fits` 0.44 to 0.74 between two
identical runs, which is as much as the gap to Sonnet.

## Verdict

**No: Haiku does not replace Sonnet as the single judge.** The rule was
overall kappa ≥ 0.7 and `length_fits` ≥ 0.6 on both runs. Haiku fails it
on both counts in run a (0.62, 0.44) and on overall kappa in run b (0.69,
with `length_fits` 0.74). It is close, and within noise of Sonnet's
control (0.71), but it did not clear the bar on either run.

**Split judge candidates.** Items where Haiku reaches kappa ≥ 0.7 in both
runs: `answered` (0.75 / 0.82, better than Sonnet's 0.67 today),
`no_unintended_action` (1.00 / 1.00), `right_agent` (1.00 / 1.00),
`language` (1.00 / 0.74). Haiku could take those four; the sample is
small for `right_agent` (6 pairs) and `language` (10), and `answered`
and `no_unintended_action` are the only ones with 18+ pairs.
`length_fits` is borderline (0.44 / 0.74): not enough evidence, would
need another pair of runs. Keep Sonnet for `facts_current`,
`no_hallucination` and `ear_friendly` (Haiku is clearly worse on the
first, equal and poor on the other two, where neither model meets 0.7),
and for the explanation items.

Caveat: the rubric and prompt were tuned on this same set, and Haiku is
at most a partial replacement. The split would route about 61 of 126
pairs (answered, no_unintended_action, right_agent, language) to Haiku. A
split would also not save wall-clock time at today's numbers, since
Haiku was slower than Sonnet; its case is the plan quota, not speed.
