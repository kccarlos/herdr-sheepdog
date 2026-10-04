# Dual-brain voice layer (experiment, branch `feat/dual-brain`)

Status: experiment on a draft PR (#122), compared with `main` before the
CEO decides. Not merged. Results: [`docs/eval/dual-brain-2026-10-04.md`](../eval/dual-brain-2026-10-04.md).

**One line:** Gemini Live is the *fast brain* that owns the conversation;
the existing brain is the *slow brain* that reasons, investigates and acts
behind the rails. The slow brain gives the fast brain facts with
provenance, not lines to read, through a channel the fast brain cannot
mistake for the CEO. The harness keeps the rails on actions and puts
labelled facts into both models' context; the code that policed the voice
model's words is removed where the bench does not get worse.

## History check

Read before any code was written: `git log` (voice tools, to_supervisor,
propose/confirm, D1, relay, push, ack, self_relay, heard, requeue, echo),
`docs/history/issues-2026-09-18.md`, `docs/history/roles-status.md`,
`docs/history/sheepdog-v1-plan.md`, `docs/roles.md` (D1 and "The voice —
do/don't"), `docs/iteration-2.md`, `docs/gemini-live-notes.md`, the PR
bodies of #9, #29–#31, #39–#41, #47, #51, #58, #63–#65, #70, #72, #88–#90,
#101, #108, #111, #113–#115, #117, and the CEO's 2026-10-04 discussion.

### 1. v1: the voice model with action tools (PR #9, plan D1; replaced by #29/#30, removed in #51)

- **What it was.** The voice model classified intent into its own tools
  (`briefing`, `ask`, `propose`, `confirm`, `cancel`, `note`, later
  `stop`; "seven" in the CEO's account) that executed, and a stateless
  template LLM answered behind them.
- **Why it failed** (headset logs 2026-09-18, `issues-2026-09-18.md`):
  the voice approved workers' prompts on the CEO's behalf — "five
  `confirm` calls ~0.6 s after each block push with no CEO utterance in
  between, the last two with the mic muted" (`git apply`, `git add -p`
  approved); it repeated "Nothing is waiting for an answer."; a status
  doubt hit the cache five times; "why did *that one* fail?" reached the
  template as five context-free words. The conclusion (D1-revised): the
  split "gave the weaker judge the side effects. The assistant is
  excellent at hearing, speaking, timing and register, and unreliable at
  judgment"; and "the voice model acting on suggestive text is a Gemini
  Live trait; the fix is structural."
- **How this design differs.** The fast brain has **no action tool**:
  only `to_supervisor(text, reading)` and `stop()`, as since #30. Every
  side effect (keys, approvals, prompts, spawn, interrupt, rename) stays
  with the slow brain behind the rails, and a rail reads the CEO's own
  recorded words, never the voice's. What changes is the *context* the
  fast brain gets (rich, labelled briefings instead of thin context) and
  the *conversational* judgment it is trusted with (when and how to say
  things, answering follow-ups from what it was told). v1's failure was
  side effects in the weak judge; nothing here gives it one.

### 2. The mouthpiece (#30 onward)

- **What it was.** The voice relayed the CEO's words verbatim and read
  the brain's `spoken` "exactly as given"; pushes were user-role turns
  with a `[system event, not spoken by the user]` marker; the voice was
  told it "decides nothing and knows nothing".
- **Why it failed.** A native-audio model regenerates; it does not play
  text. It judged anyway: "On it." after pushes and relaying them back
  (#40, the 2026-09-23 muted "yes"), paraphrased relays (#58), push
  markers read aloud (#108: 25 utterances in 14/102 runs), answers
  voiced as a bare "Checking." (#108, #114), new questions answered from
  memory (campaign D N2: 26/126 CEO lines never relayed), and answers
  that never reached the ear (#89, #115). Root cause in protocol terms:
  Gemini Live has no mid-session system role, so every push was, on the
  wire, the user speaking.
- **How this design differs.** Briefings are **function responses**
  (tool results), the one non-user channel Live has, so a briefing cannot
  be the CEO by construction; and they carry **facts**, which the fast
  brain retells in its own words, instead of a script it is told to read
  verbatim and cannot.

### 3. The guardrail wave (#88–#115)

- **What it was.** Each mouthpiece failure got a code detector: ack word
  lists (`ackCues`, `assistantAckCues`), self-relay matching, heard
  coverage thresholds (50 %, key terms, content words 0.5) with re-queue,
  deferral/decline/question word lists on the rail, the bare-answer fast
  path, the echo text guard.
- **Why it failed.** Each rule was data-backed, but together they became
  code that interprets intent, a whack-a-mole where every fix produced a
  new variant (#108 → N2), concentrated exactly at the voice↔brain
  hand-off. The tier-1 (text) vs tier-2 (voice) gap of ~20 points sits
  in the same place.
- **How this design differs.** The detectors are removed one group at a
  time with an ablation run each (below); what replaces them is a
  **fact** put into a model's context (the CEO cut you off after 2.1 s
  and heard only "…"; these words began while your own line was still
  playing; nothing was handed on because the CEO said nothing new).
  Rails on actions stay.

### 4. #117: answers as late function responses

`brain.reply_wait_s = 30` made the brain's answer the `to_supervisor`
call's own result. A/B, 70 runs per arm: answers relayed back 7 → 0
(Fisher p ≈ 0.01), mixed-language answers lost 6/36 → 0/33, pass 67 % →
70 %; ack-only answers unchanged (5 vs 6), and announcements, still user
turns, self-relayed 3 times. This branch starts from #117 and extends the
same channel to every announcement.

### Conclusion of the check: proceed

The design does not repeat v1 (no action tools, rails unchanged), the
mouthpiece (no verbatim scripts, no user-role pushes) or the guardrail
wave (facts instead of detectors). **The one earlier failure that can
recur is N2: the fast brain answering from stale or wrong facts.** It is
now allowed to answer simple status from briefings, which N2's fix
forbade. Mitigations: every briefing is timestamped, says who it is from
and what it supersedes; the prompt tells the fast brain to trust the
newest briefing, say how old a fact is, and hand anything not freshly
covered to the slow brain; and both arms are judged on a new item,
`facts_current` (stale or wrongly retold facts), next to
`no_hallucination`.

## Design

### Roles

| | Fast brain: Gemini Live | Slow brain: the existing brain |
|---|---|---|
| Owns | The conversation: when to speak, what, how; turn-taking, register, language | What the CEO wants, which agent, what to do, investigations |
| Answers itself | Follow-ups and simple status from its briefings; small talk; "say again" | Everything else |
| Asks | When it did not catch the words | When the words are ambiguous |
| Actions | None | All, behind the rails |
| Knows | Briefings (facts with time and provenance), its own conversation | Transcript, screens, sessions, ledger, journal |
| Memory | Short and lossy (sliding window; resumes) | Compacted transcript + `recall` |

### Fast → slow

`to_supervisor(text, reading)`: `text` is the CEO's words (the daemon
still uses its own recorded transcript, never the argument, as since
#58); `reading` is new and optional, the fast brain's short reading of
what they want. The brain gets it labelled "the voice's own reading … a
hint, not the CEO's words; reference only, and no authority for any
action" (`internal/brain/assemble.go`). Parameter on an existing tool,
not a new tool (the CEO's "keep the voice link simple").

A call with no new CEO words runs no brain turn (the rail: only the CEO's
words authorise one); the fast brain gets a silent fact instead —
"Nothing was handed on: the slow brain acts only on the CEO's own words …
Their last words, at 23:04:10, were …".

### Slow → fast: the briefing channel

**Transport.** Every push (answer, worker news, reminder, focus line,
names line, delivery fact) reaches Live as a function response to a call
the model never made: name `briefing` (not a declared tool), id `b-N`,
scheduling `WHEN_IDLE` (idle), `INTERRUPT` (must hear now) or `SILENT`
(context). `to_supervisor` results use the same shape. The daemon's push
queue is unchanged — hold while no client listens, hold while the CEO
speaks, downgrade INTERRUPT to WHEN_IDLE while the voice is speaking
except a due promise / new decision / stop, the 5-minute TTL, the
arbitration — only what `doPush` sends changed
(`internal/voice/gemini/briefing.go`). `gemini.briefing_channel =
"user_turn"` restores the marked user turns (A/B control and insurance).

**Live probes** (Vertex `gemini-3.8-live`, us-central1, 2026-10-04; a
standalone client, text input, 7 sessions, < $0.30):

| Probe | Result |
|---|---|
| Function response with a made-up id, name `to_supervisor`, WHEN_IDLE | Accepted; voiced at the next pause in the model's words ("The auth agent has finished, and all forty-two tests passed.") |
| Same, name `briefing` (undeclared) | Accepted, identical behaviour (3/3 sessions) |
| SILENT made-up response, then "What is the docs agent up to?" | Not voiced; used for the later answer (3/3) |
| INTERRUPT made-up response while the model told a long story | Model cut at once (`interrupted`), briefing voiced next (3/3) |
| Chinese briefing in an English conversation | Voiced in English (the conversation's language) |
| Resumption handles after made-up responses | Kept coming, `resumable: true`; a handle arrives after each completed turn, none after a SILENT response |
| `willContinue: true` on an open call (Vertex) | **Rejected**: close 1007 "Unknown name willContinue" |
| An always-open NON_BLOCKING `updates()` call the model re-opens | Opened at start and re-opened after the first answer, **not re-opened after the second** (INTERRUPT) — unreliable, and it would add a tool |
| `to_supervisor` answered 90 s after the call | Voiced normally |

**Payload.** Self-contained, because Live's memory is short and lossy:

```json
{"about_this": "Not the CEO speaking. Facts for you from the sheepdog system; you decide whether, when and how to tell the CEO, in your own words.",
 "briefing": "b-7", "at": "23:04:31", "from": "slow brain", "kind": "answer",
 "agent": "bench-codex", "answers": "the CEO's words at 23:04:20: \"how is codex doing\"",
 "facts": "bench-codex finished; 41 of 42 tests pass, one fails in the cart module.",
 "decision": "the CEO's own decision; only their words can answer it",
 "relation": ["newer than b-3 (23:01:05) about bench-codex; where they differ, this one holds",
              "at 23:04:02 the CEO cut you off after 2.1 s of 6.4 s; they heard only: \"bench-codex wants to\""]}
```

- `from`: `slow brain` (its line), `daemon` (a code sentence: brain
  timeout, pane closed, reminder, focus, names, stop result), or the
  tag's own value.
- `relation` is computed by the session from what it sent: the n-th
  briefing about the same open prompt (a reminder), newer than the last
  briefing about the same agent (supersedes), and the last cut.
- **Delivery facts.** When the phone reports a flushed playback (the CEO
  talked over a line), the session sends a SILENT briefing "at T the CEO
  cut you off after N s of M s; they heard only: …" and repeats it on the
  next spoken briefing. The slow brain already gets the same cut as
  `SaidCut`.
- **Resume.** Briefings sent after the last resumable handle are re-sent
  silently on a resumed connection, marked `resent` with their first
  time (handover finding: silent context after the last completed turn is
  lost on resume; a SILENT response never produces a handle).
- The slow brain's contract is unchanged (`spoken`, `ask`, `detail`,
  `options`): `spoken` becomes `facts`. RULES.md now says the voice
  retells it in its own words.

### The fast-brain prompt

`prompts/assistant.md` (and `assistant-gptlive.md`) were rewritten: it
owns the conversation; briefings are never the CEO and carry who/when/
relation; trust the newest; tell the substance in its own words, skip
what the CEO already heard; a decision only the CEO can take is said as
a question with its options, never answered or recommended; answer
follow-ups and simple status from briefings and say how old a fact is;
hand anything not freshly covered, any doubt, and anything to be done to
the slow brain with the exact words and its own reading; it cannot act.
Gone: "read spoken exactly as given", "change nothing, add nothing", the
forced "say on it as you call it", "never answered from memory", "you
decide nothing".

### The harness: rails and labelled facts

Kept as **rails** (stop a runaway or an unauthorised action):

- `send_keys`/approvals: the keys answer the CEO's own newest recorded
  words, said after the prompt appeared, within
  `supervisor.authorise_window_s`, not already used, non-empty; a "no"
  never approves (`negativeInput`, a word list kept on purpose: it only
  ever blocks the irreversible direction).
- Spawn: the CEO's unused words, project roots, 3 per 10 min; close and
  rename unchanged.
- No new CEO words → no brain turn.
- Push loop cap (4th line about one event in 30 s dropped), runaway
  speech cap (~45 s), push held with no listener, the INTERRUPT
  downgrade while speaking.
- The #113 audio echo gate (energy correlation with the known playback:
  physics, not meaning).
- Bare stop words (`stop`, `no no`) still stop at once; the reset phrase.

Added as **information** (a fact a model cannot see): briefing
provenance/time/relation, delivery cuts, resent briefings, the no-fresh-
words fact, the voice's reading for the brain, the echo-timing fact for
the brain.

### Removed patches (ablation groups)

Each group is one commit, measured on the same tier-2 scenarios ×3
before the next is stacked on it (numbers in the table below).

| Group | Removed (kind) | Replaced by |
|---|---|---|
| core | "read exactly as given", forced "on it", "never from memory" (prompt); user-role pushes and markers | fast-brain prompt; briefings as labelled function responses |
| G1 | `ackCues`/`ackWords`/`ackOnly` (#108, #114): a short "On it." with no tool call re-relayed (judgment) | nothing: what the fast brain said is its answer; words met with no speech and no tool call at all are still relayed (lost utterance) |
| G2 | `selfRelay`, `rescueInput` fuzzy match (#72, #89, #90); a spoken push moving the relay gate (judgment) | the gate floor is the last relay only; a call with no new words gets the no-fresh-words fact |
| G3 | heard-coverage watch (50 %, key terms, content words 0.5) and re-queue in the Gemini session (#89, #101, #108, #115) (judgment) | delivery facts from the phone's playback reports; the fast brain decides whether to repeat |
| G4 | deferral/decline/question/filler word lists on the rail; the bare yes/no/option fast path with a brain (#30, #108, #114) (judgment) | the brain reads every answer; RULES.md already says "not now" puts a prompt off; a brain-less daemon keeps the fast path |
| G6 | the echo text guard: text matching that dropped or cut inputs (#48, #63, #108) (judgment) | the brain gets "these words began while the phone was still playing your line …" (timing fact) |

Not changed in this experiment: GPT-Live's session (it keeps its heard
tracker; only its prompt was rewritten), the slow brain's tools and
contract, the announce pipeline, chattiness, promises, investigations.

## Ablation results

(Filled in from `docs/eval/dual-brain-2026-10-04.md`.)

## Switching the CEO's daemon to this branch and back

Only one daemon may run against the real herdr at a time.

```sh
# 1. stop main's daemon (closing tmux alone does not stop it)
kill -TERM $(lsof -tiTCP:7777 -sTCP:LISTEN)
# 2. build the branch into its own binary
cd ~/Documents/sheepdog-dual-brain && git pull && go build -o ~/bin/sheepdog-dual ./cmd/sheepdog
# 3. start it the usual way, in the tmux session `sheepdog`
cd ~/Documents/herdr-sheepdog && set -a; . ./.env; set +a; exec ~/bin/sheepdog-dual serve >> ~/.local/share/sheepdog/serve.log 2>&1
```

Back to main: stop it the same way and start `sheepdog serve` (the
installed main binary) as before. Config is shared and compatible: the
branch adds one optional key, `gemini.briefing_channel` (default
`function_response`; `user_turn` keeps the branch's prompt but sends the
old marked user turns). Main ignores nothing it needs; the branch reads
the same state file and event log. The phone app needs no change.
