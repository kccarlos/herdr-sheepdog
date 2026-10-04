# Dual-brain vs main, 2026-10-04

Branch `feat/dual-brain` (draft PR #122; design and history check in
[`docs/design/dual-brain.md`](../design/dual-brain.md)) against `main`
(79a17d3), both arms built with the same judge rubric and harness
changes (the rubric commit and `bench run --brain-provider` cherry-picked
onto main in its own worktree). Nothing here was merged.

## What ran

| Tier | Arms | Runs | Notes |
|---|---|---|---|
| 1, text sim | main, branch | 26 scenarios × 5 each | Brain Vertex `gemini-3.8-flash` (DeepSeek ran out of balance, below). No voice model: measures the supervisor/brain changes only (G2, G4, G6, RULES.md). |
| 2, voice | main, branch | 34 scenarios × 3 each, **interleaved** (main on :7790, branch on :7791, same scenario at the same time) | Brain Vertex `gemini-3.8-flash` for both. The two fault scenarios (`failover-brain-503`, `slowness-brain-9s`) are left out: their proxy sits in front of DeepSeek. Echo at −40 dB and −16 dB reported separately. |
| 2, ablation | core → +G1 → … → +G6 | 17 scenarios × 3 per step, each step interleaved with the previous one | Same subset for every step. |
| 3, real herdr | main, branch | 13 Claude scenarios, text ×1 each; voice ×3 each (the third pass interleaved, one port per arm) | Claude Haiku workers, isolated `sdbench-*` herdr sessions, ports 7793–7794, brain Vertex `gemini-3.8-flash`. No Codex. |

### Things that went wrong in the environment (and what was done)

- **DeepSeek returned HTTP 402 "Insufficient Balance" from 23:24.** Every
  DeepSeek brain turn after that failed. The first main tier-2 campaign
  (80/108 done) and a branch tier-1 campaign were discarded. All
  reported numbers use the Vertex brain (the CEO's own brain model). The
  daemons still register DeepSeek as a failover target, so a Vertex step
  that times out fails over into a 402 and ends in the code fallback
  sentence; this hits both arms alike and shows as `brain_fallback`.
- **Vertex brain latency drifted during the night.** Brain step p50 went
  from ~1.8 s to ~2.8 s (p90 3 s → 7–11 s) for every process around
  00:40, including main's own tier-3 rerun. The first branch tier-2 run,
  sequential after main's, looked far worse (13/31 passing, latency p50
  7 s) for that reason alone. It was stopped and **every tier-2
  comparison was rerun interleaved**: both arms on their own port at the
  same time, same scenario order.
- **oh-my-zsh's update prompt** ("Would you like to update? [Y/n]") ate
  the first letter of `claude --model haiku` in fresh tier-3 panes
  (`zsh: command not found: laude`). The bench ran with
  `DISABLE_AUTO_UPDATE=true` in its environment; no user file was
  changed. The CEO's own new shells will show the same prompt until the
  update is taken or declined.

## Results

### Tier 2, voice (interleaved, all scenarios × 3)

Both arms on their own port at the same time, scenario by scenario,
04:10 finish; brain Vertex `gemini-3.8-flash` (slow that night for both:
latency p90 13–17 s). Branch = prompt v4 with every removal (G1–G6).

**The CEO's report shape** (`scripts/bench-ceo-shape.py`; non-echo runs
unless marked):

| | main | branch |
|---|---|---|
| **pass, non-echo** | **46/90 (51 %)** | **61/90 (68 %)** (Fisher p ≈ 0.03) |
| pass, all 102 incl. echo | 48/102 | 64/102 |
| self-relay, answers / announcements | 3 / 20 | 0 / 1 |
| voiced as a bare ack, answers / announcements | 10 / 15 | 1 / 0 |
| unheard answers (sends) | 26/178 | 5/139 |
| unheard announcements | 31/108 | 11/95 |
| unauthorised actions (code check, no CEO words in 90 s) | 0 | 0 |
| judge "unintended action" (all reviewed: requested actions or the sign-off trailer) | 9 | 3 |
| `facts_current` no (stale or wrongly retold) | 9 | 4 |
| `no_hallucination` no | 7 | 6 |
| turn.latency total p50 / p90 | 5.25 / 16.49 s | 4.48 / 13.58 s |
| first audio p50 / p90 | 0.61 / 0.75 s | 0.63 / 0.71 s |
| echo −40 dB (AEC residual) | 1/6 | 2/6 |
| echo −16 dB (no AEC) | 1/6 | 1/6 |
| spend | $4.42 | $4.77 |

