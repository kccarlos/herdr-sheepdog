# Gemini Live context retention across resumes (2026-10-04)

Work item "Gemini Live context retention across resumes" (docs/handover-2026-10-04.md).
Measurement and information only: nothing here changes a product rule; the
CEO decides what to do.

**Finding.** The sharp prompt-token drops after a GoAway resume are real
context loss, and the suspect is none of the four on the list. The canary
reproduces it with a bare minimum: **a resumption handle issued by a
connection that was itself resumed restores only what was said on that
connection, not the context it inherited.** The first resume of a session
is lossless; the second and every later one loses everything older than the
last connection. The daemon's own logs fit this: 7 of 7 first resumes keep
their tokens (ratio 1.00 to 1.35), 23 later resumes have a median ratio of
0.73 and 12 of them are below 0.8.

Setup: `cmd/livecanary` (README there), Vertex `us-central1`,
`gemini-3.8-live`, the daemon's setup (voice Puck, compression trigger 16 000
/ target 4 000, handle without `transparent`, transcription on, one
non-blocking tool). Eight spoken facts, then the arm, then one probe question
per fact, graded by keywords. Raw JSONL: `live-resume-context-results/`.
Estimated Vertex spend for everything on this page: about $3 (the canary
runs are cents each; the 9-minute GoAway runs and the 5-minute mic run the
most).

## Per-arm results

Recall counts the eight spoken facts plus the arm's own facts. Tokens are the
last usage report before the resume and the first after it (which includes the
probe turn, so +30 to +40 is "unchanged").

| Arm | Recall | Prompt tokens before to after | Note |
|---|---|---|---|
| control (no resume) | 8/8 | 1109 | within-connection baseline |
| resume (clean manual) | 8/8 | 1102 to 1138 | baseline, same as the earlier probe |
| silent context after the last handle | 10/11 | 1176 to 1205 | the parcel line (sent after the last handle) is lost; the #123 re-send exists for this |
| briefing function responses | 11/11 | 1213 to 1246 | survive |
| natural GoAway (541 s) | 8/8 | 1559 to 1591 | GoAway 30 s left, as before |
| natural GoAway with the context grown to 9.5k | 8/8 | 9506 to 11199 | first resume of the session; the rise is the new probe turns |
| fill, 13.5k tokens, below the trigger | 9/9 | 13538 to 13573 | no-resume control 9/9 |
| long (4 full long spoken answers, 8.6k) | 8/8 | 8560 to 8594 | no-resume control 8/8 |
| tool (2 real call/response round trips, a fact told in between) | 11/11 | 1550 to 1582 | both calls made (2 of 2) |
| interrupt (barge-in over a long story) | 11/11 | 2014 to 2048 | 2 of 3 interruptions cut the story (`interrupted` events), the facts told in them survive |
| longmic (5 min continuous mic: noise plus a remark every 45 s) | 8/8 | 2427 to 2475 | continuous noise adds almost no audio tokens (about 1.2k for 6 remarks) |
| compress (past the 16k trigger) | 1/9 | 4274 to 4309 | compression drops the oldest facts, with or without a resume |
| compress, no resume | 1/9 | 4279 | same loss in one connection: by design (sliding window) |
| chain, idle, gen-1 handle reused 4x, 12.7k tokens | 8/8 | 12731 to 12760 | no turn between resumes: no new handle, the same gen-1 handle |
| chain, idle, fill 0 | 8/8 | 1168 to 1201 | same |
| **chain, a turn after each resume (3 to 4 resumes)** | **0/8** | **5191 then 167, 166 (and 1192 then 215, 173; 1220 then 168, 170; ...)** | **7 of 7 runs: step 1 fine, step 2 (handle from a resumed connection) falls to the system prompt** |
| chain, the same with `-transparent` | 0/8 | 1220 then 168 | no difference |
| chain, 45 s idle before the next resume | 0/8 | 1206 then 168 | no difference |
| **chain with one new fact per connection (3 resumes, 2 runs)** | **1/10 both** | 1178 then 1219 then 170 | only the fact told on the last connection survives; the original eight and the fact told on connection 2 are gone |

Confirmation runs: the suspect arm (chain with a turn) was run 7 times
(different fills, `-transparent`, a wait, with and without graded probes)
and failed 7 of 7; `compress` was run twice (resume and no resume) with the
same loss. Two harness bugs were found and fixed on the way (a stale
turn-completion misaligned probe answers in longmic and interrupt; the
interrupt arm's system prompt let the model answer "I don't know" to the
story request); those runs were discarded and rerun.

### The mechanism, in the canary

```text
conn 1 (fresh): facts ... handle H1 (5153 tokens)
conn 2 = resume(H1): 5191 tokens, everything present, one "Ok." turn, handle H2
conn 3 = resume(H2): 167 tokens = the system prompt plus conn 2's own "Ok."
```

With `-chain-newfact` a password is told on each connection; after the third
resume only the password told on the connection just before it is recalled.

## The daemon's own logs fit

`sheepdog scorecard` (this work) pairs each resumed connection with the last
`voice.usage` before it and the first after it. All 30 resumes in
`~/.local/share/sheepdog/events` (2026-09-22 to 2026-10-04) that had a usage
report on both sides:

