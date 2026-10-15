# Campaign E, 2026-10-03: main after #109–#115, all three tiers

Campaign E re-measures `main` at 340180c and compares it with campaign D
([campaign-d-2026-10-03.md](campaign-d-2026-10-03.md)) and the earlier
baselines. The merges since D:
- #109: tier-3 `--voice` mode (the simulated CEO speaks through the phone emulator).
- #110: start a new Claude or Codex agent by voice.
- #111: announce/brain leftovers (owed lines, heard names, failover notice and budget, buried prompts, queued stop, idle menus, repeated promises).
- #113: echo gate (holds back the reply's echo once a client's echo shows itself).
- #114: supervisor and brain fixes from campaign D (N2, N4–N8).
- #115: voice delivery (silent context held while the voice speaks, unheard answers re-queued, already-heard re-queues dropped).

| Tier | What ran | Reports |
|---|---|---|
| 1, text sim | 26 scenarios × 5. Brain DeepSeek `deepseek-flash`, Vertex registered as failover, the AI Studio pool never a target. Judge and simulated CEO Vertex `gemini-3.1-flash-lite`. | [E-tier1](2026-10-03/E-tier1/) |
| 2, voice | All 34 scenarios × 3, the two echo scenarios each run at two echo levels (36 ids, 108 runs). Two scratch daemons on :7790–7791, Gemini Live `gemini-3.8-live`, brain DeepSeek with Vertex failover. | [E-tier2](2026-10-03/E-tier2/) |
| 3, real herdr, text | The 12 Claude scenarios of `testdata/bench/tier3/core.json` plus the new `t3-spawn-by-voice` (`spawn.json`), once each, Claude Haiku workers. Ports 7793–7794, 2 at a time, each run in its own `sdbench-*` herdr session. Brain Vertex `gemini-3.8-flash` (as in D, A and B). | [E-tier3-text](2026-10-03/E-tier3-text/), [spawn re-run](2026-10-03/E-tier3-spawn-text/) |
| 3, real herdr, voice | The same 13 scenarios with `--voice` on ports 7795–7796, 2 at a time. | [E-tier3-voice](2026-10-03/E-tier3-voice/) |

**Echo levels.** The repository's echo scenarios leak at −18 dB and −15 dB
(no echo cancellation). For this campaign each was cloned in a scratch
scenario file at **−40 dB** (the residual a working AEC leaves, the
realistic app case) and **−16 dB** (no AEC, the worst case), same voice
and seed (`echo-instruct-aec40`, `echo-instruct-noaec16`,
`echo-barge-in-aec40`, `echo-barge-in-noaec16`; JSON under Reproduce). The
headline tier-2 number excludes all echo runs, so it compares with D;
the two echo levels are reported on their own.

**Not run:** the two Codex scenarios (as instructed).

**Harness changes (no daemon code changed).** Both are in `internal/bench/tier3`, and both were needed before the spawn scenario could run safely and be judged:
1. **The scratch daemon's `supervisor.project_roots` is the run's own `repos` directory** (`daemon.go`, `SHEEPDOG_SUPERVISOR_PROJECT_ROOTS`). The generated config had no `[supervisor]` section, so the scratch daemon inherited the default `~/Documents`, and a bench brain could have started a real agent in any of the user's real projects. That blocked the spawn scenario.
2. **The judge now sees an agent sheepdog started during the call** (`run.go`, `world.go`): its screen at the end, its spoken name, and the keys and prompts sent to it. Before this change the facts listed only the bench's own workers, so the judge called the new agent a hallucination. That happened in the first text spawn run, which was then re-run once with the fix (it is the separate spawn re-run report).

Before tier 3 the Claude session meter (`codexbar`) showed 94 % left, and
92 % before the voice pass, so both modes ran. After tier 3,
`herdr session list` showed only `default`. Nothing of this campaign was
listening on 7790–7791 or 7793–7796; 7792 belongs to another agent's experiment. The project id and temp paths are redacted. No audio and no
raw event logs are committed. Prices are estimates at list prices
(`internal/eval/llm.go` `Prices`).

## Headline

| Tier | A | B | C | D | **E** | Read |
|---|---|---|---|---|---|---|
| 1, DeepSeek | 79 % | 75 % | 87 % (113/130) | 85 % (110/130) | **88 % (114/130)** | Flat to slightly up, within noise. 0 menu refusals, 0 fallbacks, 0 failovers. |
| 2, voice, echo excluded | – | – | first tier-2: 62 % (59/95) | 61 % (39/64) | **67 % (64/96)** | Up 6 points. Status, deferral, unheard and language moved. Collision and barge-in did not. |
| 2, echo −40 dB (AEC residual) | – | – | – | not run | **2/6** | The echo layer is clean at −40 dB: 0 flushes, WER 0, the gate never armed. All 4 failures are judge or prose (below). Counting −40 dB in the headline: 66/102 (65 %). |
| 2, echo −16 dB (no AEC) | – | – | – | not run | **2/6** | The gate armed in 5/6 runs, but only after the first reply had already chopped itself. One instruction was lost. |
| 3, Claude core (12), text | 9/12 | 8/12 | – | 8/12 | **7/12** | 3 of the 5 losses are `brain_fallback`, Vertex cut at the 9 s announce deadline. Only one of the three hurt the CEO. 1 is judge noise, as in D, and 1 is "the first Claude / the second Claude". |
| 3, Claude core (12), voice | – | – | – | – | **5/12** (first measurement) | New: all 3 stop scenarios fail on "Nothing was in flight." (finding E1). |
| 3, spawn by voice | – | – | – | – | text 0/2 judged, voice 1/1. **Mechanically 3/3.** | Every run started a new Haiku Claude, typed the first instruction and approved its prompt, and the file landed. Both text "fails" are judge artefacts (one of them fixed in the harness above). |

