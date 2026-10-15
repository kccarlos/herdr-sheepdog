# Scorecard baseline: 2026-09-23 / 2026-09-24

WP0a (`docs/dev/iteration-2.md`): the scorecard's first run over the two days
the iteration's rules cite as the baseline. Read-only over
`/Users/kc/.local/share/sheepdog/events/{2026-09-23,2026-09-24}.jsonl`;
nothing in this run wrote to those files or changed daemon behaviour.

Command:

```
sheepdog scorecard --events-dir /Users/kc/.local/share/sheepdog/events \
  --from 2026-09-23 --to 2026-09-24 --format md
```

## Report

# Scorecard

Command: `sheepdog scorecard --events-dir /Users/kc/.local/share/sheepdog/events --from 2026-09-23 --to 2026-09-24 --format md`

## total

15542 events, 2026-09-23T00:00:22Z to 2026-09-24T06:07:54Z

### Latency

| metric | n | mean | median | p90 |
|---|---|---|---|---|
| answer latency (ms) | 48 | 5700 | 5060 | 11093 |
| time to first word (ms) | 48 | 136360 | 3960 | 24642 |
| brain.step prompt_tokens | 142 | 19777 | 18931 | 33605 |
| brain.turn dur_ms (announce) | 73 | 6156 | 6237 | 9002 |
| brain.turn dur_ms (stop) | 1 | 2488 | 2488 | 2488 |
| brain.turn dur_ms (utterance) | 44 | 6970 | 6132 | 12334 |

### Ear metrics

| pool | n | mean words | median words | p90 words | share > 30 words | share with dash | share ending closed | mean jargon/line |
|---|---|---|---|---|---|---|---|---|
| spoken (tool_result + push) | 136 | 23.3 | 8.5 | 64.5 | 36.8% | 44.9% | 16.9% | 0.46 |
| brain lines dropped (push_late, added:false) | 36 | 68.8 | 70.5 | 88.5 | 97.2% | 100.0% | 36.1% | 1.92 |

### Repeat-ask rate

5 / 48 utterances (10.4%)

### Lost-utterance rate

2 / 35 utterance boundaries (5.7%). voice_initiative_ignored: 2. echo_ignored: 2.

### Brain-line drop rate

48 / 61 brain.push_late dropped (78.7%): 30 needs_user, 18 not, 12 with empty spoken text.

brain.fallback by reason:

- cancelled: 1
- timeout: 14

### Ctrl-C sends

1 sends, 1 preceded by a stop word within 5s.

- 2026-09-24T00:13:18Z pane=wJ:p2 preceded_by_stop=true trigger="Oh, never mind."

### Comprehension follow-ups

3

### Duplicate speech

2 instance(s) within 10s sharing ≥ 60% of words.

- push "discoursecopilot-claude started on \"Please cut a release of the current main (the UI…\"." at 2026-09-23T16:36:58Z / tool_result "Sent it: discoursecopilot-claude is to cut a release from the current main — work out the next version from what's changed, update the version and release notes, run the tests first and only proceed if they pass, then commit, tag and push, and report back with the version, files and anything it needs a decision on. Shall I let you know when it's done?" at 2026-09-23T16:37:00Z
- push "ucsummary-codex finished." at 2026-09-23T17:08:54Z / tool_result "Told ucsummary-codex Skip." at 2026-09-23T17:08:55Z

## Sanity check against the hand count

The brief gives five hand-counted numbers to check against. Three match
almost exactly; two need an explanation.

| hand count | scorecard | match |
|---|---|---|
| 30 needs_user=true, not added | `DroppedNeedsUser` = 30 | exact |
| utterance brain.turn mean ≈ 7.0 s | 6970 ms | exact (within 1%) |
| announce brain.turn mean ≈ 5.8 s | 6156 ms | close (6% over) |
| 59 brain.push_late total | 61 total | close (2 more) |
| 171 spoken lines > 40 chars, mean 39 words, p90 76, 65% with a dash | see below | pool size differs, quality metrics don't |

The "spoken" pool is the two channels docs/dev/iteration-2.md names
(`voice.tool_result` with a `spoken`, `voice.push` not silent): 136 lines,
81 over 40 characters. The hand count likely also counted
`voice.transcript.out`; the quality numbers still match (mean 37.7 words,
p90 73, 75 % with a dash). Time to first word's mean is distorted; see
"Not measurable as logged today".

## Splitting by session

`--session` (splitting at each `daemon.start`) finds 9 daemon runs across
the two days and reproduces the same per-metric shapes at smaller N; it
is otherwise not reproduced here in full to keep this baseline readable.
Command:

```
sheepdog scorecard --events-dir /Users/kc/.local/share/sheepdog/events \
  --from 2026-09-23 --to 2026-09-24 --session --format md
```

## Not measurable as logged today

- **Time-to-first-word's mean (136 s) and p90 (24.6 s) are pulled far
  above the median (4.0 s)** by a handful of `voice.tool_call`s that
  landed with no fresh `voice.transcript.in` immediately before them (a
  fast-path tap answer, or a call long after the shepherd's last
  utterance while a conversation sat idle). The metric as
  docs/dev/iteration-2.md defines it ("time from the last
  voice.transcript.in before it to the first voice.transcript.out after
  it") has no way to tell "the shepherd just finished speaking" from "the
  shepherd spoke minutes ago and this call is unrelated to it" — both
  produce the same shape (a transcript.in, then eventually a tool_call).
  **Suggested fix**: a `caused_by` or `utterance_id` field on
  `voice.tool_call` linking it to the specific transcript segment that
  produced it (Gemini/GPT-Live both know this at the point they emit the
  tool call) would let the scorecard exclude a call with no recent
  utterance instead of guessing from adjacency.
- **`voice.transcript.in.final` is essentially never `true`** in these
  two days (0 of 277 `voice.transcript.in` events checked). The
  lost-utterance and repeat-ask metrics fall back to "the last fragment
  before a `voice.turn`" as docs/dev/iteration-2.md anticipates, but this
  means the daemon's own utterance-boundary signal is not really in the
  log; a `voice.turn`-keyed "here is the utterance text for this turn"
  event (rather than reconstructing it from fragments) would remove the
  guesswork.
- **No `voice.tool_call{name:"stop"}` or Ctrl-C-adjacent stop events
  occurred in this window** beyond the one Ctrl-C send found, so J6
  ("never Ctrl-C on 'never mind'") is close to unverified by this
  baseline; the one instance found was in fact preceded by a stop word.
  A future session that exercises the appendix script (WP0c) will give
  this metric a real sample size.
- **Duplicate-speech and Ctrl-C sections are small-N** (2 and 1
  respectively) in this window, too few to call a rate; they are useful
  as a spot-check list (both rendered instances above read as genuine
  near-duplicates) but not as a trend yet.