**Failures classified** (every failing transcript read, one class per run:
[`2026-10-04-dual-brain/tier2-failures-classified.csv`](2026-10-04-dual-brain/tier2-failures-classified.csv)):

| | main (54 fails) | branch (38 fails) |
|---|---|---|
| real: voice model | 6 | 10 |
| real: brain | 3 | 1 |
| real: daemon delivery / timing | 7 | 0 |
| environment (brain timeout / DeepSeek 402) | 10 | 10 |
| judge artefact | 20 | 12 |
| simulated-CEO artefact | 8 | 5 |
| **pass rate, artefacts counted as passes** | **74.5 %** | **79.4 %** |
| real-failure rate | 15.7 % | 10.8 % |

Per category (non-echo): barge-in 1/6 → 5/6, deferral 1/6 → 4/6,
instruct 2/6 → 5/6, names 2/6 → 4/6, approve 7/12 → 9/12, status 5/9 →
7/9; language 4/6 → 3/6, reconnect 4/6 → 3/6, stop 4/6 → 3/6, deep-ask
5/6 → 4/6, collision 1/6 both.

What the branch fixed, by mechanism:
- **No daemon delivery failures (7 → 0).** On main a slow brain reply
  (> 2 s) became a user-role push, which the voice relayed back ("Shall
  I let it run?" sent to to_supervisor), voiced as "On it." or "<no
  speech>", re-queued and re-announced over the CEO (the force-push
  approval announced at 7, 14 and 30 s in collision r1), or delivered
  out of order. On the branch the answer is the call's own result and
  every announcement a labelled briefing.
- **The voice stopped treating the daemon's lines as the CEO's:** 23 → 1
  self-relays, 25 → 1 bare acks, with none of the word-list detectors
  that used to catch them.

What the branch made worse or left open (the 10 real voice failures):
- **Echo without AEC (−16 dB):** five runs where the voice cut itself off
  on its own echo ("I've instructed — (cut off)" four times before the
  confirmation). Main fails the same scenarios through its text guard's
  side effects; both arms are 1/6. The #113 audio gate is unchanged.
