# Reply as the function response: A/B on tier 2 (2026-10-03)

Question: should the brain's answer reach the voice as the `to_supervisor`
call's own function response, even when it arrives seconds later, rather
than as a user-role push? The second opinion of 2026-10-03 (§2–§3) called
this Stage 1's central bet. Nobody had tried it, because every answer that
took more than 2 s came back `pending` and was then pushed. Campaign D N3
showed the cost of the push channel. The voice reads an answer pushed as a
user turn as a request: it says "On it." or "Checking.", or it relays the
answer back as the CEO's words (`supervisor.self_relay`). Most of these
failures were in Chinese.

**Decision: ship it.** The new default is `brain.reply_wait_s = 30`, up from 2.
Arm B removed every answer relayed back as the CEO's words (7 of 147 → 0 of
134, Fisher p ≈ 0.01) and every lost Chinese or code-switched answer (6 of
36 → 0 of 33). Pass rate went from 67 % to 70 %. p50 latency went from
4.21 s to 4.17 s. Answers voiced as "On it." did not go down (5 → 6). The
heard watch added in this PR now catches those in both arms and re-queues
them.

## What changed

The supervisor side existed already. `awaitReply` waits `ReplyWait` for the
turn and returns the reply inline. On a barge-in (`ToolCallCancellation`)
or when the page leaves, it detaches and the reply is pushed. A wait above
the utterance budget (25 s + 3 s final grace) makes the reply the
function response in practice. Gemini schedules it with the reply's own
WHEN_IDLE or INTERRUPT. No new setting: arm B is `reply_wait_s = 30`. A
negative value is not the same thing. It runs the turn under the call's
context, so a barge-in cancels the turn and the answer is lost.

A call that stays open for a whole brain turn needed four fixes:

| Gap | Fix | Test |
|---|---|---|
| C23: a function response for a dead connection generation was dropped (`sendOn` → `ErrNotConnected`) | an answer that cannot be sent goes through the push queue, which outlives connections (`voice.tool_result_pushed {send_error}`). Same for a call cancelled after the reply came (`{cancelled}`) | `TestLateAnswerOnDeadConnectionIsPushed` |
| a late result was not held while the CEO speaks (only model speech downgraded INTERRUPT) | a spoken result waits on `speakingUntilLocked`, as pushes do (≤ 10 s, `voice.push_deferred {shepherd_speaking}`). stop() never waits | `TestLateAnswerHeldWhileShepherdSpeaks` |
| an answer sent as a function response was not watched by the heard judge, the re-queue or the re-seed recap | spoken `to_supervisor` answers are watched like spoken pushes (tags `answers_utterance`, `call_id`). Voiced as "On it." they are judged unheard and re-queued | `TestAnswerAsFunctionResponseIsWatched` |
| with a 30 s window a newer request can be accepted while the first call is still open, and the inline path had no stale check | a reply that lands after `acceptedSeq` moved goes the late way: dropped, or pushed silent if the turn acted. The call gets a silent result, not `pending` | `TestLongWaitStaleReplyIsNotInline` |

A fifth fix came from reading arm B's events (see "Mechanisms"). It is not
measured in the A/B. Sometimes the voice gets the answer and then calls
`to_supervisor` again with the same words. That call used to make the
unheard answer stale ("asked since"), so it was never re-queued. Now a
re-call whose text equals the question the answer belongs to is not
counted as new words (`TestRequeueStaleAnswer/same_question_again`).

## Setup

- Tier 2 (`sheepdog bench run --tier 2`), one scratch daemon on port 7792
  only, `--parallel 1`. Voice: Gemini Live on Vertex. Brain: DeepSeek
  flash, with Vertex for failover. The judge and the simulated CEO run on
  Vertex Flash-Lite. The voice prompt is the same in both arms.
- Scenarios (10): status-en, status-zh-tired, deep-ask-detail,
  approve-codex, approve-claude-permission, deferral-zh,
  collision-block-while-talking, barge-in-long-answer,
  lang-mixed-codeswitch, unheard-approval-requeue.
