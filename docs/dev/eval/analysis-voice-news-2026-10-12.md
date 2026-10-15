# Voice-layer missed news and untold actions, 2026-10-12

Harness iteration 2, phase A (analysis, no live runs). Question from [vertex-2026-10-11.md](vertex-2026-10-11.md): with the voice layer (tier 2) the user misses 35 % of the news (29/84) against 15 % without it (tier 1, 32/212, same brain and prompts), and 21 % of the actions are never told (6/28) against 7 % (5/72). Where is each item lost: never delivered to the voice, delivered silent, held, cut off, superseded by the user, or said but not counted?

## Data and method

- Tier 2, 2026-10-11, the 12 workday runs of each prompt arm (`docs/dev/bench/results/2026-10-11-t2/{a,rl,nc}/merged`): 84 news items and 28–29 actions per arm. Arm `a` (main prompts) is labelled by hand, item by item; `rl` and `nc` (voice prompt variants; the delivery code is the same) are labelled by the same rules with a script, and three of their labels were corrected by hand. Tier 1 Gemini (`2026-10-11-t1-gemini/merged`, 30 runs) is the comparison.
- Tier 2, 2026-10-05 (`2026-10-05-workday/runs-and-events.tar.gz`, 21 runs, an older build): checked for the largest class only (below); its push events carry no agent tag, so the other classes cannot be separated.
- Per item: the missed-news and said-vs-done checks rerun offline (`internal/northstar`) on the voice side and on the orchestrator side of the same run; then the event log around it: `brain.push_decision`, `voice.push` (channel, `scheduling_sent`, briefing id), `voice.push_deferred`, `voice.briefing_unvoiced`, the "cut you off" fact, `herdr.transition`, the user's lines and the phone's utterances (drained or flushed). The window is the check's: until the user's next line or 30 s after the news, whichever is later.

## Findings

1. **The largest class is not the voice: a finish right after an approved prompt is never announced** (33 of 83 missed items over the three arms, 10 of 29 in arm a). The worker goes `blocked → done` without herdr reporting `working` in between, and the supervisor treats only edges off `working` as a finish (`internal/supervisor/transitions.go`). The brain receives the worker event, but no announcement is made, so the voice is never told; when the user asks "what's new?" the voice answers from its own context without asking the orchestrator. In all 12 such edges per arm the prompt had been answered yes 4–6 s earlier; the 2 edges per arm that followed a decline (`esc`) went `blocked → idle` 0.3 s later. Tier 1 has the same gap (15 of its 32 missed items: 15/35 such finishes missed against 17/177 other news); the voice layer makes it worse (10/14 missed in arm a) because tier 1's next reply comes from the orchestrator, which may mention the finish. The bench world skips `working` after an approval, so the bench over-represents the case; in production it happens when the command is short enough to fall inside herdr's poll or the status event is missed and the snapshot diff recovers it. The production gain will be smaller than the bench gain.
2. **Untold actions is a measurement gap, not a delivery gap.** 10 of the 11 untold actions in the three arms were told: the voice rephrased the orchestrator's line in words the claim phrase list lacked ("I have confirmed that refactor-codex is stopped", "我已经让 refactor-codex 停下来", "已为您停止", "I've set Pricing-claude to apply the first option", "has been started for pay-codex", "我已指示", "设为第一个方案"); the orchestrator's own line was matched in each arm-a case. One is real: nc scale-ten r1, the approval's tool result arrived while the user was asking the next question and the voice moved on to it. With the phrase lists extended (measurement only), arm a's actions never told fall from 6/28 to 2/28 (7 %, the same as tier 1's 5/72).
3. **Six of arm a's missed news items (17 of 83 overall) were said** and not counted: a wording the news word list lacked (修復, 實現, 生成, 提交, "resolved", "optimized", 未找到), an agent named differently ("Accessibility Claude"), or a sentence split across two phone utterances (name in the first, "passed" in the second). With the word lists extended, arm a's missed news falls from 29/84 (35 %) to 23/84 (27 %).
4. **What is left is the voice layer proper, 27 items over the three arms** (11 in arm a): news delivered as `function_response`/`WHEN_IDLE` that landed just as the user spoke and drew no speech (`briefing_unvoiced`, 9), news held 35–80 s in the queue behind block announcements and sent after the window (8), news the voice began and the user cut off (7), and news delivered to an idle voice that it acknowledged ("Understood.") or talked over with an answer to the user's earlier question (3). In every unvoiced and cut case the voice received the delivery fact ("briefing b-17 … was not voiced", "the user cut you off … they heard only …") as a silent push, and in none of them did it come back to the news within the window.
5. **Six items are screen-only updates**: notes-claude, already `done`, writes a correction ("SSL 证书其实 10 月 7 日到期") with no state change, so sheepdog has no event at all. Tier 1 misses the same item in 5 of 5 runs; not a voice problem.

