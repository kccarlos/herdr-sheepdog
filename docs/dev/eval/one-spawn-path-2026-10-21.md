# One start path for new agents (herdr `agent.start`), 2026-10-21

The orchestrator's `spawn` and the app's New agent used two copies of the
pane-and-launch code, each typing a hard-coded `claude` or `codex`
command. They now share one helper (`startAgent` in
`internal/supervisor/spawn.go`): it opens or splits the pane, waits for
its shell and starts the agent with herdr's `agent.start`, which types the
kind's own executable and reports when it detected the agent. The kinds a
tap may start are herdr's supported kinds found on PATH; the orchestrator
still starts only `brain.SpawnKinds` (Claude and Codex). The approval rule
and the spawn cap stay in code. The orchestrator's tool and prompt did
not change, but its spawn now goes through new code, so this eval checks
that the voice path is no worse (CEO decision: a DeepSeek tier-1 eval
before and after). Raw data, scripts and logs:
[../bench/results/2026-10-21-spawn/](../bench/results/2026-10-21-spawn/).

## Setup

- Tier 1, the 6 non-adversarial workday scenarios, **x3 per arm = 18 runs
  per arm**, `--personas rotate`, no failover provider. The orchestrator
  and the simulated user are DeepSeek `deepseek-flash`; chattiness chatty
  (the default).
- Arms: **main**, origin/main 21557a58 built from `git archive`;
  **spawn**, the branch (dfd598e7) on the same base. Both ran side by side
  per scenario (`driver.sh`, `run-chunk.sh`).
- Judge: Claude Sonnet (`bench rejudge --judge claude --judge-model
  sonnet`, run level and segments) with the branch binary for both arms,
  then `bench northstar --vs`. The rejudge was stopped twice by the
  5-hour Claude quota and resumed; every run is graded (18/18 per arm).
- Spend: DeepSeek about $0.25–0.48 per scenario chunk at list prices.

## Results (Sonnet judge)

Difference = spawn − main with its 95 % CI. Sources:
[northstar-main-to-spawn.md](../bench/results/2026-10-21-spawn/northstar-main-to-spawn.md),
[itemdiff.md](../bench/results/2026-10-21-spawn/itemdiff.md), `segsafe.py`.

| Metric | main | spawn | spawn − main (95 % CI) |
|---|---|---|---|
| **Understood, segments** | 196/215 (91 %) | 200/220 (91 %) | **−0 pp (−5 to +5)** |
| **Safe, segments** | 253/261 (97 %) | 264/270 (98 %) | **+1 pp (−2 to +3)** |
| Done (required actions) | 18/18 | 18/18 | 0 |
| Safe (runs) | 14/18 | 14/18 | 0 |
| `answered`, segments | 97 % | 94 % | −3 pp (−6 to +0) |
| `no_hallucination`, segments | 89 % | 87 % | −1 pp (−7 to +4) |
| `right_agent`, segments | 100 % | 100 % | 0 |
| Missed news | 20/121 (17 %) | 14/124 (11 %) | −5 pp (−14 to +4) |
| Turns to goal | 12.5 | 12.8 | +0.25 (−0.58 to +1.15) |
| Time to goal | 93.9 s | 95.2 s | +1.3 s (−11.9 to +13.5) |

Every CI spans 0: no evidence of a difference. `answered` is the closest
to the edge (−3 pp, upper bound +0); watch it in the next workday run.

## Decision

Merge: the shared start path does not change what the user hears or how
safely the orchestrator acts, and it removes the drift between the two
copies and the hard-coded kinds (the `agy` blocker).
