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
| `labels.json` | The CEO's labels, once given: `[{"item_id","rubric_id","label":"pass|fail|na","note"}]`. |

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
  --out docs/eval/calibration/2026-10-05/judge.json --env-file .env
sheepdog eval calib agree --labels labels.json --judge-file docs/eval/calibration/2026-10-05/judge.json [--json]
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

It gets one call per rubric item (temperature 0, a final verdict, no
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
