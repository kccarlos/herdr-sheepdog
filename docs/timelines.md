# Timelines, collisions and invariants

Written 2026-09-25 against `main` at `0be70e6`. Code-verified: every claim cites the
file that decides it. Cells marked **[V]** were read in code (and, where named, a
test); **[I]** are inferred from code paths no test or log exercises. Log facts come
from `~/.local/share/sheepdog/events/2026-09-2{3,4,5}.jsonl`, timestamps UTC as logged,
event names and counts only.

**Why this page.** The bugs of 2026-09-24/25 were not missing states inside one
component. They were collisions between timelines that each behaved correctly on
their own. The voice model's filler landed between a brain tool call and its result
(#62). A brain reply arrived after the page closed (#64, #65). An interrupt push came
after Gemini finished generating while the phone was still playing (#70). The phone's
speaker output came back as an utterance (#63, #65).

The stance this page follows: the harness exists to make up for what a
single-threaded model cannot see (ordering, delivery, timing, provenance). It must
not fence in the model's judgement. Not every combination deserves a code path.
Only a failure seen in a real log gets a fix; the rest become eval or audio-e2e
scenarios (docs/iteration-2.md, "Rules for every change").

## 1. Timelines (bottom-up)

### 1.1 Worker pane — `internal/herdr`, dispatched by `internal/app/app.go`

| State / step | Advanced by | Logged as |
|---|---|---|
| `idle`, `working`, `blocked`, `done`, `unknown` (+ `Closed`) — `herdr/protocol.go` | herdr socket status events, fleet events, the 30 s `session.snapshot` diff, `Inject` (`watcher.go` `applyEvent`) | `herdr.state`, `herdr.transition` |
| dedup | same (pane, state) from another channel within `DedupWindow` 2 s | `herdr.dedup` |
| `blocked` debounce | `blocked` must persist `Debounce` 3 s (`debounce_ms`); any other state cancels it; others emit at once | `herdr.debounce_cancelled` |
| synthetic `blocked → blocked` | post-answer poll every 1 s for `reprompt_poll_s`, and the reconciler every 30 s + at startup (`supervisor/reprompt.go`) | `supervisor.reprompt`, `supervisor.reprompt_poll` |
| `agent_session` | carried on the Transition from the snapshot; the transcript since the last read is read at each block/finish (`internal/workerlog`) | `worker.session_missing` |
| dispatch | one goroutine per Transition, **serialised per pane** (`app.go` `paneLocks`); `HandleTransition` blocks for the pane read and the whole announce turn | — |

### 1.2 Supervisor — `internal/supervisor`

| Timeline | States as the code names them | Advanced by | Logged as |
|---|---|---|---|
| state row (`state.Agent`) | `State`, `StateSince`, `CurrentTask`→`LastTask`, `LastBlock{Question, Answered}`, `LastResult` | `store.Apply` in `transitions.go` `HandleTransition`; `finishTask`, `recordBlock`, `recordResult` | `supervisor.state_error` on failure |
| pending action | **one slot for the whole daemon** (`store.Pending`), kind `answer_prompt` or `send_instruction`; TTL `pending_ttl_s` 120 s, checked every 1 s; an expired `answer_prompt` is remembered for revival (`pending.go`) | `setPending`, `answerKeys`, `dropPendingFor`, `ExpirePending`, `revivePrompt` | `pending.created/replaced/confirmed/cancelled/expired/revived` |
| promises | `open` → `kept` / `expired` / `cancelled`; TTL 2 h; marked kept *before* the line is spoken (`promises.go` `keepMatching`) | matching worker event, `ExpirePromises`, first voice connect (`announceStartupPromises`) | `promise.created/kept/expired/carried_over` |
| investigations | running (one per pane; a newer one supersedes) → done / error / cancelled; ceiling `investigate_max_s` (`investigate.go`) | the brain's `investigate` tool, job exit, stop, shutdown | `investigation.started/done/error/cancelled` |
| announce wait | finish: waits for the brain's turn (6 s, 15 s when owed). Block (or a finish that asks): code sentence at `announce_wait_s` 2 s unless the brain answers first; a late line is added only for `needs_user` / owed / `relates_to` (`announce.go`) | `announce`, `applyResult`, `codePath`, `dropLate` | `brain.push_decision {wait, waited_ms}`, `brain.push_late`, `brain.push_stale` |
| utterance slot (`inflightUtterance`) | claimed → `inBrain` → `started` → `done` \| `detached` (after `reply_wait_s` 2 s) \| `coalesced` \| `dropped` (`utterance.go`) | `ToSupervisor`, `beginUtterance`, `coalesce`, `awaitReply`, `deliverLate`, stop, reset | `supervisor.relay`, `brain.reply_pending/delivered/stale`, `supervisor.utterance_coalesced` |
| shepherd inputs | voice input `open` → `claimed` / `used` / `answered`; a tool call waits up to `inputGrace` 1.5 s for its transcript (`inputs.go`) | transcripts, typed text, taps, `claimInput` | `supervisor.voice_initiative_ignored`, `supervisor.echo_dropped` |
| relay watch | armed on each input; fires after 4 s with no tool call and no speech of the voice's own (`relay.go`) | `heard`, `toolCalled`, `voiceSpoke`, reset, shutdown | `supervisor.relay_by_daemon` |

### 1.3 Brain — `internal/brain`

| Step | States / rule | Logged as |
|---|---|---|
| queue (`queue.go` `turnLock`) | free / busy; a CEO list and an event list; a CEO waiter always goes first, FIFO within a priority; an event waiter is keyed by agent alias (`investigation:<alias>` for a delivery), and a newer one replaces the queued one (`ErrCoalesced`) | `brain.event_coalesced` |
| turn (`loop.go` `Turn`/`run`) | queued → running steps 1..`MaxSteps` 6. The last step, or a step that starts with less than `StepReserve` left, is answer-only. Budgets: utterance 12 s, announce 6 s (15 s when owed), stop 5 s, plus `FinalGrace`; **time spent in the queue counts against the budget** | `brain.turn`, `brain.step`, `brain.tool_call/tool_result` |
| yield | before each model call, an event turn with a CEO waiter queued stops with `ErrYielded`; a step already in flight is never cut | `brain.yielded` |
| fallback | reason `timeout`, `cancelled`, `rate_limited`, `model_error`, `bad_reply`, `step_budget`, `yielded`, `coalesced`, `queue_timeout`; a turn that already acted returns the action's echo instead (`fail`) | `brain.fallback` |
| after-turn | prune → compact past 12 k tokens → release the lock (`afterTurn`) | `brain.compaction` |
| ingestion outside turns | `Heard`, `Said`, `SaidCut`, `Echo`, `Pushed`, `WorkerEvent`, `PromiseEvent`, in arrival order (`brain.go`); rendered in `modelOrder` (`assemble.go`) | — |
| model switch | `SetModel` changes the next turn; a running turn keeps its snapshot | `brain.model_changed` |
| reset / closed | `Reset` takes the lock with CEO priority, then starts a new segment; `Close` refuses new turns (`ErrClosed`), cancels the ones in flight and waits | `brain.reset` |
| 429 | Gemini: rotate to the next key per request; DeepSeek: one key, fails the call (`model.go`, `openaicompat.go`) | `brain.rate_limited` |

### 1.4 Voice session — `internal/voice` (behind `switch.go` `Switcher`)

**Gemini Live** (`gemini/{run,reader,turn,push,toolexec,session}.go`)

| Timeline | States / rule | Logged as |
|---|---|---|
| connection | `connected` → GoAway: `proactiveReconnect` with the handle. Read error → `reconnecting`. A 1011 drops the handle (fresh connect, context lost); a 429 waits `retryDelay`, then rotates the key; backoff 1 s→30 s. Recycle after 2 stalls (`stall_after_s` 10 s) or 2 push timeouts. `Reset` drops the handle. `EventResumed{ContextLost}` makes the supervisor push a **silent briefing** only | `voice.connect/connect_error/goaway/disconnected/resume{resumed}/handle_dropped/rate_limited/key_rotated/recycle/stalled/push_timeout/reset/closed` |
| turn | `turnOpen` (push or tool result sent) → `speaking` (first audio or output transcript) → `turnComplete` or `interrupted` (by `model` or `push`). **`Speaking()` = generation OR the client still playing** (`PlaybackTracker.Playing`; the timing guess without reports) | `voice.turn`, `voice.interrupted`, `voice.audio_timing`, `voice.playback` |
| tool call | NON_BLOCKING; handler in its own goroutine; the response waits for `turnDone` (≤ `ToolResponseWait` 1.5 s); INTERRUPT is downgraded to WHEN_IDLE while `Speaking()`, except `stop`. Sent only on the connection generation that issued the call (`sendOn`) | `voice.tool_call/tool_result{send_error}/tool_cancelled`, `voice.push_deferred{kind: tool_result}` |
| push queue | FIFO, **head only**. Silent: goes on any connection. Interrupt: goes when the shepherd is not speaking, and is downgraded to idle while `Speaking()` unless tagged `due_promise`, `needs_user` or `stop`. Idle: also waits for turn complete (or `TurnTimeout` 30 s), the end of playback, and `PushGap` 2 s. Held while no client; dropped after 5 min. Re-queued up to 3 send errors | `voice.push`, `voice.push_held/released/dropped`, `voice.push_deferred` |
| mute | frames dropped, `audioStreamEnd`; a muted shepherd never counts as speaking | `voice.muted` |

**GPT-Live** (`gptlive/{session,live,loop,tools}.go`)

| Timeline | States / rule | Logged as |
|---|---|---|
| billed session | `standby` → open (speech above `OpenRMS` with a client attached and unmuted, or a spoken push with a client) → closing on idle 45 s (not while a delegation, a push for a present client, input or speech is in flight), reset or shutdown → `standby`. Open failures back off 5 s | `voice.standby/session_open/session_error/session_closing/session_close/billed_seconds` |
| replay | a reopened session starts with the recap (12 turns, 2,000 chars) plus the silent pushes held in standby; `Reset` drops both | `voice.system_prompt {recap_items}` |
| input / turn | the utterance settles 700 ms after its last delta (`EventTranscript{Final}`); speaking is synthesised (voiced audio or transcript within 800 ms); `speechEnd` emits `EventTurnComplete`, held back while the shepherd is talking | `voice.transcript.in/out`, `voice.audio_timing` |
| delegation | `session.delegation.created` → the unclaimed transcript → `to_supervisor` or `stop` (`Classify`); the result goes on the delegation while that session is live, **else as a push** | `voice.delegation`, `voice.delegation_empty`, `voice.tool_result{delivered_as}` |
| keepAlive / pushes | silence every 40 ms tick while the client sends nothing; one push per tick, same hold/defer rules as Gemini; interrupt → `instructions.append`, idle → `commentary.append`, silent → `thinking.append` | as Gemini |

### 1.5 Client — `internal/web/ws.go`, `web/voice/{app,audio-util}.js`, iOS `SheepdogKit`

| Timeline | Web page | iOS app | Daemon side |
|---|---|---|---|
| socket | `connecting` → `open` → `closed` → reconnect with backoff 1–10 s, **also after `replaced`** (`app.js` `onclose`) | `SessionState` (derived, `SessionState.swift`): `disconnected`, `connecting`, `reconnecting`, `voiceOff`, `audioPaused`, `stalled`, `speaking`, `thinking`, `muted`, `silent`, `listening`, `idle`; on `replaced` it shows a "take it back" banner and does not reconnect (`SessionCore.swift`) | one client; a new one replaces the old (`closed/replaced`); `ClientAttached(true/false)` (`ws.go` `handle`) — `web.client.connect {replaced}` |
| mute | owned by the page; re-sent on open | owned by the app (`MuteSync`); muted in the background | a new client starts unmuted — `web.client.mute`, `voice.muted` |
| capture gate | `SilenceGate` (100 ms pre-roll) → `silence` messages; `speaking` messages when it has a detector | gate → `silence`; voice processing (echo cancellation) on | the gate is the speech detector unless `speaking` arrives (`setSilence`) — `web.client.silence/speaking` |
| player | `Player` `idle / buffering / playing`; reports `started / drained / flushed {played_ms}` | `PlaybackQueue` (adaptive jitter buffer); **sends no `playback` or `speaking` messages** (`WSMessage.swift`: `mute, silence, cancel, text, ping`) | `PlaybackTracker` (`voice/playtrack.go`): reports, or the timing guess — `voice.playback` |
| stop | local flush + `cancel` | same | `afterCancel`: flush + an interrupt push tagged `stop` |

## 2. Interruptions (arrive unannounced)

| Interruption | Enters at |
|---|---|
| Shepherd speaks over the voice (barge-in) | Gemini `Interrupted` / `ToolCallCancellation` (`reader.go`); GPT-Live input while speaking; client `flushed` |
| Shepherd says stop | bare word (`handleUtterance` → `Stop`), `stop` tool, stop button (`ws.go` `TypeCancel`) |
| Mute / unmute | `ws.go` `setMuted` → `Session.SetMuted` |
| Client disconnect / replace / reconnect | `ws.go` `handle` → `ClientAttached` |
| Voice provider switch | `Switcher.SetProvider` |
| New conversation | `Supervisor.ResetConversation` (API, bare phrase + confirm) |
| Brain model switch | `Brain.SetModel` |
| Worker finishes / blocks / exits | `HandleTransition` |
| New prompt while already blocked | `pollReprompt`, `reconcileReprompts` (synthetic `blocked → blocked`) |
| Gemini GoAway / 1011 / stall | `gemini/run.go` `run` |
| GPT-Live idle close | `gptlive/loop.go` `idleCheck` |
| Brain timeout / model error / 429 | `loop.go` `fail` → `brain.fallback` |
| Investigation completes | `runInvestigation` → `deliverInvestigation` |
| Promise fires | `keepMatching` inside `announce`; `ExpirePromises` |
| Daemon shutdown | `Supervisor.shutdown`, `Brain.Close`, `Switcher.Close`, `hub.shutdown` |

## 3. Collision table

Status: **ok** (handled), **partial**, **gap**. The four incidents come first.

| # | Interruption × layer | What happens today (decider) | Status |
|---|---|---|---|
| C1 | **#62** voice line lands mid-step × brain request | `Said("On it.")` was ingested between `read_screen`'s call and its result; DeepSeek returned HTTP 400, and every later turn of that segment fell back (log: `brain.fallback model_error` 18:16:43, then 18:17:10 stop, 18:18:27 announce, …). Now the renderers visit the window in `modelOrder`, moving context to just after the step, and `cutPoint` never splits a step (`assemble.go`, `engine.go`) [V] | ok |
| C2 | **#64** brain reply after the page closed × voice | `brain.reply_delivered` at 18:22:04, and Gemini spoke 12 s to nobody. Now spoken pushes are held while no client is attached (`holdPush`, `pumpPushes`) and dropped after 5 min with `voice.push_dropped`. `awaitReply` checks `hasListener` before returning inline and pushes instead (#65) [V] | ok |
| C3 | **#65** detached brain turn × daemon shutdown | `Supervisor.shutdown`: stop the relay → `endLife` → cancel investigations → abandon the utterance → wait; `Brain.Close` refuses and cancels (`supervisor.go`, `brain.go`) [V] | ok |
| C4 | **#70** interrupt push after `turnComplete` × client playback | `Speaking()` = generation OR `PlaybackTracker.Playing`; the push gap runs from `drained` (`push.go` `pushReady`, `loop.go` `pumpPushes`). **iOS sends no playback reports**, so on the phone this is the timing guess (`playtrack.go` `!p.reports`: audio sent minus wall time) [V] | partial |
| C5 | **#63/#65** speaker output × shepherd input | The phone's speaker came back as "new change" (log: `voice.interrupted by:model` 18:23:51, 18:23:54). Now the echo guard (`utterance.go` `echoDropped`: ≥ 2 words, ≥ 80 % aligned, started within 2.5 s of the voice's output) plus `Brain.Echo` takes it out of the window. The window is anchored to `turnComplete`/interrupted (`voiceStopped`), not to the end of playback, so the echo of a long reply's tail can pass [I] | partial |
| C6 | barge-in × voice output | server `Interrupted` → `interrupt("model")`: out-queue flush, `EventInterrupted` → hub `flush`; the client's `flushed` → `SaidCut` keeps only what was heard [V `TestBargeInCutsTheTranscript`] | ok |
| C7 | barge-in × running CEO turn | `ToolCallCancellation` cancels the handler context; `awaitReply` detaches; the turn runs on; `deliverLate` pushes the reply, or drops it as stale if a newer request was accepted (`acceptedSeq`) [V `TestToolContextCancelDoesNotCancelTurn`, `TestStaleReplyWithoutActionIsDropped`] | ok |
| C8 | shepherd speaks × running event turn | the CEO waiter makes the event turn yield at its next model call; an announce falls back to its code sentence; an investigation delivery retries once, then says "ask me what it found" [V `TestEventTurnYieldsToCEO`, `TestAnnounceYieldsToCEO`] | ok |
| C9 | second utterance × first still queued / running | queued: `coalesce` merges them into one turn (`supervisor.utterance_coalesced {n}`); running: never cut, and the first reply is stale when it lands late [V `TestBackToBackUtterancesCoalesce`] | ok |
| C10 | stop × in-flight work | the slot is dropped and its context cancelled, so write tools never run; a recent instruction gets Ctrl-C (30 s); an open prompt stays open; **event turns are not cancelled** (intended, `stop.go`); the closing stop turn queues as CEO behind a running event step and falls back to `stopSentence` if its 5 s budget runs out [V] | ok |
| C11 | stop × voice push queue | the queue is not cleared: an idle announce line queued before the stop plays after "Stopped" [I] | gap |
| C12 | mute × pushes and the rail | a muted shepherd is never "speaking", so pushes go on; no transcript arrives, so nothing is authorised [V `TestMutedClientAuthorisesNothing`] | ok |
| C13 | client disconnect × voice | spoken pushes held; the playback tracker and speech detector are reset (`ClientAttached`); the model's own reply to audio it heard is lost, logged `web.audio_unheard` [V] | partial |
| C14 | client replaced × client | a second web page replaces the first; the first reconnects 1 s later and replaces it back, in a loop (`app.js` `onclose` ignores the reason); iOS stops with a banner. `replaced:true` never appears in the log [I] | gap |
| C15 | voice provider switch × queued pushes | the old session is `Close`d; its `pushQ` is discarded with **no event**; `EventInterrupted` flushes the client; a tool handler in flight is cancelled, so its reply goes the late path [V code] | gap |
| C16 | New conversation × everything | `abandonUtterance`; `brain.Reset` waits for the lock (CEO priority); the voice `Reset`; relay counters bumped so old inputs cannot authorise. Pending prompts, promises and investigations are kept, and a delivery lands in the fresh conversation by design [V `TestResetDropsADetachedUtteranceReply`, `TestResetPreventsPreResetInputFromAuthorising`] | ok |
| C17 | brain model switch × running turn | the turn keeps its snapshot; the next one rebuilds the other model's calls without `Raw` [V `TestSetModelMidTurnKeepsSnapshot`] | ok |
| C18 | a worker event × the same pane's announce | the pane lock (`app.go`) is held for the whole announce turn (≤ 6/15 s + grace), so a block on that pane waits behind its own finish's turn. The brain's per-agent coalesce is never reached for one pane's events (`TestAnnounceCoalesces` calls `HandleTransition` concurrently and bypasses the lock) [I] | gap |
| C19 | block × another pane's open question | the single slot is replaced ("Dropping the earlier question from X"). An instruction to B also replaces A's open `answer_prompt`; A's bare "yes" then goes to the brain (`send_keys` revives it); the reconciler skips A (`LastBlock` unanswered). `pending.replaced` never appears in the log [I] | gap |
| C20 | prompt answered × its announce turn | `pendingStillOpen` fails → `brain.push_stale`, nothing said; a promise that event kept is already marked kept, so its line is lost [V code] | gap (small) |
| C21 | new prompt while blocked | the poll or the reconciler raises a synthetic transition; `claimPrompt` stops a double raise [V `TestPollAndReconcileNeverDoubleRaiseTheSamePrompt`]; `raiseReprompt` calls `HandleTransition` outside the pane lock [I] | ok |
| C22 | worker exits mid-work | the pending action is dropped; an interrupting push [V `TestAgentExitPushesInterrupt`] | ok |
| C23 | Gemini 1011 / GoAway × voice memory and tool results | GoAway resumes with the handle; a 1011 drops it, so the model forgets the conversation and gets back only the silent briefing. Log 09-23…25: 74 closes with 1011, 68 of 77 resumes `resumed:false`, 48 `handle_dropped server_internal_error`. `pushQ` survives, but a tool response due on the old generation is dropped (`sendOn` → `send_error`; none in the log) [V] | gap (seen) |
| C24 | GPT-Live idle close × delegation / push | `idleCheck` waits for delegations, input and speech; a push reopens the session with the recap; a result whose session is gone is pushed [V `TestIdleCloseWaitsForADelegationWhileMuted`] | ok |
| C25 | brain timeout / model error / 429 | an announce falls back to the code sentence; an utterance to "Sorry, I couldn't get an answer…"; a turn that already acted reports it. Log 09-24: 11 announce `timeout` fallbacks (DeepSeek) [V] | ok |
| C26 | promise fires × voice speaking | past due: tagged `due_promise`, which is exempt, so it interrupts; otherwise idle [V `TestPushExemptTagsStayInterruptWhileSpeaking`] | ok |
| C27 | voice calls `to_supervisor` with no input | `voice_initiative_ignored` (log 00:24:13, 2 s after `voice.push_timeout`) [V `TestRelayWithoutShepherdInputIsIgnored`] | ok |
| C28 | transcript vs tool call race | `claimInput` waits up to 1.5 s for the input [V `TestRelayWaitsForATrailingTranscript`] | ok |
| C29 | interrupt push behind an idle head | the queue looks at the head only, so an urgent or owed line waits for the idle head's turn and gap (up to `TurnTimeout` 30 s). Log 09-24: `voice.push_timeout` at 00:24:11 and 16:21:42 [V] | gap (seen) |
| C30 | daemon shutdown × queues | the supervisor joins its turns; the voice sessions discard `pushQ` (acceptable: nobody is left to hear it); the hub sends `closed/shutdown` [V] | ok |

## 4. Arbitration: who wins

| Principle | As the code implements it | Where it deviates |
|---|---|---|
| (a) live input beats everything | A CEO turn goes before event turns (`queue.go` `release`); an event turn yields at a step boundary; barge-in flushes speech (C6); stop cancels the in-flight shepherd turn and a fresh instruction (`stop.go`) | Event turns are not cancelled by stop (intended). A step in flight is never cut, so the stop's closing turn can wait out one event step. Barge-in does not cancel the brain turn; its reply comes later or goes stale (C7, intended) |
| (b) what is owed beats unsolicited news | Owed finishes get 15 s, not 6 s; kept promises are spoken at every chattiness level; late `needs_user` lines are added; investigation answers skip the ceiling; late replies are pushed | The brain's event queue is FIFO whatever is owed, and so is the voice push queue (C29). An investigation delivery yields at most twice, then becomes "ask me what it found" |
| (c) blocked > finished > idle chatter | A block speaks its code sentence at 2 s; a finish waits for the brain; interrupt only for `needs_user` on a new block (`applyCeiling`) | No kind priority in the brain queue or the push queue. The pending slot is last-writer-wins: the newest block, or an instruction, displaces an older question (C19). A pane's block waits behind its own finish turn (C18) |
| (d) never over the shepherd, never to nobody, hold don't drop | `speakingUntilLocked` / `vad.Hold` gate every non-silent push; pushes held while no client (TTL 5 min + event); `hasListener` before an inline reply; the pending prompt revives after expiry | Drops by policy, with events: `brain.push_late`, `push_stale`, `reply_stale`, coalesced. Drops without an event: provider switch (C15), a push after 3 send errors (only `EventError`), a Gemini tool response on a dead generation (`send_error` field only). A Gemini tool result is not gated on the shepherd speaking; the model schedules it |
| (e) newer replaces older per agent; FIFO across agents | Brain event key = alias; a newer delivery replaces an older one; utterances coalesce while queued | Unreachable for one pane's worker events (C18). Agents with no alias share the key `"an agent"` and can coalesce across panes [I]. The push queue never coalesces |
| (f) ties by the daemon's clock, logged | Windows are clock-based (authorise 30 s, cancel 30 s, echo 2.5 s, relay 4 s, input grace 1.5 s); decisions logged: `brain.push_decision {waited_ms}`, `brain.rail_blocked {reason}`, `brain.reply_stale` | Staleness is decided by sequence counters (`inputSeq`, `acceptedSeq`, `connGen`, relay `gen`), not the clock. Order at a mutex (turn lock, `pushQ`, transcript ingestion) is arrival order and is not logged |

**The fixed mechanisms, and where each is used**

1. **Single writer with priorities** — the brain `turnLock` (and `Brain.Reset` takes it as CEO).
2. **Coalesce** — brain event key; `supervisor.coalesce` for queued utterances; `claimRunLocked` joins a run of inputs.
3. **Hold / release** — `holdPush`/`pumpPushes` (no client); interrupt → idle while speaking; `claimInput` grace; `AnnounceWait`; `ReplyWait` then detach.
4. **Yield at a step boundary** — event turns before each model call; the delivery retry.
5. **Atomic re-check at send time** — `AnswerPrompt` under `s.mu` (the rail); `pendingStillOpen`; `hasListener`; `execTool`'s `Speaking()`; `deliverLate`'s `acceptedSeq`; `relayUnanswered`'s `gen`.
6. **TTL with an event** — `pending.expired`, `promise.expired`, `voice.push_dropped`, `brain.fallback timeout`, reset-confirm 20 s, `investigate_max_s`.

**Collisions handled by none of these:** C1 is fixed by render-time normalisation (`modelOrder`), not arbitration. C5 is a text-and-timing heuristic. C18 is a seventh, implicit mechanism: the per-pane mutex in `app.go`. The Gemini `sendOn` generation check re-checks and then *drops* instead of holding. C3 is a lifecycle join. C15 is plain discard.

## 5. Invariants

| Invariant | Guard today |
|---|---|
| A step's tool calls and results stay adjacent in every model request | `TestModelOrderVoiceLineMidStep`, `TestModelOrderMultiCallStep`, `TestVoiceLineFixtureHeals`, `TestCutPointKeepsStepWhole` (`brain/order_test.go`), `TestVoiceLineDuringToolCall`; a violation shows as `brain.fallback model_error` HTTP 400 |
| A spoken push is heard or recorded unheard | `TestPushHeldWhileNoClientAndReleasedOnAttach`, `TestHeldPushDroppedAfterTTL` (gemini), `TestPushHeldWhileClientGoneEvenWithSessionOpen` (gptlive), `TestReplyWithNoListenerIsHeld`; events `voice.push_held/released/dropped`, `web.audio_unheard`. **Unguarded:** provider switch, 3 failed sends, a push sent just before a 1011 (logged `voice.push`, never spoken) |
| "Speaking" means audio is still playing on a client | `TestInterruptPushAfterTurnCompleteWaitsForDrained`, `TestGPTLiveInterruptPushWaitsForClientDrained`, `TestNoPlaybackReportsFallsBackToTimingGuess`, `TestPlaybackStartedNeverEndedIsBounded`; event `voice.playback`. iOS: timing guess only |
| Our own line never becomes the shepherd's utterance | `TestEchoOfTheAssistantIsNotRelayed`, `TestEchoLeavesTheBrainWindow`, `TestPushRelayedBackNeverReachesTheBrain`, `TestRelayWithoutShepherdInputIsIgnored`; events `supervisor.echo_dropped`, `voice_initiative_ignored`. By design a one-word or fast-path echo ("yes") passes |
| `send_keys` only on real shepherd input within `authorise_window_s` | `TestBrainSendKeysBlockedByTheRail`, `TestInputPastTheAuthoriseWindowIsRefused`, `TestNoInputCannotAuthoriseYesKeys`, `TestRailStillRefusesSendKeysOnAnEventTurn`, `TestResetPreventsPreResetInputFromAuthorising`, eval `TestRunSVPOfflineRailBlocksInjection`; event `brain.rail_blocked` |
| The pending prompt is single-slot (daemon-wide) and re-checked at send time | `AnswerPrompt`/`fastAnswer` re-check under `s.mu`; `TestAnnounceBlockOneLineAndStale`, `TestReplacementAnnouncement`, `TestTapRejectsStaleIDsAndBadOptions`, `TestExpiredPromptRevivedByFastPath`; events `pending.*` |
| A promise is kept once | Guarded by code, no dedicated test: `resolvePromise` moves only an `open` promise, and `keepMatching` marks it before speaking |
| A coalesced turn answers every utterance it absorbed | `TestBackToBackUtterancesCoalesce` (one reply over the joined text), `TestEventTurnsCoalesce`, `TestAnnounceCoalesces` (the replaced event still gets its code sentence) |
| A stale reply never answers a newer question | `TestStaleReplyWithoutActionIsDropped`, `TestStaleReplyThatActedIsSilent`; `brain.reply_stale` |
| No brain call after the supervisor stopped | `TestShutdownWaitsForInvestigationDelivery`, `TestCloseEndsATurnInFlight` |
| The shepherd's turn goes first | `TestEventTurnYieldsToCEO`, `TestAnnounceYieldsToCEO` |

## 6. Gaps, ranked

Per docs/iteration-2.md: only a failure seen in a log gets code. Everything else
becomes a scenario that must fail before any fix lands. Ranked first by whether a log
has seen it, then by blast radius. The fix direction is one line; no refactor is
proposed.

| Rank | Gap | Seen? | Scenario it needs | One-line direction |
|---|---|---|---|---|
| 1 | Gemini 1011 loses the voice's conversation (C23): after 68 of 77 resumes the voice knows only the silent briefing, so a "what does that mean?" answered from `detail` (J5) has nothing to draw on | yes, 3 days of logs | audio-e2e: a 1011 injected between a reply and its follow-up | replay a short recap on a context-lost resume, as GPT-Live does |
| 2 | iOS sends no playback or speaking reports (C4, C5): the #70 and #63 fixes run on timing guesses on the phone, where both incidents happened; no cut transcripts; the echo window starts at `turnComplete` | yes (both incidents were on the phone) | audio-e2e with an iOS-profile client (no reports), a long reply and an echo at its tail | send `playback`/`speaking` from `PlaybackQueue`; anchor `echoWindow` to the end of playback |
| 3 | Push-queue head-of-line (C29): an urgent or owed line waits behind an idle head for up to 30 s | yes (`voice.push_timeout` ×2, 09-24) | unit test in `gemini/session_test.go`: interrupt queued behind an idle push during an open turn | let a ready item pass a waiting idle head |
| 4 | Per-pane serialisation (C18): a block waits behind its own pane's finish turn (≤ 15 s + grace) | no | supervisor scenario through `app.go`'s pane lock: finish, then block within the turn budget | do not hold the pane lock across the announce wait |
| 5 | Single daemon-wide pending slot (C19): an instruction or a second block displaces an open question; the bare-"yes" fast path is lost for it | no (`pending.replaced` 0) | eval case: A asks, the brain instructs B, the shepherd says "yes" | keep one slot per pane |
| 6 | Silent drops (C15, C20, send retries): provider switch, 3 failed sends, a kept promise on a stale block | no | unit: switch provider with a held push; announce stale with a kept promise | log `voice.push_dropped` on every discard |
| 7 | Web page ping-pong after `replaced` (C14) | no (`replaced:true` 0) | two-client ws test | stop reconnecting on `closed/replaced`, as iOS does |
| 8 | Stop leaves queued pushes (C11) | no | audio-e2e: stop while an announce is queued | none until seen |
