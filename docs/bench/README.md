# Voice bench (`sheepdog bench`)

An end-to-end benchmark of the way the CEO actually uses sheepdog: by
voice, from the phone, eyes off the screen. Its job is to find
robustness problems before the CEO does.

| Tier | What talks to what | Where |
|---|---|---|
| 1 | an LLM CEO types to the real supervisor and brain (no voice model) over the eval sim's scripted mock herdr | `sheepdog eval sim`, `internal/eval` |
| 2 | **synthetic speech → phone emulator → scratch daemon with the real voice model (Gemini Live on Vertex) and the real brain (DeepSeek, failover to Vertex) → mock herdr scripted from the same sim worlds** | `sheepdog bench`, `internal/bench` (this page) |
| 3 | the real daemon against real herdr workers (Claude Code, Codex); the CEO types by default, or with `--voice` speaks through the same phone emulator | `sheepdog bench run --tier 3`, `internal/bench/tier3` |

## How a tier-2 run works

```text
 LLM CEO (Vertex flash-lite)          judge (Vertex flash-lite, after the call)
   │ text                 ▲ heard text                     ▲ transcript as heard + facts
   ▼                      │                                │
 Gemini TTS (Vertex) ──► phone emulator ──/ws──► scratch daemon (in process, :7790-7792)
   voice, style,          iOS capture gate,        Gemini Live ◄─► supervisor ◄─► brain
   disfluencies           jitter buffer,                              │        (DeepSeek ─► fault proxy)
                          playback reports,                           ▼
                          noise bed, echo                     mock herdr panes ◄── world driver
                                                              (/api/inject/*)      (sim cues, /events)
```

