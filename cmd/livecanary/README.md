# livecanary: Gemini Live context-retention canary

A standalone platform check for what Gemini Live keeps across a session
resumption. It does not touch a sheepdog daemon. It speaks eight facts to a
Live session set up like the daemon's (Vertex `us-central1`, model
`gemini-3.8-live`, voice Puck, compression trigger 16 000 / target 4 000,
resumption handle without `transparent`, input and output transcription,
one non-blocking tool), optionally does something to the session (an
*arm*), resumes it with the latest handle, then asks one question per fact
and grades the answers by keywords. It measures; nothing reacts to the
result and no exit code depends on it.

Work item: "Gemini Live context retention across resumes"
(docs/dev/handover-2026-10-04.md, part B). Results and the write-up:
docs/dev/research/live-resume-context-2026-10-04.md; raw JSONL in
docs/dev/research/live-resume-context-results/.

## Running it

```sh
set -a; . ./.env; set +a          # VERTEX_API_KEY, VERTEX_PROJECT
go run ./cmd/livecanary -arm resume -out docs/dev/research/live-resume-context-results
```

Flags: `-arm`, `-no-resume` (the same arm without the resume: the
within-connection control), `-out DIR` (one dated JSONL per run),
`-model`, `-location`, `-transparent`, `-mic-minutes` (longmic),
`-goaway-max`, `-fill-to` (goaway). Every line of the JSONL carries `t`
(seconds), `kind` and `arm`; the last `summary` line has the recall
(`8/8`), prompt tokens before the resume and at the first usage report after
it (total and per modality), and the missed facts.

Cost: a short arm is about 1.5 minutes of Live audio (cents); `goaway`
waits for the server's GoAway (about 9 minutes) and `longmic` streams 5
minutes of audio; all of it is well under $1 a run. Run weekly, on a model or
backend change, and before a release; keep the dated results.

## What is checked

Facts (spoken once each, `audio/f1..f8.wav`): the blue key is in drawer
seven; the sister's cat is called Pickle; the Dana meeting moved to Thursday
at 3:00; the server room code is 4492; the red bike has a flat rear tire; the
Wi-Fi hint is mango river; the library book is due on the 12th; the spare
charger is in the green backpack. After the arm and the resume the canary
asks one question per fact (recall is checked by the answer, not by tokens),
plus the arm's own facts.

| Arm | What it does before the resume | Extra facts asked |
|---|---|---|
| `control` / `resume` | nothing (clean manual resume; `control` = no resume) | none |
| `silent` | three silent `clientContent` lines, the last one after the last handle | door, plant, parcel |
| `briefing` | three `briefing` function responses (two silent, one when-idle) | door, plant, parcel |
| `goaway` | waits for the server's GoAway (about 541 s, 30 s left), then resumes like the daemon: new connection first, old one closed; `-fill-to N` grows the context with long answers while waiting | none |
| `compress` | text filler until the context crosses the 16k trigger and compression visibly shrinks it, then one late fact | parking spot |
| `fill` | text filler to about 12k prompt tokens, below the trigger | parking spot |
| `long` | four complete long spoken stories (the model's own audio output dominates the context) | none |
| `chain` | resumes several times in a row, each from the handle the previous (resumed) connection issued; with `-chain-newfact` a new fact is told on every connection; `-chain-fill N`, `-chain-talk`, `-chain-probe`, `-chain-wait`, `-chain-turns`, `-chain-n` shape it. Without `-chain-talk` the connections stay idle and the one gen-1 handle is reused | per step facts with `-chain-newfact` |
| `tool` | two real tool round trips: the system instruction makes the model call `lookup`; a spoken fact is told between the first call and its result | umbrella, museum hours, bus 42 |
| `interrupt` | three barge-ins: the user speaks a new fact over a long story | parking, dentist, gift |
| `longmic` | five minutes of continuous microphone audio: ambient noise plus an unrelated spoken remark about every 45 s | none |

Grading: the answer's output transcript, lower-cased with punctuation
removed, must contain at least one alternative of every keyword group of the
fact (for example `drawer` and `seven|7`). A miss is worth reading in the
JSONL before it is believed; the grader is crude (it was wrong once, see the
results note).

The compression arms lose the oldest facts by design (the sliding window), with
or without a resume: read `compress` against `compress -no-resume`.

## Baselines (2026-10-04, `gemini-3.8-live`, Vertex `us-central1`)

See the results note for the table. In short:

- A resume from a handle issued by a connection that was opened fresh
  (generation 1) keeps every spoken fact and the prompt token count is
  unchanged (+2 to +4 %, the probe turn): `resume`, `briefing`, `tool`,
  `interrupt`, `long`, `fill` (13.5k tokens), `longmic` (5 min), `goaway`
  (natural GoAway, 541 s, 30 s left; also at 9.5k tokens) and an idle
  `chain` that reuses the gen-1 handle all recall 8/8 (or 11/11).
- **A resume from a handle issued by an already resumed connection
  (generation 2 and up) restores only what was said on that connection**:
  `chain -chain-talk` (and `-chain-newfact`) recall 0/8 spoken facts, the
  prompt falls to about 170 tokens (the system prompt), only a fact told
  on the last connection survives. Reproduced 9 of 9 times, with and
  without `-transparent`, with a wait, with 7 turns on the resumed connection. A healthy canary shows
  `chain -chain-talk` recalling the facts; today it does not.
- By design: silent context sent after the last handle is lost (the daemon
  re-sends it, #123) and compression drops the oldest content with or
  without a resume (`compress` against `compress -no-resume`: 1/9 both).

## Clips

`audio/f1..f8.wav` (the facts) are from the earlier probe; `int*.wav` and
`narr*.wav` are made by `audio/gen.sh` with the macOS `say` voice. All are
16 kHz mono PCM16 and embedded in the binary.
