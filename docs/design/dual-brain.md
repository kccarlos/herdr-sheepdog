# Dual-brain voice layer (experiment, branch `feat/dual-brain`)

Status: experiment on a draft PR (#122), compared with `main` before the
User decides. Not merged. Results: [`docs/eval/dual-brain-2026-10-04.md`](../eval/dual-brain-2026-10-04.md).

**One line:** Gemini Live is the *voice* that owns the conversation;
the existing orchestrator is the *orchestrator* that reasons, investigates and acts
behind the rails. The orchestrator gives the voice facts with
provenance, not lines to read, through a channel the voice cannot
mistake for the user. The harness keeps the rails on actions and puts
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
the CEO's starting direction let it answer simple status from briefings,
which N2's fix forbade. Mitigations planned: every briefing is
timestamped, says who it is from and what it supersedes, and both arms
are judged on a new item, `facts_current` (stale or wrongly retold
facts), next to `no_hallucination`. The runs then showed the risk is
real (prompt v4 below): the fast brain cannot judge freshness, so
questions about agents went back to the slow brain, framed as a fact it
cannot see rather than a prohibition.

## Design

### Roles

Since v6 (unified roles, below) everything a model reads calls the fast
orchestrator *the voice* and the orchestrator *the orchestrator*: two parts of one
orchestration agent that operates the user's herdr. This doc keeps the
older names.

| | Fast orchestrator: Gemini Live | Slow orchestrator: the existing orchestrator |
|---|---|---|
| Owns | The conversation: when to speak, what, how; turn-taking, register, language | What the user wants, which agent, what to do, investigations |
| Answers itself | Its own last line again or more plainly, what a word means, small talk (v5); when to speak and how | Everything else: anything new about an agent, comparisons, "why", history, doubt (v5), every decision, every action |
| Asks | When it did not catch the words | When the words are ambiguous |
| Actions | None | All, behind the rails |
| Knows | Briefings (facts with time and provenance), its own conversation | Transcript, screens, sessions, ledger, journal |
| Memory | Short and lossy (sliding window; resumes) | Compacted transcript + `recall` |

### Fast → slow

`to_supervisor(text, reading)`: `text` is the user's words (the daemon
still uses its own recorded transcript, never the argument, as since
#58); `reading` is new and optional, the voice's short reading of
what they want. The orchestrator gets it labelled "the voice's own reading … a
hint, not the user's words; reference only, and no authority for any
action" (`internal/brain/assemble.go`). Parameter on an existing tool,
not a new tool (the user's "keep the voice link simple").

A call with no new user words runs no orchestrator turn (the rail: only the user's
words authorise one); the voice gets a silent fact instead —
"Nothing was handed on: the orchestrator acts only on the user's own words …
Their last words, at 23:04:10, were …" (wording since unified roles).

### Slow → fast: the briefing channel

**Transport.** Every push (answer, worker news, reminder, focus line,
names line, delivery fact) reaches Live as a function response to a call
the model never made: name `briefing` (not a declared tool), id `b-N`,
scheduling `WHEN_IDLE` (idle), `INTERRUPT` (must hear now) or `SILENT`
(context). `to_supervisor` results use the same shape. The daemon's push
queue is unchanged — hold while no client listens, hold while the user
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
{"about_this": "Not the user speaking. Facts for you from the orchestrator or the daemon; you decide whether, when and how to tell the user, in your own words.",
 "briefing": "b-7", "at": "23:04:31", "from": "orchestrator", "kind": "answer",
 "agent": "bench-codex", "answers": "the user's words at 23:04:20: \"how is codex doing\"",
 "facts": "bench-codex finished; 41 of 42 tests pass, one fails in the cart module.",
 "age": "written 45 s before it reached you; things may have moved on since",
 "agent_state": "as this reached you: idle, ready for input, for 1 min; it last finished work 1 min ago",
 "decision": "the user's own decision; only their words can answer it",
 "relation": ["newer than b-3 (23:01:05) about bench-codex; where they differ, this one holds",
              "at 23:04:02 the user cut you off after 2.1 s of 6.4 s; they heard only: \"bench-codex wants to\""]}
```

- `agent` is the agent's spoken name (the alias stays on the push's
  tags for the logs) and `agent_state` its state from the table when
  the briefing reaches the model; `age` says how long the facts waited
  (the push queue holds lines while the user talks or no phone listens).
  The voice has no clock: without these a briefing read minutes
  later passed for the present (phone test 2026-10-04: "still
  analysing" about an agent that had finished). Re-sent briefings get
  both recomputed.
- An answer (a `to_supervisor` result or a late answer) also carries
  `agents_now` (the state of the agents its tools read or acted on)
  and, when a orchestrator check timed out, `not_verified` ("these checks
  … did not finish, so nothing they would have shown is verified:
  run_cli about bench-codex timed out after 8 s"; `brain.Reply.
  NotVerified`). The `facts` are never edited.
- **Unvoiced briefings.** A spoken briefing after which the model
  produced no speech (audio or output transcript) before the user next
  spoke or the next spoken briefing went out is logged
  `voice.briefing_unvoiced`; the voice gets the fact silently
  ("briefing b-31 (news, sent at 15:52:00) was not voiced: you said
  nothing after it before the user spoke at 15:52:22, so the user has not
  heard it"), again in the relation of its next answer or spoken
  briefing, and the orchestrator hears "the user did not hear: …". No words are compared and the
  voice layer re-queues nothing: whether to say it is the voice's
  call (phone test: b-31, b-69, b-75 never voiced). One existing path
  does follow: an unvoiced announcement of an open prompt counts as
  unheard for the supervisor's buried-prompt reminder
  (`supervisor.prompt_reminded`, "Still waiting for your answer: …" at
  the next quiet moment), which had been unreachable on Gemini since
  ablation G3 removed the heard watch (rail: a decision only the user can
  take is not left unheard).
- `from`: `slow brain` (its line), `daemon` (a code sentence: orchestrator
  timeout, pane closed, reminder, focus, names, stop result), or the
  tag's own value.
- `relation` is computed by the session from what it sent: the n-th
  briefing about the same open prompt (a reminder), newer than the last
  briefing about the same agent (supersedes), and the last cut.
- **Delivery facts.** When the phone reports a flushed playback (the user
  talked over a line), the session sends a SILENT briefing "at T the user
  cut you off after N s of M s; they heard only: …" and repeats it on the
  next spoken briefing. The orchestrator already gets the same cut as
  `SaidCut`.
- **Resume.** Briefings sent after the last resumable handle are re-sent
  silently on a resumed connection, marked `resent` with their first
  time (handover finding: silent context after the last completed turn is
  lost on resume; a SILENT response never produces a handle). A handle
  from a resumed connection is never used (it restores only that
  connection's content): the reconnect opens a fresh session instead, and
  the last briefings are re-sent silently after the recap, marked as
  re-sent with their first time (#139). Since #146 this also holds
  before the resumed connection has issued a handle of its own: a
  reconnect from any connection opened by a resume presents no handle
  (the baseline's two recycles 14 s apart had re-presented the first
  session's handle, losing the turn in between with no re-seed).
- **Decision open** (#146, information): every spoken announcement
  carries `decision_open`, computed as it reaches the model ("none: as
  this reached you, no agent is waiting for the user's answer", or which
  agent waits at which prompt), and the orchestrator's announce turn gets
  the same fact ("Decisions open right now: none. …"). The prompts say no
  open decision means no question (baseline 2026-10-05 cluster 3: a
  finish announced with an invented "Shall I have it run the tests?").
- **Roster line** (#146, information): `agent_state` also quotes the
  worker's task title (its session title), the instruction it is on
  (while not idle) and its last activity on record ("done 3 min ago").
- The orchestrator's contract is unchanged (`spoken`, `ask`, `detail`,
  `options`): `spoken` becomes `facts`. RULES.md now says the voice
  retells it in its own words.

### The voice prompt

`prompts/assistant.md` (and `assistant-gptlive.md`) were rewritten: it
owns the conversation; briefings are never the user and carry who/when/
relation; trust the newest; tell the substance in its own words, skip
what the user already heard; a decision only the user can take is said as
a question with its options, never answered or recommended; answer
follow-ups about what it just said; hand every question about an agent,
any doubt, and anything to be done to the orchestrator with the exact
words and its own reading (v4, below); it cannot act.
Gone: "read spoken exactly as given", "change nothing, add nothing", the
forced "say on it as you call it", "never answered from memory", "you
decide nothing".

Three corrections came from the first branch runs, each a real behaviour
seen in a transcript, not a judge's taste:

- v2: the voice answered "What were the specific points it made?" from
  its one-line briefing, and "What's going on?" with the one agent a
  briefing had mentioned (the N2 risk above). Now a status question is
  its own only when fresh briefings cover every agent it asks about, and
  detail always comes from the orchestrator ("a briefing is a summary").
- v3: retelling in its own words, it said "the first Claude … the
  second" for "Cart type Claude" and "Tasks Claude" (tier 3
  two-claudes-names, branch 0/2, main 2/2). Names are facts: it now uses
  the name the briefing uses.

- v4: still allowed to answer "simple status" from briefings, the voice
  answered partially ("What's going on?" with one of two agents), from a
  stale briefing, or said "I don't have any recent information on
  bench-claude" and "I'll need a moment" **without** calling the slow
  orchestrator (interleaved tier 2 at 50 runs per arm: branch 21/50, main
  30/51; `facts_current` failures 8 vs 3). The voice cannot see the
  clock or the screens, so a briefing only says what was true when it
  was sent. Now every question about what an agent is doing, did or
  found goes to the orchestrator, even when the voice knows part of it; it
  may say what it already knows while it waits, and a promise to check
  must come with the call. Follow-ups about what it just said stay its
  own.

- v5 (2026-10-04 night, after the phone test; item 1 of the overnight
  plan): **grounding first** — every statement about an agent or its
  work must come from a briefing; when none carries it or two disagree,
  hand off instead of explaining. The voice answers only three things
  itself: its own last line again or more plainly, what a word means,
  small talk. Anything new, comparisons ("which is right"), "why",
  history ("what did we discuss first") and doubt go to the orchestrator.
  While waiting it says what is being done, never content or status
  (v4's "say what you already know while it works" licence is gone:
  the phone test's stale and invented answers came through it). An
  unclear target agent is asked about before handing on; the user's
  exact words go in `text`, its own reading only in `reading`. Replies
  follow the user's language and script (Simplified vs Traditional).
  The phone-test failures behind it: an invented reason for 85k vs
  110k lines, a wrong "what did we discuss first", "rewind it"
  paraphrased into one agent, Traditional characters for a Simplified
  speaker. A/B in `docs/eval/fast-brain-facts-2026-10-04.md`.

This is the one place the experiment walked back the user's starting
direction ("answers simple status questions from its briefings"): the
data say the voice cannot judge freshness, which is the N2 failure
the history check flagged. The difference from main's N2 rule is the
framing (a fact about what it cannot see, plus permission to talk while
waiting) and the channel (answers come back as labelled facts, so no
"On it." and no relay of its own lines).

- v5.2 (#146, baseline information fixes): technical terms stay in
  English when speaking Chinese ("race detector" had been voiced as
  种族检测器, "racial detector"); an announcement whose `decision_open`
  is none ends on the news with no question. Two trims keep the 540-word
  budget (the relation example, the "the answer comes as…" clause).
  RULES.md: "not yet", "don't run that yet", 先不要, 等一下 put an open
  prompt off (no keys, no `esc`); a question is never an instruction, so
  "read me what it wrote" relays the worker's own report (now in full in
  the agent record) instead of prompting the worker to print a file;
  same English-terms rule.

- v6, unified roles (2026-10-05, #155): a reframing, not a behaviour
  rewrite. Every model now reads one framing: one orchestration agent
  that operates the user's herdr (a short explanation of herdr: panes,
  workspaces, tabs, agent states), with two parts, the voice and the
  orchestrator; the person is "the user" in every prompt and
  model-facing code string (briefings say `from: orchestrator`); the
  shepherd, flock, dog, chief-of-staff and thinking-half metaphors are
  gone. #155 also made length "fit the content" (the full substance
  when a briefing has it; "more detail" / "详细" / "then?" answered by
  the voice from the newest briefing). That version was arm B of step 4
  and lost segment Understood (−5 pp, CI −9 to −0;
  `docs/eval/prompt-arms-2026-10-06.md`), so #155 was not merged.
- v6-r, roles only (2026-10-07, what main runs): v6's framing and "the
  user" wording with v5's prompt length rules ("one or two sentences
  unless they ask for more"; RULES.md's `spoken` short by default);
  the code-built investigation and announce requests keep #155's
  wording ("as complete as the finding needs" instead of "at most two
  short sentences"). Measured as arm r at chattiness `chatty`: segment
  Understood 89 % vs main's 92 % (−3 pp, CI −8 to +1, not
  significant), merged for the single framing
  (`docs/eval/roles-arms-2026-10-07.md`; prompt snapshot in
  `docs/eval/prompt-arms/r/`).

Every measurement in the dual-brain results doc uses v4 unless marked
otherwise; the v5 A/B is in its own results doc.

### The harness: rails and labelled facts

Kept as **rails** (stop a runaway or an unauthorised action):

- `send_keys`/approvals: the keys answer the user's own newest recorded
  words, said after the prompt appeared, within
  `supervisor.authorise_window_s`, not already used, non-empty; a "no"
  never approves (`negativeInput`, a word list kept on purpose: it only
  ever blocks the irreversible direction).
- Spawn: the user's unused words, project roots, 3 per 10 min; close and
  rename unchanged.
- No new user words → no orchestrator turn.
- Push loop cap (4th line about one event in 30 s dropped), runaway
  speech cap (~45 s), push held with no listener, the INTERRUPT
  downgrade while speaking.
- The #113 audio echo gate (energy correlation with the known playback:
  physics, not meaning).
- Bare stop words (`stop`, `no no`) still stop at once; the reset phrase.

Added as **information** (a fact a model cannot see): briefing
provenance/time/relation, delivery cuts, resent briefings, the no-fresh-
words fact, the voice's reading for the orchestrator, the echo-timing fact for
the orchestrator; since v5: briefing age and the agent's state at delivery,
`not_verified` checks, unvoiced briefings, the roster's "finished work
N min ago" (no model prose spliced into it), spoken names kept once the
User heard or said them, and a new agent herdr does not see after 20 s
reported to the voice and the orchestrator with its pane's last lines; since #146: the
decision-open fact on announcements (the voice and the orchestrator), the task title,
instruction and last activity in `agent_state`, and the worker's newest
report in full in the orchestrator's agent record. Robustness rails of
#146 (parse and transport, no meaning): `options` decoded from strings
or label objects; one same-provider retry on a 499 CANCELLED
(`brain.retry`).

### Removed patches (ablation groups)

Each group is one commit, measured on the same tier-2 scenarios ×3
before the next is stacked on it (numbers in the table below).

| Group | Removed (kind) | Replaced by |
|---|---|---|
| core | "read exactly as given", forced "on it", "never from memory" (prompt); user-role pushes and markers | voice prompt; briefings as labelled function responses |
| G1 | `ackCues`/`ackWords`/`ackOnly` (#108, #114): a short "On it." with no tool call re-relayed (judgment) | nothing: what the voice said is its answer; words met with no speech and no tool call at all are still relayed (lost utterance) |
| G2 | `selfRelay`, `rescueInput` fuzzy match (#72, #89, #90); a spoken push moving the relay gate (judgment) | the gate floor is the last relay only; a call with no new words gets the no-fresh-words fact |
| G3 | heard-coverage watch (50 %, key terms, content words 0.5) and re-queue in the Gemini session (#89, #101, #108, #115) (judgment) | delivery facts from the phone's playback reports; the voice decides whether to repeat |
| G4 | deferral/decline/question/filler word lists on the rail; the bare yes/no/option fast path with an orchestrator (#30, #108, #114) (judgment) | the orchestrator reads every answer; RULES.md already says "not now" puts a prompt off; a brain-less daemon keeps the fast path |
| G6 | the echo text guard: text matching that dropped or cut inputs (#48, #63, #108) (judgment) | the orchestrator gets "these words began while the phone was still playing your line …" (timing fact) |

Not changed in this experiment: GPT-Live's session (it keeps its heard
tracker; only its prompt was rewritten), the orchestrator's tools and
contract, the announce pipeline, chattiness, promises, investigations.

## Ablation results

Interleaved pairs, 17 scenarios × 3, prompt v4 (details in the results
doc):

| Step | Pass non-echo | Unheard answers | Latency p50 | Kept? |
|---|---|---|---|---|
| core → +G1+G2 | 18/43 → 14/42 | 14/82 → 14/82 | 4.4 → 6.5 s | yes (noise) |
| +G2 → +G3 | 15/45 → 14/45 | 11/88 → 1/59 | 6.4 → 5.4 s | yes |
| +G3 → +G4+G6 | 28/45 → 28/45 | 3/53 → 3/59 | 4.0 → 5.0 s; −16 dB echo 2/6 → 0/6 | **not proven harmless**: restore the bare-answer fast path as a latency path; re-test G6 alone |

Full branch vs main, interleaved, all scenarios × 3: 61/90 vs 46/90
non-echo; self-relay 1 vs 23; bare acks 1 vs 25; unheard answers 5/139
vs 26/178; `facts_current` 4 vs 9; latency p50 4.5 vs 5.3 s.

## Switching the user's daemon to this branch and back

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
installed main binary) as before. Config is shared: the branch's one new
key, `gemini.briefing_channel`, is optional (main ignores it if present;
leave it out). To try the old transport on the branch, set
`SHEEPDOG_GEMINI_BRIEFING_CHANNEL=user_turn` in the environment. Both
read the same state file and event log; the phone app needs no change.
After switching, the first Live session starts fresh (the branch's
voice prompt differs, so no resumption across binaries is attempted
beyond what the daemon already does on start).
