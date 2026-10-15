# WP7b end-to-end run — 2026-09-17

`2026-09-17-e2e.jsonl` is the daemon's event log for the whole run
(`sheepdog serve --voice=gemini --herdr=socket --dev`, one JSONL file since
every timestamp falls on the UTC day 2026-09-18; local time was the
evening of 2026-09-17, Pacific). Redactions: the hostname, the user's home
path, the worktree path and the agy account e-mail are replaced by
`<hostname>`, `<home>`, `<repo>`, `<email>`. No key material was ever
logged. Scratchpad paths under `/private/tmp/claude-501/…` are left as
recorded, like the WP0 fixtures in `testdata/herdr/`.

Real workers, all started with approvals on: `agy` 1.2.5 (Antigravity CLI,
default mode — alias `auth-one`, and a second one `docs-two` for the
ambiguity scenario), Codex 0.154 (`--sandbox read-only -a on-request`,
alias `billing-cx`; a first attempt with `--sandbox workspace-write` never
asked for approval), Claude Code 2.1.275 (`--permission-mode default
--model haiku`, alias `search-cc`). The voice client was a Go WebSocket
client (no microphone): `testdata/audio/question.wav` streamed as 20 ms
frames plus 4 s of trailing silence for the audio trials, `{"type":"text"}`
utterances otherwise. The full narrative, latencies and bugs are in
`docs/dev/history/e2e-results.md`.

Four daemon runs (line ranges are 1-based line numbers in the JSONL):

| Lines | Run | Binary |
|---|---|---|
| 1–5 | smoke start/stop while workers were set up | 7db2ee6 |
| 6–334 | **Phase A**: scenarios 1, 4, 2/3 (agy), 5, 6 (first attempt), 7, 9 | 7db2ee6 |
| 335–834 | **Phase D**: scenario 6, 2/3 with Codex, 3 with Claude, 8, chatter, server-side disconnect (scenario 10 material) | 7db2ee6 |
| 835–982 | **Phase E**: re-verification after the fixes (stale row, cancel, push prompt, agy "no") | c7e12b2 (= 5a68474 after a subject reword) |

## Scenario ↔ line index

| # | Scenario | Lines | What to look for |
|---|---|---|---|
| 1 | Status (audio ×3) | 14–50 | `voice.transcript.in` "What is going on with the agents?" → `voice.tool_call briefing` → `voice.tool_result` → `voice.transcript.out`; **no `llm.call`** in 13–78 |
| 1 | Status (typed ×2) | 54–78 | `web.utterance` "What's going on?" → `briefing` |
| 4 | Instruction with echo | 79–103 | `propose` → `llm.call` resolve_instruction → `pending.created send_instruction` → `herdr.send kind=prompt` → echo transcript; 99/103 show the herdr misclassification of agy's edit prompt as `done` |
| 2 | Proactive block, agy (hook-shaped event) | 116–134 | `herdr.state source=http blocked` → 3 s debounce → `herdr.transition` → `herdr.read` → `llm.call summarize_block` → `pending.created` → `voice.push` → spoken; 134 = dropped by the 30 s snapshot tick (herdr says `done`) |
| 2/3 | Proactive block + "Yes." (agy, retry timed after the snapshot tick) | 159–191 | 161 transition, 164/165 pending + push, 171 "Yes." → 177 `herdr.send keys=["1"]` → 178 `pending.confirmed` → 191 finished |
| 5 | Cancel in time | 203–232 | 209/210 send, 217 "Stop." → 224 `herdr.send keys=["ctrl+c"]` → 225 `pending.cancelled reason=user`; 232 the spurious "finished" push (fixed in 5a68474, see 905) |
| 6 | Ambiguity, first attempt (FAIL) | 251–269 | the voice model passed `agent:"auth-one"` for "tell it to continue" and it was sent |
| 7 | Deep ask | 290–312 | "Checking" transcript first, `voice.tool_call ask`, 304 `cli.run backend=codex 24.1 s`, `llm.call spoken_answer`, ≤ 2 sentences |
| 9 | Reconnect | 322–331 | client disconnect → connect (`client_id` 2) → "What did I just ask you?" answered from context |
| 6 | Ambiguity, retries | 345–367 | 345 "Tell it to continue." → `confirm(answer:"continue")`, nothing sent; 357 "Tell it to run the tests again." → 362 `supervisor.propose_ambiguous` → clarify spoken, nothing sent (PASS). Note the stale `docs-two` in the clarify list (fixed in e4ecc2c, see 840) |
| 2/3 | Codex | 465–524 | 465 transition (hook first, socket deduped), 468/469 pending + push (push **not** spoken: stuck behind a silent turn, fixed in 4d324ef), 486 briefing mentions the question, 509 "Yes, go ahead." → answered only after the zero-silence stream was stopped (34.7 s), 523 `keys=["1"]` |
| 3 | Claude | 551–621 | 551 transition, 554 pending, 577 push released by the next tool call, 598 "Yes." → 607 `keys=["1"]` → 609 confirmed |
| 8 | Expiry (Claude) | 634–726 | 638 pending (TTL 120 s), 683 periodic silent briefing push, 685 `pending.expired`, 687 "Yes." → 690/691 `pending.revived` + expiry sentence, 710 second "Yes." → 714 `keys=["1"]` → 715 confirmed |
| 10 | Long session / GoAway | 343–834 | 13 min session; no `voice.goaway` arrived; 785 server closed with 1011 at 13.1 min; 786–831 every reconnect with the resumption handle failed at setup (~30 s each, only visible as WS `error` messages — the daemon did not log them; fixed in 4d324ef) |
| E | Stale row closed at start | 840 | `herdr.transition wB:p1 closed=true source=snapshot` |
| E | Cancel without "finished" | 877–905 | 898 ctrl+c, 905 `supervisor.finished_suppressed` |
| E | agy edit prompt, "No." | 954–970 | 957 `no_keys:["2"]`, 963 "No." → 969 `keys=["2"]` (agy disables Esc on edit prompts) |