Every spoken news push in the 36 runs went out on the `function_response` channel; 81 % of them `WHEN_IDLE`, the rest `INTERRUPT`. The time from the brain's decision to the push was 0.1–2.1 s at the median, about 20 s at the 90th percentile and 77 s at most (9 of about 63 pushes per arm waited over 15 s); the long waits are news queued behind owed lines (blocks) in clustered scale-ten and morning-standup moments.

## Missed news by class

Counts are missed items (arm a by hand; rl and nc by script with hand corrections). "Voice-specific" marks the classes a tier-1 run cannot have.

| Class | What happened | a | rl | nc | Total | Voice-specific |
|---|---|---|---|---|---|---|
| **N1** never announced: finish right after an approved prompt | `blocked → done` 4–6 s after sheepdog answered yes; no push decision, no push; the orchestrator side misses it too | 10 | 11 | 12 | **33** | no (tier 1: 15/35 missed) |
| **M** measurement: said, not counted | wording outside the word list, agent named differently, or one sentence split over two utterances | 6 | 5 | 6 | **17** | partly |
| **U** delivered as the user spoke, never voiced | `WHEN_IDLE` push landed at the end of a user turn (`briefing_unvoiced`); the voice answered the user; the fact went out silent and was not acted on | 5 | 2 | 2 | **9** | yes |
| **L** held in the queue, sent after the window | decision on time, push 35–80 s later behind block announcements (and their late re-announcements) | 2 | 3 | 3 | **8** | yes (the queue is the voice session's) |
| **C** voiced, cut off by the user, not resumed | the user started talking 1–5 s into the news; "cut you off" fact sent silent; not repeated | 2 | 4 | 1 | **7** | yes |
| **W** screen-only update on a done pane | no state change, so no event | 2 | 2 | 2 | **6** | no (tier 1: 5/5 missed) |
| **V** delivered to an idle voice, not said | voice said "Understood." or answered the user's earlier question instead | 2 | 0 | 1 | **3** | yes |
| **Total missed** | | 29 | 27 | 27 | **83** of 252 | |

No missed item was sent silent, and none was overwritten by a newer briefing before it could be spoken; two of the U and C items had also waited 25 s in the queue first (morning-standup r1 and r2, tests-codex).

2026-10-05 (older build, 21 runs, 84 of 141 missed): 17 of the 84 are N1 (`blocked → done` with no push decision), so the gap is old. The rest cannot be classed from those logs (push events without the agent).

## Untold actions by class

| Class | a | rl | nc | Total |
|---|---|---|---|---|
| Measurement: told in other words (claim phrase list) | 6 | 3 | 1 | 10 |
| Superseded by the user's next question (tool result arrived while the user spoke; voice moved on) | 0 | 0 | 1 | 1 |
| Never delivered | 0 | 0 | 0 | 0 |

## Arm a, item by item

Times are seconds into the call. "Decision → push" is `brain.push_decision` to the spoken `voice.push`.

| # | Run | News | t | Delivered to the voice | Why the user did not hear it (evidence) | Class |
|---|---|---|---|---|---|---|
| 1 | explain-deep r1 | a11y-claude finished | 292.5 | push 301.4, `when_idle` | said at 302: "A11y-claude 已經修復了全部無障礙問題 … 實現零違規" (修復/實現 not in the word list) | M |
| 2 | explain-deep r2 | flaky-codex finished | 230.9 | push 234.4, `when_idle` | said in two utterances: "Flaky Codex applied the fix … all one hundred" / "and twenty eight tests passed" (name and finish word in different utterances) | M |
| 3 | explain-deep r2 | a11y-claude finished | 506.5 | push 508.8, `when_idle`, voice idle | the voice said only "Understood." | V |
| 4 | interruptions r1 | ci-codex finished | 180.5 | push 188.6 | said at 189: "ci-codex has optimized the build pipeline …" ("optimized" not in the list) | M |
| 5 | interruptions r1 | refactor-codex finished | 292.3 | decision 301.2; push held while the user spoke (303.7–310.8), sent 311.4 | `briefing_unvoiced` (user spoke at 312.0); the voice answered the user's search-claude question; not resumed | U |
| 6 | interruptions r2 | infra-claude result | 185.2 | push 189.6 | said at 190: "检查显示未找到昨晚备份的记录 …" (未找到 not in the list) | M |
| 7 | long-haul r1 | seo-codex finished | 81.5 | push 84.1 | said at 84: "SEO Codex 已经 … 生成了站点地图，并已成功提交" (生成/提交 not in the list) | M |
| 8 | long-haul r1 | deploy-codex finished | 169.4 | none: `blocked → done` 6 s after "y" | never announced | N1 |
| 9 | long-haul r1 | pricing-claude finished | 298.2 | none: `blocked → done` 5 s after "1" | never announced | N1 |
| 10 | long-haul r1 | infra-codex finished | 370.7 | push 372.3 | the user cut in after 1.3 s ("顺便告诉您，infra-co"); not resumed | C |
| 11 | long-haul r2 | deploy-codex finished | 170.5 | none: `blocked → done` 6 s after "y" | never announced; at 239 "what's new?" the voice listed perf and audit only | N1 |
| 12 | long-haul r2 | pricing-claude finished | 345.8 | none: `blocked → done` 5 s after "1" | never announced | N1 |
| 13 | long-haul r2 | infra-codex finished | 415.5 | push 419.3, as the user's request ended | `briefing_unvoiced`; the voice handled the bugs-codex request | U |
| 14 | morning-standup r1 | tests-codex finished | 96.5 | decision 103.8, push 129.2 (25 s behind billing-claude's two block lines) | the user cut in after 4.1 s | C |
| 15 | morning-standup r1 | infra-codex finished | 97.4 | none: `blocked → done` 6 s after "y" | never announced | N1 |
| 16 | morning-standup r2 | web-claude finished | 33.6 | push 35.9, as the user's question ended | `briefing_unvoiced`; the voice answered "which need me" with infra-codex only | U |
| 17 | morning-standup r2 | tests-codex finished | 121.8 | decision 127.2, push 151.8 (25 s behind block lines) | `briefing_unvoiced`; the user had asked about billing-claude | U |
| 18 | morning-standup r2 | web-claude finished | 198.3 | push 200.0, as the user's question ended | `briefing_unvoiced`; the voice handled the infra-codex question | U |
| 19 | morning-standup r2 | api-codex finished | 260.0 | push 263.3, voice idle | the voice answered the user's earlier infra-codex question instead | V |
| 20 | scale-ten r1 | pay-codex finished | 61.7 | none: `blocked → done` 4 s after "y" | never announced | N1 |
| 21 | scale-ten r1 | docs-claude finished | 72.9 | decision 79.9, push 120.7 (+41 s behind cache/etl block lines and their late versions) | sent after the window | L |
| 22 | scale-ten r1 | cache-codex finished | 151.9 | none: `blocked → done` 4 s after "y" | never announced | N1 |
| 23 | scale-ten r2 | pay-codex finished | 84.0 | none: `blocked → done` 4 s after "y" | never announced | N1 |
| 24 | scale-ten r2 | lint-codex finished | 93.5 | push 104.0 | said at 104: "lint-codex has resolved all thirty-seven …" ("resolved" not in the list) | M |
| 25 | scale-ten r2 | docs-claude finished | 96.5 | decision 103.1, push 138.2 (+35 s behind block lines) | said at 139.5, 8 s after the window closed | L |
| 26 | scale-ten r2 | cache-codex finished | 180.9 | none: `blocked → done` 4 s after "y" | never announced (the voice's next line was about the approval) | N1 |
| 27 | zh-evening r1 | notes-claude correction | 437.5 | none: screen changed on a `done` pane, no transition | no event | W |
| 28 | zh-evening r2 | ui-claude finished | 247.7 | none: `blocked → done` 5 s after "1" | never announced | N1 |
| 29 | zh-evening r2 | notes-claude correction | 305.3 | none: no transition | no event | W |

Untold actions, arm a (all told, in other words): interruptions r1 interrupt ("I have confirmed that refactor-codex is stopped"); interruptions r2 interrupt ("我已经让 refactor-codex 停下来并处于空闲状态了", read as a sent prompt); long-haul r2 keys 1 ("I've set Pricing-claude to apply the first option"); morning-standup r2 keys y ("I have told infra-codex to proceed with the Terraform plan.Billing-claude is asking …": no space after the period, so one sentence with "should" in it, read as a plan); scale-ten r1 keys y ("The command to run the payment tests has been started for pay-codex"); scale-ten r1 prompt to etl-codex ("I've canceled the" cut off, then "etlcodex has successfully moved all the files to an archive folder").

## Corrected baseline (offline rescoring of the same records, phrase lists extended)

| | Missed news | Actions never told |
|---|---|---|
| Tier 1 Gemini | 32/212 (15 %), unchanged | 5/72 (7 %), unchanged |
| Tier 2 a | 29/84 → **23/84 (27 %)** | 6/28 → **2/28 (7 %)** |
| Tier 2 rl | 27/84 → 26/84 | 3/28 → 0/28 |
| Tier 2 nc | 27/84 → 27/84 | 2/29 → 1/29 |

The untold-actions gap disappears. The missed-news gap shrinks to about 12 pp; N1 alone is 10 of arm a's remaining 23, and with N1 announced and voiced arm a would be near tier 1.

## Fix proposals, ranked by expected impact

| Rank | Label | Proposal | Items it addresses | Expected effect | Status |
|---|---|---|---|---|---|
| 1 | **rail / information** | A `blocked → done` or `blocked → idle` edge after a prompt sheepdog answered yes is a finish: read the screen and announce it like any finish (the brain writes the line; chattiness rules apply). A decline, or a prompt answered outside sheepdog, stays unannounced. | N1: 33/83 (both tiers) | tier 2 missed news −8 to −12 pp on the bench; tier 1 about −5 pp; smaller in production | implemented (`fix(supervisor)`), unit-tested, verification below |
| 2 | **measurement** | Extend the news word list and the claim phrase list with the voice's rephrasings (修復/修复, 實現, 生成, 提交, 撤销, 部署, resolved, optimized, applied, reverted, deployed, 未找到; 已为您停止, 让 X 停下来, 指示, 设为, "set X to", "has been started", "is stopped"). | M and the untold actions | corrects the metric (a: −7 pp missed news, −14 pp untold); no product effect | implemented (`fix(northstar)`), unit-tested |
| 3 | **rail** (needs a decision) | Re-queue an unvoiced or cut-off news briefing once, `WHEN_IDLE`, after the voice's reply to the user. This reverses the 2026-10-04 rule in `internal/voice/gemini/unvoiced.go` ("nothing is re-queued … the voice's call"); 16 of 16 such facts were not acted on in these runs. An information-only variant: put the briefing's text in the fact and hand it over with the voice's next tool response (`pendingDeliveryFactsLocked`) instead of a silent push in the middle of the user's turn. | U + C: 16/83 | up to −6 pp | proposed, for the CEO |
| 4 | **rail** | Keep news from starving behind owed lines in clustered moments: today each block gets a template line and, seconds later, the brain's late line (`brain.push_late`), both owed, so a finish decided at the same time waits 35–80 s. Overlaps iteration 3 (clustered events / orchestrator overload). | L: 8/83, and the 25 s waits behind U/C items | up to −3 pp | proposed, for iteration 3 |
| 5 | **feature** | Watch `done`/`idle` panes for new output (a screen-only correction), or mark such scenario beats as not exercised. Not voice-specific. | W: 6/83 (tier 1 too) | −2 pp both tiers | proposed |
| 6 | **prompt** | The voice acknowledges a briefing ("Understood.") or talks over it with an earlier answer. A rule that a briefing is said, not acknowledged, would target it; three items only. | V: 3/83 | about −1 pp | not worth an iteration alone |
| 7 | **measurement** | Join consecutive phone utterances of one reply (the split "one hundred / and twenty eight tests passed"); count "needs to know", "would you like to run" as block words. | part of M | small | proposed |

Fixes 1 and 2 are small, rail (fix 1) and measurement (fix 2), and go to verification. Fix 3 changes a documented decision and is left to the CEO.

## Phase B verification

Tier 2, production config (brain DeepSeek `deepseek-flash`, voice Vertex Live `gemini-3.8-live`, Vertex TTS, simulated user Vertex `gemini-3.5-flash`, chatty), the 6 non-adversarial workday scenarios ×2 for the fix and for current main (which includes the nc voice rule #203 and the turn-budget fix #205), chunks interleaved fix, main; ports 7921–7922; the personas of `--personas rotate` repeats 1 and 2. The plan was ×3; workday chunks cost $4.2–4.9 each on the bench meter, so after two pairs ($18.10) a third pair, even on four scenarios, would have passed the $20 cap and was not run. Sonnet segment judge (`bench rejudge --judge claude --judge-model sonnet --orchestrator`), both arms measured with the extended phrase lists. Raw data and tables: `docs/dev/bench/results/2026-10-12-it2/` (`northstar-main-to-fix.md` is fix − main).

| Metric | main | fix | fix − main (95 % CI) |
|---|---|---|---|
| Missed news | 26/82 (32 %) | 23/84 (27 %) | −4 pp (−18 to +9) |
| Actions never told | 8/29 (28 %) | 7/30 (23 %) | −4 pp (−26 to +18) |
| Segment Understood | 100/145 (69 %) | 113/146 (77 %) | +8 pp (−6 to +23) |
| Segment Safe (judge) | 166/175 (95 %) | 173/177 (98 %) | n.s. |
| Run Safe | 5/12 | 5/12 | 0 |

The fix did what it was built for: main left 9 finishes after an approval unannounced (all `blocked → done` 4–6 s after a yes); the fix announced every such finish (0 unannounced; two of them were pushed but the voice did not say them). The headline metric moved less than the class did: the fix's remaining misses are the voice-layer classes (5 unvoiced, 2 cut off, 2 not said, 1 held past the window), 2 screen-only updates, 3 finishes in a cluster of block announcements, and 8 lines that were said in words still outside the lists ("ci codex finished" with a space, "refactor-codex 已经停止", "has run the plan", "cleaned up"). With 12 runs per arm the CI is ±13 pp, wider than the class's share.

**Decision (merge rule: the targeted metric beyond its CI, segment Understood not worse, Safe not worse): the fix is not merged.** Understood and Safe are not worse, but missed news (−4 pp) and actions never told (−4 pp) do not improve beyond their CIs. The fix is kept on branch `iter/2-finish-after-approval` for a larger run or a CEO decision; the analysis and the measurement fix (phrase lists, measurement only) are merged. Next candidates: fix 3 above (re-queue unvoiced news; needs a decision because it reverses a documented rule), and a confirmation of this fix at n ≥ 30 per arm, where the class's −8 to −12 pp expected effect would be separable.
