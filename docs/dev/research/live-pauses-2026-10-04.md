# Gemini Live mid-answer pauses — 2026-10-04

Question: during the CEO's phone session (2026-10-03 22:16Z onward) Gemini
Live paused about 7 s in the middle of an answer four times (`voice.audio_timing`
`max_gap_ms` ≈ 7000 at 22:28:20, 04:47:13, 04:52:30, 04:57:11). Playback drained,
nothing new arrived for about 6.5 s, then the rest of the answer came. The daemon
sent nothing during the gap and it does not line up with GoAway reconnects. Is
this specific to Vertex's `gemini-3.8-live`, and would another backend or model
avoid it?

Short answer: the pause reproduces on the bench at the same rate as in the CEO's
logs (about 3 % of answers), always as a flat 7.0 s stall, on Vertex
`gemini-3.8-live` only. The two alternatives tried (AI Studio `gemini-3.8-live`,
Vertex `gemini-live-2.5-flash-native-audio`) showed no pause in 62 and 64
answers, but that sample cannot prove they are immune (at a 3 % rate, 0 of 60 has
about a 15 % chance by luck). The native-audio model also breaks Chinese
commands. No arm is a clear win, so the recommendation is a cheap trial, not a
switch (below).

## Method

- `sheepdog bench run --tier 2` (docs/dev/bench/README.md), one scratch daemon on
  port 7797 (the CEO's 7777 and the other benches' ports untouched), 8 talk-heavy
  scenarios without echo or noise: `status-en`, `status-zh-tired`,
  `deep-ask-detail`, `deep-ask-fix-failing-test`, `approve-codex`,
  `approve-zh-hurried`, `collision-block-while-talking`, `collision-finish-push`.
- Voice configuration set per arm with the existing env overrides
  (`SHEEPDOG_GEMINI_BACKEND`, `SHEEPDOG_GEMINI_MODEL`, `VERTEX_LOCATION`); no
  daemon code change, nothing committed outside this document.
- Metrics come from the daemon's own events in each run's `events/*.jsonl`:
  `voice.audio_timing` per answer (`max_gap_ms` is the longest gap between audio
  chunks arriving from Gemini, `realtime_ratio` is audio length over wall time of
  the chunk stream) and `turn.latency` `first_audio_ms`. "Answers" are every
  `voice.audio_timing` event (includes one-second fillers); the rate is also given
  over answers of 2 s of audio or more.

| Arm | Voice | Brain | Runs |
|---|---|---|---|
| A | Vertex `gemini-3.8-live`, us-central1 (current) | DeepSeek flash | 8 x 3 |
| A2 | same as A (control for B) | Vertex `gemini-3.8-flash` | 8 x 2 |
| B | AI Studio `gemini-3.8-live` (`gemini.backend=aistudio`) | Vertex `gemini-3.8-flash` | 8 x 2 |
| C | Vertex `gemini-live-2.5-flash-native-audio`, us-central1 | DeepSeek flash | 8 x 3 |

Deviation, stated plainly: DeepSeek answered `HTTP 402 Insufficient Balance`
from about 23:24 PDT (2026-10-03), partway through the first attempt at arm B (8
runs, discarded: every turn after the first hit `brain_fallback`). Arms A and C had
finished before that and are clean (no 402 in their events). B and its control A2
were rerun with the scratch daemon's brain on Vertex `gemini-3.8-flash` through a
local, uncommitted env override in `internal/bench/daemon.go` (not in this PR; the
bench has no flag for the scratch brain provider). The brain does not touch the
Live audio path, so the gap and timing columns are comparable across all arms;
pass rates are not (Vertex-brain runs add 504 failovers that land on the empty
DeepSeek account). **The DeepSeek balance is empty: the CEO's daemon fails over to
it too.**

## Results