## Measured latencies

Utterance end = last speech frame written to the WebSocket (audio) or the
moment the `text` message was sent; first audio = first binary frame back.

| Measurement | Trials | min | median | max | Target |
|---|---|---|---|---|---|
| briefing, audio (`question.wav`) → first audio | 3 | 1629 ms | 1663 ms | 1921 ms | < 1200 ms |
| … of which utterance end → `voice.tool_call` (server VAD + model) | 3 | 939 ms | 994 ms | 1326 ms | (Gemini) |
| … of which `voice.tool_call` → first audio (handler < 1 ms, rest is the model) | 3 | 595 ms | 635 ms | 724 ms | |
| briefing, typed text → first audio | 4 | 919 ms | 978 ms | 992 ms | < 1200 ms |
| `briefing` tool handler itself | 5 | < 1 ms | < 1 ms | < 1 ms | < 300 ms |
| confirm, typed "Yes."/"No." → first audio (healthy session) | 4 | 925 ms | 1085 ms | 1512 ms | |
| propose, typed → first audio (includes resolve `llm.call` + `agent.prompt`) | 4 | 1870 ms | 2080 ms | 2297 ms | |
| Proactive block, agy: hook event received → first audio | 3 | 4.56 s | 4.79 s | 4.96 s | ~5 s |
| … sheepdog part without the 3 s debounce (read + summarize + push + model) | 3 | 1.56 s | 1.79 s | 1.96 s | |
| Proactive block, Codex: prompt sent → herdr `blocked` (herdr detection) | 1 | 9.0 s | | | |
| Proactive block, Claude: prompt sent → herdr `blocked` | 2 | 3.0 s | | 4.6 s | |
| `summarize_block` `llm.call` (gemini-3.5-flash-lite) | 7 | 770 ms | 1040 ms | 1278 ms | |
| `resolve_instruction` `llm.call` | 5 | 677 ms | 722 ms | 844 ms | |
| Deep ask: "Why did … fail?" → "Checking" audio | 1 | 768 ms | | | |
| Deep ask: `cli.run` (codex exec, read-only) | 1 | 24.1 s | | | |
| Deep ask: question → answer audio | 1 | 27.4 s | | | |
| Voice connect (`voice.connect`) | 4 | 337 ms | 420 ms | 441 ms | |
