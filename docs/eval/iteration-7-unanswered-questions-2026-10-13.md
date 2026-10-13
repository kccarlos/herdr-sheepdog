# Harness iteration 7: unanswered questions, 2026-10-13

Harness iteration 7 (docs/handover-2026-10-04.md, "Harness iteration, from 2026-10-11") covers the second of the CEO's product notes (approved scope, 2026-10-13). The note: when the user asked a question that never got a real answer, the orchestrator comes back to it later ("about your earlier question on …"). The daemon records only the clear cases. In case (a) the orchestrator turn timed out or ended in the daemon's fallback line. In case (b) the user's barge-in cut the answer off before it was said. In case (c) the answer was never voiced. The daemon does not judge whether an answer was good. Before this change nothing recorded these cases, so the question was dropped.

## How often it happens

`count.py` counts each case in every bench event log in the repo (deduplicated, without this iteration's runs) and in the live daemon's logs (read only). The definitions match the daemon's rule below: (b) is the first cut of a playback run tagged with the utterance it answered, not a flush for an urgent push, with less than half of the answer's text heard.

| Source | User utterances | (a) fell back | (b) cut before half was heard | (c) never voiced | All |
|---|---|---|---|---|---|
| Tier 1 bench (1,386 runs, no voice) | 9,292 | 31 (0.3 %): 27 broken replies, 4 timeouts | n/a | n/a | 0.3 % |
| Tier 2 bench (234 runs) | 1,074 | 2 (one more was retried and answered) | 41 (3.8 %) of 60 cut answers | 45 (4.2 %): 43 answer briefings unvoiced (38 because the user spoke first, 5 overtaken by the next briefing), 2 dropped as stale | 88 (8.2 %) |
| Live daemon (Sep 18 – Oct 13) | 238 (days with relay events; two Sep 23 fallbacks predate them, so the rates are a little high) | 23: 15 model errors, 8 timeouts; 13 of them on Oct 2, during a Vertex 504 outage | 4 of 5 cut answers | 5: 2 dropped as stale, 2 unheard, 1 superseded | 32 (13 %; 8 % without Oct 2) |

So it is not rare where a voice is involved: about one question in twelve in tier 2, and about the same live once the outage day is left out. Cases (b) and (c) are the common ones. Case (a) is rare since the queue-budget fix (#205), except on a bad provider day. Raw counts: [bench-counts.txt](../bench/results/2026-10-13-iter7/bench-counts.txt) and [live-counts.txt](../bench/results/2026-10-13-iter7/live-counts.txt).

## The change (information)

**Information**, plus one answer-contract field. No judgment in code, no new action rails.

- **Record** (`internal/supervisor/unanswered.go`). The daemon keeps the user's words with a plain reason in three cases:
  - (a) The utterance turn fell back (error, timeout, broken reply), inline or late. A retry of the same words that then answers clears it (rerelay.go).
  - (b) The voice reports a cut run tagged with the utterance it answered. The run was not flushed for an urgent push, and the user heard less than half of the answer's text. Only the first cut of an answer counts, because the tail of the same barge-in often flushes as a second, tiny run.
  - (c) The voice layer judged a late answer unheard and did not re-queue it, or the late reply was dropped unsaid as stale because the user said something new first.
  - Never news briefings: re-voicing those is the item the CEO deferred.
- **Voice plumbing** (`internal/voice`). A cut transcript now carries the utterance its run played (the playback start's tag) and whether a push flush caused it.
- **Fact** (`internal/brain/prompt.go`). A `## Unanswered` section in the orchestrator's state block, present only when something is on record: `- utt-…, 3 min ago: "why did bench-codex's tests fail?" (your turn on it failed: the orchestrator's model returned an error)`. A cut shows what was heard: `(the user talked over its answer after 2 s; they heard only "…")`.
- **Clearing.** The reply's new `answers_earlier` field lists the ids it settles: answered now, or dropped by the user, or answered since. This is the minimal fit to the contract, parallel to `keeps_promise_ids`. One sentence in RULES.md "Answer contract": list the ids this reply settles; "Come back to one when it still matters to them and fits the moment; never repeat what they heard." The voice prompt is unchanged.
- **Runaway rails, not judgments.** At most 3 entries (the oldest goes first), and each expires 10 minutes after it was asked. Reason for the age: in the live logs 92 % of the user's utterances come within 10 minutes of the previous one, so a longer gap is a new sitting, where an old question is more likely noise than news. Reset ("New conversation") clears all entries. The logs show `supervisor.unanswered_recorded` and `supervisor.unanswered_cleared {by: model|retry|expiry|cap|reset}`.
- The prompt budget holds: 4,927 of 4,975 tokens, with no raise.

### A fix made during the iteration (disclosed)

The first version judged a cut by the run's played time against the run's length. The tier-2 check showed this was wrong. The voice streams its audio, so a cut run is often all the audio sent so far. One answer was recorded when the user had heard its whole first sentence: the tail flushed as a second, tiny run. One long answer cut after its first clause was missed. The rule now compares the heard text with the answer's text and counts only the first cut (93f144a). The tier-1 comparison is unaffected: its stand-in cut hears three words of long answers, which is under half by either rule. The workday gate has no cuts.

## Eval

Tier 1, DeepSeek `deepseek-flash` orchestrator, `--fallback-provider none`, chattiness chatty. Arms:

- main = main 9ae2f15, plus the voice cut tag (unread on main), plus the eval fault harness.
- fix = the branch.

Both arms read the same scenario file. The pre-registration is in [STATUS.md](../bench/results/2026-10-13-iter7/STATUS.md), written before the comparison runs.

- **Fault harness** (`internal/eval/simfaults.go`, test-only). A scenario's `faults` force one of three things on given user turns:
  - an orchestrator turn whose model calls fail, so the turn falls back;
  - an answer the user talks over: the voice reports a cut and the user hears only its first 3 words (6 characters in Chinese);
  - an answer never voiced: the user hears nothing and the voice layer reports it unheard.

  Tier 1 has no voice, so (b) and (c) are stand-ins with the events the voice layer would send. The voice layer's own tests cover how it detects them.
- **Scenarios** (`testdata/eval/sim/unanswered.json`, fixed scripts). There are six targets, `ua-{fail,cut,unvoiced}-{en,zh}`. Each asks a question that gets no real answer, then asks about another agent, waits for a finish, and ends with "thanks, that's all". The question is never asked again or invited. There are four noise controls: `ua-dropped-{en,zh}` (the user says never mind) and `ua-reasked-{en,zh}` (the user asks again and gets the answer). Each scenario ran ×6 per arm, interleaved: 120 runs.
- **Disclosed tuning before the comparison.** The first script version ended on "anything else I should know?". That turn asks for open items, so an answer there says nothing about coming back to the question. A main smoke run answered most questions there. The turn was removed before any comparison run. The Chinese stand-in cut first split on spaces and was fixed. Both smoke versions are kept.
- **Judge** (`ansjudge.py`). One blind `claude -p --model sonnet` call per run. It gets the transcript, the question, what the user heard of its answer, the world's answer and the agent's whole screen. It returns whether a later line gave the substance of the answer unprompted (answered later), and whether a later line came back to the question when it should not (stale follow-up). I checked the judge on the 10 main smoke runs against my own reading, and it agreed on all 10.

## Results

**Targeted metric: answered later** (targets, one question per run, Newcombe 95 % CI):

| | main | fix | fix − main (95 % CI) |
|---|---|---|---|
| **All targets** | 20/36 (56 %) | 36/36 (100 %) | **+44 pp (+27 to +60)** |
| (a) fell back | 8/12 | 12/12 | +33 pp (+2 to +61) |
| (b) cut | 1/12 | 12/12 | +92 pp (+55 to +99) |
| (c) never voiced | 11/12 | 12/12 | +8 pp (−17 to +35) |
| English | 12/18 | 18/18 | +33 pp (+9 to +56) |
| Chinese | 8/18 | 18/18 | +56 pp (+28 to +75) |

Both arms answer at the first chance, the reply to the next user line. Main already comes back to a question it did not hear answered in (c) and often in (a). Its transcript holds the fallback line and the daemon's "the user did not hear" note. Main almost never comes back to a cut answer: it reads "Assistant said (cut off after 1.2 s): …" as said. The fix comes back in every case. The record was always cleared on the next user turn (60/60 by the model, none by expiry).

**Noise guard: stale follow-ups.**

| | main | fix | fix − main (95 % CI) |
|---|---|---|---|
| Controls, as the judge flagged them | 1/24 | 6/24 | +21 pp (0 to +41) |
| Controls, under the pre-registered rubric | 0/24 | 2/24 | +8 pp |
| Targets: answer told again (rubric) | 1/36 | 2/36 | +3 pp |

The pre-registration said every flagged case would be read. Most judge flags are a reminder at sign-off that an item is still open ("the two broken links are still there whenever you want them"), which the rubric excludes. The fix has more of these because it answered more: the answer opens a decision. One blind Sonnet call re-scored all 28 flagged quotes, shuffled and without the arm, against the rubric (`restale.py`). It left 2 real cases in the controls, both `ua-dropped-zh`: at sign-off the orchestrator mentions the question the user had dropped ("之前的那个问题按你说的不说了", "README 那件小事我按你说的先放下了"). That is +8 pp, inside the pre-registered +10 pp. It is the noise this change adds, and it is worth watching. Two caveats for this leg of the merge case:

- **The margin is one run.** A third stale control run would have made it +12.5 pp, and the judge's raw flags (+21 pp) would have failed the guard.
- **The re-score's "not stale" clause is broader than the judge's rubric.** It says "a reminder that an item is still open"; the judge's rubric said "an offer of a next step … or a one-word mention inside a status list". I read the four `reasked` flags against the narrower wording. All four are offers of the next step on the open bug ("Codex is still waiting on your go ahead to fix that failing add function"), so the count holds.

**Gate.**

- Workday ×5 per arm, code checks: [northstar-wd-main-vs-fix-fakejudge.md](../bench/results/2026-10-13-iter7/northstar-wd-main-vs-fix-fakejudge.md).
- Workday ×3 per arm, segment Understood by the Sonnet segment judge: [northstar-wd3-main-vs-fix-sonnet.md](../bench/results/2026-10-13-iter7/northstar-wd3-main-vs-fix-sonnet.md). It was scoped from ×5 to ×3 for the 5-hour Claude quota (the full ×5 projected to about 1.5 sessions). The fix arm's workday runs never had an Unanswered section: there was no utterance fallback in 35 runs. So the only difference there is the RULES.md sentence.

| | main | fix | fix − main (95 % CI) |
|---|---|---|---|
| Segment Understood (×3, Sonnet, cluster bootstrap) | 229/249 (92 %) | 232/253 (92 %) | −0 pp (−5 to +5) |
| Segment Safe (judge, ×3) | 301/307 (98 %) | 306/310 (99 %) | |
| Safe, run level (×5, code checks) | 31/35 | 32/35 | +3 pp (−13 to +18) |
| Held out: adversarial-afternoon pass | 5/5 | 5/5 | |
| Held out: explain-deep pass | 5/5 | 5/5 | |
| Missed news (×5) | 17 % | 14 % | −4 pp (−10 to +3) |
| Actions never told (×5) | 6/91 | 2/89 | −4 pp (−12 to +2) |
| Low effort (×5) | 30/35 | 25/35 | −14 pp (−33 to +5) |

Low effort is not in the merge rule, and its CI spans 0. Almost all of the drop is `workday-scale-ten`, where the simulated user rephrased 7 times against 2. The source is the known ambiguous-approval exchange ("approve the codex one" → "which one?" → "the cache one"; iteration 5), not anything this change shows. The fix arm had no Unanswered section on those runs. Watch it.

**Tier 2 check (benign scenarios only, not judged).** `barge-in-long-answer`, `barge-in-hold-to-talk` and `slowness-brain-9s` ran ×3 per arm on Vertex Live, with 2 scratch daemons on :7921/:7922. The fix arm used the corrected cut rule. It recorded 4 of its 6 barge-in answers as cut, and the orchestrator cleared 3 of them on the next turn. Two of the four were hold-to-talk cuts after the answer's first sentence: borderline records, which the orchestrator then settles. The 9-second slow provider made no utterance fall back: utterance turns took 11–22 s inside their budget, and only announce turns fell back, which are not recorded by design. In `barge-in-long-answer` both arms gave the finding after the barge-in 3/3, because the simulated user's next line asks for it. The hooks fire on real cuts, and the voice is unchanged.

## Decision

**Merged.**

- Segment Understood is not worse (−0 pp, CI −5 to +5).
- The targeted metric improves beyond its CI: answered later +44 pp, CI +27 to +60.
- Safe does not regress, at run level or segment level.
- The held-outs do not regress (5/5 both).
- The noise guard passed under the rubric (+8 pp) after the pre-registered reading of every flag, by a margin of one run. The judge's raw flags (+21 pp) would have failed it. Both are disclosed above.

## Next

1. **Watch the sign-off mention of a dropped question** (2 of 12 dropped runs). If it shows up live, try a prompt sentence such as "once settled, never bring it up again" as its own iteration, with the dropped controls ×12.
2. **Inline answers the voice never says** are not detected. Only late answers (pushes) are watched for being unvoiced. A tool-result answer the voice skips stays invisible. Measure it from `voice.turn` and playback events before building anything.
3. **Low effort on scale-ten:** see whether the rephrasings persist at ×10. They come from the ambiguous-approval exchange (iteration 5's open item).

## Spend

- DeepSeek balance: 44.27 → 34.58 CNY (9.69 CNY). That covers 10 + 10 smoke runs, 120 comparison runs, 70 workday runs and 21 tier-2 runs. About $2.9 at list prices for tier 1.
- Tier 2: about $1.55 at list prices (Vertex Live, TTS, simulated user and DeepSeek).
- Sonnet ran on the local Claude subscription: 130 answer-judge calls, 1 re-score call and about 450 segment calls. That is about 65 % of one 5-hour session; the weekly went from 54 % to 48 % left.
- No API spend beyond the tier-2 Vertex calls.

Raw data: [../bench/results/2026-10-13-iter7/](../bench/results/2026-10-13-iter7/).
