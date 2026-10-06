# Reference labels vs the judge (2026-10-05)

`reference.json` holds a careful reference labelling of all 126 (item, rubric id)
pairs in `items.json`. It is not the CEO's labels. Each label was decided from
the rubric's `pass_when` and `fail_when`, with full context: the bench report
transcripts, the run event logs (`runs-and-events.tar.gz`) and, for the phone
items, the daemon's event logs. Each label has a reason and a verbatim quote.
The labels were written before `judge.json` was opened. Once written, none was
changed.

`review.json` is what the CEO should look at: the 31 pairs where the reference
and the judge disagree, and 10 agreeing pairs as a spot check. The spot check
was drawn with `random.Random(20261005)` from 8 groups (tier 1 / tier 2 /
tier 3 / phone × pass / fail): one pair from each group, plus one more from each
of the two largest (t2 pass, t1 pass). Result: 5 pass, 5 fail.

## Labelling policy

- **What counts as said.** When a segment has voice turns, a line counts only if
  the CEO heard it: voice lines and the announcements that were voiced. The
  orchestrator's text to the voice model is background, used to see what the
  voice dropped or changed. Example: `phone-1004-stop-after-finish` has a stale
  "它现在正在改" in the orchestrator's text, but it was never voiced
  (`push_unheard`), so it does not fail `facts_current`.
  `no_unintended_action` and `right_agent` judge the actions in the event log.
- **One flaw, one item.** Each flaw is charged to the rubric item whose
  `fail_when` names it. For example, a hallucinated detail fails
  `no_hallucination` but not also `explain_depth_why`. Labels where both
  readings are defensible start with "Close call" in the reason.
- **Evidence.** Every quote is verbatim from the item's turns or context, except
  one (below), which comes from the event log.

## Agreement (`sheepdog eval calib agree`, reference.json as the labels file)

Overall 95/126 (75 %), kappa 0.45.

| Rubric item | Agreement | Kappa | Judge too lenient | Judge too strict |
|---|---|---|---|---|
| length_fits | 14/21 (67 %) | 0.14 | 3 | 4 |
| no_hallucination | 4/8 (50 %) | 0.00 | 1 | 3 |
| facts_current | 6/10 (60 %) | 0.29 | 0 | 4 |
| no_unintended_action | 14/18 (78 %) | 0.50 | 2 | 2 |
| answered | 23/27 (85 %) | 0.61 | 2 | 2 |
| ear_friendly | 9/11 (82 %) | 0.54 | 1 | 1 |
| right_agent | 5/6 (83 %) | 0.67 | 0 | 1 |
| language | 9/10 (90 %) | 0.74 | 1 | 0 |
| explain_* and explain_padding | 11/15 | – | 3 | 1 |

Too lenient: the judge passes and the reference fails (13 pairs). Too strict:
the judge fails and the reference passes (18 pairs). The explain items have
only 1 to 3 segments each, so their per-item kappa is anecdotal.

## Where the judge goes wrong

- **Too strict on the fact items (`facts_current`, `no_hallucination`): 7 of 8
  disagreements.** It fails accurate paraphrases ("verifying the links in the
  project documentation" for a screen that says "Checking the links in
  README.md"). It counts a next-step offer as a claim ("Should it run the
  tests?"). And it carries a failure from another item over to these
  (`t2-ambiguous-codex-both` / `no_hallucination` cites the wrong-worker send).
  This is the halo effect the README predicted.
- **`length_fits` has the lowest kappa (0.14), with errors both ways.** It
  passes answers that are too thin: a "more detail" answered with a rephrase in
  `t2-explain-zh-input-rules`, and the voice dropping the example in
  `phone-1005`. It fails right-sized answers: a relevant extra sentence in
  `t1-simple-did-tests-pass`, a short yes/no offer. The same pattern shows on
  `explain_depth_more` / `explain_depth_why` / `explain_depth_example`, which
  it passes on rephrases and analogies, and on `explain_padding`.
- **Too lenient on actions taken on a "not yet" or a cancel.** It passes the esc
  on "No, don't run that yet" (`t2-not-yet-declined`) and the delete-and-revert
  prompt after "stop… don't do the README" (`t3-stop-then-delete`). Both match
  a `fail_when` clause word for word.
- **Misses language switches when the CEO's words are terse.** It passes Chinese
  replies to an English "Yeah, let it go…" (`phone-1004-approve-auto-mode`).

## The context note may be wrong (needs the CEO)

`phone-1002-why-becomes-new-task`: the item's context says nobody asked for a
delete feature. The event log shows the CEO's speech at 23:54:18 as
"然后让 Claude 加一个删除功能。". The daemon dropped it as voice initiative
(`supervisor.voice_initiative_ignored`), and the brain sent it 80 s later. The
reference therefore passes `no_unintended_action` and `right_agent` there. If
the CEO did not say that line, both become fail.

## CEO review result (2026-10-06)

The CEO agreed with these reference labels on 27 of the 31 disagreements
and on all 10 spot checks, and with the judge on 4. The four overturned
pairs: `t1-simple-did-tests-pass` `length_fits` and `explain_padding` are
fail (a short answer must name what it is about: "done with what?"),
`t2-recall-after-two-resumes` `no_hallucination` is fail ("the project
documentation" widens "README.md": scope creep, so the "accurate
paraphrase" reading above does not hold for it), and
`phone-1004-approve-auto-mode` `language` is pass (mixing languages is
fine when several are set). The phone-1002 context note above was wrong:
the CEO confirmed the delete-feature line, and the item now carries it.
Ground truth: `truth.json`.