| Day | Resumes | Tokens after / before | Ratio | 1st resume of a session | Later resumes |
|---|---|---|---|---|---|
| 2026-09-23 | 1 | 2372 / 2226 | 1.07 | 1.07 (n=1) | n/a |
| 2026-09-29 | 3 | 5693 / 6458 | 0.88 | 1.35 (n=1) | 0.64 (n=2) |
| 2026-10-02 | 15 | 65896 / 98943 | 0.67 | 1.04 (n=3) | 0.60 (n=12) |
| 2026-10-03 | 3 | 13369 / 14666 | 0.91 | n/a | 0.91 (n=3) |
| 2026-10-04 | 8 | 62249 / 81911 | 0.76 | 1.07 (n=2) | 0.72 (n=6) |

Generation counted from the daemon start, a fresh session (`resumed:false`,
`voice.handle_dropped`, `voice.session_refreshed`). First resumes (7): ratios
1.00, 1.04, 1.07, 1.07, 1.07, 1.31, 1.35. Second and later (23): median
0.73; 12 of 30 resumes are below 0.8 and none of them is a first resume.
The log has no per-modality numbers before this change (#134 adds them).

A rough prediction from the canary's mechanism: tokens after a later resume
are about the system prompt (the median first usage of a fresh session is
910) plus what the connection added since it was opened (the usage before this
resume minus the usage after the previous one). Compared with the log: 10-02
00:29 predicted 3.8k, logged 3.9k; 00:39 4.8k, 5.1k; 00:48 5.4k, 6.1k; 00:57
7.3k, 7.8k; 10-04 16:07 4.7k, 5.5k; 16:16 14.4k, 15.4k. It is a back-of-envelope
fit (a new probe turn and re-sent briefings add some), not a proof, and it
breaks where compression ran in between (10-04 16:25). It also explains the
idle cases: 10-02 01:45 (7610 to 1192) and 10-03 00:28 (11869 to 848) had no
turns for a long time, so a connection with almost no content of its own was
resumed.

Tokens are not recall, but the canary shows that in this case the tokens are
the recall.

## Bench scenario (tier 2) and a run

`resume-recall` (testdata/bench/scenarios/robustness.json, category
reconnect): the opening, "What is bench-codex working on exactly?", then the
daemon recycles the voice connection once (`behavior.resume`, the existing
recycle path: a new connection resumed from the latest handle, the old one
closed), then "Remind me, what did you just tell me about bench-codex?".
Three runs, Vertex brain `gemini-3.5-flash` (DeepSeek avoided), $0.14 total:
pass 3/3, every time the voice answered itself correctly with the parser
refactor facts (no `to_supervisor` call after the resume), and
`voice.resume_context` logged 1745 to 2446, 1629 to 2322, 1771 to 2459
(AUDIO 330 to 608, TEXT 1415 to 1838): the first resume of the session, so no
loss, as the canary predicts. A scenario that resumes **twice** before asking
would exercise the loss; the bench has `behavior.resume` for one resume per
call (`after_turn`), so extending it to a list is the follow-up if the CEO
wants it. Report and events: `live-resume-context-results/bench-resume-recall/`.

## Which suspect

| Suspect from the handover | Result |
|---|---|
| Real tool-call round trips | not it: 2 real calls, 11/11, tokens unchanged |
| Interrupted generations | not it: barge-in over stories, 11/11 |
| Long continuous mic audio | not it: 5 minutes, 8/8 (continuous noise costs almost no tokens) |
| Sessions crossing the 16k trigger | not the resume drops: compression drops the oldest content with or without a resume, and the daemon's drops happen at 7 to 13k, below it (one 19.5k to 3.1k drop with no resume in the 10-04 log is the compression) |
| Big context alone, long answers, natural GoAway timing | not it (13.5k, 8.6k of model audio, 9.5k at a natural GoAway: first resumes, lossless) |
| **Resuming a resumed session (generation 2 and up)** | **it: 7 of 7 canary runs; 0 of 7 first resumes and 12 of 23 later resumes in the daemon log lose tokens** |

Open: the canary loses everything older than the last connection; the daemon
logs show partial loss. The partial figures follow from the same mechanism
(the last connection's own content survives), but the log has no recall
data, so whether the fast brain actually answered wrongly after those resumes
is unproven. The new events (#134) and the scorecard line measure it going
forward.

## Options for the CEO (none implemented)

1. **Avoid generation 2: never resume a resumed session.** At a GoAway start
   a fresh session and re-seed it from the daemon's own transcript, which the
   daemon already does when a handle is dropped (`reseed.go`, the recap) plus
   the briefings. Simple and uses existing code; but the re-seed is a
   summary of the recent conversation, not the full context, and it costs a
   few seconds and tokens at every GoAway.
2. **Re-seed after every second-generation resume.** Keep resuming (the first
   resume is lossless) and, when the resumed connection is itself resumed
   (the daemon knows the generation), send the recap and briefings as silent
   context. Same re-seed quality as 1, but only when needed; needs the
   generation bookkeeping.
3. **Extend the session life instead of resuming.** Nothing in the data
   shows the 9-minute GoAway can be moved; the only lever is the compression
   trigger (it does not postpone the GoAway). Not a fix.
4. **Leave it, rely on the slow brain.** The slow brain answers whatever the
   fast brain cannot (the fast brain hands off when it has no facts), at the
   price of latency and the stale or invented answers seen in the phone test.
   Cheapest; the loss stays invisible to the CEO.
5. **Report it to Google** (a handle issued after a resume should carry the
   whole context) and keep 1 or 2 as the workaround meanwhile. The canary is
   the reproducer: `go run ./cmd/livecanary -arm chain -chain-fill 0 -chain-talk`.

Whatever is chosen, run the canary after a model or backend change:
`chain -chain-talk` is the check that fails today.
