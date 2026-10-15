# Harness iteration 6b: nothing new, so ask, 2026-10-13

Harness iteration 6b follows iteration 6 ([iteration-6-facts-before-explaining-2026-10-13.md](iteration-6-facts-before-explaining-2026-10-13.md)). Iteration 6 got the orchestrator to look again on a follow-up, but even with a fresh look it restated in 46 % of follow-ups. The CEO's reading (2026-10-13): after the first explanation the screen is spent. On "why", "more" or "an example" the orchestrator re-reads the screen, finds nothing new, cannot tell, and restates. The remaining facts are reachable only by asking the worker or the code, and the orchestrator started `investigate` on 1 of 70 detail turns. The CEO approved two changes, built on iteration 6's RULES.md change.

## The changes (branch `iter/6b-nothing-new`, not merged)

**A, information** (`internal/brain/reads.go`). The daemon keeps each agent's last four screen reads and session reads. Before comparing, it removes spinner and cursor glyphs, clock times, elapsed times, token counts and extra whitespace. When a read's content is contained in a read from an earlier turn, the tool result gets a note after the worker-output end marker:

> Note from the daemon: this is the same as what you read of bench-codex's screen at 15:04:05 (2 min ago), which you answered the user from; nothing in it has changed since.

"Which you answered the user from" appears only when that turn's answer was spoken. A session read is compared by its last message. The pruned stub keeps "unchanged since 15:04:05", and "New conversation" clears the record. Event screens of announce turns are not recorded in this iteration. The comparison is mechanical: there is no judgment about the user's words and no rail. Unit tests cover these cases: identical read, noise-only change, a shorter read of the same screen, a real change, a longer read, reset, the stub, a session read with moved tool counts, and relayed versus skipped answers.

**B, prompt** (`prompts/svp/RULES.md`, "Intent"). Iteration 6's "When neither holds what they ask, say so and `investigate`" now reads:

> When the read holds nothing they have not heard (the daemon marks a read unchanged since you answered from it), do not restate: say that is all it shows, `ask` "Want me to ask it?", not a decision they heard. On their yes, or "find out" / "ask it": `investigate` (source worker for why it did something, what it tried or decided; cli for code, a log or a diff); `prompt` the worker itself only when they say to ask it directly, or it is idle.

The prompt-size test failed on main plus iteration 6 alone (5,047 of 4,975 tokens). B is paid for by trims of descriptive text that repeats the tool declarations, in Tools, Record and Fallback. The result is 4,971 tokens. The smoke runs led to one disclosed tuning before any comparison run: the orchestrator said "that is everything it has", then put the worker's open decision in `ask` (0 offers in 4 worlds). B therefore gained "not a decision they heard".

## The scripted world had to be able to say yes

Neither simulated user could accept the offer. Fixed scripts ignore it, and the agenda user was told to say no to anything its agenda does not cover. The eval harness (commit 799fe88, run by every arm) changes three things:

- A fixed script with `offer_yes` says "Yes, ask it." / "好，问问它。" when sheepdog's reply to a script line ends on a question offering to find something out (`OffersToFindOut`, a regex on the last question). It does this once per script line, then waits once for the answer. These lines are tagged `answer` and do not move the script on.
- The agenda user's prompt gains: "An offer only to find something out for you (ask a worker, look deeper) changes nothing: say yes when you still want that answer."
- The four explain worlds get `offer_yes` and room for the extra turns. A worker there also answers a direct prompt with its scripted finding, because B allows a prompt to an idle worker and every explain worker is idle.

Limits, disclosed:

- **Offers with a choice list.** The scripted yes missed 3 of the fix arm's 9 offers. In these replies an options list followed the question, so the reply did not end on it.
- **No sessions in tier 1.** No worker has a session record there, so `investigate source worker` is refused and the orchestrator falls back to cli.

## Setup

- **Tier and models.** Tier 1. DeepSeek `deepseek-flash` runs both the orchestrator (`--fallback-provider none`) and the simulated user. Chattiness: chatty.
- **Four arms.** Each binary embeds its own prompts:
  - main: main e04ace5 plus the harness.
  - iter6: iteration 6's RULES.md plus the harness.
  - b: iteration 6 plus B, without A. fix − b is A's contribution.
  - fix: A plus B.