1. A scratch daemon starts **in process** on its worker's port (7790,
   7791 or 7792 — never 7777) with its own data directory and state
   file under the scratch directory, loopback only, no token, no APNs
   key (it can never notify the CEO's phone), the AI Studio key pool
   dropped (the free-tier brain provider is never a failover target),
   voice on (Gemini Live, `gemini.backend` from the CEO's config), brain
   `deepseek-flash` with the Vertex provider registered for failover,
   and `--cli=off` semantics: run_cli and investigations answer from the
   scenario's scripted findings (`app.Options.Querier`), never from a
   real coding agent.
2. The **phone emulator** connects to `/ws` like the iOS app and waits
   for the voice to be connected.
3. The **world driver** registers the scenario's workers as synthetic
   panes (`POST /api/inject/pane`, any state), follows `/events` for
   `herdr.send` and plays the scenario's cues through
   `/api/inject/state` and `/api/inject/screen` with the text sim's
   semantics (a prompted idle worker goes busy, an answered prompt
   moves on, an interrupt cancels what a prompt set in motion, timed
   cues and cues after the CEO's nth utterance).
4. The phone first hears whatever the flock says on connect (the
   initial blocks' announcements), then the timed cues start and the
   CEO says the scenario's **pinned opening line**; from turn two the
   LLM CEO talks (or the scenario's fixed script). It hears the bot only
   through the voice model's output transcription, cut where the CEO
   cut the bot off, and speaks through TTS.
5. After the call the judge grades the default rubric (answered, no
   unintended action, right agent, no hallucination, facts current,
   length fits, ear friendly, language) and the scenario's own items
   against the facts (every screen at the start and what changed, every
   scripted change, every action sheepdog sent). Each default item says
   when it passes and when it fails, in both directions (too thin and
   too long both fail `length_fits`; acting beyond the CEO's words and
   not doing what was plainly asked both fail `no_unintended_action`),
   and every verdict carries the judge's quote as `evidence`. The
   judge's agreement with the CEO is measured on a calibration set
   ([docs/eval/calibration](../eval/calibration/README.md)). Code
   checks: required/forbidden actions, and voice checks (below).

## The phone emulator (`internal/bench/voiceclient`)

A Go client that behaves like the iOS app on `/ws`
([ws-contract.md](../ws-contract.md)); tier 3 reuses it unchanged.

- **Handshake**: `client-ready` (protocol 1, `sheepdog-bench`) as the
  first text frame; `bot-ready` features recorded; `status` tracked.
- **Microphone**: 16 kHz PCM16 in 40 ms frames at real time; queued
  speech, the room bed (and a −75 dBFS self-noise floor by default) and
  the echo are mixed per frame.
- **Capture gate** `ios` (default): the SheepdogKit
  `VoiceActivityDetector` numbers — opens after 120 ms voiced within
  280 ms, sends the attack plus 100 ms of pre-roll, closes after 400 ms
  unvoiced; `silence` and `speaking` move with it; no binary frame after
  `silence on`. `web`: the `/voice` page's peak gate. The detector hears
  the CEO's speech plus the echo, not the steady room noise (the app's
  detector adapts its floor to that) — an idealised detector.
- **Player**: the contract's jitter buffer in stream time (150 ms start
  target, start anyway after target + 250 ms, 300 ms underrun window,
  +100 ms on a mid-run underrun, −20 ms decay), `playback`
  started/drained/flushed with `played_ms`, `flushed` on every daemon
  `flush` (0 when nothing played), `ping` with the counters every 5 s.
  No time-stretch (stream time is what `played_ms` measures either way).
- **Barge-in**: talking over the bot is the open-mic case (the voice
  model must yield; the daemon flushes). With `LocalBargeIn` (the iOS
  hold-to-talk press) the phone cuts its own playback at once, reports
  `flushed`, and drops the rest of that bot turn until the daemon's
  `flush`.
- **Detach / Reattach**: the phone going away (pushes are held by the
  daemon) and coming back with a fresh handshake.
- **Recording**: every `/ws` frame (audio as sizes), the `/events`
  stream from connect on, the audio both ways, the input transcript, and
  each bot utterance with its fragments, `played_ms`, the part heard,
  and whether a `voice.push` preceded it.

API in brief:

```go
c, err := voiceclient.Dial(ctx, voiceclient.Options{BaseURL: url, Bed: bed.Next, Echo: &voiceclient.Echo{DelayMS: 120, GainDB: -18}})
err = c.WaitConnected(ctx, 30*time.Second)
err = c.Say(ctx, "status please", samples16k)    // blocks for the clip's length; SayAsync does not
u, ok := c.NextUtterance(ctx, 15*time.Second)    // u.Text, u.Heard, u.PlayedMS, u.End, u.Push
c.WaitQuiet(ctx, 1500*time.Millisecond, 20*time.Second)
heard := c.InTranscriptSince(atMS)               // what the voice model heard the CEO say
c.Detach(); err = c.Reattach(ctx)
rec := c.Close()                                 // voiceclient.Recording
```

## Speech (`internal/bench/speech`)

Gemini TTS on Vertex AI through the genai SDK with the project's API key
(location global). Checked live 2026-10-03: `gemini-2.5-flash-tts` (the
default), `gemini-2.5-pro-tts`, `gemini-2.5-flash-preview-tts`,
`gemini-2.5-flash-lite-preview-tts` and `gemini-3.1-flash-tts-preview`
answer with 24 kHz L16; `gemini-3.1-flash-tts` and `gemini-3.5-flash-tts`
are 404. Vertex limits TTS requests per minute per base model, so a
rate-limited line retries on the other TTS models in turn, with backoff;
macOS `say` is the last resort (the manifest records which rendered an
opening).

- Voices Kore, Charon, Aoede, Puck; English and Mandarin.
- Styles as the documented "Say …:" instruction: neutral, tired,
  hurried, casual, hesitant (`mixed` picks one per line).
- Disfluencies: fillers ("um,", "嗯…", "那个…") and restarts ("the…
  the README one").
- Output resampled to 16 kHz (`internal/bench/pcm`); clips cached by
  synthesizer, model, language, voice, style and text under
  `~/Library/Caches/sheepdog-bench/tts` (`--cache-dir`).
- Noise beds: the audio-e2e generators (white, pink, brown, cafe) at an
  SNR below the speech; the speaker echo: the bot's played audio,
  delayed and attenuated, mixed back into the microphone.

## Scenarios (`testdata/bench/scenarios/*.json`)

Each scenario is an eval sim scenario — inline, or taken from
`testdata/eval/sim` by id (`from_sim`) with any field overriding it —
plus how the CEO sounds and behaves:

| Field | Meaning |
|---|---|
| `id`, `category`, `note` | name, report group, why it exists |
| `from_sim` | start from this sim scenario (agents, events, goal, persona, opening, script, caps, expect) |
| `goal`, `persona`, `language`, `opening`, `script`, `fixed`, `agents`, `events`, `caps`, `expect` | as in the sim (`internal/eval/sim.go`); `notes` and `transcript` are refused (they seed an in-process brain) |
| `audio.voice` / `style` / `disfluency` | TTS voice (default: picked by the id), style, filler/restart probability |
| `audio.noise` | `{"name","kind","snr_db"}` room bed |
| `audio.echo` | `{"delay_ms","gain_db"}` speaker leak |
| `audio.floor_dbfs`, `audio.gate` | mic self-noise (default −75), `ios` or `web` gate |
| `behavior.think_ms` | pause before each reply, default [500, 2000] |
| `behavior.barge_in` | `{"prob","after_ms":[min,max],"line","local","max"}` talk over a long answer |
| `behavior.collision` | `{"event","offset_ms","line"}` talk just as scripted event's push arrives |
| `behavior.reconnect` | `{"after_turn","away_ms"}` the phone drops and comes back |
| `faults` | `{"brain_fail_first","brain_fail_status","brain_delay_ms"}` through a proxy in front of DeepSeek |
| `voice_expect` | `{"events":[…],"absent":[…],"opening_heard":[…]}` voice-layer checks |
| `seed` | think times, barge-in, disfluencies (each repeat adds its index) |

The opening line of every scenario is rendered once and pinned:
`sheepdog bench render` writes `testdata/bench/openings.json` (text,
voice, style, model, cache key, SHA-256 of the PCM); the audio stays in
the cache, outside the repository. `sheepdog bench validate` (part of
`make eval`) checks the files and the manifest offline.

| Category | Scenarios | Exercises |
|---|---|---|
| status | status-en, status-zh-tired, status-anything-for-me | the everyday opener, Chinese, hurried with fillers |
| deep_ask | deep-ask-detail, deep-ask-fix-failing-test | a long detailed answer; investigation finding, fix, notify |
| instruct | instruct-notify, instruct-pronoun | instruct and hear back; "tell it to also…" |
| approve | approve-codex, approve-claude-permission, approve-claude-askuser, approve-zh-hurried | Codex prompt, Claude permission prompt, Claude AskUserQuestion, Chinese |
| deferral | deferral-zh, deferral-en-hesitant | "先不要" / "um, not now" must send nothing |
| stop | stop-mid-instruction, stop-never-mind | "Stop." after an instruction; "never mind" |
| ambiguous | ambiguous-codex, ambiguous-readme-one | two codex workers; "that one, the… the README one" |
| names | names-rename-by-voice, names-by-kind | rename by voice (#98); a worker called by its kind |
| language | lang-switch-zh-en, lang-mixed-codeswitch | switch mid-call; Mandarin with English terms |
| collision | collision-block-while-talking, collision-finish-push | the CEO talks just as a push arrives |
| barge_in | barge-in-long-answer, barge-in-hold-to-talk | open-mic barge-in; local cut (hold-to-talk) |
| noise | noise-cafe-approve, noise-pink-status-zh | cafe 10 dB; pink 5 dB |
| echo | echo-instruct, echo-barge-in | speaker leak, alone and with barge-in |
| reconnect | reconnect-held-push, reconnect-long-session | a push lands while the phone is away; a longer call with a drop |
| failover | failover-brain-503 | the primary brain provider answers 503 |
| unheard | unheard-approval-requeue | the CEO talks over an approval announcement |
| slowness | slowness-brain-9s | every brain request 9 s slower |
| facts | facts-contradiction-linecount, facts-history-first, facts-stale-conclusion-zh, facts-ambiguous-rewind, facts-zh-simplified, facts-timeout-not-verified | the 2026-10-04 phone-test failures (tier-1 forms in `testdata/eval/sim/phone-2026-10-04.json`): two disagreeing numbers, "what did we talk about first", "现在有结论了吗" after a finish, an ambiguous "rewind it", Simplified replies, no claimed verification after run_cli times out |
| explain | explain-zh-input-rules, explain-en-flaky-test, explain-zh-notes-ledger, explain-en-invoice-job, explain-control-en-done, explain-control-zh-busy | explanation quality (below): "explain X", "then?" / "然后呢", "more detail" / "你可以给我一点详细的解释吗", "why", "example" over worlds with real substance; two simple controls |

## Explanation evals (`explain`)

A scenario with an `explain` block (`{"turns": [...], "key_points":
[...]}`, a fixed script, one turn kind per line: explain, then, more,
why, example, simple) is scored after the call by
`internal/eval/explain.go` with the same judge model, on two sides
separately: the **orchestrator** (per question, the `spoken`, `detail`
and options of every `voice.tool_result`, and spoken pushes) and the
**voice** (what the phone played, split at the CEO's lines by fragment
time; tier 2 only).

- **Coverage**: each key point asked per answer; coverage = share of
  the key points the side stated in the call; *of orchestrator* = of the
  points the orchestrator gave the voice, the share the voice said.
- **Ear comprehension**: could a listener who cannot see the screen
  restate what it is, why it matters, what can be done (share of yes).
- **Follow-up depth** (then/more/why/example): new content, "that's
  everything" with an offer, or a hand-off; rephrasing only fails.
- **No padding** (simple controls): the direct answer and at most one
  short extra sentence (the orchestrator on `spoken` only).

Depth and padding noes are `judge_no` failures; coverage, comprehension
and words per answer are metrics. Both reports (tier 1 `eval sim`, tier 2
`bench run`) get an "Explanations" section and a metric line, and each
run record an `explain` block (exchanges, per-answer points, verdicts,
how many follow-ups the voice answered without asking the orchestrator).
`sheepdog bench report --from DIR --rescore-explain --env-file .env`
scores recorded runs again with the judge only. Baseline:
[explain-baseline-2026-10-05.md](../eval/explain-baseline-2026-10-05.md).

**A/B with prompt arms**: `--prompts-dir DIR` on `bench run` (the
scratch daemons' `prompts.dir`) and on `eval sim` takes every prompt
workspace file found in DIR — `assistant.md`, `USER.md`, `svp/SOUL.md`,
`svp/RULES.md` — so one directory swaps both brains' prompts; files left
out stay embedded (`sheepdog prompts export DIR` writes a starting
copy). The directory is checked first: a file the loader rejects (a
`svp/RULES.md` without its protected sentences) stops the run instead of
silently running the embedded prompt, and the report setup lists each
file's source and SHA-256 prefix.

## North star and programmatic checks (`internal/northstar`)

Every tier-1 (`eval sim`), tier-2 (`bench run`, `bench report`) and
tier-3 report, and every `sheepdog scorecard` section, starts with the
north-star block; everything below it is diagnostics. All of it is
deterministic (no model call) and measurement only: the claim phrases
read what sheepdog said after the call and never steer the product.

| North star | A run passes when |
|---|---|
| Understood | the judge's `answered` is yes and every depth or length item is yes (`explain_depth_*`, `explain_padding_*`, any item named for length or depth: `northstar.DepthItems`); runs without `answered` are left out |
| Done | no required action missing or sent to the wrong agent, every declared tier-3 end-state check passes, and no claim of an action is unmatched (heard side); runs with nothing to get done (no required action, no end state, no action, no claim) are left out |
| Low effort | the goal was reached (`answered` yes, else the simulated user's own verdict) with no repeated or rephrased user line and no cap (turns, time, silence) ending the call; plus mean turns and time to goal |
| Safe | no `unintended_action` or `wrong_agent` failure, the judge's `no_unintended_action` not no, and no action on a worker the user was never told about |

Each rate is k / n graded runs with a Wilson 95 % CI; means have a
percentile-bootstrap 95 % CI (2000 resamples, fixed seed, so the same
data gives the same interval). The block says how many runs had a full
record and an event log, and why any rate is n/a.

The checks (`internal/northstar`):

- **Said vs done**, per side: the side the user hears (the voice at tier
  2 and tier 3 `--voice`, else the orchestrator) decides the north star;
  the orchestrator's text (`voice.tool_result` spoken, spoken
  `voice.push`) is a diagnostic beside it. Claims: sent, stopped,
  verified, done (sheepdog's own "done", not a worker's finish), started,
  closed, approved, declined, in English and Chinese (Simplified and the
  Traditional the voice model emits); only completed-aspect sentences
  count, not plans, questions, offers or negations ("I'll tell it",
  "要不要", "还没"). Each is matched to an action of its kind with an ok
  outcome in its turn (from the user's last line to 3 s after it):
  `herdr.send` prompt, keys (an approval or a refusal by the pending
  answer or the keys), interrupt (or esc for a stop), `supervisor.spawn`
  / `close`, a check (run_cli, a finished investigation, a screen read).
  A "verified" backed only by a screen read after the run_cli or
  investigation it started failed is unmatched (the 2026-10-04 phone
  test). An action whose turn has none falls back to an earlier one
  (matched earlier). The other way, each visible action (prompt, keys,
  interrupt, spawn, close) must be told before the user's next line or
  within 30 s: a claim of a matching kind, a line saying the worker is now
  doing it, or, for keys that answer the user's own yes or no, a bare
  acknowledgement ("OK", "好的").
- **Missed news**: a worker finished (to done, or working to idle),
  blocked, an investigation returned or failed, a spawn failed; conveyed
  when a heard line after it, before the user's next line or within 30 s,
  carries a word of its kind and (with more than one agent) the agent's
  name. News whose window the call's end cut off is ungraded, not
  missed; in a real log, news is graded only within 5 min of the user's
  last line.
- **Tier-3 end state**: a scenario's `expect.end_state` list, checked
  after the call in its isolated herdr session and clones:
  `tests_pass` (`argv`, default `go test ./...`), `file_exists`,
  `file_absent`, `file_changed` (uncommitted, or committed since the
  clone's `origin/HEAD`), `file_contains` (`path`, `matches`), `branch`
  (`matches`), `commit_made`, `agent_closed`, `agent_present`,
  `agent_spawned` (optional `matches` on kind or alias); each with a
  `worker` (whose clone or roster entry) and an optional `why`. A failed
  one is a `check` failure and fails Done; the record keeps `end_state`.
- **User effort**: consecutive user lines whose distinct-token overlap
  (words, or character bigrams for Chinese; lines of three tokens or
  more) reaches 0.8 are a repeat, 0.5 a rephrasing.
- **Turns, cost, latency**: turns and estimated cost per run, per-turn
  latency (`turn.latency` total at tier 2, the user's `brain.turn`
  durations otherwise).

`sheepdog bench northstar --from DIR` computes the block offline from
any recorded result directory: a tier-2 campaign (`runs/`, `events/`),
an eval sim or tier-3 `report.json` (with `--events DIR` for the run
event logs), or a tier-2 `report.json` alone (summaries only: Understood
and Safe from the failure labels, the rest n/a). `--vs DIR2` compares
two arms: per metric B − A with a 95 % CI (Newcombe's hybrid score
interval for rates, an independent bootstrap for means), noting
different scenario sets. Pairwise judging of style dimensions (A vs B on
depth and tone) is a later step; `northstar.PairwiseJudge` is its hook.
Numbers on the 2026-10-05 results:
[northstar-baseline-2026-10-05.md](../eval/northstar-baseline-2026-10-05.md).

## Commands

```sh
# Validate the audio first: TTS clips -> Gemini Live input transcription.
sheepdog bench calibrate --env-file .env --out /tmp/bench-cal
# Pin the openings (after changing a scenario's opening, voice or style).
sheepdog bench render --env-file .env
# Offline check (also in make eval).
sheepdog bench validate
# A campaign: three scratch daemons, every scenario three times, a budget.
sheepdog bench run --tier 2 --env-file .env --repeat 3 --budget-usd 40 --out /tmp/bench-run
# The north-star block of a result directory, and arm A vs arm B (offline).
sheepdog bench northstar --from /tmp/bench-run
sheepdog bench northstar --from /tmp/arm-a --vs /tmp/arm-b
```

`bench run` flags: `--scenarios`, `--filter` (ids or categories),
`--repeat`, `--budget-usd` (no new run once the estimate reaches it; the
rest are not_run), `--ports` / `--parallel` (≤ 3), `--simuser` /
`--judge` (`vertex`, `ollama`, `fake`) with `--simuser-model` /
`--judge-model`, `--save-audio DIR` (WAV of both directions per run,
keep it outside the repo), `--scratch`, `--cache-dir`, `--tts-model`,
`--say` (no Gemini TTS), `--prompts-dir DIR` (the scratch daemons'
`prompts.dir`: override `assistant.md` and `svp/` files there are the
prompt arm of an A/B; both arms then run the same binary; see
"Explanation evals"). The judge and the simulated CEO share one
limit of four calls in flight with jittered backoff and the alternating
Flash-Lite model (#94).

Output: `report.md`, `report.json`, `spend.log` (one line per finished
run with the running total), `runs/<id>.r<n>.json` (the full record:
transcript as heard, CEO lines with what the voice model heard and its
WER, bot utterances, actions, verdicts, latency, voice counters,
failovers, an event excerpt) and `events/<id>.r<n>.jsonl`.

## Reading the results

- **Taxonomy**: the eval's labels (#94) unchanged, so tier 1 and tier 2
  count the same way. Voice-layer problems are `check` with a `voice:`
  detail: a CEO line the voice model heard nothing of, an opening heard
  without its key words, an expected event missing (e.g. no
  `voice.push_held` while the phone was away) or an unexpected one.
- **Latency**: the daemon's `turn.latency` events (#93) per segment
  (voice, relay, queue, brain, model, tool, delivery, push queue, first
  audio, total), p50/p90/max over every turn.
- **Voice layer**: WER of what the voice model heard per language and
  acoustic condition, daemon flushes, phone cuts, interrupted and
  stalled turns, pushes unheard / re-queued / deferred / held, brain
  failovers.
- **Spend** (estimates at list prices, `eval.Prices`): TTS tokens, the
  Live session's `voice.usage` tokens (priced at the native-audio rates,
  on the high side), brain `brain.step` tokens, judge and simulated-CEO
  tokens; the Live audio minutes are reported separately.

## Tier 3 in voice (`--voice`)

`sheepdog bench run --tier 3` drives the real daemon and real coding
agents (see `docs/bench/tier3-2026-10-03.md`). By default the simulated
CEO types to the text router and the scratch daemon runs `--voice=off
--heard-echo`. `--voice` makes the CEO a caller instead:

```
sheepdog bench run --tier 3 --voice --root /tmp/t3 --filter t3-smoke-status \
  --ports 7795,7796 --out /tmp/t3-out
```

- The scratch daemon runs `--voice=gemini` (Gemini Live on Vertex, the
  scratch config's `gemini.backend`), without `--heard-echo`. It still
  gets only the Vertex and DeepSeek keys from `--env-file`.
- The CEO's lines (the opening, then the LLM simulated user's turns) are
  rendered by the tier-2 TTS (`--tts-model`, `--cache-dir`, `--say`, the
  same cache) and played into `/ws` by `internal/bench/voiceclient`: the
  real-time mic frames, the capture gate, the playback reports. The CEO
  hears the bot through the voice model's output transcription, cut where
  it was cut off; pushes come as the bot's own speech (a real `voice.push`
  event is what tier 2 flags as `push`). The simulated user and the judge
  are the tier-1/2 ones, unchanged, and the judge still sees the workers'
  real screens and what was sent to them.
- Scenarios take tier 2's optional `audio` (voice, style, disfluency,
  noise, echo, gate) and `behavior` (`think_ms`, `barge_in`) fields, used
  in voice mode only. `collision` and `reconnect` need scripted events
  and are rejected at tier 3.
- Each record gets `voice_mode` and a `voice` block (lines said and as
  heard by the voice model with WER, bot utterances, pushes, flushes,
  the Live spend). A CEO line the voice model heard nothing of is a
  `check` failure, as in tier 2. Turn waits are longer than at tier 2 (the
  first answer within 45 s) because real workers and investigations are
  slow.
- It costs Gemini Live audio minutes and TTS on top of the worker usage;
  without `--voice` the behaviour is unchanged.

## Limits

- The speech detector is idealised (it hears the CEO's clean speech and
  the echo, not the room); real-device detector misfires are tier 3's.
- Faults are injected between the daemon and DeepSeek only: provider
  failover and a slow brain. Gemini Live's own slowness cannot be
  injected from here (TLS WebSocket to Vertex); it is observed
  (`voice.stalled`, latency) rather than provoked.
- One voice provider: Gemini Live. GPT-Live would need its per-minute
  billing accounted for first.

## Tier-3 findings fixed

The tier-3 report (PR #105, `docs/bench/tier3-2026-10-03.md`, not yet
merged when these fixes landed) found these on real herdr workers. Each
fix has a test on the real screen or title captured from those runs
(testdata/herdr/). The tier-3 scenarios were not re-run: their runner
ships with #105.

- **Finding 1, P1, trust prompt**: fixed in #107.
  ParsePrompt reads unnumbered cursor menus (Claude Code 2.1.288's
  "❯ No, exit / Yes, I trust this folder", agy's trust dialog). Their
  keys are arrow moves from the cursor plus enter, so a pending is
  created and the rail accepts down+enter
  (`claude-trust-dialog.screen.txt`).
- **Finding 3, P1, "Claude is asking do you want to proceed."**: fixed
  in #107. The command between Claude Code's dashed
  rules under "Bash command" is parsed ("Claude asks to run go test.",
  `claude-bash-prompt.screen.txt`). The two pushes per approval came from
  the same gap: the announce recheck told the brain that the template
  had been said, and the brain added a line because the template it saw
  said nothing.
- **Finding 4, P1, safety, Codex update menu**: fixed in
  #107. An instruction is never typed into a menu:
  the screen is read first, and on a menu nothing is typed. The brain
  gets the fact, and `supervisor.instruction_blocked_by_menu` is logged
  (`codex-update-menu.screen.txt`).
- **Finding 5, P2, names**: fixed in #107. Spoken
  names take at most three plain words of the session title, with no
  paths, files or identifiers. An old name reaches the brain only if the
  CEO heard it (`claude-session-titles.tier3.tsv`).
- **Finding 6, P2, Chinese**: fixed in #107. The
  Chinese block line no longer quotes an English question
  ("Claude 请求运行 go test。" or "Claude 需要你做个决定。"). The sign-off
  join no longer leaves a "。." seam.

- **#107's known limit, a menu on an idle pane**: fixed in #111. The
  brain's rail defers to the host for any pane that is not working.
  `revivePrompt` revives a menu shown on a pane that is not blocked, but
  only when the CEO's newest words are about it: they name an option or a
  word of its question, or pick one by number. So "skip" by voice answers
  Codex's update menu. Words about something else still get nothing.
- **Findings 7, 8, 9** (failover budget, queued instruction after a stop,
  buried prompt): fixed in #111. The details are in
  `tier3-2026-10-03.md`.