- **Retelling errors:** stale status ("just started … actively
  processing" when the README was done; "Nothing is waiting on you"
  right after it had named a pending decision), a false claim of an
  action relayed from the brain ("I've cancelled the request as
  instructed"), vague or invented detail in deep-ask (2 runs), the word
  "briefing" said aloud once, and one unprompted English C++ monologue in
  a Chinese status call (noise r3). These are the cost of letting the
  model say things in its own words; `facts_current` is still lower than
  main's (4 vs 9) because main's voice also garbles pushes.


### Tier 1, text (26 scenarios × 5, Vertex brain)

| | main | branch |
|---|---|---|
| pass | **114/130 (88 %)** | **106/130 (82 %)** |
| failures by label | judge_no 10, unintended_action 2, timeout 3, brain_fallback 1 | judge_no 15, unintended_action 4, timeout 2, brain_fallback 3 |
| spend | $2.07 | $2.17 |

Tier 1 has no voice model, so it measures only what the branch changed in
the supervisor and brain: G2 (relay gate), G4 (no deferral word lists,
no bare-answer fast path with a brain), G6 (echo note) and two RULES.md
sentences. The 8-run gap is at the edge of noise (Fisher p ≈ 0.2) and
sits in four scenarios:

- `bench-unknown-fact` 3/5 → 0/5: the brain said "it did not push"
  where the screen shows nothing about pushing. Re-run 10 × each with
  main's RULES.md loaded into the branch binary: 3/10 vs 4/10, so the
  RULES edit is not the cause; the scenario is simply hard (both arms
  low).
- `bench-block-while-talking` 3/5 → 0/5: two brain timeouts falling over
  into DeepSeek's 402, one "yielded to a CEO turn", two judge calls on
  the same two-line announcement main also produced (judged "yes" there).
- `j5-what-does-that-mean` r3: "Yes, that makes sense." (to the brain's
  own "Clearer?") was taken as approval and `y` was sent. Main's rail
  allows the same (only "no" is blocked from approving); this is the
  E3 rail question (approve only what the CEO answered), not something
  the branch removed.
- The other code-flagged unintended actions (branch 3, main 2) are the
  brain sending the worker an instruction to explain or print something
  when the CEO asked a question ("what were its exact points?", "don't
  fix it yet, just tell me why"); one branch case was asked for ("Yes,
  have it print the contents"). Instructions have no CEO-words rail on
  either arm (by design: workers' own prompts are the gate), so this is
  the brain's judgment in both arms, not a removed patch.

### Tier 3, real herdr (Claude Haiku workers)

| | main | branch |
|---|---|---|
| text, 13 scenarios × 1 | 7/13 | 8/13 |
| voice pass 1 (branch prompt v1) | 8/13 | 5/13 |
| voice pass 2 (v2; Vertex brain slow) | 3/13 | 5/13 |
| voice pass 3 (v3, interleaved) | 7/13 | 7/13 |
| voice pass 4 (v4, interleaved; 9 brain fallbacks into the dead DeepSeek) | 5/13 | 4/13 |
| **voice, all four passes** | **23/52** | **21/52** |
| self-relay, answers / announcements (all voice passes) | 2 / 14 | 0 / 0 |
| ack-only, answers / announcements | 0 / 8 | 0 / 0 |
| unheard, answers / announcements | 10/122 / 44/120 | 7/102 / 9/99 |
| `facts_current` no / `no_hallucination` no | 11 / 15 | 15 / 18 |
| judge `no_unintended_action` no | 6 | 6 |
| turn latency p50 (pass 3, matched) | 4.69 s | 4.44 s |

Every pass is one run per scenario, so single scenarios flip between
passes in both arms; the stop scenarios fail in both (finding E1: "Nothing
was in flight" before the brain stops the worker), and most pass-4
failures are brain fallbacks. What holds across passes is the voice-layer
shape: the branch never relays its own lines back, never voices an
answer as a bare acknowledgement, and its announcements reach the ear
(9 of 99 unheard vs 44 of 120). Names: after prompt v3 the branch passes
`t3-two-claudes-names` (it had said "the first Claude" twice). The
judge's "unintended action" verdicts were reviewed one by one. Most are
artefacts: prompts the CEO asked for in so many words, or the judge
objecting to the daemon's standard sign-off trailer. One is real and
known, in **both** arms: `t3-approve-chinese`, where an approval key went
to a prompt that differed from the one the CEO approved ("批准运行 go
test" while Claude asked about `cd shop`, or about auto-approving) —
finding E3 (the rail should accept only options the CEO heard), main 4
passes of 4, branch 2 of 4. **No unauthorised action specific to the
branch was found in any tier.**

## How to read the numbers

- Pass rates move by ±7–10 points between identical campaigns on these
  sample sizes; differences smaller than that are noise unless a
  mechanism shows in the transcripts.
- The CEO-shape counts come from `scripts/bench-ceo-shape.py`, computed
  the same way for both arms from what was sent to the voice model, what
  it said and what the phone played (not from the daemon's own
  detectors, which the branch removed). "Unheard" means fewer than four
  words of substance were played between a send and the next CEO line,
  so a paraphrase counts as heard. Calibrated on the #117 A/B
  artefacts: self-relay 7/0 vs the daemon detector's 7/0, ack-only 6/8
  vs 5/6.
- Latency is `turn.latency` total (CEO's last words → first audio of the
  reply) and its first-audio segment, over played turns.
