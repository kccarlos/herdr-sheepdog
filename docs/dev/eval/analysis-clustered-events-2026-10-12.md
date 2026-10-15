# Clustered events on workday-scale-ten: where the time goes (2026-10-12)

Harness iteration 3 (docs/dev/handover-2026-10-04.md, "Harness iteration, from 2026-10-11"). Phase A: analysis of existing tier-1 event logs, no new runs. Question: why does `workday-scale-ten` (10 workers, a burst of events) fail at run level, 0/5 on DeepSeek flash in the 10-08 re-baseline and with brain fallbacks on Gemini (10-11), when the other workday scenarios rarely fall back?

Data (all tier 1, `workday-scale-ten` x5 each unless noted):

| Set | Brain | Path |
|---|---|---|
| 10-08 re-baseline | DeepSeek `deepseek-flash` | `docs/dev/bench/results/2026-10-08-rebaseline/merged/events` |
| 10-07 chatty a0 | DeepSeek `deepseek-flash` | `docs/dev/bench/results/2026-10-07-chatty/a0/events` |
| 10-11 tier 1 | Vertex `gemini-3.5-flash` | `docs/dev/bench/results/2026-10-11-t1-gemini/merged/events` |
| Other workday scenarios (30 runs per set) | DeepSeek | same 10-08 and 10-07 dirs |

Events used: `brain.turn_started` (`queue_ms`, `budget_ms`), `brain.step` (`dur_ms`, `prompt_tokens`), `brain.turn`, `brain.fallback`, `brain.yielded`, `brain.recheck`, `brain.push_decision`, `context.step`.

## The scenario

Five worker events land 1 s apart (`mail` done, `lint` done, `cache` blocked, `etl` blocked, `docs` done, 1.5-5.5 s after beat 3), and the simulated user speaks during the burst ("bottom line, top one or two"). Every event is a different agent, so per-agent coalescing never applies: `brain.event_coalesced` = **0** in every set.

## Findings

### 1. The "timeouts" are queue starvation, not model latency

Every `timeout` fallback in all three sets is an announce turn that **waited 7.6-8.7 s in the turn-lock queue out of a 9.0 s budget** (`AnnounceTurnTimeout` 6 s + `FinalGrace` 3 s), then started a model step with 0.9-1.1 s left, and that step was cut (`deepseek: read response: context deadline exceeded`; the cut steps are logged at 0.87-1.13 s).

| Set | `timeout` fallbacks | queue_ms of those turns | announce step p50 / p95 / max |
|---|---|---|---|
| 10-08 DeepSeek | 3 | 7 868, 8 003, 8 129 | 1.77 / 2.23 / 2.28 s |
| 10-07 DeepSeek | 4 | 7 601, 8 003, 8 005, 8 200 | 1.69 / 2.01 / 2.15 s |
| 10-11 Gemini | 1 | 8 659 | 1.60 / 3.85 / 5.46 s |

No DeepSeek announce step in any scale-ten run took more than 2.3 s, a quarter of the budget. Utterance steps: p50 2.1 s, p95 3.5 s (10-08). The cause is in `internal/brain/loop.go` `Turn`: the turn's context, `start` and soft deadline were all set **before** `b.lock.acquire`, and docs/dev/timelines.md said so ("time spent in the queue counts against the budget").

