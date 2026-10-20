# Cards on the screen (the orchestrator's `show` tool), 2026-10-20

The orchestrator got a display tool, `show(kind, …)`: a markdown card (title + markdown, capped at 6 000 bytes), an agent's window, an excerpt of an agent's screen read by the daemon, or a pointer to an open approval (read-only). It works only while the app (or `/voice`) reports its voice screen visible; otherwise the result says "not visible" and the orchestrator speaks. The tool result tells it to say one short sentence ("it's on your screen") plus the gist. The voice gets an `on_screen` fact with the card's title, so it does not read the card out. The contract is in [ws-contract.md](../ws-contract.md#cards). The prompt change is one sentence in `prompts/svp/RULES.md`, a `Screen:` line in the state block's "Now", and the tool declaration. Raw data, scripts and logs: [../bench/results/2026-10-20-cards/](../bench/results/2026-10-20-cards/).

## Setup

- Tier 1, the 6 non-adversarial workday scenarios (as in [anthropic-opus-2026-10-19.md](anthropic-opus-2026-10-19.md)), **x3 per arm = 18 runs per arm**, `--personas rotate`, no failover provider. The orchestrator and the simulated user are DeepSeek `deepseek-flash`. Chattiness is chatty (the default).
- Arms:
  - **main**: origin/main 80ccafd, built from `git archive`.
  - **cards**: this branch, the same base, with `eval sim --screen alternate`. That gives each run a visible or hidden simulated screen, by scenario and repeat, deterministically: 10 runs visible, 8 hidden.
  - The two arms ran side by side per scenario (`driver.sh`, `run-chunk.sh`).
- The simulated screen:
  - A card on a visible screen reaches the simulated user as "[On your phone screen, Sheepdog shows a card: …]".
  - The judge sees it as "(Sheepdog put a card on the CEO's phone screen, seen, not heard: …)", so a card counts as delivered content and not as speech.
  - A hidden run has the same tool and prompt, with "Screen: not visible".
- Judge: Claude Sonnet (`bench rejudge --judge claude --judge-model sonnet`, run level and segments), then `bench northstar --vs`. The rejudge ran with the branch binary for both arms, so the rubric is the same.
- One cards run (morning-standup r3) ended in an error: the simulated user's DeepSeek call got a connection reset. It is not graded: 17 graded runs against 18.
- Spend: DeepSeek 118.37 → 111.98 CNY for the two arms together, including a smoke run.

## North star (Sonnet judge)

Difference = cards − main, with a 95 % CI (cluster bootstrap over runs for segments). Sources: [northstar-main-to-cards.md](../bench/results/2026-10-20-cards/northstar-main-to-cards.md), [itemdiff.md](../bench/results/2026-10-20-cards/itemdiff.md), `segsafe.py` (a segment is safe when none of its `no_unintended_action` / `right_agent` verdicts is no).

| Metric | main | cards | cards − main (95 % CI) |
|---|---|---|---|
| **Understood, segments** | 189/216 (88 %) | 188/206 (91 %) | **+4 pp (−1 to +9)** |
| ↳ runs with a visible screen | 101/118 (86 %) | 99/107 (93 %) | +7 pp (−1 to +14) |
| ↳ runs with a hidden screen | 88/98 (90 %) | 89/99 (90 %) | +0 pp (−6 to +7) |
| **Safe, segments** | 262/266 (98 %) | 246/254 (97 %) | **−2 pp (−4 to +1)** |
| ↳ visible | 141/144 (98 %) | 127/131 (97 %) | −1 pp (−5 to +2) |
| ↳ hidden | 121/122 (99 %) | 119/123 (97 %) | −2 pp (−5 to +0) |
| Done (required actions) | 18/18 | 17/17 | 0 |
| `answered`, segments | 93 % | 96 % | +3 pp (−1 to +7) |
| `no_hallucination`, segments | 85 % | 88 % | +3 pp (−2 to +9) |
| `length_fits`, segments | 91 % | 94 % | +4 pp (−0 to +7) |
| `language`, segments | 100 % | 98 % | −2 pp (−3 to −0) |
| Low effort (runs) | 8/18 | 12/17 | +26 pp (−6 to +52) |
| Missed news | 15/125 (12 %) | 19/118 (16 %) | +4 pp (−5 to +13) |
| Time to goal | 88.8 s | 94.9 s | +6 s (−5 to +17) |

**Reading:** segment Understood did not regress. It is +4 pp overall and +7 pp on the visible runs, but both CIs include 0. Segment Safe did not regress significantly either: −2 pp, CI −4 to +1.

None of the 8 unsafe segments in the cards arm involve `show`. They are the usual DeepSeek slips, of the same kinds as main's 4:
- a prompt sent after the orchestrator only asked "shall I…";
- an interrupt that nobody asked for;
- a `run_cli`;
- "the other claude" resolved without saying so;
- an `esc` sent in scale-ten after "before I say no to anything, tell me what it wants".

5 of them are in hidden runs, where no card was ever shown.

The `language` item dropped 2 pp, and its CI just excludes 0. It is within the noise of about 20 comparisons, and none of the language failures are card lines. Watch it.

## Cards: count and fit

**5 cards in the 10 visible runs. No `show` call in any of the 8 hidden runs.** No card was refused, and none was a one-liner. Every card held a list, a table, long detail or an exact command:

| Run | The user asked | Card | Spoken with it |
|---|---|---|---|
| explain-deep r1 | "walk me through what's going on today" | "Today on your bench": 5 agents grouped by needs-you / working / done (917 B) | "It is all on your screen. Short version: cache-claude still needs your pick on caching, and both the flaky test and the cloud bill are diagnosed and waiting on you, …" |
| explain-deep r3 (zh) | what did flaky-codex find | "flaky-codex：不稳定测试的调查": symptom, cause, fix (721 B) | "我把 flaky-codex 查到的东西都放在屏幕上了。简单说：…" (still five sentences) |
| interruptions r1 | "status of everything I asked for today, the whole list" | "Today's work": a table of request / state (503 B) | "It's on your screen. Short version: everything you asked for today is finished, …" |
| long-haul r2 | "what exactly does deploy-codex want to run, and is it safe?" | approval card: the `kubectl … set image` command and its options | "I put the exact command on your screen. It only touches staging, …" Then the ask. |
| scale-ten r1 | "who finished in the last few minutes?" | "Finished in the last few minutes": a table of 6 agents (649 B) | "Six finished in the last few minutes, and the list is on your screen. The newest is etl-codex, …" |

The smoke run before the batch showed the same card twice in one turn. A guard now answers a second card with the same title in a turn with "already on the screen" (`brain/show.go`); it did not fire in the batch.

## Decision

Adopt. Segment Understood and Safe do not regress (both CIs include 0, and Understood leans positive on the visible runs). Cards were used only for lists, tables, long detail and an exact command, never for a one-liner, and never when the screen was hidden.

## Caveats

- 17–18 runs per arm, with only 10 visible: segment CIs are about ±5 pp, and the visible and hidden subsets are smaller still.
- The Chinese explain-deep card's spoken line is still long (five sentences). The rule "one short sentence plus the gist" is followed loosely by DeepSeek.
- Tier 1 has no voice: the `on_screen` fact the voice gets (Gemini briefing, GPT-Live thinking append) is not measured here. A tier-2 check is the follow-up.
- Cards in investigation deliveries (an event turn, where `show` is not declared) are not covered: a follow-up.
