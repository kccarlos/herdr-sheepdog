# Claude Opus 5.5 orchestrator against DeepSeek, 2026-10-19

Step 5 of [plan-model-setup.md](../plan-model-setup.md): the tier-1 workday suite for the new Anthropic orchestrator ([anthropic-adapter.md](../anthropic-adapter.md)) against DeepSeek, before the setup page shows any number for Opus. Raw data, scripts and logs: [../bench/results/2026-10-19-opus/](../bench/results/2026-10-19-opus/).

## Setup

- Binary: main 963f46c (#287, `eval sim --provider anthropic`), the same binary for both arms, run in one batch, the two arms side by side per scenario (`driver.sh`, `run-chunk.sh`).
- Tier 1, the 6 non-adversarial workday scenarios (as in [vertex-2026-10-11.md](vertex-2026-10-11.md)) **x3 per arm = 18 runs per arm**, `--personas rotate`, no failover provider, simulated user DeepSeek `deepseek-flash`, chattiness chatty.
- Arms: **Opus** = `claude-opus-5-5` with the adapter's defaults (`anthropic.effort = medium` on user turns, `anthropic.event_effort = low` on event turns, server-side fallbacks on); **DeepSeek** = `deepseek-flash`, the control.
- Judge: Claude Sonnet (`bench rejudge --judge claude --judge-model sonnet`, run level and segments), then `bench northstar --vs`. One DeepSeek run (scale-ten r1) needed a second rejudge pass (`no verdict for right_agent` once); every run is judged.
- **Scope cut, disclosed:** the plan asked for x5 and a third arm (Opus at effort `low` everywhere). The smoke runs showed about $1.30 per Opus run, so x5 (about $39 per arm) did not fit the $25 Anthropic cap. Runs were cut to the minimum 3 x 6 and the low-effort arm was skipped. Anthropic spend: about **$25.1** (smoke $2.6 + runs $22.5, estimate, below).

## Smoke run (2 runs of workday-morning-standup, before the batch)

Requests succeeded; no 400 about thinking or history; `brain.thinking_dropped` never fired (no replayed block was dropped or rejected); `cache_read_input_tokens` was above 0 from the second request on. A logging proxy captured the request bodies of one smoke run and diffed consecutive requests: **cross-turn cache reuse covers only tools + system**, because pruning (`brain.prune_after_turns` = 2) rewrites a tool result or event screen two turns back on every turn, which invalidates the messages cache from there. Within a turn, the next step reads the whole window back. This is the cost the design note expected for history edits between turns, not an adapter bug; no adapter fix was needed.

## North star (18 runs per arm, Sonnet judge)

Difference = Opus − DeepSeek, 95 % CI (Newcombe for rates, cluster bootstrap over runs for segments). [northstar-deepseek-to-opus.md](../bench/results/2026-10-19-opus/northstar-deepseek-to-opus.md), [itemdiff.md](../bench/results/2026-10-19-opus/itemdiff.md).

| Metric | DeepSeek | Opus 5.5 | Opus − DeepSeek (95 % CI) |
|---|---|---|---|
| **Understood, segments** | 198/216 (92 %, 89–95) | 199/216 (92 %, 87–96) | **+0 pp (−5 to +6)** |
| **Done** (required actions) | 18/18 | 18/18 | 0 |
| Done, claim part (judge `said_vs_done`, segments) | 71/73 (97 %) | 87/87 (100 %) | +3 pp (+0 to +9) |
| **Safe, segments (judge)** | 259/267 (97 %, 95–99) | 259/266 (97 %, 96–99) | 0, n.s. |
| Safe, run level | 13/18 (72 %) | 16/18 (89 %) | +17 pp (−10 to +41) |
| `no_hallucination`, segments | 238/267 (89 %) | 251/266 (94 %) | **+5 pp (+1 to +10)** |
| `facts_current`, segments | 248/267 (93 %) | 257/266 (97 %) | +4 pp (−0 to +8) |
| `length_fits`, segments | 252/267 (94 %) | 246/266 (92 %) | −2 pp (−5 to +2) |
| `language`, segments | 265/267 (99 %) | 259/266 (97 %) | −2 pp (−4 to −0) |
| Low effort | 10/18 (56 %) | 12/18 (67 %) | +11 pp (−19 to +39) |
| Missed news | 20/127 (16 %) | 19/127 (15 %) | −1 pp (−10 to +8) |
| Actions never told | 3/48 (6 %) | 2/47 (4 %) | −2 pp (−13 to +9) |
| Time to goal | 94 s | 170 s | **+76 s (+48 to +106)** |

Run-level Understood is 5/18 for both (the depth and length items fail somewhere in most runs); use the segment rows.

**Reading:** the two orchestrators are understood equally well on this suite. Opus says fewer things the facts do not support (`no_hallucination` +5 pp, the only quality difference whose CI excludes 0; with about 20 comparisons here one could be chance) and its claims about its own actions all matched. It is much slower, so sessions take longer.

## Latency (orchestrator turns, `brain.turn` `dur_ms`, turns that did not fall back)

[latency.md](../bench/results/2026-10-19-opus/latency.md). "First" is the first turn of that kind in a run (nothing cached yet).

| Turn kind | Arm | n | p50 | p90 | first turn p50 |
|---|---|---|---|---|---|
| announce (event) | DeepSeek | 118 | 1.6 s | 2.0 s | 1.7 s |
| announce (event) | Opus, effort low | 110 | **4.8 s** | **7.2 s** | 6.7 s |
| utterance (user) | DeepSeek | 227 | 3.0 s | 5.6 s | 2.9 s |
| utterance (user) | Opus, effort medium | 226 | **7.4 s** | **12.5 s** | 5.1 s |

Against the budgets:

- **Event turns (`brain.announce_turn_timeout_s` = 6 s):** 31 of 110 Opus announce turns took more than 6 s (some of them owed announces, which get 15 s); 4 times the daemon spoke its own sentence because the turn ran out, and 7 announce turns fell back (1 timeout, 3 queued behind a slow user turn past their budget, 3 yielded to the user). The first announce of a run is the slowest (6.7 s p50; it timed out in both smoke runs): nothing is cached yet. DeepSeek: none over 6 s, no fallbacks. Event turns already run at the lowest effort, so effort cannot fix this.
- **User turns (`brain.utterance_timeout_s` = 25 s):** 1 of 226 Opus turns took longer than 25 s. 97 of 226 (43 %) took longer than 8 s, where the voice says it is still checking and delivers the answer as a push; DeepSeek 8 of 227 (4 %).

## Cache, cost, refusals

| | DeepSeek | Opus 5.5 |
|---|---|---|
| Prompt-cache hit rate (Σ `cache_hit_tokens` / Σ `prompt_tokens`) | 82 % | 61 % |
| Orchestrator spend, 18 runs (estimate) | $2.18 ($0.12 per run) | $22.53 ($1.25 per run) |
| Failed model calls | 0 | 1 (a timeout) |
| Refusals (`stop_reason` refusal) | – | 0 |
| Thinking blocks dropped or rejected | – | 0 |
| Rate limited (429) | 0 | 0 |

Opus costs about 10 times DeepSeek per run. The estimate bills uncached input at the 5-minute cache-write price ($5 per MTok, a slight overestimate), cache reads at $0.20 and output at $20, and leaves out calls that timed out (they report no usage). Server-side fallbacks: not observable in these logs (the served model is kept in the transcript's raw message, not in an event); there were no refusals for one to answer.

## Decisions

- **Effort default unchanged** (`medium` on user turns, `low` on event turns). User turns fit the 25 s budget (p90 12.5 s); the event-turn misses happen at the lowest effort already. No low-effort arm was run (budget), so there is no evidence that `low` on user turns holds quality; it is the next experiment if the 43 % of answers past 8 s matter in use.
- **Labels:** "★ most recommended" stays (the CEO's call). The numbers next to it and next to DeepSeek come from this report: 92 % segment Understood for both, fewer made-up facts for Opus (94 % vs 89 %), about 10 times the cost, slower replies. The Recommended card no longer says "understands your agents best", which these runs do not show.
- **Follow-ups, not done here:** (1) a low-effort user-turn arm (about $20 at 3 x 6); (2) cache warm-up at daemon start for the first announce; (3) prune in batches (every few turns) so cross-turn caching holds, which would cut Opus cost and first-token time, and help DeepSeek's cache too; (4) log the served model on `brain.step` so server-side fallbacks are countable.

## Caveats

- 18 runs per arm: segment CIs about ±4 pp, run-level ±20–30 pp. The suite is benign workday only (no adversarial or `rm -rf` scenarios), so Safe here says nothing about the scale-ten approval case.
- The DeepSeek arm reads 92 % segment Understood here against 91 % in the 10-08 baseline (34 runs, 7 scenarios); different runs and scenario mix.
- The prompts were written and tuned on DeepSeek; nothing was tuned for Opus.