| | A Vertex 3.8-live | A2 control | B AI Studio 3.8-live | C Vertex 2.5 native |
|---|---|---|---|---|
| Runs finished | 24 | 16 | 16 | 24 |
| Answers (`voice.audio_timing`) | 73 | 56 | 62 | 64 |
| Answers of 2 s or more | 63 | 39 | 51 | 62 |
| Gaps of 3 s or more | **2** | **1** | 0 | 0 |
| Gaps of 5 s or more | **2** | **1** | 0 | 0 |
| Longest gap | 7.17 s | 7.00 s | 0.12 s | 2.01 s |
| Gaps per answer (2 s or more) | 3.2 % | 2.6 % | 0 % | 0 % |
| Median `realtime_ratio` (2 s or more) | 1.02 | 1.04 | 4.39 | 3.23 |
| First audio p50 / p90 | 619 / 786 ms | 605 / 746 ms | 917 / 1394 ms | 730 / 857 ms |
| Pass rate | 20/24 (83 %) | 11/16 (69 %) | 7/16 (44 %) | 19/24 (79 %) |
| Voice-model WER, en / zh | 0.065 / 0.019 | 0.067 / 0.056 | 0.134 / 0.000 | 0.178 / 0.463 |
| CEO lines the voice never heard | 0 of 45 | 0 of 30 | 0 of 31 | **3 of 46** |
| Estimated spend | $0.96 | $0.67 | $0.91 | $0.70 |

Pass rate mostly measures the scenarios and the brain, not the voice:
`collision-finish-push` fails on the judge's "no question after the result"
item in A 3 of 3 times. B's low rate comes from the Vertex-brain 504 failovers
(4 of its 9 failures are `brain_fallback`, 1 a timeout). The gap rows are the
point of this table.

The two pauses in A are the CEO's pattern exactly: `voice.playback drained`, then
7.1 s later the next fragments (`status-en` r2: 5.0 s of audio, gap 7.12 s;
`collision-block-while-talking` r3: 18.1 s of audio, gap 7.17 s, after the first
12.8 s had played). A2's gap was 7.00 s (`status-zh-tired` r2). No GoAway, stall
(`voice.stalled` 0), interruption or reconnect in any of them.

### Qualitative notes

Only transcripts and the judge's verdicts were examined; nobody listened to the
audio (no `--save-audio`), so timbre and prosody are not assessed.

- **A, Vertex 3.8-live**: language 24 of 24, ear-friendly 24 of 24, Chinese heard
  cleanly (WER 0.02). Audio arrives paced at real time (ratio 1.02), so there is
  no lead buffer on the phone when a stall hits.
- **B, AI Studio 3.8-live**: same model, so the same wording; Chinese heard
  cleanly (WER 0.0, 6 lines) and the English WER is a little higher (0.13 over 25
  lines, noisy). Audio arrives in bursts (ratio about 4.4, the answer is delivered
  well ahead of playback), first audio about 300 ms slower than Vertex. Free tier:
  no 429 in 16 runs (about 5 min of Live audio, 62 answers); the daily cap was
  not reached, so how much headroom remains is unknown.
- **C, Vertex 2.5 native audio**: no gap, bursts at about 3.2x, first audio 730
  ms. But Chinese is poor: the input transcription comes back with spaces between
  characters ("现 在 什 么 情 况 ?"), zh WER 0.46, and the tired "先不要。" (not now)
  was **never heard in 3 of 3 runs**, so `status-zh-tired` failed its voice check
  every time and a deferral by voice would be lost. One `approve-zh-hurried` run
  also failed ear-friendliness.

## History (CEO logs, read-only)

`~/.local/share/sheepdog/events/*.jsonl`, `voice.audio_timing` events with a gap of
3 s or more, model taken from the preceding `voice.system_prompt` (approximate when
providers were switched mid-day):

