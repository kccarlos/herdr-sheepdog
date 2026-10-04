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

(Filled in below as the interleaved runs finish.)

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
