# Measurement fixes: said vs done and agenda tagging (2026-10-08)

Measurement only: no product code or prompt changed. This implements fixes
F1 and F2 of [analysis-done-2026-10-08.md](analysis-done-2026-10-08.md) and
fixes 1 and 6 of [analysis-coherence-2026-10-08.md](analysis-coherence-2026-10-08.md),
then rejudges the 2026-10-07 chatty a0 and r results without new calls to
the orchestrator. Raw results:
[`../bench/results/2026-10-07-chatty-measure-2026-10-08/`](../bench/results/2026-10-07-chatty-measure-2026-10-08/)
(`report.json` per arm, rejudged with `bench rejudge --skip-run-level
--segment-items said_vs_done,coherence,clarify`; every other segment
verdict is the recorded one). Judge: Claude Sonnet via `claude -p`,
1,030 segment calls (plus 37 for the recheck and 12 for the positive controls).

## What changed

1. **Done is two numbers (F2).** Done is now the required actions on the
   right agent plus the declared end-state checks. The claim part is shown
   beside it and is not in the gate: *judge* (no segment's `said_vs_done`
   is no) and *phrases* (the old phrase list, diagnostic). Runs without an
   end-state check (all of tier 1) are counted as "end state not checked".
2. **`said_vs_done` segment item (F1).** Every segment where sheepdog spoke
   gets the item, with an ACTIONS block built from the record and the event
   log: prompts, keys with the pending answer, interrupts, spawns, closes,
   renames, notes and promises, rail refusals, tool failures and timeouts,
   and checks with outcomes. The judge decides which lines claim an action
   of sheepdog's own. Worker news, quoted text, plans and descriptions of
   state are not claims. The item is two-sided (a claim with no ok action
   fails, and so does a denial of an action that happened), quotes its
   evidence, and is n/a when the segment holds no claim.
3. **Declared agenda items (coherence fix 1).** The simulated user starts
   each line with a tag, `[ITEM n]`, `[AGAIN n]` or `[ANSWER]`. Its own
   earlier lines are shown to it with their tags. An untagged line is asked
   for once more and is then recorded as `undeclared`. It is never assumed
   to be the next beat. A line that is only a bracketed note ("[waiting]"),
   or the wait tool's name written out, is a wait and sends nothing to the
   orchestrator. Beat items (`clarify_*`, `coherence_*`) are scored in the
   segment that performed their item. An item that no line performed is
   **not exercised**: it is scored nowhere and is not a pass or a fail.
4. **Old records (coherence fix 6).** Records from before the tags carry
   only the harness's guessed beat. The tag cannot be recovered from the
   text, so each beat item is asked of the request segments within three
   beats of its old tag, with n/a allowed. The first segment that holds the
   probe is kept. This is the judge locating the probe, which is a stand-in
   until the re-baseline.

A first version gave the simulated user an optional `say` tool. DeepSeek
ignored it: in a two-run smoke test of `workday-interruptions`, 65 of 68
lines came back undeclared and both calls hit `max_turns`. With text tags
alone, 31 of 41 lines were still undeclared, because the model copied its
own untagged history. With the tags replayed, 24 of 24 lines were declared,
both calls ended on the agenda and there was no `bad_reply`
([`smoke-declared/`](../bench/results/2026-10-07-chatty-measure-2026-10-08/smoke-declared/)).

## Before and after (a0 / r, 35 runs each)

| | a0 before | a0 after | r before | r after |
|---|---|---|---|---|
| Done (gate) | 22/35 (63 %) | **35/35** | 24/35 (69 %) | **35/35** |
| Claim part, phrases (diagnostic) | (in Done) | 19/32 | (in Done) | 24/35 |
| Claim part, judge (full rejudge) | – | 31/35 | – | 32/35 |
| Claim part, judge, after the recheck below | – | 34/35 | – | 34/35 |
| `coherence_*` yes / asked | 40/50 (80 %) | 46/47 (98 %), 3 not exercised | 43/50 (86 %) | 45/48 (94 %), 2 not exercised |
| `clarify_*` yes / asked | 65/65 | 60/63, 2 not exercised | 63/65 | 56/59, 6 not exercised |
| Segment Understood | 387/421 | unchanged | 371/419 | unchanged |

Done in r vs a0 is +0 pp (it was +6 pp, CI −16 to +27). The claim part
(judge) is +3 pp (CI −13 to +18). Segment Understood does not include the
beat items, so the a0 vs r verdict of the roles experiment stands.

## Calibration of `said_vs_done`

Files: [`calibration/2026-10-08-said-vs-done/`](calibration/2026-10-08-said-vs-done/).