- Arm A: `SHEEPDOG_BRAIN_REPLY_WAIT_S=2` (main's behaviour). Arm B:
  `SHEEPDOG_BRAIN_REPLY_WAIT_S=30`. Both arms ran the branch binary, so
  only the setting differs. With a 2 s wait the new code paths are almost
  never taken, except the heard watch on inline answers, which makes the
  heard numbers comparable.
- Round 1 ran A then B, 4 repeats each. Round 2 ran B then A, 3 repeats
  each, to cancel out time-of-day drift. Pooled: 70 runs per arm. Round 2
  also had the silent-not-pending stale result.
- The metrics come from the daemon's events (`events/*.jsonl`):
  - `voice.push_unheard` restricted to answers (`answers_utterance`).
  - `supervisor.self_relay`, split into relays of an answer and relays of an
    announcement by matching the relayed text against the run's answers.
  - `turn.latency` (`outcome: played`). `total_ms` runs from the CEO's
    words to the answer's first audio. `first_audio_ms` runs from the
    answer's hand-off to its first audio.
  - "Lost" is a distinct answer that was body-skipped or relayed back.

A probe ran before the A/B (status-en, status-zh-tired, deep-ask-detail;
arm B; $0.14). It confirmed that Vertex Live voices a late function
response word for word: a 10.8 s English answer and a 3.1 s Chinese
answer were both read verbatim. Until then no log held a Chinese function
response.

## Results (pooled, 70 runs per arm)

| | A: 2 s (main) | B: 30 s |
|---|---|---|
| pass | 47/70 (67 %) | 49/70 (70 %) |
| answers (distinct) | 147, sent 157 times: 30 as function response, 127 as push (10 re-queues) | 134, sent 139 times: 132 as function response (93 of them after > 2.5 s), 7 as push (5 re-queues) |
| `pending` results | 114 | 4 |
| **answers relayed back as the CEO's words** | **7** (6 in zh/mixed) | **0** |
| announcements relayed back | 0 | 3 |
| answers body-skipped (heard judge) | 9 | 6 |
| … of which voiced as a bare ack ("On it.", "Checking.") | 5 | 6 |
| **distinct answers lost** (skipped or relayed) | **10 (6.8 %)** | **6 (4.5 %)** |
| **zh / mixed answers lost** | **6 of 36 (17 %)** | **0 of 33** |
| answer sends judged unheard, all reasons | 38 of 157 (24 %) | 28 of 139 (20 %) |
| … CEO talked over it (`user_turn`) | 18 | 17 |
| … cut by another push | 7 | 4 |
| re-calls with the same words after an answer | 1 | 3 |
| latency total p50 / p90 | 4.21 s / 7.22 s | 4.17 s / 6.84 s |
| latency total p50, zh / mixed | 3.25 s | 3.79 s |
| first audio after hand-off p50 / p90 | 0.64 / 0.75 s | 0.64 / 0.78 s |
| barge-ins (server `Interrupted`) | 37 | 31 |
| spend | $3.47 | $3.14 |

By round:

| | A r1 (4×) | A r2 (3×) | B r1 (4×) | B r2 (3×) |
|---|---|---|---|---|
| pass | 27/40 | 20/30 | 28/40 | 21/30 |
| answers lost | 7/84 | 3/63 | 2/79 | 4/55 |
| answer self-relays | 4 | 3 | 0 | 0 |
| zh / mixed lost | 3/22 | 3/14 | 0/20 | 0/13 |
| latency total p50 | 4.26 s | 4.05 s | 4.17 s | 4.17 s |

Per scenario (passes of 7):

| scenario | A | B |
|---|---|---|
| status-en | 6 | 5 |
| status-zh-tired | 7 | 7 |
| deep-ask-detail | 2 | 0 |
| approve-codex | 7 | 7 |
| approve-claude-permission | 6 | 7 |
| deferral-zh | 6 | 6 |
| collision-block-while-talking | 4 | 5 |
| barge-in-long-answer | 2 | 1 |
| lang-mixed-codeswitch | **3** | **7** |
| unheard-approval-requeue | 4 | 4 |

## Mechanisms

- **Self-relay was an answer-channel problem, and it is gone.** All 7 of
  arm A's answer self-relays were pushes. Six were in lang-mixed-codeswitch:
  a Chinese answer pushed as a user turn, relayed back as a to_supervisor
  call. That scenario went from 3/7 to 7/7. Arm B's 3 self-relays were
  **announcements**, which are still pushes in both arms
  (approve-codex r3: "bench-codex is asking to run the full test suite…
  Approve it?" relayed 0.4 s after the push). This is Stage 2's problem:
  the announcer path or the generator call (second opinion §3).
- **The function-response channel is not immune to "On it."** In 6 of 132
  function-response answers the voice said "On it." and never read the
  body. In 3 of those it then called `to_supervisor` again with the same
  words (status-en r4: answer at 14.2 s, the same "What's going on?" at
  15.1 s, the body never voiced). This re-call pattern happened once in A.
  The heard watch caught every one. Where nothing made the answer stale
  it was re-pushed (approve-claude-permission r1: function response at
  50.8 s, `body_skipped` at 53.0 s, re-queued push at 55.0 s). The
  same-words fix above keeps the re-call from blocking the re-queue.
- **Latency did not move.** Arm A's pushes took a p50 of 4.5 s from the
  CEO's words. Its inline answers took 2.3 s, and only answers under 2 s
  could be inline. In arm B nearly every answer went through the
  function response, with a p50 of 4.17 s. The voice speaks the function
  response as fast as a push: about 0.64 s from hand-off to first audio in
  both arms. The Chinese p50 was 0.5 s higher in B, within the +1 s
  limit, on 33 turns.
- **Barge-in behaves as before.** An answer the CEO talks over is cut and
  judged `user_turn` in both arms (18 vs 17). No answer was sent after a
  newer call in either arm. The open call adds no stuck state: no
  `voice.tool_result_pushed` fired in 140 runs, because no connection was
  lost mid-turn. The C23 path is covered by its unit test only.
- **The arm-independent failures are brain content.** deep-ask-detail
  (2/7 vs 0/7) and barge-in-long-answer (2/7 vs 1/7) fail on what the
  brain said, in both arms. Examples: "the summary is all the screen
  shows", an unintended prompt to bench-claude, the same contributing-notes
  sentence repeated. No failure in those runs comes from delivery. One
  status-en failure in B is the re-call pattern above. The other is the
  judge calling `bench-codex` jargon.

## Decision against the rule

The rule: ship arm B only if it clearly reduces ack-voicing or self-relay,
without regressing pass rate, and without adding more than 1 s to p50
latency.

- Self-relay of answers: 7 → 0, clear. Lost Chinese and mixed answers:
  6 → 0.
- Ack-voicing: 5 → 6, no change. The heard watch now catches it in both
  arms.
- Pass rate: 67 % → 70 %. Latency p50: −0.04 s overall, +0.5 s in Chinese.

So `brain.reply_wait_s` now defaults to 30
(`supervisor.DefaultReplyWait`), and 0 means that default. To get the
old behaviour back, set `reply_wait_s = 2`.

## Limits

- n = 70 runs per arm. Only the self-relay difference is statistically
  clear. Lost answers overall (10 vs 6) are not, on their own.
- GPT-Live gets the same setting but was not benched. Its tool path
  appends the result to the delegation while the delegation is live, and
  pushes it otherwise (`gptlive/tools.go`), so a late result still
  reaches the CEO.
- The shepherd-speaking hold (≤ 10 s) does not know about a newer
  to_supervisor call. If one arrives during the hold, the held answer
  still goes out WHEN_IDLE. This was not observed in 140 runs.
- Announcements still go as user-role pushes. Their self-relay and
  "On it." are untouched (3 announcement self-relays in B).
- The same-words re-call fix landed after the A/B and is not measured.

## Spend

| run | runs | spend (estimate) |
|---|---|---|
| probe (B) | 3 | $0.14 |
| round 1, A | 40 | $2.04 |
| round 1, B | 40 | $1.84 |
| round 2, B | 30 | $1.30 |
| round 2, A | 30 | $1.43 |
| **total** | **143** | **$6.75** (budget $12) |
