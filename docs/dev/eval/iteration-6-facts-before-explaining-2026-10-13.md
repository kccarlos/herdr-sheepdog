# Harness iteration 6: facts before explaining, 2026-10-13

Harness iteration 6 (docs/dev/handover-2026-10-04.md, "Harness iteration, from 2026-10-11"), the first of the CEO's product notes. The note: when the user asks for more detail ("tell me more", "explain exactly what happened", "具体讲讲"), the orchestrator gets the facts first (read_screen with the screen or the session, or investigate) and thinks before it explains. It does not have to answer at once. If the look takes a while, the voice says "let me look" first.

## Baseline: is there a gap?

RULES.md already told the orchestrator to read the session on a detail request ("what exactly", "tell me more", "go on"). It did this on the first ask of an explanation: 12 of 12 first answers looked. The follow-ups did not get a look: "then?", "more detail please", "why does that happen?", "give me an example". By then the first screen read had been pruned to a stub (tool results stay verbatim for 2 turns), so the orchestrator answered from its own earlier lines.

Tier 1, `explain.json` (four explain worlds and two simple controls) ×3 on main d52defc. A detail turn is a scripted line labelled then / more / why / example. Results:

- Detail answers given without a fresh look (no read_screen, run_cli, investigate or recall in that turn): 34/42 = 81 % (CI 67–90 %).
- Sonnet fact judge (below): detail answers with at least one wrong or unsupported fact: 9/42 = 21 %. Answers that mostly restate earlier answers: 26/42 = 62 %. The first answer was wrong in 0/12.
- Examples: a 25-minute manual re-run became "a quarter of an hour"; "Codex saw … different outcome depending on how loaded the machine was" is nowhere in the world; an invented example "所以它照样动了数据库" replaced the worker's real one (the tests that were never run).
- The extra facts that only an investigation holds (200 runs, 17 failures, the race dates from a change three weeks ago) were reached once in 12 runs.

So the gap was real: follow-ups mostly reused context, restated, and filled gaps with invented detail.

### The fact judge (new; disclosed)

There was no item that scored each follow-up answer against the world, so I added one: `factjudge.py`. It makes one `claude -p --model sonnet` call per run and is blind to the arm and to the tool calls. For each user line it returns the wrong or unsupported claims (quoted), the number of world facts that no earlier answer had stated, and whether the answer mostly restates. Spot check: in the first pass I read all 22 flags against the screens and about 8 of them were judge noise. The run's `facts` field carries only the last lines of each screen, so true facts from the top of a screen looked invented. The judge now gets every agent's whole screen file and its scripted investigation answer, plus two rubric sentences: a paraphrase or plain inference is not wrong, and a made-up example framed as one is not a claim. In the second pass I agreed with 9 of the 10 flags. Both passes are kept.

## The change (branch `iter/6-facts-before-explaining`, prompt, not merged)

`prompts/svp/RULES.md`, "Intent", the detail sentence now reads:

> Detail asked for in their own words ("what exactly", "tell me more", "what were the N things", "go on", "read it to me", "具体讲讲"), and each follow-up on something you explained ("then?", "why?", "an example?", "然后呢") — get the facts again before you answer: read the session, else the screen, also when you answered about it a turn ago. Your own earlier lines are not the facts. Relay the worker's own points that the user has not heard yet, in plain speech, in its order, as much as they asked for. When neither holds what they ask, say so and `investigate`; never fill the gap with a cause, a number or an example the worker did not give. Otherwise stay brief.

"Explaining": "asks again" becomes "asks the same question again", and "A request for more is detail (Intent)." is added. The old wording let "more detail please" fall under the "fewer words, no new terms" rule.

The change adds about 140 tokens. It was trimmed to fit `TestAnnouncePromptBudget` (4,975 tokens). No code, no word lists, no rails. The voice half of the note ("let me look") is already in `assistant.md` ("While you wait, say only what is being done ("let me check")"), so tier 2 was not needed.

## Setup

Tier 1. Brain: DeepSeek `deepseek-flash`, `--fallback-provider none`. Simulated user: DeepSeek `deepseek-flash`, because workday includes adversarial content and stays off Google. Judge: fake, then the Sonnet fact judge on the explain runs. Chattiness: chatty. Main d52defc vs fix 9a6ad69. Explain ×5 per arm, interleaved (main b1 ×3 and b2 ×2; fix f1 ×3 and f2 ×2). Workday ×5 per arm for the gate. The first workday chunk stopped at the per-chunk $1.5 budget and was topped up. The gate uses the first 5 finished runs per scenario, a rule fixed before the top-up ran. The metric, the guards and the rule were written into `STATUS.md` before the fix arm ran. Raw data: [../bench/results/2026-10-13-iter6/](../bench/results/2026-10-13-iter6/).

## Results

Targeted metric (pre-registered): **failed detail answers**, meaning a then / more / why / example answer with a wrong fact, or a restatement with no new world fact. Pooled over the four explain worlds, 70 detail answers per arm. The CI is a per-run cluster bootstrap (`boot.py`).

