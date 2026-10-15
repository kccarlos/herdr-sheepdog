# Sheepdog re-baseline, 2026-10-08

Branch: `eval/rebaseline-2026-10-08` (push accepted; no fallback branch needed). Binary: main `976d585` (unified roles #185, chatty default #178, recall fixes #198, measurement fixes #199).

Setup: tier 1, `workday` x5, all 7 scenarios (35 runs), `--personas rotate`, `--live --provider deepseek --model deepseek-flash --fallback-provider none --simuser deepseek --simuser-model deepseek-flash --judge fake`, default prompts, default chattiness (chatty). Same flags as the 10-07 chatty baseline, so the product/measurement comparison is clean. Run in two chunks (3 + 4 scenarios, about 5 min each), committed per chunk. Then `bench rejudge --judge claude --judge-model sonnet --skip-run-level` (segment level, incl. `said_vs_done`), then `bench northstar`.

Files in `docs/dev/bench/results/2026-10-08-rebaseline/`: `parts/` (per scenario), `merged-raw/` (before rejudge), `merged/` (rejudged), `northstar.md`, `northstar-vs-1007-a0.md`, `rejudge*.log`, `run-chunk.sh`.

## Budget

DeepSeek spend, list-price estimate: **$3.70** (brain $3.46, simuser $0.24; 917 + 401 calls). That is about 26.6 CNY at 7.2 CNY/USD, i.e. at the 25 CNY cap by the estimate (list price; cached tokens are billed lower, so the real bill is probably under). Far under the $40 cap. Sonnet judge: 88 calls over the subscription, $0 API. No extra runs were made after the cap was reached.

## North star (35 runs, 34 graded)

| North star | Pass / runs | Rate | 95 % CI |
|---|---|---|---|
| Understood | 34 / 34 | 100 % | 90-100 % |
| **Done, actions / end state** | 34 / 34 | 100 % | 90-100 % |
| Done, claim part (judge `said_vs_done`) | 33 / 34 | 97 % | 85-99 % |
| Done, claim part (phrases) | 22 / 32 | 69 % | 51-82 % |
| Low effort | 26 / 34 | 76 % | 60-88 % |
| Safe | 28 / 34 | 82 % | 66-92 % |

Turns to goal 12.4 (CI 12.1-12.8), time to goal 102.0 s (CI 96.0-108.2). Estimated cost per run $0.108.

- "Done" is now only the required actions and end-state checks. No tier-1 scenario declares end-state checks, so here it is the required actions alone (34/34). The claim part is reported separately: the judge item is not gated until calibrated, the phrase list is diagnostic. These are different from the old combined "Done" (63 % on 10-07), see below.
- 34 graded, not 35: workday-scale-ten r1 is an infrastructure error (`simulated user: deepseek: HTTP 502`), kept in the record, not graded.
- Segment level (cluster bootstrap): Understood 365/402 = 91 % (87-94), Safe (judge) 477/490 = 97 % (96-99). Every request segment understood: 14/34 runs (41 %).

### Coherence, probes not exercised excluded

`coherence_*`: 48 / 49 yes = 98 % (CI 93-100), n/a 0, **not exercised 0**. `clarify_*`: 60 / 62 = 97 % (92-100), not exercised 1 (excluded from the rate). `said_vs_done`: 154 / 155 = 99 %, n/a (no claim in the segment) 335. For 10-07 the same item read 40/50 = 80 % under the old untagged agenda (which could not tell whether a probe was ever performed); here an item no user line performed is "not exercised" and not scored. This is a measurement change, not evidence the product got more coherent. Only 49 coherence probes were scored in 34 runs.

### Agenda-tag coverage

Simulated-user lines carry a declared agenda tag now: of 426 user lines, 399 `item`, 17 `answer`, 10 `again`, **0 undeclared** (10-07: none had tags). Agenda items performed: 407 / 495 = 82 % over all 35 runs, 404 / 482 = 84 % over the 34 non-error runs. Runs end with `agenda done` but typically skip 1-2 items (morning-standup 12/14 in all five runs; long-haul 13-14/18; adversarial 10-11/13), because the simulated user does not always get to perform every item. The skipped items are exactly the probes counted as not exercised above, so they widen the CIs rather than bias the rates.

### bad_reply and other brain fallbacks

Fallback causes (35 runs, 8 events): timeout 3 (deepseek read-response deadline), yielded to a CEO turn 3, queue_timeout 1, **bad_reply 1** (`prose: no JSON object in reply`, long-haul r2). 10-07: bad_reply 3 (`brain: reply without spoken text`), timeout 4, yielded 2, queue_timeout 1. Run failure labels: brain_fallback 4, timeout 3, unintended_action 1, error 1 (the 502).

The concentration is workday-scale-ten: **0/5 passed at run level** (r1 502 error; r2-r5: timeouts and yielded-to-CEO fallbacks, 1-2 turns each fell back). That is DeepSeek flash latency under clustered events, not a regression: on 10-07 scale-ten also had 4 of 5 runs fail on brain_fallback (2) or timeout (2). Adversarial-afternoon had one `unintended_action`.

## Comparison to the 10-07 chatty baseline (a0, same 35-run design)

From `northstar-vs-1007-a0.md` (B-A there; here reproduced as new minus 10-07):

| Metric | 10-07 a0 | 10-08 | Difference (95 % CI) |
|---|---|---|---|
| Understood | 35/35 | 34/34 | 0 |
| Done (actions/end state) | 35/35 | 34/34 | 0 |
| Done, claim part (phrases) | 19/32 (59 %) | 22/32 (69 %) | +9 pp (-14 to +31) |
| Low effort | 26/35 (74 %) | 26/34 (76 %) | +2 pp (-18 to +22) |
| Safe | 27/35 (77 %) | 28/34 (82 %) | +5 pp (-14 to +24) |
| Understood, segments | 387/421 (92 %) | 365/402 (91 %) | -1 pp (-6 to +3) |
| Unmatched claims (orchestrator) | 19/145 (13 %) | 12/144 (8 %) | -5 pp (-12 to +2) |
| Actions never told | 9/90 (10 %) | 5/84 (6 %) | -4 pp (-13 to +5) |
| Missed news | 45/240 (19 %) | 40/222 (18 %) | -1 pp (-8 to +6) |
| Turns / time to goal | 12.5 / 108.7 s | 12.4 / 102.0 s | within noise |

**No metric moved beyond its CI.** Direction on the claim-related numbers (unmatched claims, never-told, claim phrases, Safe) is favourable, consistent with the recall fixes (#198), but at n=35 it is not distinguishable from noise.

### What changed in measurement vs in product

- **Measurement (#199):** the 10-07 STATUS reported **Done 22/35 = 63 %**. That figure included unmatched claims in the Done gate. Recomputed with the current code, the same 10-07 data gives Done 35/35 on actions alone and the claim part 19/32 (59 %). So the "Done" jump from 63 % to 100 % is **entirely the Done split, not a product improvement**; compare the claim-part rows instead (59 % to 69 %, within noise). The new `said_vs_done` judge item has no 10-07 counterpart (judged on 10-07 data it would be n/a), so the claim-part (judge) number has no baseline. Coherence/clarify "not exercised" exclusion and the agenda-tagged simulated user also changed what is scored: 10-07 had no tags, so its coherence 80 % is not comparable.
- **Product:** unified roles (#185), recall fixes (#198, the user's words in any language from the start), and the retry/re-relay fix (#192, already in 10-07). Visible effects, all within CIs: fewer unmatched claims and never-told actions, fewer `bad_reply` fallbacks (3 to 1, different message type). Latency/timeouts unchanged (scale-ten still fails run-level on DeepSeek flash timeouts).
- The brain and simulated user models, personas, scenarios and judge model are the same as 10-07, so differences besides the above are sampling noise.

## Caveats

- Two runs were not segment-rejudged: **workday-explain-deep r2** (segment 9, beat 14: `no verdict for right_agent, coherence_1`) and **workday-long-haul r2** (segment 9, beat 9: `no verdict for right_agent`). The Sonnet judge returned no verdict on those segments in three attempts, so they keep the fake-judge segment verdicts (everything yes) and weigh toward optimistic segment rates. 33 of 35 runs are Sonnet-judged. The first rejudge also hit the 30-minute background cap and was resumed (`rejudge-1.log`, `-2`, `-3`).
- `--skip-run-level` was used as on 10-07: run-level verdicts still come from the fake judge; the north-star Understood/Safe come from the Sonnet segment verdicts where present.
- `--fallback-provider none` as on 10-07, so a brain timeout falls back to the scripted line instead of another provider.
- n = 34-35 per cell; CIs are wide (about +/-12 pp at best). Only differences beyond CIs should be acted on, and there were none.
