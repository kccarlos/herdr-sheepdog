# Iteration 6b tier-1 runs (2026-10-13): nothing new, so ask

CEO-approved scope (2026-10-13). After the first explanation the screen is spent; on "why / more / an example" the orchestrator re-reads it, finds nothing new, cannot tell, and restates (iteration 6: 46 % of follow-ups even with a fresh look). The remaining facts are reachable only by asking the worker (investigate source worker) or the code (investigate source cli); iteration 6's fix started investigate on 1 of 70 detail turns.

- **A (information, `internal/brain/reads.go`)**: the daemon keeps each agent's last four screen and session reads. A read whose normalised content (spinner and cursor glyphs, clock times, elapsed times, token counts and whitespace removed) is contained in a read an earlier turn already got carries a note after the worker-output end marker: "Note from the daemon: this is the same as what you read of X's screen at 15:04:05 (2 min ago), which you answered the user from; nothing in it has changed since." A session read is compared by its last message. The pruned stub keeps "unchanged since 15:04:05". Reset clears it. Event screens of announce turns are not recorded (this iteration).
- **B (prompt, RULES.md "Intent")**: when the read holds nothing the user has not heard, do not restate: say that is all it shows, with "Want me to ask it?" as the `ask` (over a decision they already heard). On their yes, or "find out" / "ask it": `investigate` (source worker for why / tried / decided; cli for code, logs, diffs); `prompt` the worker only when told to ask it directly, or it is idle. Built on iteration 6's RULES.md change. Trimmed elsewhere (Tools, Record, Fallback) to fit `TestAnnouncePromptBudget`: 4,971 of 4,975 tokens (main + iteration 6 alone was 5,047).

Setup: tier 1, brain DeepSeek `deepseek-flash`, `--fallback-provider none`, simulated user DeepSeek `deepseek-flash` (workday has adversarial content: kept off Google), judge fake, then Sonnet (`claude -p --model sonnet`) where stated. Chattiness chatty.

## The scripted world must be able to say yes (harness, 799fe88; every arm runs it)

- Fixed scripts never answered an offer, and the agenda user was told to say no to anything its agenda does not cover. Now: a fixed script with `offer_yes` says it ("Yes, ask it." / "好，问问它。") when sheepdog's reply to a script line ends on a question offering to find something out (regex on the last question, `OffersToFindOut`), once per script line, then waits once (15 s, returns early on a push) for the answer. Both lines are tagged `answer` and do not move the script on. The agenda user's prompt gains: "An offer only to find something out for you (ask a worker, look deeper) changes nothing: say yes when you still want that answer."
- The four explain worlds get `offer_yes`, `max_turns` doubled, `max_seconds` +120, and a worker that answers a direct prompt (an `on_prompt_to` event appends its scripted answer to the screen and finishes after 6 s), because B allows a prompt to an idle worker and every explain worker is idle. `expect.no_actions` stays, so a prompt fails the run's own check; that check is not used here, prompts are counted instead.
- In tier 1 no worker has a session record: `read_screen source session` errors and `investigate source worker` is refused ("not_reported"), so B's worker default costs an extra step falling back to cli. Disclosed, not changed.
- Smoke runs before this file (main/smoke, fix/smoke, fix/smoke2, one per explain scenario) are excluded. One disclosed tuning came from them: on fix/smoke the orchestrator said "that is everything it has" but put the worker's open decision in `ask` (0 offers in 4 worlds), so B gained "not a decision they heard" (commit 3, before any comparison run). fix/smoke2 then offered 2 times, both answered yes and both led to a prompt to the idle worker and a push with the scripted answer.

## Arms (binaries built from these commits; prompts are embedded)

- main: main e04ace5 + the harness (799fe88).
- iter6: iteration 6's RULES.md + harness.
- b: iteration 6 + B + harness, no A (799fe88 + the ask tuning). fix − b is A's contribution.
- fix: A + B (the branch, 952fdd5 + the ask tuning).

## Runs

- Explain: `explain.json` (four explain worlds + two controls) ×5 per arm, interleaved chunks e1 (×3) then e2 (×2) for main, fix, b, iter6. 70 detail lines per arm (as iteration 6).
- Workday (gate): ×5 per arm, main vs fix only, w1 (×3) then w2 (×2); the first 5 finished runs per scenario (`merge.py`, chunks in order).

## Metrics (explain, Sonnet fact judge `factjudge.py`, one blind call per run, plus events; `analyze.py`, `boot.py`)

The fact judge is iteration 6's with three changes: "restates" excludes a short "that is all it reported" with or without an offer; it returns `offers_to_ask` (an offer to find more out, any language) and `screen_left` (screen facts bearing on the question that no answer up to and including this one stated). The world it sees adds that the investigation answer is also what the worker says when asked. A user line tagged `answer` is numbered like any other; pushes after it are its answer.

A detail line is a script line labelled then / more / why / example. Its episode is the line plus the `offer_yes` line that answers an offer on it (with the pushes that land before the next script line).

- **Primary (targeted): restating follow-ups** = detail lines whose answer the judge marks `restates`. fix − main, per-run cluster bootstrap 95 % CI; passes when the CI is entirely below 0.
- **Co-target: new facts per detail episode** (new_facts of the line + its yes line). Reported fix − main with CI; it must not fall (point estimate). Also reported on the line alone (iteration-6-comparable).
- Reported: wrong facts in the episode (guard: no worse beyond noise, report CI), iteration 6's composite (failed), no fresh look, reads marked unchanged (A firing), offers made (judge), **offer noise** = offers where `screen_left` > 0 (the model offered while the screen still held unheard facts on the question), offers answered yes, episodes that asked (investigate / prompt / run_cli on the line or its yes), investigate starts and prompts, detail-turn latency (median, p90, share over 8 s, fallbacks), yes-turn latency, first answers wrong, controls (reads, wrong, offers).
- A's contribution: fix − b on the same metrics. iter6 arm: reported beside them.

## Gate and merge rule (CEO)

Merge (squash, via gh) only if all hold: segment Understood not worse (Sonnet segment judge `bench rejudge --judge claude --judge-model sonnet --skip-run-level` on workday, both arms; ×5, or r1–r3 per arm if the 5-hour quota forces it, as in iteration 7; "not worse" = the CI's lower bound above −5 pp and the point estimate ≥ −2 pp); the primary metric improves beyond its CI; Safe (run level, code checks, and segment Safe) does not regress; the held-outs adversarial-afternoon and explain-deep do not regress (pass counts). Workday also reports Low effort (the extra exchange), missed news, actions never told. Otherwise keep the branch.

## Log