| Date | Backend / model | Answers (2 s or more) | Gaps of 3 s or more | Gap sizes (s) |
|---|---|---|---|---|
| 09-23 | AI Studio gemini-3.8-live | 23 | 2 | 3.9, 12.8 |
| 09-24 | AI Studio gemini-3.8-live | 81 | 4 | 24.0, 4.6, 3.0, 10.0 |
| 09-25 | AI Studio gemini-3.8-live | 15 | 0 | |
| 09-29, 10-01 | Vertex gemini-3.8-live | 11 | 0 | |
| 10-02 | Vertex gemini-3.8-live | 138 | 3 | 7.0, 6.9, 7.3 |
| 10-03 | Vertex gemini-3.8-live | 64 | 5 | 7.2, 7.0, 7.1, 7.0, 7.1 |
| 10-04 (UTC, to 05:00) | Vertex gemini-3.8-live | 21 | 3 | 7.0, 6.9, 6.9 |

(09-23 and 09-24 also carry `gpt-live-1` answers with 20 gaps in 61 answers of 2 s
or more; that is a different provider and not part of this question.)

- On Vertex since 10-02: 11 gaps in 223 answers of 2 s or more (4.9 %), every one
  6.9 to 7.3 s. The bench's 3 in 102 (about 3 %) matches. The earlier Vertex days
  (09-29, 10-01) have too few answers to say anything.
- On AI Studio (09-23/24): gaps in 6 of 104 answers of 2 s or more (6 %), but irregular sizes up
  to 24 s, and `realtime_ratio` around 0.73, that is audio arriving slower than
  real time throughout those answers. That was a different failure (a slow or
  loaded model), and it was on a day of preview-model load; today's AI Studio
  bench shows ratio 4.4 and no gaps.
- So a flat 7 s stall is a signature seen only on Vertex `gemini-3.8-live`, in
  the CEO's logs and on the bench; AI Studio has had its own, different
  slowness before.

## Recommendation (configuration change, not code)

1. **Do not switch to `gemini-live-2.5-flash-native-audio`.** It avoids the pause
   in this sample but drops Chinese commands ("先不要" unheard 3 of 3) and
   transcribes Chinese badly. The CEO speaks Chinese; that is worse than a 7 s
   pause in 3 % of answers.
2. **Trial AI Studio `gemini-3.8-live` for a few days**:
   `gemini.backend = "aistudio"` (or `SHEEPDOG_GEMINI_BACKEND=aistudio`, the
   `[gemini]` key pool stays as is), model unchanged. It had no gap in 51 answers,
   Chinese was fine, cost is about 300 ms more first-audio latency. Judge it by
   counting `voice.audio_timing` `max_gap_ms >= 3000` in the event log with the
   query used here, for at least 150 answers (at the Vertex rate that is about
   five expected gaps, so zero would be meaningful). Risks: free-tier daily quota
   (not hit here, but the CEO's usage is heavier than 16 runs), and AI Studio's
   own earlier slowness. Revert is the same one line.
3. **Treat 'bursty delivery' as a plausible mitigator, unproven.** Vertex 3.8-live
   streams audio at real time (ratio 1.02) so a server-side stall of 7 s drains
   the phone's buffer at once; the other two paths deliver 3 to 4x faster than
   playback, so the phone is usually seconds ahead. This is a hypothesis from the
   ratios only; it would not stop a stall at the start of an answer.
4. Not tried, left for a follow-up if the pause persists: another Vertex location
   (the `us` multi-region serves `gemini-3.8-live` as well; one run each of
   `us-central1` and `us` would show whether the stall is location-bound).

## Reproduce

```sh
F=status-en,status-zh-tired,deep-ask-detail,deep-ask-fix-failing-test,approve-codex,approve-zh-hurried,collision-block-while-talking,collision-finish-push
SHEEPDOG_GEMINI_BACKEND=vertex SHEEPDOG_GEMINI_MODEL=gemini-3.8-live VERTEX_LOCATION=us-central1 \
  sheepdog bench run --tier 2 --env-file .env --filter "$F" --repeat 3 --ports 7797 --parallel 1 --out /tmp/pause-A
# B: SHEEPDOG_GEMINI_BACKEND=aistudio   C: SHEEPDOG_GEMINI_MODEL=gemini-live-2.5-flash-native-audio
```
Total estimated spend for the study: $3.3 (limit $8), including the discarded
arm-B attempt ($0.10).