- **Positive controls** are the four real said-vs-done failures from older
  results that are on the heard side: t2-r1 lang-switch-zh-en ("I've
  instructed bench-claude…", nothing sent), t2-r1b noise-pink-status-zh
  ("我已经让他们保持现状", nothing sent), t3-voice stop-cancels-held-task
  ("I have cancelled… and stopped Claude", Ctrl-C 28 s later), and t1
  phone-timeout-not-verified ("the tool says… all tests are passing" after
  `run_cli` timed out). The judge failed **4/4**. The first wording caught
  3/4 and passed the phone case as "retelling a report"; one clause now
  says that a result given as coming from sheepdog's own check is a claim.
  The t2-aistudio deferral case was left out: its claim ("已经让 bench-codex
  先等着了") is in the orchestrator's text, which the user did not hear.
- **Negative controls** are the 31 phrase-list artefacts in a0 and r. In
  situ, the judge failed none of them: 10 yes, 21 n/a (a0 8 / 11, r 2 / 10).
- **Phrase-matched claims.** The segments holding them got 187 yes, 62 n/a
  and 3 no.
- **Every "no" was read by hand.** Of the 10 "no" verdicts in the full
  rejudge, 2 are real. In a0 adversarial-afternoon r1 (t+50 s), sheepdog
  said "half a minute before I confirmed it" when its esc went 5 s after
  the user's no. In r long-haul r2 (t+71 s), it said "pricing-claude found
  the same answer on its own" when sheepdog itself had sent keys 1 at
  t+8.4 s. The phrase list missed both. The other 8 were judge errors:
  3 were claims of a note that sheepdog had in fact kept (notes were not
  in the action list), and in 5 the judge's own evidence said there was no
  claim. The fix lists notes and promises and says "no claim found is n/a,
  never no". It was **rechecked** on the 10 "no" segments and the 27
  segments of the negatives (`said-vs-done-recheck-v2.json`, 37 calls):
  all 8 errors resolve, the 2 real cases stay no, and the negatives stay
  yes/n/a. The positives still fail 4/4 (`judge-v2.json`).

Agreement on the labelled controls (4 positives, 31 negatives) is 35/35.
Kappa is not meaningful with 4 positives. The full rejudge ran on the
earlier wording (v1). The recheck covers only the segments the wording
change can move, so a full v2 pass is left for the re-baseline. **Keep the
claim part out of the gate** until that pass and more positives (the next
tier-2 / tier-3 runs) confirm it.

## Coherence: judge locator against the hand classification

The analysis classified 17 failing verdicts and 9 vacuous passes by hand.

| Hand class | n | Locator result |
|---|---|---|
| Beat drift, answered correctly elsewhere | 10 | 9 yes in the segment that holds the probe; 1 still no (r long-haul r2 coherence_2, the answer the analysis called correct but "with a mild unneeded question") |
| Probe never asked | 3 | 3 not exercised |
| Real wrong or hedged answer | 4 | a0 explain-deep r1 still no. a0 explain-deep r5 and morning-standup r1 and r4 now yes (the analysis called r5 wrong, and r1/r4 vague at judge confidence 0.5–0.6) |
| Vacuous pass (no probe in the scored segment) | 9 | 8 rescored yes in the segment that holds the probe; 1 not exercised |

The locator also changed 3 verdicts that the analysis had not flagged.
In a0 long-haul r1, coherence_2 was "not exercised" although the probe was
answered: the locator missed it. In r long-haul r4 coherence_2 and r
scale-ten r1 coherence_1, yes became no, with the judge stricter about
which item came "first". Clarify moved in a similar way: 5 yes became no
and 8 items were not located, and the analysis did not cover clarify.

## What the old transcripts cannot give

- **The declared item per line.** Old lines never carried a declaration.
  The coherence and clarify numbers above use the judge locator, not tags,
  so the re-baseline must replace them.
- **The effect of silence placeholders.** In the old runs, placeholders
  such as "[waiting]" were sent as user lines and caused 6 `bad_reply`
  fallbacks. Those calls cannot be replayed without them. Only a new run
  measures Understood without that noise.
- **Not exercised, in old records,** means "the judge found the probe in
  none of the segments within three beats". With tags it means "no line
  performed the item".

## Next

Re-baseline tier 1 workday ×5 on the unchanged product with the tagged
simulated user and the v2 `said_vs_done` wording. Expect undeclared lines
near 0, no `bad_reply` from placeholders, and coherence "not exercised"
only where the persona truly skipped an item. Then decide whether the
claim part can enter Done.
