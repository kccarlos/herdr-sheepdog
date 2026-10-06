# Judge calibration

Step 1 of "Evals first" (docs/handover-2026-10-04.md). Before the suite
grows, we measure how often the LLM judge agrees with the CEO, one rubric
item at a time. Items where they disagree get fixed before anyone
trusts their numbers.

## Files (`2026-10-05/`)

| File | What it holds |
|---|---|
| `items.json` | 30 transcript segments to label. Each has `id`, `source` (file and time range), `lang`, `context` (world facts, what the orchestrator knew, known judge artefacts), `turns` (`user` / `voice` / `orchestrator` / `action` / `event` with `mm:ss` times) and `rubric_ids`. The top-level `rubric` is the production rubric, each item with `question`, `pass_when` and `fail_when`. |
| `judge.json` | The judge's verdicts on every (item, rubric id) pair: `item_id`, `rubric_id`, `verdict` (`pass` / `fail`), `evidence` (the judge's quote). Vertex `gemini-3.1-flash-lite`, the production judge model and prompt; 126 verdicts, 171 calls, about $0.07. |
| `reference.json`, `reference-summary.md`, `review.json` | The Opus reference labels of all 126 pairs (#161) and the 41 pairs the CEO reviewed. |
| `labels-ceo.json` | The CEO's 41 review labels (2026-10-06): `[{"item_id","rubric_id","label":"pass|fail|na","note"}]`. |
| `truth.json` | **Ground truth** for all 126 pairs: the CEO's label where reviewed (`"source": "ceo"`, 41), else the reference label (`"source": "reference"`, 85). The CEO agreed with the reference on 27/31 disagreements and 10/10 spot checks. 84 pass, 42 fail, no n/a. Use it as `--labels`. |
| `bakeoff/` | The 2026-10-06 judge bake-off: every candidate's verdicts (`r1-*`, `r2-*`, `r3-*`, `raw-facts-sonnet.json`) and `agree-*.md` for the final round. |

Mix: tier 1 (7), tier 2 (10), tier 3 (4), real phone sessions from
2026-10-02, -04 and -05 (9). English, Chinese and mixed. Passes and
failures. Topics: explain, status, approvals, deferrals, stop, ambiguous
target, echo, resume, recall, missed news, and actions beyond the CEO's
words. Ten items are "known cases": the production judge's verdict there
was disputed in the 2026-10-05 baseline review, or it passed a real
failure. Each one's context says so.

## Commands

```sh
sheepdog eval calib check --items docs/eval/calibration/2026-10-05/items.json
sheepdog eval calib rubric        # the rubric block, from the code (items.json must match it; a test checks)
sheepdog eval calib judge --items docs/eval/calibration/2026-10-05/items.json \
  --out /tmp/judge.json                    # Claude Sonnet via `claude -p` (default)
sheepdog eval calib judge … --judge vertex --judge-model gemini-3.1-flash-lite --env-file .env
sheepdog eval calib agree --labels docs/eval/calibration/2026-10-05/truth.json --judge-file docs/eval/calibration/2026-10-05/judge.json [--json]
```

`agree` prints agreement and Cohen's kappa overall and per rubric item,
agreement per segment, and every disagreement with the judge's evidence
and the CEO's note. Pairs where the CEO says `na` are left out of
agreement and kappa and counted per item as "CEO n/a". Many n/a labels
on one item mean it was asked of segments where it does not apply.
Kappa is undefined (shown as –) when both raters used one label
throughout. The explanation items have only 1 to 3 segments each, so
read their per-item kappa as anecdotal.

## How the judge sees an item

It gets one call per rubric item (temperature 0 on Vertex; the Claude CLI has no temperature setting; a final verdict, no
"wait"). The item's `context` goes in as FACTS and its turns as the
CONVERSATION. When the segment has voice turns, the voice is what the
CEO heard and the orchestrator's text is marked as not heard. Without
voice turns (tier 1, typed tier 3), the orchestrator's text is what the
CEO heard. The judge therefore works from the same information the CEO
labels from, not the full worker screens a live run gives it. The
calibration measures the rubric and the judge's reading, not
screen-parsing.

Calibration-only wording: the questions of `answered`, the depth items
and `explain_padding` cover the whole segment ("each time the CEO asked
why …") instead of one quoted CEO line. Their pass/fail definitions are
the production ones.

## Two-sided rubric (2026-10-05)

Every default item and every explanation item now says when it passes
and when it fails, in both directions:

- `length_fits` (new): too thin fails (reasons, example, numbers or
  options dropped; a request for more detail answered with a rephrase).
  Too long fails (background, screen details or option lists on a simple
  question).
- `ear_friendly` (changed): no longer "short by default". It fails on
  paths, ids, code or unexplained jargon read out, and also when the
  thing the answer hinges on is blurred to sound simple.
- `answered`: also fails on claiming more than happened ("done",
  "verified" with no basis).
- `no_unintended_action`: also fails when a plainly asked action was not
  taken. Daemon-side renames, closes and confirmed prompts count as
  actions. Text on a worker's own screen does not.
- `right_agent`: also fails on asking "which one?" when only one fits.
- `no_hallucination`: also fails on "I can't tell" when the facts
  plainly show it.
- `facts_current`: also fails on hedging away a current, plainly shown
  fact.
- `language`: also fails on translating a name or technical term so
  that it loses its meaning (种族检测器 for "race detector") or on a
  switch the CEO did not make.
- Explanation items (`explain_point`, `explain_comprehension_*`,
  `explain_depth_more/why/example`, `explain_padding`): thin fails, and
  so do buried, guessed, rambling or code-read-out answers.
  `explain_padding` also fails when the answer is too thin (no direct
  answer).

The judge returns `evidence` (a verbatim quote) on every verdict. Its
system prompt no longer carries a general "short" preference: it
follows each item's own definition. The explain scenarios no longer skip
`ear_friendly`, since it no longer penalises length. Scenario-specific
`expect.rubric` strings in testdata are unchanged. New ones should state
both directions.

## First observations (before any labels)

- The judge's evidence is not always verbatim. On Chinese lines,
  flash-lite sometimes changes characters in the quote (`其交代` for
  `已交代`, `我其在让` for `我已经让`). A quote that is not in the item is
  a sign that the judge did not read what it cites. A later `agree`
  column could check that the quote really appears in the item.
- Halo effect: on a segment with one glaring failure, the judge tends to
  fail every item asked. `t2-ambiguous-codex-both` fails
  `no_hallucination` and `answered` along with `right_agent`. Watch for
  this in the agreement numbers.

## Ground truth and the judge bake-off (2026-10-06)

Queue item 1 of the night of 2026-10-06 (docs/handover-2026-10-04.md).
Full write-up with every number: [judge-bakeoff-2026-10-06.md](../judge-bakeoff-2026-10-06.md).

**Ground truth** is `truth.json` (above). The CEO's review overturned the
reference on four pairs: `t1-simple-did-tests-pass` `length_fits` and
`explain_padding` (a bare "done" must say done with what),
`t2-recall-after-two-resumes` `no_hallucination` ("the project
documentation" for the README is scope creep, not a fair paraphrase), and
`phone-1004-approve-auto-mode` `language` (mixing is fine when several
languages are set). The reference's "judge too strict on paraphrase"
reading in reference-summary.md is therefore partly wrong.

**Rubric changes** (ids kept, both directions kept; `items.json`'s rubric
block regenerated from the code):

- From the CEO's notes: `language` passes a mix when the CEO has set
  several languages or speaks a mix; `length_fits` and `explain_padding`
  fail a short answer that does not name what it is about; `no_hallucination`
  fails a specific fact widened into something broader (scope creep);
  `no_unintended_action` and `right_agent` pass an ambiguous instruction
  only when sheepdog asked or said its assumption ("I'm assuming you mean
  X"); `explain_depth_example` fails an analogy in place of a real case.
- From the disagreements of the first round (tuned on this set; there is
  no held-out calibration set yet): the judge's system prompt charges each
  flaw only to the item that names it (the halo effect); `answered` fails
  on over-claiming only about what was asked; `ear_friendly` treats the
  listener as a developer (regex, race detector, a short file name need no
  explanation); `length_fits` allows a short closing offer or yes/no
  question, and only a list of three or more options counts as too long.
- Item fixes: `phone-1002-why-becomes-new-task` now has the CEO's line
  "然后让 Claude 加一个删除功能。" (the event log has it; the CEO confirmed),
  and `phone-1004-approve-auto-mode`'s context says both languages are set.

**Result** (final rubric, 126 pairs vs `truth.json`): Claude Sonnet via
the CLI, two runs: agreement 87 % / 87 %, kappa **0.72 / 0.72**,
`length_fits` kappa **1.00 / 0.70**. Flash-Lite 84 %, kappa 0.65,
`length_fits` 0.63 (it was 0.45 / 0.14 against the reference before the
rubric work). Gemini 3.1 Pro (preview) 0.69 on the first-round rubric with
`length_fits` 0.42, about $1.1–1.8 per 126 pairs. Gemini 3.5 Flash could
not be measured: Vertex started answering every call with 403 "Request
rejected in accordance with our terms of use and policies" midway through
the bake-off. Only Sonnet met the target (kappa ≥ 0.7 overall, ≥ 0.6 on
`length_fits`) and it is now the default judge.

**The CLI judge.** `--judge claude` (the default) runs one
`claude -p --model sonnet --output-format json --json-schema … --system-prompt …
--tools "" --strict-mcp-config --setting-sources "" --no-session-persistence`
process per call, in an empty temporary directory, prompt on stdin, on the
signed-in Claude subscription (ANTHROPIC_API_KEY is removed from its
environment, so it never bills the API). No temperature control: a rerun
moves pairs (the two final runs each agree with the truth on 110 of 126, but differ from each other on 14, mostly close calls; read single-run per-item numbers with that in mind). The rationale
call is skipped (a no carries its evidence quote). Cost per 126 pairs:
about 520 k input and 50 k output tokens, 3 min 45 s at three processes,
about 2 % of the Max plan's 5-hour window, $0 API (about $2.7 at list
price). `--judge vertex` keeps Flash-Lite (about $0.06–0.16 per 126 pairs).

## Segment judging on the calibration set (2026-10-06)

Segment-level judging asks one call per transcript segment that scores
every item that applies (docs/bench/README.md, "Segment-level judging").
The calibration items are segment-sized, so the same 126 pairs measure
it: `sheepdog eval calib judge --segment` asks one call per item (30
calls) scoring all its rubric ids, and `calib agree --labels truth.json`
compares as before. Results in `bakeoff/seg-sonnet-{a,b,c,d}.json` and
`agree-seg-sonnet-*.md` (final segment prompt):

| Run | Agreement | Kappa | `answered` kappa | `length_fits` kappa | Too lenient / too strict |
|---|---|---|---|---|---|
| a | 86 % | 0.69 | 0.58 | 0.67 | 6 / 12 |
| b | 90 % | 0.77 | 0.75 | 0.77 | 5 / 8 |
| c | 87 % | 0.72 | 0.65 | 0.88 | 6 / 10 |
| d | 89 % | 0.76 | 0.75 | 0.89 | 5 / 9 |

Mean kappa 0.74 (0.69–0.77), against 0.72 / 0.72 for one call per pair
(r3 above); two earlier runs on the first segment prompt (before it named
the segment's time span) gave 0.71 and 0.74. One call per item instead of
one per pair: 30 calls, 1 min 30 s, about 1 % of a 5-hour window. It errs
mostly strict, as one call per pair did (8–12 strict vs 5–6 lenient of
126). Run d's
first try lost one 8-item answer that left ids out; the judge now asks
again once when an id is missing.