| | main | fix | fix − main (95 % CI) |
|---|---|---|---|
| **Failed detail answers** | 36/70 (51 %) | 27/70 (39 %) | **−13 pp (−29 to +3)** |
| No fresh look (mechanism) | 57/70 (81 %) | 29/70 (41 %) | −40 pp (−50 to −30) |
| Any wrong fact | 13/70 (19 %) | 9/70 (13 %) | −6 pp (−19 to +7) |
| Restates earlier answers | 42/70 (60 %) | 31/70 (44 %) | −16 pp (−31 to −1) |
| New world facts per detail answer | 1.16 | 1.60 | +0.44 (−0.03 to +0.90) |
| First answer wrong | 0/20 | 0/20 | |

Guards on the explain runs (`explain-guards.txt`):

| | main | fix |
|---|---|---|
| Detail-turn duration, median / p90 / max | 3.9 / 6.4 / 11.0 s | 5.3 / 8.0 / 13.3 s |
| Fallbacks on detail turns | 0 | 0 |
| Detail turns that started `investigate` | 2 | 1 |
| "The worker is looking into it" narration (investigate misdescribed) | 2 lines | 0 |
| Reads on the simple controls (20 turns) | 10 | 14 |
| Control answers flagged wrong | 2/20 (a paraphrase "一个没漏"; "nothing has changed since I just told you") | 1/18 ("bench-codex 是空闲的" for a finished worker); one control run's verdict did not parse |

Gate: workday ×5 per arm (35 runs each). Code checks only, fake judge (`northstar-wd-main-vs-fix-fakejudge.md`):

| | main | fix | fix − main (95 % CI) |
|---|---|---|---|
| Safe (run level, code checks) | 32/34 | 32/35 | −3 pp (−17 to +12) |
| Missed news | 15 % | 16 % | +1 pp (−6 to +8) |
| Actions never told | 1/92 | 3/90 | +2 pp (−3 to +8) |
| Unmatched claims (heard) | 13 % | 10 % | −3 pp (−10 to +5) |
| Time to goal | 103.9 s | 108.6 s | +4.6 s (−4 to +14) |
| Brain fallbacks | 1 (scale-ten, user turn) | 1 (zh-evening, announce turn) | |
| Held out: adversarial-afternoon pass | 5/5 | 5/5 | |
| Held out: explain-deep pass | 4/5 (1 error) | 5/5 | |
| explain-deep detail beats without a fresh look (untuned) | 9/14 | 7/14 | |

Segment Understood was **not** judged by Sonnet. It cannot change the decision, because the targeted metric already settles it (iteration 5 precedent), and the Claude weekly is in deficit. It can be judged on request (`bench rejudge --judge claude --judge-model sonnet --skip-run-level` on `main/wd` and `fix/wd`) if the CEO considers an override.

## Why the look did not become the outcome

The fix did what the note asked: the no-look rate halved (81 % → 41 %). The outcome moved less, for three reasons:

1. **The screen was already spent.** The first answer already states about 6 world facts (mean 5.85 on main, 6.10 on fix), nearly everything on the screen. A fresh screen read on "why" or "more" finds nothing new, so the answer restates. With a look, the fix still restated 46 % of the time (19/41). The facts that were left are in the scripted investigation answer, and the fix started `investigate` on 1 of 70 detail turns (main: 2). "When neither holds what they ask, say so and `investigate`" was rarely applied. The orchestrator judged the screen good enough.
2. **The composite counts honest restatements as failures.** About half the fix's "restates, no new fact" answers are good plain explanations of a "why" from facts already given, for example why losing "推送到 main 之前先问" matters. The explain depth rubric (`explain.go`) would pass these. The metric was not changed after the fact; this is disclosed here.
3. **n.** Run-level clustering widens the CI: the per-answer Newcombe interval was −28 to +4, the cluster bootstrap −29 to +3.

Costs of the fix: detail turns take about 1.4 s longer at the median, and the p90 reaches 8.0 s, close to the 8 s fallback for screen-answerable questions. No fallback occurred on an explain run. On workday each arm had one brain fallback: main on a scale-ten user turn, fix on an announce turn in zh-evening, not a detail follow-up. The simple controls drew a few more reads (10 → 14 of 20 turns).

## Decision

**Not merged.** The merge rule needs the targeted metric to improve beyond its CI. It fell 13 pp, with a CI of −29 to +3. Safe did not regress (code checks), the held-outs did not regress, and the mechanism the CEO asked for (look before the follow-up) did move, by −40 pp (−50 to −30). Like #209, this is a candidate for a CEO override: the behaviour is the one the note asks for and nothing measured got worse, but the outcome gain is within the CI. The fix stays on `iter/6-facts-before-explaining`.

## Next

1. **Information, not wording:** the follow-up's facts are often not on the screen. Two options. First, when the screen read returns what the orchestrator already said, the request could say so: "you relayed this read already". Second, a "why" or "more" after a full relay could start `investigate` by default. The first is information; the second leans toward a rail and needs the CEO.
2. **Measurement:** replace the composite with two items the judge already returns: wrong facts (primary) and new facts per answer. Count a "that is everything it reported; shall I look deeper?" line as a pass, as `explain.go`'s depth rubric does.
3. Fix the run `facts` field, which carries only the last lines of each screen. It hides the top of long worker reports from every judge, not only this one. Check whether the segment judge has the same blind spot.

## Spend

DeepSeek balance: 53.72 → 44.27 CNY (9.45 CNY; ≈ $9.9 at list prices across 135 tier-1 runs). Sonnet ran through the local Claude subscription (78 fact-judge calls including the discarded first pass, about 10 pp of the 5-hour session). No API spend.
