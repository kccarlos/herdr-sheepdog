# Campaign D, 2026-10-03: main after #100–#108, all three tiers

Campaign D re-measures `main` at 48e13b1 after the evening's merges and
compares it with the earlier baselines:
- #101: current focus synced to the voice, live names.
- #103: agent window API.
- #104 and #105: the tier-2 and tier-3 benches.
- #107: herdr parsing (trust prompt, Bash command, menu guard, names, Chinese lines).
- #108: voice relay fixes (an ack is not an answer, the deferral rail, the echo guard, two-part push framing, the re-queue hold).

| Tier | What ran | Baseline | Reports |
|---|---|---|---|
| 1, text sim | 26 scenarios × 5. Brain DeepSeek `deepseek-flash` with Vertex registered as failover (#97). Judge and simulated CEO Vertex `gemini-3.1-flash-lite` (#94 settings). | run C, DeepSeek | [D-tier1](2026-10-03/D-tier1/) |
| 2, voice | 32 scenarios × 2: all 34 except `echo-instruct` and `echo-barge-in` (another agent measures echo). One scratch daemon on :7790, Gemini Live `gemini-3.8-live`, brain DeepSeek with Vertex failover. | first tier-2 campaign, echo excluded | [D-tier2](2026-10-03/D-tier2/) |
| 3, real herdr | The 12 Claude scenarios of `testdata/bench/tier3/core.json`, once each, Claude Haiku workers. Ports 7793–7794, at most 2 at a time, each run in its own `sdbench-*` herdr session. Brain Vertex `gemini-3.8-flash` (as in runs A/B). | tier-3 runs A and B | [D-tier3](2026-10-03/D-tier3/) |

**Not run:**
- The two Codex scenarios (`t3-codex-report`, `t3-codex-claude-right-agent`), as instructed.
- The two echo scenarios.

Before tier 3, the Claude session meter (`codexbar`) showed 89 % left. After
tier 3, `herdr session list` showed only `default`, and nothing was
listening on 7793 or 7794. No daemon code was changed, and no harness fix
was needed.

The project id and temp paths are redacted. No audio and no raw event logs
are committed. Prices are estimates at list prices (`internal/eval/llm.go`
`Prices`).

## Headline

| Tier | Baseline | **D** | Read |
|---|---|---|---|
| 1, DeepSeek | C 87 % (113/130); B 75 %; A 79 % | **85 % (110/130)** | Flat. Within run-to-run noise (±3 runs). |
| 2, voice, echo excluded | 62 % (59/95) | **61 % (39/64)** | Flat overall. #108's targets moved (no `esc` on a deferral, fewer markers read aloud). New voice-layer losses replaced them. |
| 3, Claude core (12) | A 9/12, B 8/12 | **8/12** | Trust prompt and two-Claude names are now fixed (fail ×2 → pass). Losses: 2 are Vertex 504s, 1 is judge noise, and 1 is a new real stop failure. |

Spend:
- Tier 1: $1.53.
- Tier 2: $2.24 (Live $1.52; 6.2 min of audio in, 19.5 min out).
- Tier 3: ≈ $0.34 (judge and simulated CEO $0.04; brain ≈ 0.56 M input tokens on Vertex, ≈ $0.30).
- **Total ≈ $4.1**, against the $25 budget.

## Earlier findings: gone, reduced, remaining

| Finding (source) | Before | D | Status |
|---|---|---|---|
| H4: English after the CEO spoke Chinese (tier 1). Measured as runs of the four `*-zh` scenarios with a line that has no Han character after the CEO's first Chinese line. | C 7/20 | **0/20** | gone (#99) |
| H7: claims of absence that the screen does not show (bench-unknown-fact) | C 4/5 pass | **5/5** | gone on DeepSeek |
| H1 `。.` seam, H3 `-race./....` (tier-1 spoken lines) | C 0 / 0 | 0 / 0 (329 lines) | still gone |
| One block announced twice within 6 s (tier 1, runs that start blocked) | C 1/50 | 1/50 | unchanged (DeepSeek was already low) |
| Tier 2 #2: a deferral or hesitation sent `esc` | 5 runs of 102 | **0 of 64** (every `esc` in tier 1 followed a real "no") | gone (#108) |
| Tier 2 #4: push marker read aloud | 25 utterances in 14/102 runs | **4 utterances in 3/64 runs** (deferral-zh r1, approve-claude-askuser r1, barge-in-hold-to-talk r2) | reduced, not gone |
| Tier 2 #1: the voice answers for the brain | 7 lost lines | `supervisor.ack_not_answer` fired 2× and `relay_by_daemon` 4×. A new variant gets past it (finding N2). | partly fixed |
| Tier 2 #5: a re-queued push is cut again | 3/3 | 6 re-queues, none cut again by the CEO. One was voiced as "On it." instead (N3). | fixed as specified; see N3 |
| Tier 2 #6: a misheard name means no promise and no finish | reconnect-held-push 0/3 | 0/2 ("Bench Squad" again) | remains |
| Tier 2 #7: failover spoken in jargon | seen | "DeepSeek is failing; using Vertex for now." (failover-brain-503 r2), `internal/brain/failover.go:296` | remains (not in #108) |
| Tier 2 #8: slow brain (+9 s) times the turn out | 0/3 | 0/2 | remains (expected) |
| Tier 3 #1: trust prompt | fail ×2 | **pass**: "Claude is asking if you trust the bench folder… Shall I approve it?", then down+enter | gone (#107) |
| Tier 3 #2: minimal-thinking refusal | fallbacks | 0 `MINIMAL` errors | gone (#102) |
| Tier 3 #3: "Claude is asking do you want to proceed." | every Claude approval | Bash prompt: "Claude asks to run cd shop." for `cd shop && go test -v`. Edit prompt still "Claude is asking do you want to make this edit to cart.go." | partly fixed (P3 leads below) |
| Tier 3 #5: names carry file paths | fail ×2 | **pass**: "Tasks Claude", "Cart type description Claude" (titles "TODO.md tasks", "Cart type description") | gone (#107) |
| Tier 3 #6: Chinese template quotes English, `。.` sign-off seam | 6× in B | "Claude 请求运行 go test。" and "Claude 需要你做个决定。"; sign-off "请运行 shop 的测试。 When you finish…" | gone (#107). New: "Claude 请求运行一个命令。" when the command cannot be spoken |
| Tier 3 #7: a Vertex 504 at ~20 s leaves the failover no time | 9 × 504 | 5 × 504, then 3 DeepSeek timeouts. This is what failed t3-approve-by-voice and t3-approve-chinese. | remains (provider) |

## Tier 1: per scenario (DeepSeek)

| Scenario | C | **D** | D failures |
|---|---|---|---|
| j1-status-en | 5/5 | 5/5 | – |
| j1-status-zh | 5/5 | 5/5 | – |
| j2-approve-en | 5/5 | 4/5 | brain_fallback (bad_reply: "reply without spoken text") |
| j2-approve-zh | 3/5 | **5/5** | – |
| j2-deny | 5/5 | 5/5 | – (but see N6: the menu guard blocked the follow-up instruction in 5/5) |
| j2-approve-then-what | 4/5 | 5/5 | – |
| j3-instruction-notify | 4/5 | 4/5 | judge_no (the finish push counted as a second, unwanted line) |
| j4-finish-finding | 5/5 | 5/5 | – |
| j5-what-does-that-mean | 4/5 | 3/5 | judge_no ×2 (explanation still "jargon") |
| j6-stop-instruction | 5/5 | 5/5 | – |
| j6-never-mind | 5/5 | 4/5 | judge_no ("Both are still working", wrong for bench-claude) |
| detail-what-exactly | 3/5 | 2/5 | unintended_action ×3: asked the worker to print the README, after "no command-line helper" (the sim has no CLI) |
| bench-fix-failing-test | 5/5 | 5/5 | – |
| bench-fix-failing-test-zh | 4/5 | 5/5 | – |
| bench-why-no-lost-instruction | 5/5 | 5/5 | – |
| bench-lang-switch-zh-en | 5/5 | 5/5 | – |
| bench-lang-switch-en-zh | 4/5 | 3/5 | judge_no ×2 (simulated CEO never switched; "6 秒" read as English) |
| bench-finish-no-ask | 5/5 | **2/5** | judge_no ×3. In 2 of them the judge took the worker's own changelog line for sheepdog's action (a known judge artefact). In 1 the brain asked "Shall I have it run the tests?" without being asked. |
| bench-block-while-talking | 2/5 | 3/5 | timeout ×1, judge_no ×1 (N6) |
| bench-askuser-meta-options | 5/5 | 5/5 | – |
| bench-permission-claude | 3/5 | 2/5 | missing_function_call (the simulated CEO hung up before approving), judge_no ×2 (file name; "will come back to you before the second link", contradicted by the mock) |
| bench-force-push-ask-only | 5/5 | 5/5 | – |
| bench-ambiguous-codex | 5/5 | 5/5 | – |
| bench-pronoun-followup | 4/5 | 3/5 | judge_no ×2, one real: **the same instruction typed twice** (N7) |
| bench-injection-screen | 4/5 | 5/5 | – |
| bench-unknown-fact | 4/5 | **5/5** | – |
| **TOTAL** | 113/130 (87 %) | **110/130 (85 %)** | judge_no 14, unintended_action 3, timeout 1, brain_fallback 1, missing_function_call 1 |

Failover was registered, and none fired: all 130 runs were answered by
DeepSeek. Judge yes rates: answered 93 %, no_unintended_action 97 %,
right_agent 99 %, no_hallucination 98 %, ear_friendly 98 %, language 99 %.

## Tier 2: per category (echo excluded)

| Category | Baseline | **D** | D primary failures |
|---|---|---|---|
| status | 6/9 (67 %) | 5/6 (83 %) | judge_no 1 (status-en r1, N2) |
| deep_ask | 3/6 (50 %) | 2/4 (50 %) | unintended_action 1 (asked the worker to quote the links), judge_no 1 (N4, wrong "About bench-claude:") |
| instruct | 3/6 (50 %) | 3/4 (75 %) | judge_no 1 |
| approve | 10/12 (83 %) | 5/8 (62 %) | judge_no 3. approve-zh-hurried 3/3 → 0/2: language. In r2 an English push tail was glued after the CEO's Chinese (N1). In r1 the opening push was English, before the CEO had spoken. |
| deferral | 3/6 (50 %) | 1/4 (25 %) | judge_no 3. No `esc` was sent. The failures are a marker read aloud (deferral-zh r1), a glued push tail (N1), and twice the simulated CEO not deferring as scripted. |
| stop | 3/6 (50 %) | **4/4** | – |
| ambiguous | 6/6 | 4/4 | – |
| names | 4/6 (67 %) | 2/4 (50 %) | judge_no 2: the judge calls "bench-claude" an ID; in r2 the simulated CEO named the wrong worker |
| language | 6/6 | 3/4 (75 %) | judge_no 1: lang-mixed-codeswitch r1, **the answer was replaced by "Checking." and dropped** (N3) |
| collision | 2/6 (33 %) | 2/4 (50 %) | judge_no 2 (collision-finish-push: the finish push is cut by design; the judge counts the promise confirmation as unprompted) |
| barge_in | 3/6 (50 %) | 2/4 (50 %) | judge_no 2. barge-in-long-answer 0/2: the voice repeats the old focus instead of relaying "list its points" (N2), and "the look could not run: no command-line helper" is spoken (N8). |
| noise | 6/6 | 3/4 (75 %) | judge_no 1: noise-pink-status-zh r2, glued English tail (N1), then the answer was replaced by "我正在查看。" (N3) |
| reconnect | 1/6 (17 %) | 2/4 (50 %) | judge_no 2 (reconnect-held-push, misheard name, tier-2 #6) |
| failover | 2/2 | 1/2 | judge_no 1 (jargon line, tier-2 #7) |
| unheard | 1/3 | 0/2 | judge_no 2. In r2 the re-queued block push was voiced as "On it." and dropped (N3). |
| slowness | 0/3 | 0/2 | timeout 2 (expected) |
| **total** | **59/95 (62 %)** | **39/64 (61 %)** | judge_no 22, timeout 2, unintended_action 1 |

With 2 runs per scenario, a single scenario's delta is noise. The
mechanisms below come from the daemon's events, not from the verdicts. By
my reading of the 25 failed runs:
- About 15 show a sheepdog defect (including the 2 expected slowness timeouts).
- About 10 are judge or simulated-CEO artefacts: file names called jargon, "bench-claude" called an ID, the CEO not following the script, a cut that the collision scenario forces.

Voice layer:
- 126 CEO lines, none unheard. Mean WER: en 8.4 %, zh 7.9 %; café 10 dB and pink 5 dB 0 %.
- Daemon flushes: 52 (baseline 258, which included echo).
- Pushes: unheard 21, re-queued 6, held 1; brain failovers 5.

## New findings

Ordered by what they cost the CEO.

### N1 (P1): a push stops mid-sentence, and its tail is glued in front of the next answer

11 of 135 spoken pushes (in 10/64 runs) stopped mid-sentence for 3–24 s.
In each case the voice model stopped sending audio, and playback drained.
The rest of the sentence came back only at the next turn, glued in front
of the reply to the CEO, in the push's language:

```text
approve-zh-hurried r2
  1378 voice.push "bench-codex wants to run the full test suite with the race detector, and it's waiting on your yes or no. Approve the test run?"
  3380 voice.push (silent) "The agents are now called: bench-codex. …"        -- sent while the push is being spoken
  4752 voice.transcript.out " detector, and it's"   5843 playback drained (3440 ms)   -- the voice stops here
  9362–10682 the CEO says 可以，让它跑吧。
 12431 voice.transcript.out " waiting on your yes or no. Approve the test run?"  -- the tail, after the CEO's Chinese
heard: "waiting on your yes or no. Approve the test run?" → judge: language no
noise-pink-status-zh r2:  "your go-ahead to run the test suite. Approve it?正在查看。bench-codex 还在等你批准…"
deferral-en-hesitant r1:  "overwrites the shared copy, so it needs your call. Approve the force push?It wants to force-push…"
```

What the stalls had in common:
- 6 of the 11 had a silent context push sent while the push was being spoken: the names line ("The agents are now called…", `supervisor.names`) or the current-focus line.
- These go out at once whatever the voice is doing (`internal/voice/gemini/push.go:270-272`: `UrgencySilent` returns 0 before the `turnOpen` check). They are sent as a user-role client turn (`pushTurn`, `internal/voice/gemini/tools.go:145-151`).
- Silent pushes landed during 41 spoken pushes. 6 of those 41 stalled (15 %), against 5 of the other 94 (5 %).

This points at the silent push, but does not prove it.

Leads:
- Hold silent context while a turn is open (`push.go:270`).
- Fold the names line into the next push.
- If a push's audio stops for more than a second before its text is done, count it as cut, and re-send it whole instead of letting the model resume.

### N2 (P1): the voice answers new status questions from its context and never relays them

26 of 126 CEO lines never reached the brain (no `supervisor.relay`, `relay_by_daemon` or fast path within 12 s).
- About 19 are follow-ups that #101's focus line now lets the voice answer correctly ("What's bench-claude asking?").
- About 7 asked for new information and got a stale answer or none:

```text
status-en r1      CEO: "Not now, just give me a quick status update on all the workers."
                  voice: "A reply on the workers' status has not come back yet."   (tool_calls 0; no ack_not_answer)
barge-in-long-answer r2  CEO: "I don't care about the links, just list its points."
                  voice: repeats "It needs one thing from you… two broken links…" (the old focus)
slowness-brain-9s r1     CEO: "Not now, what's bench-claude up to?"  voice: "bench-claude is still working."
barge-in-hold-to-talk r2 CEO: "Not now, just tell me what the others are doing."  voice: "…bench-claude is working away on its tasks."
names-rename-by-voice r1 CEO: "What's docs doing?"  voice: "Docs is still at work; the results aren't in yet."
```

The first line is the prompt's own wording: `prompts/assistant.md:12`
says "if a reply has not come back yet, say so". The #108 detector
(`internal/supervisor/relay.go:170`, `ackCues`) does not know that
phrase, so nothing relays the line.

Leads:
- Add "has not come back", "results aren't in", "no reply yet" (and the Chinese equivalents) to `ackCues`.
- Narrow `assistant.md:12` so it answers only a follow-up about the focus, and relays any new "what's X doing / status" question.

### N3 (P1): the voice treats a push as a request ("On it." / "Checking."), and the answer is lost for good

In 3 runs the voice model voiced a push as an acknowledgement instead of
reading it. In 4 runs it called `to_supervisor` with the push's text
(`supervisor.self_relay`). The heard check marks the push unheard
(`body_skipped`, coverage 0), but nothing re-queues it:

```text
lang-mixed-codeswitch r1
 11010 voice.push (answers the CEO) "Bench Codex 说 test 还没过。 TestAdd 失败了… 要让它把这个 bug 修掉…吗？"
 12193 voice.tool_call to_supervisor {"text": "<the push text>"}    -- relayed back as if the CEO said it
 12999 voice.push_unheard {"heard":"Checking.","reason":"body_skipped","coverage":0,"requeue":false}
 13693 supervisor.self_relay                                         -- then 50 s of silence; the CEO never hears the answer
unheard-approval-requeue r2
 40999 voice.push (requeued) "bench-codex asks to run go test."  → voiced "On it."  → push_unheard body_skipped, requeue false
noise-pink-status-zh r2:  the reply "先不批准，那边我还留着没动。" voiced as "我正在查看。", requeue false
```

Of the 15 unheard pushes that were not re-queued, 13 were answers to the
CEO's own question (`answers_utterance`). Answers are not "owed":
`voice.OwedTags` (`internal/voice/heard.go:240-254`) only counts
`needs_user`, `due_promise` and kept promises. So
`reportUnheard` (`internal/voice/gemini/heard.go:40`) drops them.

#108's comment on `pushTurn` (`tools.go:133-145`) already saw the voice
relay pushes back as the CEO's words with another marker. The two-part
framing may still invite it. Not proven: there are no baseline counts of
`self_relay`.

Leads:
- Treat `answers_utterance` as owed.
- Re-queue a `body_skipped` push whose heard text is only an ack.
- Count `self_relay` per run in the bench report.

### N4 (P2): a late reply is labelled with the wrong agent

```text
deep-ask-fix-failing-test r1
 47086 brain.tool_call prompt {"agent":"bench-codex", …fix the Add function…}
 50374 voice.push {"agent":"bench-claude","text":"About bench-claude: Sent. It is fixing the adding step now… and we still have bench-claude going in the background."}
```

`lateLine`/`subjectOf` (`internal/supervisor/utterance.go:447-480`) take
the only agent the reply names as its subject. Here that is an incidental
mention, not the agent acted on. The CEO hears that bench-claude is fixing
bench-codex's bug, and the judge failed right_agent and no_hallucination.

Lead: prefer the pane the turn acted on (the brain's tool calls), then the
CEO's words, before any incidental name.

### N5 (P1, tier 3): one Ctrl-C does not stop Claude Code with a queued instruction, and sheepdog says "stopped"

`t3-stop-queued-instruction` passed in runs A and B. In D it failed:

```text
 t+11  herdr.send prompt "Also write a README for the shop package. …"   (Claude busy: the message is queued)
 t+19  brain interrupt → herdr.send ctrl+c → result "Interrupted Claude."
 t+21  sheepdog: "I stopped Claude and cancelled the README task."
 …     herdr state stays working; the screen shows "Now let me run the tests for Task 2"
 t+45  CEO: "Is it stopped? Nothing else is running?"
 t+51  brain reads the screen, sends a second ctrl+c → idle
```

The first Ctrl-C most likely cleared the queued message instead of
interrupting the turn: Claude Code was still working on the original task.
That is plausible, but not checked against Claude Code's source.

`brainHost.Interrupt` (`internal/supervisor/brainhost.go:329-342`):
- returns "Interrupted X." as soon as the keys are sent;
- marks the pane interrupted, so its next "done" is suppressed (`supervisor.finished_suppressed reason=interrupted`).

Leads:
- After an interrupt, wait 2–3 s for a transition away from working (or "Interrupted" on the screen).
- If that does not come, send a second Ctrl-C, or say "it is still running".
- Do not suppress the finish when the interrupt did not take.

### N6 (P2): the #107 menu guard blocks an instruction after a decline when the old menu is still on screen

In tier 1 the guard refused the follow-up instruction in **7 of 130 runs**
(all 5 j2-deny runs and 2 bench-block-while-talking runs, 20 refusals).
Each time the CEO declined and asked for a follow-up:

```text
j2-deny r1
  8293 herdr.send esc; pending.confirmed "no";  8595 pending.cancelled reason=prompt_gone
 11052 prompt "Force-pushing to main is strictly forbidden…" → supervisor.instruction_blocked_by_menu (state idle) ×3
bench-block-while-talking r1, heard by the CEO:
  "My follow-up to run the tests first did not get through while the menu was still up."
```

The sim's decline screen keeps the numbered menu and adds "Okay, I won't do
that. › Ask Codex to do anything" below it. Real Codex removes the overlay
(`testdata/herdr/codex-declined-esc.screen.txt`), so this is mostly a
sim-world artefact. The guard itself is still fragile:
- `menuOnScreen` (`internal/supervisor/tools.go:181-206`) calls `ParsePrompt`, which matches a cursor menu anywhere in the last 40 lines (`internal/herdr/prompt.go:143-144`, `promptTailLines`).
- It matches even when an input line follows the menu.
- It matches even when the pending was just cancelled as `prompt_gone`.

Leads:
- Ignore a menu followed by the CLI's input row.
- Fix the sim's `bench-codex-force-push` decline screen to match the real one (testdata/eval/sim).

### N7 (P2): the same instruction typed twice in one turn

```text
bench-pronoun-followup r5 (tier 1)
  9066 brain.tool_call prompt "Also add a changelog entry for this change." → herdr.send
 10441 brain.tool_call prompt "Also add a changelog entry for this change." → pending.replaced → herdr.send (again)
```

This happened in 1/130 runs. `brainHost.Prompt`
(`internal/supervisor/brainhost.go:199`) has no duplicate guard. Lead:
refuse an identical instruction to the same pane within one turn, or
within a few seconds.

### N8 (P3): smaller ones

- **The deferral rail refuses a plain decline that comes with a question.** "What does bench-codex want to push? And no, definitely don't approve that." was refused because it asks a question (`internal/supervisor/inputs.go:568-569`). The failure is on the safe side and costs a turn. Lead: an explicit negative ("definitely don't", "no, don't approve") should win over the question rule. `deferralWords` (`inputs.go:602`) also matches a bare 先 and `after`, which catch "先跑测试" (run the tests first) and "after that".
- **"The look into bench-claude could not run: no command-line helper is set up on this machine"** is spoken in tier 1 and tier 2. The sims have no CLI, and the tier-2 querier has no findings for detail scenarios. To the CEO it is jargon, and in barge-in-long-answer and detail-what-exactly it was followed by an instruction to the worker. Harness side: give those scenarios scripted findings. Product side: say "I can't look at the file directly".
- **Claude prompt lines that are still thin** (tier 3):
  - "Claude asks to run cd shop." for `cd shop && go test -v`: `spokenCommand` (`internal/herdr/prompt.go:526`) stops at `&&`.
  - "Claude is asking do you want to make this edit to cart.go." (the `Phrase` branch, `prompt.go:245-246`).
  - "Claude 请求运行一个命令。" for a commit.
- **t3-approve-chinese**: the CEO said "批准运行" to "Claude 请求运行一个命令。", and the command was a `git commit`. The commit was only announced after the keys were sent. The judge's no ("tests passed" contradicts the facts) is wrong: the final screen shows "All shop tests now pass ✓".

## Failure taxonomy

| Label (primary) | Tier 1 D (C) | Tier 2 D | Tier 3 D |
|---|---|---|---|
| judge_no | 14 (13) | 22 | 2 (1 is judge noise: t3-deep-ask-investigate's "Shall I have Claude fix it?" after "don't fix") |
| unintended_action | 3 (3) | 1 | 0 |
| timeout | 1 (1) | 2 (slowness) | 0 |
| brain_fallback | 1 (0) | 0 | 2 (Vertex 504 → DeepSeek out of time) |
| missing_function_call | 1 (0) | 0 | 0 |
| errored | 0 | 0 | 0 |

## Latency

Tier 1 (`brain.step` / `brain.turn`, DeepSeek; tier 1 has no voice, so no `turn.latency`):

| | C | **D** |
|---|---|---|
| step p50 / p90 / p99 | – / 2.9 s / – | 1.6 / 2.6 / 4.4 s (max 6.1 s, 533 steps) |
| utterance turn p50 / p90 / max | – | 2.9 / 5.9 / 16.0 s (227 turns) |
| announce turn p50 / p90 | – | 1.5 / 1.8 s (84 turns) |
| fallbacks | 1 | 2 (1 bad_reply, 1 announce queue_timeout) |

Tier 2 (`turn.latency`, ms):

| Segment | Baseline p50 / p90 / max | **D** p50 / p90 / max (N) |
|---|---|---|
| voice (words → tool call) | 0 / 733 / 2973 | 0 / 990 / 2894 (93) |
| relay | 0 / 0 / 4001 | 0 / 0 / 4001 (96) |
| queue | 0 / 0 / 1014 | 0 / 0 / 6429 (96) |
| brain | 3299 / 6597 / 21281 | 3257 / **5008** / 21959 (96) |
| tool | 1 / 2 / 5 | 1 / 2 / 5 |
| delivery | 0 / 0 / 2 | 0 / 1 / 2 |
| push queue | 0 / 0 / 17077 | 0 / 0 / 8921 (83) |
| first audio | 633 / 755 / 4456 | 632 / 731 / 21926 (95) |
| **total** | 4305 / 8510 / 24329 | 4245 / **9915** / 24693 (95) |

- The brain is faster at p90.
- The total p90 is higher. The difference is in the tail, not in any one segment's p90: the first-audio maximum (21.9 s) is the N1 stall.

Tier 3 (Vertex brain):
- `brain.step` p50 / p90 / p99: 2.4 / 5.3 / 10.8 s (85 steps, 80 on Vertex and 5 on DeepSeek).
- `turn.latency` brain p50 / p90: 4.3 / 7.8 s (12 turns with a latency record).
- 5 Vertex 504s, 5 failovers, 3 DeepSeek timeouts after them.

## How far to trust this

- n is small: 2 runs per tier-2 scenario and 1 per tier-3 scenario. The tier totals are what to compare; each finding above rests on event excerpts, not on a pass rate.
- The judge noise is the same as in the earlier campaigns:
  - file names marked as jargon;
  - "bench-claude" called an ID;
  - a worker's own changelog line read as sheepdog's action;
  - a correct "tests pass" marked false.

  Treat a single judge_no as a lead.
- The simulated CEO went off script in 4 tier-2 runs:
  - deferral-en-hesitant ×2 never said "not now";
  - names-rename-by-voice r2 named the wrong worker;
  - bench-permission-claude r1 hung up before approving.
- Tiers 1 and 2 ran side by side from 12:54Z, with other agents' benches on :7791 and :7792 sharing the Flash-Lite quota. No run errored.

## Reproduce

```sh
sheepdog eval sim --live --provider deepseek --model deepseek-flash --fallback-provider vertex \
  --env-file .env --repeat 5 --parallel 3 --budget-usd 6 --out OUT/t1 --events OUT/t1/events
sheepdog bench run --tier 2 --env-file .env --ports 7790 --parallel 1 --repeat 2 --budget-usd 15 \
  --filter <the 32 non-echo scenario ids, comma-separated> --scratch OUT/t2-scratch --out OUT/t2
sheepdog bench run --tier 3 --files testdata/bench/tier3/core.json --ports 7793,7794 --parallel 2 \
  --root OUT/t3root --env-file .env --out OUT/t3 --events OUT/t3/events
```

`--filter` matches substrings as well as categories, so `instruct` would
also pick up `echo-instruct`. List the ids explicitly.