- **Explain runs.** `explain.json` ×5 per arm, interleaved: four explain worlds and two controls, 70 detail lines per arm.
- **Workday gate.** Workday ×5 per arm for main and fix, code checks, fake judge.
- **Fact judge.** Iteration 6's Sonnet fact judge (one blind call per run, whole screens) with three additions:
  - `restates` no longer counts a short "that is all it reported";
  - `offers_to_ask`;
  - `screen_left`: the number of screen facts on the question that no answer up to and including this one has stated.
- **Pre-registration.** The metrics, the arms and the merge rule were committed before the comparison runs ([STATUS.md](../bench/results/2026-10-13-iter6b/STATUS.md)). CIs are a per-run cluster bootstrap (`boot.py`). Raw data: [../bench/results/2026-10-13-iter6b/](../bench/results/2026-10-13-iter6b/).

## Results

The primary metric is **restating follow-ups**, pre-registered as the targeted metric: then / more / why / example answers that the judge marks `restates`. A detail line's episode is the line plus the yes that answers an offer on it, including the pushes that land before the next line.

| per detail line (70 per arm) | main | iter6 | b (B, no A) | fix (A + B) | fix − main (95 % CI) |
|---|---|---|---|---|---|
| **Restating follow-ups** | 51 % | 31 % | 47 % | 39 % | **−13 pp (−29 to +4)** |
| New facts per episode | 1.33 | 1.81 | 1.50 | 1.53 | +0.20 (−0.28 to +0.70) |
| New facts on the line alone | 1.33 | 1.81 | 1.41 | 1.36 | +0.03 (−0.43 to +0.53) |
| Wrong fact in the episode | 17 % | 9 % | 21 % | 30 % | +13 pp (−3 to +29) |
| No fresh look | 77 % | 44 % | 54 % | 57 % | −20 pp (−30 to −10) |
| Read marked unchanged (A firing) | 0 | 0 | 0 | 37 % | |
| Offers to ask (judge) | 3 % | 6 % | 4 % | 13 % | +10 pp (+1 to +20) |
| Offers while the screen still held unheard facts on the question (noise) | 0/2 | 3/4 | 1/3 | 8/9 | |
| Episodes that asked (investigate, prompt or run_cli) | 2 | 4 | 2 | 5 | |
| investigate starts / prompts to the worker | 1 / 0 | 2 / 0 | 0 / 2 | 3 / 3 | |
| Detail-turn duration, median / p90 (share over 8 s) | 3.9 / 7.5 s (5/70) | 5.4 / 8.8 s (7/70) | 5.1 / 8.2 s (7/70) | 5.4 / 7.9 s (6/70) | |
| Yes-turn duration, median / max | | | 4.3 s | 6.6 / 9.6 s | |
| Fallbacks on detail and yes turns | 0 | 0 | 0 | 0 | |
| First answer wrong | 1/20 | 1/20 | 0/20 | 1/20 | |
| Controls: reads / offers on 20 turns | 14 / 0 | 15 / 0 | 12 / 0 | 11 / 0 | |

Other comparisons (`explain-boot.txt`):

- **A's contribution (fix − b).** Restates −9 pp (−24 to +8); offers +9 pp (−1 to +19); wrong facts +9 pp (−6 to +24); new facts per episode +0.03.
- **B with the trims (b − iter6).** Restates +16 pp (+0 to +31); wrong facts +13 pp (+3 to +23).
- **Iteration 6 alone (iter6 − main).** This comparison is exploratory. It was reported beside the others, not pre-registered, and the rubric changed. Restates −20 pp (−36 to −4); new facts per episode +0.49 (−0.03 to +1.03); wrong facts 17 → 9 %.
- **fix − iter6.** Wrong facts +21 pp (+8 to +36).

Gate: workday ×5 per arm, code checks ([northstar-wd-main-vs-fix-fakejudge.md](../bench/results/2026-10-13-iter6b/northstar-wd-main-vs-fix-fakejudge.md)).

| | main | fix | fix − main (95 % CI) |
|---|---|---|---|
| Safe (run level, code checks) | 32/35 | 32/35 | +0 pp (−15 to +15) |
| Held out: adversarial-afternoon / explain-deep pass | 5/5, 5/5 | 5/5, 5/5 | |
| Missed news | 16 % | 12 % | −3 pp (−9 to +3) |
| Actions never told | 5/97 | 3/97 | −2 pp (−9 to +4) |
| Unmatched claims (heard) | 12 % | 8 % | −4 pp (−11 to +2) |
| Low effort | 23/35 | 18/35 | −14 pp (−35 to +8) |
| Brain fallbacks / investigate calls / reads marked unchanged | 3 / 5 / 0 | 1 / 11 / 28 | |

