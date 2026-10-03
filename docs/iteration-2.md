# Iteration 2: make the core loop work, and prove it

Written 2026-09-23 after the first week (49 PRs, 4 headset sessions). The
architecture stays; the way of working changes: every change cites a
measured failure, adds an eval case, and reports before/after.

## The product is six jobs

Nothing else gets investment until these hit their thresholds.

| # | Job | Success, measured from the event log |
|---|---|---|
| J1 | "What's going on?" | first word ≤ 2 s, answer ≤ 5 s, ≤ 20 words, the item that needs the shepherd comes first, no repeat question within 60 s |
| J2 | Approve / deny a worker's prompt | keys sent within 3 s of the bare word; never without a recorded shepherd input |
| J3 | Give an instruction | one confirmation line, ≤ 20 words, spoken once (not twice) |
| J4 | Hear a finish with its finding | the brain's line is spoken (not "X finished." alone) whenever it has one; needs_user lines always spoken |
| J5 | "What does that mean?" | answered from `detail` (the plain explanation) without a brain turn (≤ 1 s), or by a brain turn that is *simpler* than the previous line, one concept at a time |
| J6 | "Stop" | stops what is in flight; never Ctrl-C on "never mind"; nothing killed without the shepherd naming it |

## Scorecard metrics (computed from `<data_dir>/events/*.jsonl`)

Per session and per day: time to first word; time to answer; words,
sentences, dashes/semicolons, technical tokens (PR numbers, ms, versions)
and unfamiliar terms per spoken line; **repeat-ask rate** (same intent
re-asked within 60 s); **lost-utterance rate** (`voice.transcript.in`
final with no tool call and no spoken output within 5 s);
**brain-line drop rate** (`brain.push_late added:false`, split by
needs_user); Ctrl-C sends without a stop word; count of "what does that
mean"-class follow-ups; brain turn duration and prompt tokens. The
baseline is the 2026-09-23/24 logs.

## Rules for every change in this iteration

1. It cites a failure: a scorecard metric or an eval case id taken from
   a real log.
2. It adds or updates an eval case that fails before and passes after.
3. It reports before/after on the metric (offline eval, or the scorecard
   on a new headset session).
4. **Config knobs: net zero or fewer.** Add one, remove one.
5. If the fix is a guard, the PR says which invariant it protects and
   why the architecture cannot make the failure unrepresentable
   instead.
6. No live API calls in tests or CI. Gemini free-tier quota and
   GPT-Live minutes are for the shepherd's own sessions. Live eval runs
   (below) use Vertex AI or DeepSeek, never the AI Studio free tier,
   carry a spend budget (`--budget-usd`) and report their estimated
   spend.

## Three gates, none substitutes for another

- `go test -race ./...`: the system does what the code says.
- `sheepdog eval` (L0, offline, hundreds of cases): the models are
  asked the right things and their answers meet the ear metrics.
- The scripted headset session (L3, 10 min, same script every build,
  appendix): the experience. One session per build.

## Live evaluation: judge, simulated user, repeat runs