What held the lock during those waits (overlap of each starved turn's wait with the turns running meanwhile):

| Set | waits | held by user turns | held by other announce turns |
|---|---|---|---|
| 10-08 DeepSeek (timeouts + queue_timeout) | 4 | 18.9 s (57 %) | 14.1 s (43 %) |
| 10-07 DeepSeek | 4 | 23.5 s (74 %) | 8.3 s (26 %) |
| 10-11 Gemini | 5 | 22.0 s (49 %) | 22.6 s (51 %) |

The user turn in the middle of the burst is the main occupant: it goes first (two-priority lock) and takes 5-9 s with tool rounds (e.g. 10-08 r4 turn 8: `read_screen` then the answer, 6.7 s). The announce turns queued behind it are the burst's last two or three events (`etl`, `docs`, sometimes `cache`).

### 2. `queue_timeout`: the same starvation, one step further

`queue_timeout` (1 on 10-08, 0 on 10-07, **4 on 10-11 Gemini**) is a turn that never got the lock within the 9 s. On Gemini r3 all three of `cache`, `etl` and `docs` timed out in the queue behind the user's "说一下现在最重要的一两件事" turn (8.7 s) and the `lint` announce turn before it (6.3 s). The eval buckets it with timeouts (`isTimeoutText` matches "context deadline exceeded").

### 3. `yielded`: the user-first rail on a block the user already heard

All yields (2 on 10-08, 2 on 10-07, 1 on 10-11, and 1 on 10-08 long-haul) have the same shape: the `cache-codex` **blocked** announce turn queued about 1 s, its first step took about 1.8 s, so it passed `announce_wait` (2 s) and the block's code sentence was already spoken; the turn then ran `brain.recheck` (one answer-only step with "the template was spoken meanwhile"), and before that step the user's utterance was waiting, so the turn yielded (`brain.yielded {step: 2, steps_done: 1}`). The yield only ever drops the recheck answer to a block whose code sentence the user has heard, and the user turn that takes the lock sees the block in its state block. This is the designed priority (user first, docs/dev/timelines.md "yield"); changing it would move that rail. It counts as a run failure (`brain_fallback`) today; whether it should is a measurement question (see "Not changed").

### 4. Prompt size does not drive latency

Prompt tokens grow from about 5.5 k at the first turn to about 19 k (scale-ten p50 12.9 k, p95 17.8 k; `context.step` tokens p50 11.7 k, p95 16.7 k), the same range as the other workday scenarios (p50 11.6 k, p95 17.0 k): ten workers do not make the window bigger than three do, because the window is compacted at 12 k and the state block is about 710 chars in every scenario (`brain.system_prompt.state_chars`). Over all 914 DeepSeek steps of the 10-08 re-baseline the correlation between `prompt_tokens` and step `dur_ms` is r = 0.10, **about 29 ms per 1 000 tokens** (Gemini: r = 0.07, 18 ms per 1 000). DeepSeek step p50 by prompt size: < 8 k 1.90 s, 8-12 k 1.85 s, 12-16 k 2.02 s, > 16 k 2.19 s.

### 5. Elsewhere it barely happens

Other workday scenarios (30 runs per set, DeepSeek): announce queue p90 0.8-1.1 s, one `queue_timeout` in 60 runs (10-07 long-haul), one `yielded`, and `bad_reply` 1-3 (an unrelated utterance fault). The starvation needs a burst of several events plus a multi-step user turn, which only scale-ten scripts.

## Fixes, ranked by expected impact

Each is labelled as in the iteration loop (prompt / information / rail); none puts a judgement in code.

1. **Rail: start a turn's budget when it gets the turn lock (budget accounting).** The queue wait is bounded separately; the step budget starts at the handover. Expected: removes every queue-starved `timeout` (3 of 6 scale-ten fallbacks on 10-08, 4 of 6 on 10-07, 1 of 6 on Gemini). It is accounting only: the model's budget is the time it actually has, the user still goes first, and nothing decides on the model's behalf. A finish's line can come later (the supervisor waits for the turn), which is acceptable for news that is not urgent; a block's code sentence still goes out at `announce_wait` 2 s. **Implemented in Phase B.**
2. **Rail: bound an event turn's lock wait by what it can wait behind.** An event turn only ever queues behind user turns (they go first) and other event turns; a user turn may hold the lock for `utterance_timeout_s + final_grace_s` (28 s). Bounding the wait by the turn's own 9 s only relabels the starved timeouts as `queue_timeout`s: the first Phase-B chunk with fix 1 alone (`docs/dev/bench/results/2026-10-12-iter3/fix-v1/c1`, 4 runs) had 0 timeouts but 3 `queue_timeout`s, all behind user turns of 9-12.6 s. So an event turn waits at most one user turn's budget (or its own, if longer); a user turn's wait stays bounded by its own budget. Expected: the remaining `queue_timeout`s (1 on 10-08, 4 on Gemini). **Implemented in Phase B together with fix 1.**
3. **Information: one announce turn for several queued finishes (batching).** When the lock frees and several event turns are queued, one turn could be given all of their events and write one line per event (or one line for all) instead of one ~2 s turn each. Announce turns hold 26-51 % of the lock during the starved waits, so this would shorten the queue by about that share during bursts, and the user would hear one summary instead of three lines back to back. Larger change (request and reply contract, per-agent push decisions in the supervisor); with fixes 1-2 the queue no longer causes fallbacks, so defer until a scenario shows the lines arriving too late to be useful.
4. **Rail: a user turn preempting queued announce turns faster.** Already the design: user turns get the lock first and an event turn yields before every model call; utterance `queue_ms` is p50 0, p90 0-0.6 s, max 2.1 s (one step in flight). Nothing to gain without cutting a step in flight, which throws the work away. Not worth doing.
5. **Information: smaller per-turn state for idle workers.** Finding 4: prompt size costs about 0.03 s per 1 k tokens, and the state block is the same size with 10 workers as with 3. Low impact on latency; it may matter for cost only.

### Not changed (measurement question)

The yields (finding 3) remain after fixes 1-2: 3 of 4 fix-v1 runs had one. A yield on a block whose code sentence was already spoken loses nothing the user has not heard, but `simFailures` labels it `brain_fallback` and fails the run. Whether a yield should fail a run is a measurement decision for a separate change, not this iteration. Run-level pass on scale-ten stays capped by it, so Phase B judges the fix on fallbacks per run and reports yields separately.

Phase B results: docs/dev/eval/harness-iteration-log.md (iteration 3).

## Phase B: fixes 1 and 2 at tier 1

Change: `internal/brain/loop.go` `Turn` (rail). The step budget starts when the turn gets the turn lock; the lock wait has its own bound: a user turn its own budget, an event turn one user turn's budget (`utterance_timeout_s` + `final_grace_s`) or its own when longer. Unit test `TestQueuedEventTurnGetsItsFullBudget` (fails on the old code).

Setup: brain DeepSeek `deepseek-flash`, `--fallback-provider none`, simulated user Vertex `gemini-3.5-flash`, judge fake then `bench rejudge --judge claude --judge-model sonnet --skip-run-level`; chattiness chatty; arms built from main 871fe38 and the branch (tier 1 has no voice layer, so the later voice-prompt merge #203 does not affect it). Interleaved chunks of 4, `--parallel 4`. Raw data: `docs/dev/bench/results/2026-10-12-iter3/` (`STATUS.md`, `*/merged`, `*/merged16`, `northstar-n8-main-vs-fix.md`, `northstar-n16-main-vs-fix.md`).

**Extension disclosed:** the plan was 8 scale-ten runs per arm. At n = 8, brain fallbacks per run were 0.75 (main) vs 0.25 (fix), difference −0.50, bootstrap 95 % CI −1.13 to +0.13: not beyond the CI. The extension to 16 per arm was decided after seeing that (the index also asks for ≥ 15 scale-ten runs for a Safe claim), written in `STATUS.md` before the next chunk ran, and the rule was applied at n = 16.

| workday-scale-ten, n = 16 per arm | main | fix | fix − main (95 % CI) |
|---|---|---|---|
| Brain fallbacks (per run) | 18 (1.12) | 6 (0.38) | −0.75 (−1.31 to −0.19) |
| Queue-starved (`queue_timeout`, or `timeout` after ≥ 7.6 s queued) | 16 | 0 | |
| `yielded` | 2 | 5 | +0.19 per run (−0.13 to +0.44) |
| Provider stalls (one step ran the whole budget, queue 0) | 0 | 1 (a 28 s utterance step) | |
| Runs with no fallback | 6 / 16 | 10 / 16 | +25 pp (−9 to +52) |
| Finish wait for its line, p50 / p90 / max | 2.5 / 9.0 / 11.9 s | 2.4 / 9.0 / 15.2 s | |

Regression set (morning-standup ×3, interruptions ×3 per arm): 0 fallbacks on main, 1 on fix (an announce step that stalled 9 s with queue 0, a provider stall).

North star, all 22 runs per arm (`northstar-n16-main-vs-fix.md`, B = fix): segment Understood 86 % → 85 % (−1 pp, −7 to +5); run-level Safe 64 → 77 % (+14 pp, −13 to +38); segment Safe (judge) 301/308 → 302/308; Understood and Done 22/22 both; actions never told 15 → 9 %; missed news 14 → 18 % (+4 pp, −3 to +11); time to goal 114 s both. Four main runs (scale-ten r7, r10, r13, r16) got no Sonnet verdict for `right_agent` on beat 13 in two attempts and keep fake segment verdicts, which favours main.

The yields are the remaining scale-ten failures; fix has more of them, plausibly because turns that used to die in the queue now run and can meet a later user utterance. Run-level pass on scale-ten stays capped by them (see "Not changed").

Decision: merge rule met at n = 16 (brain fallbacks beyond the CI, segment Understood and Safe not worse); not met at the planned n = 8.