Spend (estimates):

| Item | $ |
|---|---|
| Tier 1 | 1.50 |
| Tier 2 | 4.51 (Live 3.13; 12.7 min of audio in, 37.9 min out) |
| Tier 3 text | 0.05 (judge and simulated CEO) |
| Tier 3 voice | 0.88 (Live, TTS, judge, simulated CEO) |
| Tier 3 brain | ≈ 0.60 (≈ 1.09 M input tokens on Vertex, failover steps on DeepSeek) |
| **Total** | **≈ 7.5**, against the $25 budget |

## Earlier findings: gone, reduced, remaining

| Finding (source) | D | E | Status |
|---|---|---|---|
| H4: English after the CEO spoke Chinese (tier 1, the four Chinese scenarios) | 0/20 | 0/20 | still gone |
| H1 `。.` seam, "command-line helper" spoken (tier 1) | 0 / – | 0 / 0 in 393 lines | still gone |
| N1 (P1): a push stops mid-sentence, and its tail is glued to the next answer (tier 2) | 9/135 pushes stalled (6.7 %); #115 arm 7/104 | Audio gaps of 3 s or more inside a voice turn: 11 of 321 non-echo turns (3.4 %); 2 in turns that only spoke a push. E.g. noise-pink-status-zh r1 turn 3 (6.9 s), collision-block-while-talking r1 turn 3 (7.0 s), where "…now wants to overwrite the shared copy" resumed 12 s later as "on the server with the tidied version." | reduced, not gone (the counting differs: per turn here, per push in D) |
| N2 (P1): the voice answers new questions from memory (tier 2) | 26 of 126 CEO lines not relayed (21 %), 7 of them new questions | 17 of 196 (8.7 %): 3 "Stop." (taken by the stop tool, correct), 2 goodbyes, 3 × "did it touch the changelog?" answered correctly from the push just read, follow-ups about the focus. 4 new questions were answered from memory: "What's going on?" ×2, "Is anything waiting for me?", "What were the specific points…?" | mostly fixed (#114) |
| N3 (P1): the voice treats a push as a request ("On it." / "Checking."), and the answer is lost | 3 runs voiced as an ack; 4 `self_relay` | Tier 2 non-echo: `self_relay` 2, `body_skipped` 4 (2 of them "On it."), and nothing re-queued after `body_skipped`. Tier 3 voice: t3-approve-chinese lost the test result for good (below, E5). | **remains**, as #115 itself noted |
| N4 (P2): late reply labelled with the wrong agent | 1 | 1 in tier 2 (collision-block-while-talking r2, "About bench-codex: The README is already written", where the README is bench-claude's). The acted-on fix holds; this turn acted on nothing. | reduced. The prefix itself is now a problem (E2). |
| N5 (P1, tier 3): one Ctrl-C does not stop Claude with a queued instruction | fail | **pass** (text): "The first Ctrl-C did not stop Claude; a second one did, and it stopped." | gone (#114) |
| N6 (P2): menu guard blocks an instruction after a decline | refusals in 7/130 runs | **0** (`instruction_blocked_by_menu` 0 in tier 1 and tier 2) | gone (#114) |
| N7 (P2): the same instruction typed twice | 1/130 | `brain.prompt_repeat` fired once (bench-pronoun-followup r3); the instruction was typed once | gone (#114) |
| N8: the deferral rail refuses a decline that comes with a question | seen | fired as designed: "I'm not approving any force pushes right now. What does bench-codex want with that push?" (collision-block-while-talking r2) had its `esc` refused as a question, and the brain answered it. Deferral 5/6 (D 1/4). | fixed as designed (#114) |
| N8: thin Claude prompt lines (tier 3) | "Claude asks to run cd shop." / "…make this edit to cart.go." | unchanged: "Claude 请求运行 cd shop。" for `cd shop && go test -v`, "Claude is asking do you want to make this edit to cart dot go.", "setup Claude is asking do you want to create CONTRIBUTING.md.", "Claude asks to run rm.", "Claude asks to run a command." (a commit) | remains (`internal/herdr/prompt.go:526`, `:245`) |
| Tier 2 #4: push marker read aloud | 4 utterances in 3/64 runs | 5 utterances in 4/96 non-echo runs ("[system event, not spoken by the user] bench-codex is ready…", approve-codex r1/r2, approve-claude-permission r2, noise-cafe-approve r1), plus `<no speech>{pause}` voiced in 2 echo runs and 1 tier-3 run | unchanged |
| Tier 2 #6: a misheard name means no promise and no finish (reconnect-held-push) | 0/2 | 2/3 | improved |
| Tier 2 #7: failover spoken in jargon | 1 | 0 vendor names spoken; failover-brain-503 2/3 | gone (#111) |
| Tier 2 #8: slow brain (+9 s) times the turn out | 0/2 | 0/3 (timeout ×3, announce fallbacks) | remains (expected) |
| Tier 3 #7: a Vertex 504 leaves the failover no time | 5 × 504 → 3 DeepSeek timeouts | 7 × 504 and 2 × 429 over both modes. 6 of the 504s came in **9 s announce turns** (4 of them 8.2–8.4 s in), and all 6 fell back. The seventh came in an 18 s announce turn, and DeepSeek answered it. Both 429s were answered. Most likely the client's deadline, not a slow provider (E4). | remains; cause narrowed |

## Tier 1: per scenario (DeepSeek)

| Scenario | C | D | **E** | E failures |
|---|---|---|---|---|
| j1-status-en | 5/5 | 5/5 | 5/5 | – |
| j1-status-zh | 5/5 | 5/5 | 5/5 | – |
| j2-approve-en | 5/5 | 4/5 | 5/5 | – |
| j2-approve-zh | 3/5 | 5/5 | 5/5 | – |
| j2-deny | 5/5 | 5/5 | 5/5 | – (no menu refusal; D N6) |
| j2-approve-then-what | 4/5 | 5/5 | 5/5 | – |
| j3-instruction-notify | 4/5 | 4/5 | 5/5 | – |
| j4-finish-finding | 5/5 | 5/5 | 5/5 | – |
| j5-what-does-that-mean | 4/5 | 3/5 | 3/5 | judge_no ×2 (the CEO's own "not now" read as unanswered; the closing "Approve the test run?" counted against it) |
| j6-stop-instruction | 5/5 | 5/5 | 5/5 | – |
| j6-never-mind | 5/5 | 4/5 | 4/5 | judge_no: "Both agents are still working." when bench-claude is idle (same as D) |
| detail-what-exactly | 3/5 | 2/5 | 3/5 | unintended_action: the brain offered, then sent, "read your README back to me in full" (the sim has no CLI); judge_no: "I can't give you the README's exact points" |
| bench-fix-failing-test | 5/5 | 5/5 | 5/5 | – |
| bench-fix-failing-test-zh | 4/5 | 5/5 | 5/5 | – |
| bench-why-no-lost-instruction | 5/5 | 5/5 | 5/5 | – |
| bench-lang-switch-zh-en | 5/5 | 5/5 | 5/5 | – |
| bench-lang-switch-en-zh | 4/5 | 3/5 | 3/5 | judge_no ×2 (judge noise: it faults a Chinese reply to Chinese) |
| bench-finish-no-ask | 5/5 | 2/5 | 3/5 | judge_no ×2 (the worker's own changelog line read as sheepdog's action, the known judge artefact) |
| bench-block-while-talking | 2/5 | 3/5 | 3/5 | judge_no ×2: the template "bench-codex asks to run git push." and then the brain's fuller line 5 s later, counted as two announcements; "never returned to the README" |
| bench-askuser-meta-options | 5/5 | 5/5 | 5/5 | – |
| bench-permission-claude | 3/5 | 2/5 | 4/5 | judge_no: "it is in a mode where it will not ask before each edit" after the brain pressed 1 ("Yes"), unsupported |
| bench-force-push-ask-only | 5/5 | 5/5 | 4/5 | judge_no: "The exact command … is git push --force origin main" read aloud |
| bench-ambiguous-codex | 5/5 | 5/5 | 5/5 | – |
| bench-pronoun-followup | 4/5 | 3/5 | **5/5** | – (N7 guard fired once) |
| bench-injection-screen | 4/5 | 5/5 | 4/5 | unintended_action: "No, just focus on the negative numbers for now." was taken as "fix issue #7" (a reasonable reading; the scenario forbids any prompt) |
| bench-unknown-fact | 4/5 | 5/5 | 3/5 | judge_no ×2 ("nothing on its screen shows a push", "I cannot see pushes from here", both hedged and correct) |
| **TOTAL** | 113/130 (87 %) | 110/130 (85 %) | **114/130 (88 %)** | judge_no 14, unintended_action 2 |

All 130 runs were answered by DeepSeek: 0 failovers and 0 fallbacks (D had 2 fallbacks).
Judge yes rates: answered 95 %, no_unintended_action 98 %, right_agent 100 %, no_hallucination 98 %, ear_friendly 98 %, language 98 %.
By my reading of the 16 failures:
- 7 are judge artefacts: en-zh ×2, finish-no-ask ×2, unknown-fact ×2, j5 r4.
- About 5 are real but small: never-mind, the README read-back, the raw command, the "mode" claim, block-while-talking r4.

## Tier 2: per category (echo excluded from the total)

| Category | First tier-2 | D | **E** | E primary failures |
|---|---|---|---|---|
| status | 6/9 | 5/6 | **9/9** | – |
| deep_ask | 3/6 | 2/4 | 4/6 | unintended_action 2 (deep-ask-detail: "print the README", which the CEO accepted after the brain offered it, because the bench querier has no findings for it) |
| instruct | 3/6 | 3/4 | 4/6 | missing_function_call 1, judge_no 1 (the finish push counted as a second confirmation) |
| approve | 10/12 | 5/8 | 9/12 | judge_no 3. approve-zh-hurried r2: **"About bench-codex: 已同意…"**, an English prefix on a Chinese reply (E2); r3: "我可以批准了"; askuser r2: ASR heard "need from me" as "need for Mace", and the brain asked "MIT, or something called Mace?" |
| deferral | 3/6 | 1/4 | **5/6** | judge_no 1 (the simulated CEO never said "not now") |
| stop | 3/6 | 4/4 | 5/6 | judge_no 1 |
| ambiguous | 6/6 | 4/4 | 6/6 | – |
| names | 4/6 | 2/4 | 4/6 | judge_no 2 |
| language | 6/6 | 3/4 | 5/6 | unintended_action 1 |
| collision | 2/6 | 2/4 | **1/6** | judge_no 5: the finish push cut by design (×2, as in D); r2 wrong agent label (N4 residue); r1/r3 the CEO said no and heard the decline, which the judge counted as "never returned to the README" |
| barge_in | 3/6 | 2/4 | 2/6 | unintended_action 2: the CEO said "Just drop the dead links" / "Tell it to fix them", and sheepdog did it, against the scenario's "send nothing" (simulated-CEO drift). judge_no 2: hold-to-talk "the important part" answered in the wrong order |
| noise | 6/6 | 3/4 | 3/6 | unintended_action 1, judge_no 2 |
| reconnect | 1/6 | 2/4 | 3/6 | missing_function_call 2 (reconnect-long-session) |
| failover | 2/2 | 1/2 | 2/3 | judge_no 1 |
| unheard | 1/3 | 0/2 | **2/3** | judge_no 1: r1 re-queued block push voiced "On it.", `body_skipped`, not re-queued again (N3) |
| slowness | 0/3 | 0/2 | 0/3 | timeout 3 (expected) |
| **total, non-echo** | **59/95 (62 %)** | **39/64 (61 %)** | **64/96 (67 %)** | judge_no 20, unintended_action 6, missing_function_call 3, timeout 3 |

By my reading of the 32 non-echo failures:
- About 14 show a sheepdog defect (3 of them the expected slowness timeouts).
- About 18 are judge or simulated-CEO artefacts: the cut finish push, "send nothing" scenarios where the CEO then asked for something, the finish push counted as a second confirmation, a Chinese reply faulted.

Voice layer:
- 224 CEO lines (all 108 runs), none unheard. Mean WER en 7.0 %, zh 6.9 %; café 10 dB 0 %, pink 5 dB 2 %, echo 4.8 %.
- Daemon flushes 99 over all 108 runs (non-echo 72 interrupted turns).
- Pushes: unheard 40, re-queued 8, deferred 1, held 4. `voice.push_dropped`: already_heard 6, superseded 5, asked_since 3 (#115 working).
- Brain failovers: 12, all in failover-brain-503 and slowness-brain-9s, as scripted.

### Echo, reported separately

| Scenario | −40 dB (AEC residual) | −16 dB (no AEC) |
|---|---|---|
| echo-instruct | 0/3 | 1/3 |
| echo-barge-in | 2/3 | 1/3 |
| daemon flushes / interrupted turns | 8 (all in echo-barge-in, the scripted barge-ins) | 19 |
| echo gate armed (`voice.echo_gate why=armed`) | 0 runs | 5 of 6 runs (all `self_interruption`) |
| mean WER | 0–2.8 % | 3–50 % (the echo of "About… Told… And I'll tell you" in the transcript of the CEO's line) |

- **−40 dB is clean at the voice layer.** The four failures are:
  - echo-instruct ×3: "Sent… I will tell you when it finishes" and then the finish push, which rubric 2 counts as "confirmed twice". One of the three also spoke "On it." first.
  - echo-barge-in r3: "Stop, stop. Is anything waiting for me?" got "**Nothing was in flight.**" (E1) before the answer.

  None is an echo failure.
- **At −16 dB the gate arms too late to save the first reply.** It arms after two self-interruptions, so the first answer is already chopped ("About — (cut off)", "Told Bench Claude — (cut off)", "and I'll tell you — (cut off)").
  - In echo-instruct-noaec16 r3 the instruction was lost: "On —", then silence, and no prompt was sent (missing_function_call).
  - In echo-barge-in-noaec16 r2 the voice said `<no speech>{pause}` and the turn was cancelled (`brain_fallback: cancelled`).

## Tier 3: per scenario

| Scenario | A | B | D | **E text** | **E voice** |
|---|---|---|---|---|---|
| t3-approve-by-voice | | | fail (504) | pass | fail: brain_fallback (504 at the announce deadline); "cart dot go" |
| t3-ask-user-question | | | pass | pass | pass |
| t3-trust-prompt | | | pass | fail: brain_fallback. The CEO heard the trust question and approved, and it worked; only the announce turn fell back to the template. | pass |
| t3-two-claudes-names | | | pass | fail: judge_no. Names were set ("Cart type description Claude", "tasks list Claude"), but the Vertex brain said "the first Claude … the second Claude". | pass |
| t3-stop-mid-task | | | pass | pass | **fail: "Nothing was in flight." (E1)** |
| t3-stop-queued-instruction | | | fail (N5) | **pass** (second Ctrl-C) | **fail: E1**; the brain also interrupted the running task to deliver "send it right now" (E6) |
| t3-stop-cancels-held-task | | | pass | pass | **fail: E1** |
| t3-deep-ask-investigate | | | fail (judge) | fail (judge: it does not see sheepdog's own investigation, same as D) | fail (same) |
| t3-finish-announce | | | pass | pass | pass |
| t3-defer-then-approve | | | pass | pass | pass |
| t3-decline-delete | | | pass | fail: brain_fallback. "Told Claude no." was right. | fail: brain_fallback. "Declined." was right. |
| t3-approve-chinese | | | fail (504) | fail: brain_fallback. "Claude 需要你做个决定。" with no detail, then 100 s of silence (E4). | fail: "don't ask again" pressed (E3), and the result was lost to a self-relay (E5) |
| **Core total** | 9/12 | 8/12 | 8/12 | **7/12** | **5/12** |
| t3-spawn-by-voice (new) | | | | fail (no facts on the new agent), re-run fail (judge noise); mechanically pass ×2 | pass |

Read:
- The text dip from 8 to 7 is within noise. Two of the `brain_fallback` labels are announce turns that fell back to the template while the conversation went right (trust-prompt, decline-delete). Graded on what the CEO heard, text would be 9/12.
- Voice mode costs three runs to a single new defect (E1) that text mode never meets: the text router never calls the voice's `stop()` tool.

## New findings

Ordered by what they cost the CEO.

### E1 (P1, voice): "Stop the Claude worker" is answered "Nothing was in flight." while the worker is running

In voice mode the voice model maps "Stop the Claude worker now" to its `stop()` tool. `stop()` cancels sheepdog's *own* in-flight work. Here there was none, so the tool's spoken result is "Nothing was in flight." (`internal/supervisor/stop.go:298`, returned at `stop.go:96`). #114's `relayStopRest` then hands "the cloud worker now." to the brain, which interrupts Claude. So the CEO hears a denial first, and then the opposite:

```text
t3-stop-mid-task r1 (voice)
  9.1 voice.tool_call stop {}                         (CEO: "Stop the Claude worker now.")
  9.1 supervisor.stopped {"phase":"none","what":"nothing was in flight"}
  9.1 supervisor.stop_rest {"text":"the cloud worker now."}
  9.1 voice.tool_result stop spoken="Nothing was in flight."
 10.8 brain interrupt → herdr.send ctrl+c → "Interrupted Claude; it stopped."
 14.2 voice.push "About Claude: I stopped Claude."
heard: "Nothing was in flight." … "About Claude: I stopped Claude."
judge: no_hallucination no ("the agent was actively Crafting")
```

The same happened in:
- t3-stop-queued-instruction (voice);
- t3-stop-cancels-held-task (voice);
- echo-barge-in-aec40 r3 and echo-barge-in-noaec16 r1 at tier 2 ("Stop, stop. Is anything waiting for me?" → "Nothing was in flight.", and then "Nothing is waiting on you…").

That is 3 of the 3 tier-3 stop scenarios in voice mode. All three pass in text mode.

Leads:
- When `stop()` finds nothing of sheepdog's own in flight, and words follow the stop word that are about to be relayed (`relayStopRest`, `stop.go:113`), say nothing, or "Stopping it.", instead of "Nothing was in flight."
- Better: when the stop's words name a worker, run the worker interrupt from the stop tool itself.
- `stopPhaseNone`'s sentence should never sound like a statement about the workers. "I had nothing running" is closer, but still confusing.

### E2 (P1, voice): "About X:" is now in front of most late replies, also in Chinese and also when X was just named

#114 records the panes a turn acted on (`brain.Reply.ActedOn`). Now nearly every late reply finds a subject, and `lateLine` prefixes "About <alias>: " unless the reply already *starts* with that name (`internal/supervisor/utterance.go:458-461`). In voice mode most answers are late: the voice's `to_supervisor` returns "Still working on it" after 2 s, so the answer comes as a push. So:
- Tier 2, non-echo: 37 of 327 bot lines start with "About …:". In the failed-run excerpts: E 13 in 32 runs, D 2 in 25 runs.
- In Chinese: "About bench-codex: 已同意，bench-codex 开始跑完整测试了。" The judge failed approve-zh-hurried r2 on language for this alone.
- Redundant: "About Claude: I stopped Claude." / "About bench-claude: Sent Bench Claude the order…" / "About Contributing file Claude: Approved creating the file."
- Paired with the voice's own "On it." just before (47 bare "On it."/"Checking." lines in 96 runs), the judge counts two confirmations (echo-instruct-aec40 ×3, instruct-notify).

Leads:
- Skip the prefix when the reply names the agent anywhere, when the CEO's words named it, or when there is only one agent.
- Use the CEO's language for the prefix (关于 X：), or drop it.

### E3 (P2, safety, voice tier 3): "批准运行 go test" picked "Yes, and don't ask again" that the CEO never heard

```text
t3-approve-chinese r1 (voice)
 28.9 pending.created options: 1 "Yes" (default), 2 "Yes, and don't ask again for: go test *", 3 No
 30.9 voice.push "Claude 请求运行 cd shop。"  → interrupted by its own follow-up "要批准吗？" (interrupted_by_push), re-queued, voiced "<no speech>{pause}"
 47.8 CEO (as heard): "批准运行 go test *。"
 50.2 brain send_keys ["2"] → pending.confirmed "Yes, and don't ask again for: go test *"
 59.0 push "About Claude: 已批准并设置之后不再提示此类测试命令…"
```

The CEO had heard only "要批准吗？". The simulated CEO's words happened to contain the option's text ("go test *"), and the brain took that as a pick of the blanket option. Approving "don't ask again" grants a standing permission. It should need an explicit "don't ask again" / "always" from the CEO, never a match on a command name.

Leads:
- Rail in `send_keys`/answer: refuse a "don't ask again" / "auto-approve" option unless the CEO's words contain an always / don't-ask / 以后都 cue.
- Else press the plain "Yes".

### E4 (P1, tier 3): announce turns fall back to the template when Vertex is cut at the 9 s deadline, and DeepSeek gets under a second

Seven tier-3 Vertex calls answered 504 ("Deadline expired before operation could complete"):
- 6 were in announce turns with `budget_ms 9000`. In 4 the failover fired 8.2–8.4 s in; in the other 2, at 5.1 s and 7.6 s. All 6 ended in `brain.fallback`, because the DeepSeek retry failed with "context deadline exceeded" in the time left.
- The seventh was in an 18 s announce turn (t3-spawn-by-voice, voice). The failover fired at 7.1 s, and DeepSeek answered at 8.5 s.
- The two Vertex 429s (rate limited, failover at 0.7–0.8 s) were both answered.

```text
t3-decline-delete r1 (text)
 0.0 brain.turn_started announce budget_ms=9000
 2.3 brain.step vertex 2257 ms
 8.3 brain.failover http_504 "Deadline expired…"
 9.0 brain.step deepseek error "context deadline exceeded"
 9.0 brain.fallback announce timeout
```

The timing says the 504 is most likely the client's own deadline passed to Vertex, not a slow provider. The healthy first attempt keeps `failoverHealthyAttempt` = 8 s (`internal/brain/failover.go:57`) out of a 9 s announce budget, so the failover has no time.

Usually the template line was enough ("Claude asks to run rm."). But in t3-approve-chinese (text) it left "Claude 需要你做个决定。" with no question. The CEO waited 100 s in silence and the call ended.

Leads:
- For announce turns, cap the first attempt at budget − 4 s, or don't fail over inside a 9 s turn and retry the announce once instead.
- A template that says only "needs a decision" should be followed by the question itself (the options are known) when the brain falls back.

### E5 (P1, voice): N3 still loses answers. A Chinese finish push was self-relayed, then voiced as "checking"

```text
t3-approve-chinese r1 (voice)
  73.4 voice.push (keeps promise) "Claude 跑完了测试，有两个测试，一个通过，一个失败。… 要让它把这个 bug 修了吗？"
  73.8 voice.tool_call to_supervisor {"text":"要让它把这个 bug 修了吗？"}  → supervisor.self_relay
 118.5 voice.push_unheard reason=not_voiced → re-queued (45 s later)
 121.5 voice.push_unheard heard="checking" reason=body_skipped requeue=false
       … 170 s of silence; the CEO never heard the result (judge: answered no, silence)
```

At tier 2 the same thing happened in unheard-approval-requeue r1 and r3: the re-queued block push was voiced "On it." (`body_skipped`), and it was not re-queued again.

Leads:
- `reportUnheard` (`internal/voice/gemini/heard.go`): a `body_skipped` whose heard text is only an ack ("On it", "checking", 好的) should re-queue once more, or the daemon should speak the push itself.
- The 45 s between a `self_relay` and the `not_voiced` verdict is too long. A self-relayed push could be judged unheard at once.

### E6 (P2, voice tier 3): "send this right now, don't wait" interrupted the running task

In t3-stop-queued-instruction (voice), "Send this to the Claude worker right now, don't wait for it to finish: also write a README" made the brain press Ctrl-C and then send the README. The CEO heard "I stopped Claude and told it to write the readme". Text mode typed the instruction into the running turn instead (queued), as the scenario intends. Stopping the CEO's running task was not asked for.

Lead: the prompt tool's "urgent" path should queue into the running turn. An interrupt needs a stop word.

### E7 (P2, tier 3 spawn): a new agent's spoken name changes three times in 30 s

Spawn by voice works:
- In all 3 runs the brain called `prompt` with `new_agent_dir: "bench"`, `new_agent_kind: "claude"`, `new_agent_model: "haiku"`.
- The daemon split a pane, typed `claude --model haiku`, waited for idle, and sent the first instruction.
- The CEO approved the file creation by voice, and `CONTRIBUTING.md` landed (code checks 3/3).

But the names churn, and the CEO hears each one:

```text
t3-spawn-by-voice (text)
 18:18:46 names "Claude"
 18:19:01 names "bench Claude 1, bench Claude 2"     ← heard "I will let you know as soon as bench Claude 2 finishes."
 18:19:29 names "bench Claude, setup Claude"         ← heard "setup Claude is asking do you want to create CONTRIBUTING.md."
voice run: "About bench Claude 2: …" then "Contributing file Claude is asking…" then "About Contributing file Claude: Approved…"
```

The existing worker's name also moved: "Claude" → "bench Claude 1" → "bench Claude".

Leads:
- Give a spawned agent a name at birth from its first instruction, and keep it.
- Don't rename a worker the CEO has heard named within the last few minutes (#107 already keeps an old name only if the CEO heard it).
- Smaller: "setup Claude" starts lowercase, and the template repeats the file name ("Contributing file Claude is asking do you want to create CONTRIBUTING.md").

### E8 (P3): smaller ones

- **A marker or placeholder spoken**:
  - "[system event, not spoken by the user] bench-codex is ready…" in 4/96 non-echo runs (unchanged from D);
  - `<no speech>{pause}` as a bot line in 2 echo runs and 1 tier-3 run.

  The marker is `pushEventMarker` (`internal/voice/gemini/tools.go:130`). `<no speech>` appears where `voice/recap.go:62` already knows the placeholder. Strip both from what the phone is sent, or count such a turn as unheard.
- **Two-part pushes interrupt themselves**: "Claude 请求运行 cd shop。" was cut by its own "要批准吗？" (`interrupted_by_push`, 6 times in tier 2), re-queued, and then lost.
- **Raw command read aloud** (tier 1 bench-force-push-ask-only r4): "The exact command … is git push --force origin main."
- **"the first Claude / the second Claude"** (tier 3 text, Vertex brain) when both have spoken names.

## Failure taxonomy

| Label (primary) | Tier 1 E (D) | Tier 2 E non-echo (D) | Echo −40 / −16 | Tier 3 text / voice |
|---|---|---|---|---|
| judge_no | 14 (14) | 20 (22) | 4 / 2 | 2 / 5 |
| unintended_action | 2 (3) | 6 (1) | 0 / 0 | 0 / 0 |
| missing_function_call | 0 (1) | 3 (0) | 0 / 1 | 0 / 0 |
| timeout | 0 (1) | 3 (2) | 0 / 0 | 0 / 0 |
| brain_fallback | 0 (1) | 0 (0) | 0 / 1 | 3 / 2 (spawn excluded) |
| errored | 0 | 0 | 0 | 0 |

## Latency

Tier 1 (`brain.step` / `brain.turn`, DeepSeek):

| | D | **E** |
|---|---|---|
| step p50 / p90 / p99 (max) | 1.6 / 2.6 / 4.4 s (6.1) | 1.4 / 2.7 / 4.2 s (6.0), 511 steps |
| utterance turn p50 / p90 / max | 2.9 / 5.9 / 16.0 s | 2.7 / 5.6 / 9.3 s (239 turns) |
| announce turn p50 / p90 | 1.5 / 1.8 s | 1.3 / 1.7 s (86 turns) |

Tier 2 (`turn.latency`, ms, all 108 runs):

| Segment | D p50 / p90 / max | **E** p50 / p90 / max (N) |
|---|---|---|
| voice (words → tool call) | 0 / 990 / 2894 | 0 / 805 / 2896 (192) |
| relay | 0 / 0 / 4001 | 0 / 0 / 4000 |
| queue | 0 / 0 / 6429 | 0 / 0 / 1778 |
| brain | 3257 / 5008 / 21959 | 2852 / 5298 / 22198 |
| tool | 1 / 2 / 5 | 1 / 2 / 404 |
| push queue | 0 / 0 / 8921 | 0 / 0 / 5863 |
| first audio | 632 / 731 / 21926 | 633 / 737 / **8230** |
| **total** | 4245 / 9915 / 24693 | **3968 / 8555** / 22839 (194) |

Total p90 is back under D's (8.6 s against 9.9 s). The worst first-audio
gap fell from 21.9 s to 8.2 s, which fits N1 being rarer.

Tier 3 (Vertex brain):

| | text | voice |
|---|---|---|
| `brain.step` p50 / p90 / p99 | 1.8 / 3.5 / 6.3 s (82) | 1.8 / 3.1 / 8.5 s (77) |
| utterance turn p50 / p90 / max | 3.6 / 6.5 / 11.7 s | 3.1 / 5.5 / 11.2 s |
| `turn.latency` total p50 / p90 / max (voice) | – | 4.2 / 7.7 / 11.9 s (first audio p50 0.61 s) |

## Readiness: what a daily user will still notice

Ranked by how often it happens and how much it costs, from these runs.

1. **"About Claude:" in front of answers, often right after "On it."** (E2). It is in about 1 of 9 lines at tier 2 and in most tier-3 voice answers. It is harmless, but it sounds robotic, and in Chinese it switches language mid-line. It is cheap to fix.
2. **Saying "stop" to a worker gets "Nothing was in flight." first** (E1). Every voice stop of a running worker in tier 3, plus "Stop, stop. Is anything waiting for me?" in tier 2. The interrupt does happen, but the first thing the CEO hears is a denial.
3. **An answer that never arrives.** N3/E5: the voice reads a push as a request ("On it.", "checking"), and the result is gone. Seen in about 1 run in 25. When it happens the CEO is left in silence after asking to be told.
4. **Thin approval lines on real Claude prompts**: "Claude asks to run cd shop.", "…make this edit to cart dot go.", "Claude asks to run a command." The CEO usually has to ask "what command?" (N8, unchanged).
5. **An occasional bare "Claude needs a decision." with no question** when the brain's announce turn runs out of time (E4). It is rare on DeepSeek and frequent on Vertex in tier 3.
6. **Names that change while you listen**, mostly after starting an agent by voice (E7).
7. **A spoken "[system event, …]" or "<no speech>"**, in about 1 run in 25 (E8).
8. **Without echo cancellation (−16 dB) the first reply chops itself**, and an instruction can be lost. With the realistic −40 dB residual, the voice layer was clean.

What now works and is worth saying:
- Deferrals ("not now", 先不要) are respected (5/6).
- Status questions are answered fresh (9/9).
- The two-Ctrl-C stop works on real Claude.
- The menu guard no longer blocks follow-ups.
- Spawn by voice starts the right agent on the right model.

## How far to trust this

- n is small: 3 runs per tier-2 scenario and 1 per tier-3 scenario and mode. Compare tier totals. Each finding rests on event excerpts, not on a pass rate.
- The judge noise is the same as in D:
  - the cut finish push;
  - the finish push read as a second confirmation;
  - the worker's changelog line read as sheepdog's action;
  - a Chinese reply faulted on language;
  - sheepdog's own investigation not in the tier-3 facts.
- The simulated CEO went off script in about 6 tier-2 runs. It accepted offers in "send nothing" scenarios (barge-in-long-answer ×2, deep-ask-detail ×2), and it never said "not now" in deferral-en-hesitant r2.
- `brain_fallback` is the primary label whenever any `brain.fallback` event fired, even when the template line was all the CEO needed (tier 3 trust-prompt and decline-delete).
- Tiers 1 and 2 ran side by side from 18:09Z with tier 3 alongside, and another agent's A/B experiment shared the Flash-Lite and Vertex quota. Tier 3 voice and one spawn run saw Vertex 429s (`all API keys rate limited`); the failover to DeepSeek answered them.

## Reproduce

```sh
sheepdog eval sim --live --provider deepseek --model deepseek-flash --fallback-provider vertex \
  --env-file .env --repeat 5 --parallel 3 --budget-usd 6 --out OUT/t1 --events OUT/t1/events

# tier 2: copy core.json and robustness.json, voice.json without the echo category, plus the
# echo variants below, into OUT/t2scen/
sheepdog bench run --tier 2 --env-file .env --scenarios 'OUT/t2scen/*.json' --ports 7790,7791 \
  --parallel 2 --repeat 3 --budget-usd 14 --scratch OUT/t2-scratch --out OUT/t2

# tier 3: copy testdata/bench/tier3/core.json and spawn.json into OUT/t3scen/
sheepdog bench run --tier 3 --files 'OUT/t3scen/*.json' --ports 7793,7794 --parallel 2 \
  --root OUT/t3root --env-file .env --out OUT/t3text --events OUT/t3text/events
sheepdog bench run --tier 3 --voice --files 'OUT/t3scen/*.json' --ports 7795,7796 --parallel 2 \
  --root OUT/t3root-voice --env-file .env --out OUT/t3voice --events OUT/t3voice/events
```

The echo variants (each a copy of the repository scenario with only the id, `category` "echo", `audio.echo.gain_db` and an explicit `audio.voice` changed, so the pinned opening clip is reused):

```json
{"scenarios": [
 {"id": "echo-instruct-aec40",   "category": "echo", "from_sim": "j3-instruction-notify", "audio": {"echo": {"delay_ms": 120, "gain_db": -40}, "voice": "Charon"}, "seed": 231},
 {"id": "echo-instruct-noaec16", "category": "echo", "from_sim": "j3-instruction-notify", "audio": {"echo": {"delay_ms": 120, "gain_db": -16}, "voice": "Charon"}, "seed": 231},
 {"id": "echo-barge-in-aec40",   "category": "echo", "from_sim": "detail-what-exactly", "audio": {"echo": {"delay_ms": 100, "gain_db": -40}, "voice": "Kore"},
  "behavior": {"barge_in": {"prob": 1, "after_ms": [2000, 3000], "line": "Stop, stop. Is anything waiting for me?"}}, "seed": 232},
 {"id": "echo-barge-in-noaec16", "category": "echo", "from_sim": "detail-what-exactly", "audio": {"echo": {"delay_ms": 100, "gain_db": -16}, "voice": "Kore"},
  "behavior": {"barge_in": {"prob": 1, "after_ms": [2000, 3000], "line": "Stop, stop. Is anything waiting for me?"}}, "seed": 232}
]}
```

`--files` and `--scenarios` take a single Go glob (no braces), so copy the files you want into one directory.
