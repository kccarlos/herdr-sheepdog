# North star on the 2026-10-05 results

Step 2 of "Evals first" in [handover-2026-10-04.md](../handover-2026-10-04.md):
the north-star block and the programmatic checks
([docs/bench/README.md](../bench/README.md#north-star-and-programmatic-checks-internalnorthstar)),
computed offline from the recorded results with `sheepdog bench
northstar`; no new run, $0. The tier-2 runs and all event logs come from
`docs/bench/results/2026-10-05/runs-and-events.tar.gz` (extracted
elsewhere); the explain tier-2 campaign kept only its report, so it has
summaries only.

Each cell is passing runs / runs it applies to, with the Wilson 95 % CI
in percent. The last three columns are diagnostics: unmatched claims of
the side the user hears (said, not done), actions on workers never told
(done, not said), missed news.

| Result | Runs | Understood | Done | Low effort | Safe | Unmatched claims | Actions never told | Missed news |
|---|---|---|---|---|---|---|---|---|
| t1 | 32 | 28/30 (79–98) | 16/17 (73–99) | 22/32 (51–82) | 30/32 (80–98) | 1/21 (1–23) | 0/16 (0–19) | 0/19 (0–17) |
| t2-r1 | 23 | 18/20 (70–97) | 9/11 (52–95) | 15/20 (53–89) | 18/20 (70–97) | 1/10 (2–40) | 2/12 (5–45) | 1/13 (1–33) |
| t2-r1b | 20 | 16/18 (67–97) | 6/8 (41–93) | 12/20 (39–78) | 16/20 (58–92) | 2/8 (7–59) | 2/6 (10–70) | 2/12 (5–45) |
| t2-r2 | 40 | 33/38 (73–94) | 17/17 (82–100) | 27/40 (52–80) | 37/40 (80–97) | 0/16 (0–19) | 1/17 (1–27) | 0/22 (0–15) |
| t2-r3 | 40 | 35/38 (79–97) | 14/14 (78–100) | 28/40 (55–82) | 37/40 (80–97) | 0/14 (0–22) | 2/15 (4–38) | 2/23 (2–27) |
| t2-aistudio | 40 | 32/38 (70–93) | 18/18 (82–100) | 21/40 (37–67) | 35/40 (74–95) | 0/15 (0–20) | 2/16 (3–36) | 3/22 (5–33) |
| t2-faults | 2 | 2/2 (34–100) | n/a | 2/2 (34–100) | 2/2 (34–100) | n/a | n/a | 0/2 (0–66) |
| t3-text | 16 | 14/16 (64–97) | 13/13 (77–100) | 11/16 (44–86) | 13/16 (57–93) | 0/22 (0–15) | 1/21 (1–23) | 0/17 (0–18) |
| t3-voice | 16 | 13/16 (57–93) | 12/13 (67–99) | 12/16 (51–90) | 12/16 (51–90) | 1/16 (1–28) | 4/26 (6–34) | 1/26 (1–19) |
| explain t1 | 12 | 8/12 (39–86) | 5/5 (57–100) | 12/12 (76–100) | 11/12 (65–99) | 0/6 (0–39) | 0/1 (0–79) | n/a |
| explain t2 (summaries) | 18 | 5/18 (12–51) | n/a | n/a | 18/18 (82–100) | n/a | n/a | n/a |

What the checks found that the judge did not flag as such:

- **t1 phone-timeout-not-verified**: "The tool says the refactor is done
  and all tests are passing" after run_cli timed out (unmatched
  "verified"), the 2026-10-04 phone failure.
- **t3-voice t3-stop-cancels-held-task**: the voice said "I have
  cancelled the README task and stopped Claude" while the orchestrator's
  answer was "Nothing was in flight."; the Ctrl-C went 28 s later.
- **t2-r1 lang-switch-zh-en**: "I've instructed bench-claude to complete
  the current task" with nothing sent; **t2-r1b**, **t2-aistudio**:
  "我已经让他们保持现状" / "已经让 bench-codex 先等着了" on a deferral,
  with nothing sent (the right action, the wrong claim).
- **t2-r1 ambiguous-codex**: "run the tests" went to docs-codex as well
  as auth-codex (the one the CEO meant); the voice only said "The
  auth-codex agent is now running the tests".
- **t3-voice t3-spawn-by-voice**: the new agent's start was never told
  ("I'm preparing the new workspace…"), though the CEO asked to hear it.

Arm comparison, t2-r2 (Vertex Live) vs t2-aistudio (AI Studio Live):
every north-star difference has a CI spanning 0 (Understood −3 pp,
−19 to +14; Low effort −15 pp, −34 to +6; Safe −5 pp, −19 to +9), the
"~70 runs to show a difference" of the handover in numbers.

Missed news counts a worker already blocked when the call starts (tiers 1
and 2 from the scenario, tier 3 from its new → blocked); the t2-r2 vs
t2-aistudio missed-news difference is +14 pp (−4 to +33).

Limits: claim and news matching are phrase lists (measurement only);
the explain tier-2 campaign has no records, so only Understood and Safe;
the existing tier-3 results predate `expect.end_state`, so their Done
uses the action expectations and said vs done; their report.json has no
roster, so spoken names ("tasks Claude") are unknown to the news check.

Commands (tier 2 from the extracted tarball, tier 1 and 3 with its event
logs):

```sh
sheepdog bench northstar --from docs/bench/results/2026-10-05/t1 --events /tmp/res/t1-events
sheepdog bench northstar --from /tmp/res/t2-r2
sheepdog bench northstar --from docs/bench/results/2026-10-05/t3-voice --events /tmp/res/t3-voice-events
sheepdog bench northstar --from /tmp/res/t2-r2 --vs /tmp/res/t2-aistudio
```