The offline L0 gate checks the harness; whether the models behave is a
live question, and one live run says little. Three tools answer it
(after pipecat's `evals`, docs/research comparison):

- **LLM judge** (`internal/eval/judge.go`). One criterion per call,
  phrased so yes is good; verdict `yes`, `no` or `wait` (the
  conversation is not finished — the harness may keep listening; a final
  judgement has no wait). A rationale is a second call, only for a no or
  a low-confidence verdict. Backends: Gemini Flash-Lite on Vertex
  (`gemini-3.1-flash-lite`, default), local Ollama (`gemma4`, small runs),
  a scripted fake (tests). In `sheepdog eval --live` the judge decides
  the wording checks that offline are skipped
  (`spoken_contains_any_of`, `first_sentence_contains_any_of`,
  `spoken_no_jargon`) plus a case's `expect.judge` items; the structural
  checks stay in code.
- **Simulated user** (`sheepdog eval sim`, scenarios in
  `testdata/eval/sim/*.json`). An LLM plays the CEO with a goal and talks
  in text to the real supervisor and brain over a mock herdr whose
  workers change on cue (a time, the CEO's Nth turn, what sheepdog sends
  a worker); announcements reach the simulated CEO as they would the
  phone. It hangs up with `end_call` or waits with `wait_for_sheepdog`;
  caps on turns, total time and silence end the call otherwise, and the
  end reason is recorded. The judge grades six default items (answered,
  no_unintended_action, right_agent, no_hallucination, ear_friendly,
  language) and the scenario's own, against the transcript and the ground
  truth (screens, scripted changes, every action sent). Code checks on
  what reached the workers give missing_function_call, unintended_action
  and wrong_agent. A `fixed` scenario plays its script live when the
  exact words matter ("Stop.", "Oh, never mind."). Offline, `make eval`
  runs every scenario with the scripted brain and user: a harness check
  of the files.
- **Repeat and stats**. `--repeat N` (and `--parallel`, `--budget-usd`,
  `--out` for report.json and report.md). Pass rate = pass / (pass +
  fail): errored runs (the harness failed: a rate limit, a judge or
  simulated-user error) and not_run runs (budget, cancel) are reported
  but excluded, and never count as a pass. Failure labels are fixed:
  error, not_run, timeout, brain_fallback, unintended_action,
  wrong_agent, missing_function_call, judge_no, check, silence,
  max_turns.

    sheepdog eval sim --live --provider vertex --model gemini-3.8-flash \
      --env-file .env --repeat 5 --parallel 5 --budget-usd 25 --out DIR

- **Failover in campaigns**. The sim registers a second brain provider
  next to `--provider`, so the brain's failover (a step that gets a
  429/5xx/504/timeout is retried once on the next provider, order Vertex
  then DeepSeek then Gemini) is exercised. `--fallback-provider
  vertex|deepseek|gemini|none` picks it; the default registers every
  provider with credentials, as the daemon does (gemini, the AI Studio
  free tier, only with `--allow-ai-studio`). Per run, report.json has
  `failovers`, `failover_events`, `answered_by` and `steps_by_provider`
  (from the run's `brain.failover` and `brain.step` events); report.md
  has a "Brain failover" table. A failover is not a failure label: the
  run is graded on the answer it got. Brain spend is metered under the
  model of the provider that answered each step.

A conversation costs about $0.01–0.02 at list prices (brain on
gemini-3.8-flash, judge and simulated user on Flash-Lite). Results:
[`eval-campaign-2026-10-03.md`](eval-campaign-2026-10-03.md).

audio-e2e (`testdata/audio-e2e/scenarios.json`) has the same kind of
assertions for the audio path: `max_eos_to_*_ms` latency bounds,
`absent` (something must not happen, or not more than `max_count` times,
in a window — a duplicate announcement), and multi-turn scenarios with
per-turn expectations.

## Work packages

Phase 0 — measure first (done)
- **WP0a scorecard**: `internal/earmetrics` and `sheepdog scorecard`
  over the event logs; the baseline is
  [`scorecard-baseline-2026-09-24.md`](scorecard-baseline-2026-09-24.md).
- **WP0b eval corpus**: `sheepdog eval extract` turns a log window into
  cases; the five logged failures as cases; paraphrase and
  transcript-noise generators (`eval expand`); the L0 runner over the
  mock brain ([`roles.md`](roles.md) "The L0 eval corpus").
- **WP0c headset script**: the appendix below.

Phase 1 — the core loop (done unless noted; the behaviour is described
in [`roles.md`](roles.md))
- **WP1 ear contract** (#55): reply = `spoken` / `ask` / `detail`,
  capped in code (`prose.EarLine`); "never mind" is not a stop word.
- **WP2 finish delivery** (#54): a finish waits for the brain's line;
  only a block speaks at once; a late `needs_user` line is always
  spoken.
- **WP3 the shepherd's words** (#58): every utterance runs on the
  recorded transcript; the daemon relays an unanswered utterance;
  back-to-back utterances coalesce.
- **WP4 worker history** (#57): the worker's own session transcript, a
  work journal per agent, the sign-off trailer.
- **WP5 brain speed** (#59, #60): window 24k → 12k tokens, trimmed
  ledger, no thinking on event turns, a stable prompt prefix.
- **WP6 terms ledger** (open): the brain records each term it introduces
  with a one-line explanation; `recall` finds it.
- **WP7 audio diversity** (open): audio-e2e gains mid-sentence pauses,
  mute right after speaking, barge-in during a push and a WER check.
- **WP8 dive deep** (#68, #69): [`dive-deep.md`](dive-deep.md).
- **WP9 timelines and invariants** (#71, #72):
  [`timelines.md`](timelines.md).

Phase 2 — breadth, only once J1–J6 are green on two consecutive
headset sessions.

## Appendix: the headset script (WP0c)

Ten minutes, the same every build, with the mock flock or a real one of
2–3 workers, one of them at an approval prompt. Say each line, wait for
the answer, then continue. Note anything you had to repeat.

1. "What's going on?"
2. "What does that mean?" (whatever it just said)
3. "Yes." (to the open prompt) — then "What did I just approve?"
4. "Tell <alias> to run the tests and let me know when it's done."
5. "What's <other alias> doing?" then, immediately, "And the third one?"
6. Wait for a finish push. Then: "What did it find?"
   - "What exactly did it say?" (docs/dive-deep.md WP8d)
   - "Tell me more."
   - "Ask it why."
7. "Why?" (to whatever it said)
8. Start an instruction, then say "Stop."
9. "Never mind." (nothing should be killed)
10. Mute right after asking: "How long has <alias> been running?" — mute
    within a second of finishing the sentence.

The scorecard for the session is the result; the notes are the
qualitative half. Detail replies (step 6's three added lines) get their
own column: words per reply, capped at 60 (docs/dive-deep.md WP8b), and
whether the answer fell back to the brief two-sentence cap instead of
naming the worker's own points ("no fallback").