Low effort drops mostly through the extra exchange: lines that answer sheepdog went 19 → 25, rephrasings 12 → 13. Segment Understood was not judged by Sonnet (see Decision).

## Why it did not work

1. **The screen is not spent.** After the first explain answer, the judge finds on average 4.4–4.7 facts still on the screen that bear on the question (3.5 in b). After the last line, 1–1.6 remain. The first answer relays about six facts of a report that holds about ten. The rest is still on the screen, not only in the investigation. So "nothing new" was rarely true.
2. **A says "unchanged", and the model hears "fully relayed".** The note fired on 37 % of the fix arm's detail lines. On most of them the screen still held unheard facts, and the model offered to ask instead of relaying them: 8 of its 9 offers were made with facts left on the screen. In the worst case it said "它报的确实就是这些，没有别的了" ("that really is all it reported, nothing else"). The input-rules screen still held the 2026-10-02 example, 9 of 41 tests, the safety rule and the recommendation. Only 2 of the fix arm's 21 wrong episodes are such "that's all" claims, plus one "the screen has not changed". The rest are embellishments, as in the other arms, so most of the rise in wrong facts against main is within noise. Against iteration 6 alone it is not (+21 pp, +8 to +36).
3. **B alone barely changed behaviour.** Without the note, offers went 3 → 4 % and restates 51 → 47 %. The b arm also did worse than iteration 6 alone (restates +16 pp, wrong +13 pp). B's sentence and the trims that paid for it (Tools, Record, Fallback) are in the same arm. This iteration cannot tell which caused it.
4. **When the user says yes, the flow works.** All 5 accepted offers (3 in fix, 2 in b) led to a prompt to the idle worker, and the scripted finding came back as a push with new facts. B allows a prompt when the worker is idle, and every explain worker is idle. One of the prompts came after `investigate source worker` was refused (tier 1 has no sessions). Separately, the fix arm started `investigate` on its own on 2 detail lines.

## Decision

**Not merged.**

- **The primary metric is within its CI.** Restating follow-ups fell 13 pp, CI −29 to +4.
- **Wrong facts moved the wrong way.** +13 pp against main (not significant); +21 pp against iteration 6 alone, beyond its CI.
- **The gate is clean.** Safe 32/35 in both arms; the held-outs pass 5/5 in both; fallbacks 3 → 1.
- **Segment Understood was not judged by Sonnet.** With the primary failing it cannot change the decision (iterations 5 and 6 precedent). For an override review, run `bench rejudge --judge claude --judge-model sonnet --skip-run-level` on `main/wd` and `fix/wd`.

The branch keeps A, B, the trims and the harness. The harness commit (`offer_yes`, the agenda-user sentence, the explain worlds' prompt answers) is a measurement change and could be merged on its own.

## Next

1. **Relay what is left, then ask.** The missing step is relaying what the screen still holds, not asking the worker. If A is kept, its note should state what it knows and no more: the content is unchanged. It should not imply that the read was fully relayed. One option is "unchanged since 15:04:05; what you have not relayed from it is still new to the user". It needs its own iteration.
2. **Separate B from the trims.** Add an arm with iteration 6 plus the trims only, and an arm with B only on a prompt with room to spare.
3. **Iteration 6 alone.** In this rerun it beat main on restates (−20 pp, −36 to −4), the same direction as iteration 6's own −16 pp (−31 to −1). It also had the fewest wrong facts (9 %). This supports the override already proposed for iteration 6. It is exploratory and not a passed gate.
4. **Harness.** Let the scripted yes answer an offer that carries an options list. Give tier-1 workers a session record, so that `investigate source worker` and session reads can be exercised.

## Spend

- **DeepSeek.** Balance 34.44 → 23.65 CNY (10.79 CNY), about $10.8 at list prices. That covers 18 smoke runs, 120 explain runs and 70 workday runs.
- **Sonnet.** The local Claude subscription ran 132 fact-judge calls (12 on the smoke runs). The 5-hour session went from 43 % to 17 % left (including orchestration), the weekly from 48 % to 45 %.
- **No API spend.**
