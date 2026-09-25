# Iteration 2: make the core loop work, and prove it

Written 2026-09-23 after a review of the first week (49 PRs, 4 headset
sessions) and of the event logs of 2026-09-23/24. The verdict: the
architecture (voice hears and speaks; brain judges; workers act; the
rail in code; the event log as the one record) is right. The way of
working was not: features landed about twelve PRs per real session,
verification proved the design contract rather than the experience,
and every live failure got a guard instead of a root cause. This
iteration changes the loop, not the architecture.

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
6. No live API calls in tests. Gemini free-tier quota and GPT-Live
   minutes are for the shepherd's own sessions. DeepSeek text runs of
   the eval set happen only when the orchestrator says so, ≤ 30 cases.

## Three gates, none substitutes for another

- `go test -race ./...`: the system does what the code says.
- `sheepdog eval` (L0, offline, hundreds of cases): the models are
  asked the right things and their answers meet the ear metrics.
- The scripted headset session (L3, 10 min, same script every build,
  appendix): the experience. One session per build.

## Work packages

Phase 0 — measure first (no behaviour change)
- **WP0a scorecard**: `internal/earmetrics` (pure functions over a
  spoken line) and `sheepdog scorecard` over the event logs, markdown
  output. Run on the existing logs = the baseline.
- **WP0b eval corpus**: `sheepdog eval extract` turns a log window into
  cases (transcript, agents, screens, request); the five logged
  failures as cases; paraphrase and transcript-noise generators
  (checked-in lists, not live); L0 runner over the mock brain with
  deterministic checks. Hundreds of cases, free.
- **WP0c headset script** (appendix below).

Phase 1 — the core loop, each PR under the rules above
- **WP1 ear contract** — merged, #55: reply = `spoken` (the headline) / `ask` (the
  one question) / `detail` (the plain explanation). The existing
  `spoken` and `detail` names are kept and redefined rather than
  renamed to `headline` / `plain`: too many consumers (voice tool
  results, the web API, the debug page, evals, the iOS app) read them,
  and a rename would be churn with no change for the ear.
  `prose.EarLine` enforces ≤ 2 sentences, ≤ 30 words, no dashes /
  semicolons / PR numbers / hashes, the ask last (overflow moves to
  `detail`); ms values and versions are left to the prompt, since
  cutting them mid-sentence breaks the grammar. The voice reads spoken
  (which ends with the ask), answers "what does that mean" from
  `detail`, never adds its own follow-up question; the brain's
  "explain simpler" rule; "never mind" out of the stop words.
- **WP2 finish delivery** — merged, #54: a finish waits for the brain's line (up to
  ~10 s) before anything is spoken; only a block gets the code sentence
  at once; a late `needs_user` line is always spoken; the "started
  on…" push for the brain's own action is silent.
- **WP3 the shepherd's words** — merged, #58: every utterance runs on the recorded
  transcript, not the voice model's paraphrase; the echo / initiative
  / input-grace guards go where that makes them redundant; a final
  transcript with no tool call and no speech within N s is relayed by
  the daemon; back-to-back utterances coalesce into one turn; GPT-Live
  `settledText` appends instead of overwriting.
- **WP4 worker history** — merged, #57: keep `agent_session`; on a finish read the
  worker's own session transcript (Claude: the JSONL path herdr
  reports) for its last assistant message; a per-agent work journal in
  the ledger; `recall` over it; the sign-off convention in every
  instruction the brain sends ("end with three lines for someone who
  can't see the screen: what changed / what needs a decision / next").
- **WP5 brain speed** — merged, #59: window 24k → 12k tokens, ledger trimmed,
  thinking off for announce turns, stable prefix; target < 2 s per
  step, measured by the scorecard. (Caps themselves were tightened
  further the same day in #60: detail capped at three sentences, two on
  an announce.)
- **WP6 terms ledger** (after WP1 is measured): the brain records each
  term it introduces with a one-line explanation; recall hits it.
- **WP7 audio diversity**: audio-e2e gains mid-sentence pauses,
  mute-right-after-speaking, barge-in during a push, and a WER check of
  `voice.transcript.in` against the `say` text; the app gains an opt-in
  recording of the shepherd's utterances for replay.
- **WP8 dive deep**: talking about one worker in detail —
  [`docs/dive-deep.md`](dive-deep.md). `read_screen(source: session)`
  and detail on request (#68), `investigate(source: worker)` forking
  the worker's own CLI session (#69). Implemented; see that doc's
  status line.
- **WP9 timelines and invariants**: a code-verified map of sheepdog's
  concurrent timelines and what happens when they collide —
  [`docs/timelines.md`](timelines.md) (#71). The gaps it ranked (Gemini
  1011 reconnects losing context, the push queue looking only at its
  head, silent drops, the echo window anchored to generation instead of
  playback) were fixed in #72.

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
